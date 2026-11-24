package sourceschedule

import (
	"context"
	"log"
	"slices"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

/** RecommendationFeedSQL matches the configured public RSS feeds by their stored URLs. */
func RecommendationFeedSQL(alias string) string {
	return alias + ".kind='rss' AND btrim(coalesce(nullif(" + alias + ".config->>'feedUrl','')," + alias + ".config->>'url',''))=ANY(?::text[])"
}

// RecommendationArticleWindow includes recent first imports with a trusted publication time.
const RecommendationArticleWindow = `a.discovered_at >= now()-interval '14 days' AND
	(NOT a.backfill OR (a.backfill_reason='first-import' AND
		a.published_at BETWEEN now()-interval '7 days' AND now()))`

/** ReuseWindow caps the subscription window when a platform limit is configured. */
func ReuseWindow(requested, limit int) int {
	if limit > 0 {
		return min(requested, limit)
	}
	return requested
}

/** Lock serializes subscription and fetch completion changes to the source plan. */
func Lock(tx *gorm.DB, sourceID string) error {
	var id string
	return tx.Raw(`SELECT id FROM sources WHERE id=? FOR NO KEY UPDATE`, sourceID).Row().Scan(&id)
}

/** Recompute updates the shared schedule after a subscription or policy change. */
func Recompute(tx *gorm.DB, sourceID string, policy config.SourceSchedule) error {
	var current struct {
		Kind               string
		Enabled            bool
		IntervalMinutes    int
		ReuseWindowMinutes int
		FeedURL            string
	}
	err := tx.Raw(`SELECT kind,enabled,interval_minutes,reuse_window_minutes,
		btrim(coalesce(nullif(config->>'feedUrl',''),config->>'url','')) AS feed_url
		FROM sources WHERE id=? FOR NO KEY UPDATE`, sourceID).
		Row().Scan(&current.Kind, &current.Enabled, &current.IntervalMinutes, &current.ReuseWindowMinutes, &current.FeedURL)
	if err != nil {
		return err
	}

	// Use a fresh snapshot after acquiring the source lock.
	var count, requested, window int
	err = tx.Raw(`SELECT count(*),coalesce(min(interval_minutes),60),coalesce(min(reuse_window_minutes),10)
		FROM user_source_subscriptions WHERE source_id=? AND enabled`, sourceID).
		Row().Scan(&count, &requested, &window)
	if err != nil {
		return err
	}
	if count == 0 {
		if current.Kind == "rss" && slices.Contains(policy.RecommendationFeeds, current.FeedURL) {
			interval := max(config.RecommendationIntervalMinutes, policy.AutoMinInterval)
			if current.Enabled && current.IntervalMinutes == interval && current.ReuseWindowMinutes == window {
				return nil
			}
			return tx.Exec(`UPDATE sources SET enabled=true,interval_minutes=?,reuse_window_minutes=?,
				health=CASE WHEN health='paused' THEN 'unknown' ELSE health END,
				next_fetch_at=greatest(now(),last_ok_at + (? * interval '1 minute')),updated_at=now()
				WHERE id=?`, interval, window, interval, sourceID).Error
		}
		return tx.Exec(`UPDATE sources SET enabled=false,health='paused',next_fetch_at=NULL,updated_at=now() WHERE id=?`, sourceID).Error
	}

	if current.Kind != "rss" && current.Kind != "web_list" {
		return tx.Exec(`UPDATE sources SET enabled=true,interval_minutes=?,reuse_window_minutes=?,next_fetch_at=now(),updated_at=now() WHERE id=?`,
			max(requested, window), window, sourceID).Error
	}
	interval := max(requested, policy.AutoMinInterval, ReuseWindow(window, policy.AutoReuseWindow))
	if current.Enabled && current.IntervalMinutes == interval {
		if current.ReuseWindowMinutes == window {
			return nil
		}
		return tx.Exec(`UPDATE sources SET reuse_window_minutes=?,updated_at=now() WHERE id=?`, window, sourceID).Error
	}

	// Reactivated sources become due once the platform's automatic minimum has elapsed.
	return tx.Exec(`UPDATE sources SET enabled=true,interval_minutes=?,reuse_window_minutes=?,
		health=CASE WHEN health='paused' THEN 'unknown' ELSE health END,
		next_fetch_at=greatest(now(),
			CASE WHEN enabled THEN last_ok_at + (? * interval '1 minute') END,
			last_ok_at + (? * interval '1 minute')),
		updated_at=now() WHERE id=?`, interval, window, interval, policy.AutoMinInterval, sourceID).Error
}

/** Reconcile updates active RSS and web-list plans that differ from the worker's policy. */
func Reconcile(ctx context.Context, db *gorm.DB, policy config.SourceSchedule) error {
	var ids []string
	err := db.WithContext(ctx).Raw(`SELECT s.id FROM sources s
		JOIN (SELECT source_id,min(interval_minutes) AS requested,min(reuse_window_minutes) AS reuse
			FROM user_source_subscriptions WHERE enabled GROUP BY source_id) u ON u.source_id=s.id
		WHERE s.kind IN ('rss','web_list') AND s.enabled AND (
			s.interval_minutes <> greatest(u.requested,?,least(u.reuse,nullif(?,0)))
			OR s.reuse_window_minutes <> u.reuse
		) ORDER BY s.id`, policy.AutoMinInterval, policy.AutoReuseWindow).Scan(&ids).Error
	if err != nil {
		return err
	}

	// Resolve the configured catalog rows and let Recompute apply the subscription or background interval.
	var feeds []struct {
		ID      string
		FeedURL string
	}
	err = db.WithContext(ctx).Raw(`SELECT s.id,btrim(coalesce(nullif(s.config->>'feedUrl',''),s.config->>'url','')) AS feed_url
		FROM sources s WHERE `+RecommendationFeedSQL("s")+` ORDER BY s.id`, model.TextArray(policy.RecommendationFeeds)).Scan(&feeds).Error
	if err != nil {
		return err
	}
	found := make(map[string]bool, len(feeds))
	for _, feed := range feeds {
		found[feed.FeedURL] = true
		if !slices.Contains(ids, feed.ID) {
			ids = append(ids, feed.ID)
		}
	}
	for _, feed := range policy.RecommendationFeeds {
		if !found[feed] {
			log.Printf("recommendation RSS feed missing from source catalog: %s", feed)
		}
	}
	for _, id := range ids {
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return Recompute(tx, id, policy)
		})
		if err != nil {
			return err
		}
	}
	if policy.AutoMinInterval > 0 {
		// A new floor can also affect an earlier due time without changing the period.
		return db.WithContext(ctx).Exec(`UPDATE sources s SET
			next_fetch_at=s.last_ok_at + (? * interval '1 minute'),updated_at=now()
			WHERE s.kind IN ('rss','web_list') AND s.enabled
			AND EXISTS (SELECT 1 FROM user_source_subscriptions u WHERE u.source_id=s.id AND u.enabled)
			AND s.last_ok_at + (? * interval '1 minute') > now()
			AND (s.next_fetch_at IS NULL OR s.next_fetch_at < s.last_ok_at + (? * interval '1 minute'))`,
			policy.AutoMinInterval, policy.AutoMinInterval, policy.AutoMinInterval).Error
	}
	return nil
}
