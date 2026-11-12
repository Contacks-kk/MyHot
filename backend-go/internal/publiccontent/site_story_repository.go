package publiccontent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"aihot/backend-go/internal/topics"
)

type siteStoryTopicQuery struct {
	Slug     string   `json:"slug"`
	Tags     []string `json:"tags"`
	Pattern  *string  `json:"pattern"`
	Position int      `json:"position"`
}

type siteStoryClock struct {
	ID              string     `gorm:"column:id"`
	LastOKAt        *time.Time `gorm:"column:last_ok_at"`
	IntervalMinutes int        `gorm:"column:interval_minutes"`
}

func (r *GormRepository) SiteStory(ctx context.Context, storyID int64, now time.Time, definitions []topics.Definition) (SiteStoryData, error) {
	data := SiteStoryData{Reports: []SiteStoryReport{}, DigestReports: []v1StoryDigestRecord{}, Facts: []SiteStoryFact{}, Related: []v1StoryRelatedRecord{}, Heat: []SiteStoryHeatPoint{}, Signals: []SiteStorySignal{}}
	var story v1StoryDatabase
	if err := r.db.WithContext(ctx).Raw(`SELECT st.public_id::text, st.title, st.digest, st.digest_updated_at,
		saved.digest AS saved_digest, saved.inputs_hash, to_json(saved.article_ids) AS article_ids
		FROM stories st LEFT JOIN LATERAL (SELECT digest, inputs_hash, article_ids FROM story_digests WHERE story_id = st.id ORDER BY version DESC LIMIT 1) saved ON true
		WHERE st.id = ? AND st.merged_into IS NULL`, storyID).Scan(&story).Error; err != nil {
		return data, err
	}
	data.Story = story
	topicDefinitions := make([]siteStoryTopicQuery, 0, len(definitions))
	for _, definition := range definitions {
		topicDefinitions = append(topicDefinitions, siteStoryTopicQuery{Slug: definition.Slug, Tags: definition.Tags, Pattern: definition.Pattern, Position: definition.Position})
	}
	encodedTopics, err := json.Marshal(topicDefinitions)
	if err != nil {
		return data, err
	}
	reportSQL := `SELECT DISTINCT ON (p.article_id) p.article_id AS id, p.title, p.summary, p.url, p.selected,
		coalesce(p.published_at, p.discovered_at) AS at, s.id AS source_id, s.name AS source_name,
		(s.tier = 'T1') AS first_party, f.id AS fact_id,
		CASE WHEN EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite') THEN 'mention' ELSE fa.role END AS role,
		s.tier AS source_tier, s.config->>'publisherRole' AS publisher_role, s.owner_entity_id, f.subject AS fact_subject,
		p.body_mode, p.score, p.timeline_at,
		ARRAY(SELECT t.slug FROM jsonb_to_recordset(?::jsonb) AS t(slug text, tags text[], pattern text, position integer)
			WHERE p.tags && t.tags AND (t.pattern IS NULL OR p.title ~* t.pattern OR coalesce(p.original_title, '') ~* t.pattern
				OR (SELECT count(*) FROM unnest(p.tags) AS e(tag) WHERE e.tag LIKE 'entity:%') = 1)
			ORDER BY t.position) AS topic_slugs
		FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id JOIN sources s ON s.id = p.source_id
		WHERE f.story_id = ? AND p.visibility = 'public' AND s.participation_mode = 'editorial' AND (NOT p.selected OR p.visible_after <= ?)
		ORDER BY p.article_id, (fa.role = 'primary') DESC, (fa.role <> 'mention') DESC, f.id`
	if err := r.db.WithContext(ctx).Raw(reportSQL, string(encodedTopics), storyID, now).Scan(&data.Reports).Error; err != nil {
		return data, err
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT ON (p.article_id) f.story_id, p.article_id AS id, p.title, p.summary, s.name AS source_name,
		(s.tier = 'T1') AS first_party, coalesce(p.published_at, p.discovered_at) AS at, f.id AS fact_id,
		f.subject AS fact_subject, f.action AS fact_action, f.object AS fact_object, f.conditions AS fact_conditions,
		fa.evidence, an.output->'fact' AS structured_fact
		FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id
		JOIN sources s ON s.id = p.source_id LEFT JOIN analyses an ON an.id = p.analysis_id
		WHERE f.story_id = ? AND fa.role <> 'mention'
		AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
		AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
		ORDER BY p.article_id, (fa.role = 'primary') DESC, f.id`, storyID, now).Scan(&data.DigestReports).Error; err != nil {
		return data, err
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT id, public_id, title FROM facts WHERE story_id = ?`, storyID).Scan(&data.Facts).Error; err != nil {
		return data, err
	}
	var summary struct {
		Summary *string `gorm:"column:summary"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT st.summary FROM stories st WHERE st.id = ? AND EXISTS (
		SELECT 1 FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id
		WHERE f.story_id = st.id AND p.summary = st.summary AND fa.role <> 'mention'
		AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
		AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?))`, storyID, now).Scan(&summary).Error; err != nil {
		return data, err
	}
	data.Summary = summary.Summary
	var latest SiteStoryLatest
	latestResult := r.db.WithContext(ctx).Raw(`SELECT p.article_id AS id, p.title, coalesce(p.published_at, p.discovered_at) AS at
		FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id
		WHERE f.story_id = ? AND fa.role <> 'mention'
		AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
		AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
		ORDER BY coalesce(p.published_at, p.discovered_at) DESC, p.article_id LIMIT 1`, storyID, now).Scan(&latest)
	if latestResult.Error != nil {
		return data, latestResult.Error
	}
	if latestResult.RowsAffected > 0 {
		data.Latest = &latest
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT st.public_id::text, st.title, l.relation FROM story_links l JOIN stories st ON st.id = l.other_id
		WHERE l.story_id = ? AND st.merged_into IS NULL AND EXISTS (
			SELECT 1 FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id JOIN sources s ON s.id = p.source_id
			WHERE f.story_id = st.id AND fa.role <> 'mention'
			AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
			AND p.visibility = 'public' AND s.participation_mode = 'editorial' AND (NOT p.selected OR p.visible_after <= ?))
		ORDER BY st.latest_at DESC NULLS LAST, st.id DESC LIMIT 8`, storyID, now).Scan(&data.Related).Error; err != nil {
		return data, err
	}
	if err := r.loadSiteStoryMetrics(ctx, &data, storyID, now); err != nil {
		return data, err
	}
	if err := r.loadSiteStoryHeat(ctx, &data, storyID, now); err != nil {
		return data, err
	}
	return data, nil
}

func (r *GormRepository) loadSiteStoryMetrics(ctx context.Context, data *SiteStoryData, storyID int64, now time.Time) error {
	const currentSignals = `SELECT ss.story_id, ss.article_id, s.id AS source_id, ss.observed_at, s.created_at AS source_since,
		CASE WHEN s.participation_mode = 'editorial' THEN 'editorial' ELSE 'signal' END AS kind,
		CASE WHEN s.signal_group_id IS NOT NULL THEN 'group:' || s.signal_group_id WHEN s.owner_entity_id IS NOT NULL THEN 'owner:' || s.owner_entity_id ELSE 'source:' || s.id END AS participant_key
		FROM story_signals ss JOIN articles a ON a.id = ss.article_id JOIN sources s ON s.id = a.source_id
		LEFT JOIN publications p ON p.article_id = ss.article_id
		WHERE s.participation_mode <> 'isolated' AND coalesce(p.visibility, 'public') <> 'withdrawn'
		AND NOT EXISTS (SELECT 1 FROM grouping_overrides go WHERE go.article_id = ss.article_id AND go.mode = 'standalone')
		AND (s.participation_mode <> 'editorial' OR EXISTS (SELECT 1 FROM fact_articles fa JOIN facts f ON f.id = fa.fact_id
			WHERE fa.article_id = ss.article_id AND f.story_id = ss.story_id AND fa.role <> 'mention'
			AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')))`
	var metrics struct {
		Participants48h   int `gorm:"column:participants_48h"`
		NewParticipants6h int `gorm:"column:new_participants_6h"`
		RecentReports24h  int `gorm:"column:recent_reports_24h"`
	}
	query := `WITH current_signals AS (` + currentSignals + `), cs AS (SELECT * FROM current_signals WHERE story_id = ? AND observed_at <= ?)
		SELECT count(DISTINCT participant_key) FILTER (WHERE observed_at > ?::timestamptz - interval '48 hours') AS participants_48h,
		count(DISTINCT participant_key) FILTER (WHERE observed_at > ?::timestamptz - interval '6 hours' AND participant_key NOT IN
			(SELECT participant_key FROM cs x WHERE x.observed_at <= ?::timestamptz - interval '6 hours')) AS new_participants_6h,
		count(*) FILTER (WHERE kind = 'editorial' AND observed_at > ?::timestamptz - interval '24 hours') AS recent_reports_24h FROM cs`
	if err := r.db.WithContext(ctx).Raw(query, storyID, now, now, now, now, now).Scan(&metrics).Error; err != nil {
		return err
	}
	data.WhyHot.Participants48h, data.WhyHot.NewParticipants6h, data.WhyHot.RecentReports24h = metrics.Participants48h, metrics.NewParticipants6h, metrics.RecentReports24h
	var partial struct {
		Count int `gorm:"column:n"`
	}
	partialQuery := `WITH current_signals AS (` + currentSignals + `), behind AS (
		SELECT id FROM sources WHERE enabled AND kind IN ('rss','web_list','json_list','x_search')
		AND (last_ok_at IS NULL OR last_ok_at < ?::timestamptz - make_interval(mins => greatest(interval_minutes * 3, 90))))
		SELECT count(*) AS n FROM current_signals cs WHERE cs.story_id = ? AND cs.observed_at > ?::timestamptz - interval '48 hours'
		AND cs.observed_at <= ? AND cs.source_id IN (SELECT id FROM behind)`
	if err := r.db.WithContext(ctx).Raw(partialQuery, now, storyID, now, now).Scan(&partial).Error; err != nil {
		return err
	}
	data.WhyHot.ObservationComplete = partial.Count == 0
	var rank struct {
		Rank *int `gorm:"column:rank"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT entry.ordinality::int AS rank FROM hot_rankings h
		CROSS JOIN LATERAL jsonb_array_elements(h.entries) WITH ORDINALITY AS entry(value, ordinality)
		WHERE h.published AND entry.value->>'storyId' = ? AND h.id = (SELECT id FROM hot_rankings WHERE published ORDER BY computed_at DESC LIMIT 1)
		LIMIT 1`, fmt.Sprint(storyID)).Scan(&rank).Error; err != nil {
		return err
	}
	data.WhyHot.Rank = rank.Rank
	return nil
}

func (r *GormRepository) loadSiteStoryHeat(ctx context.Context, data *SiteStoryData, storyID int64, now time.Time) error {
	if err := r.db.WithContext(ctx).Table("story_heat_hourly").Select("hour, heat, participants").
		Where("story_id = ? AND complete AND hour > ? AND hour <= ?", storyID, now.Add(-7*24*time.Hour), now).
		Order("hour").Scan(&data.Heat).Error; err != nil {
		return err
	}
	if len(data.Heat) == 0 {
		return nil
	}
	since := data.Heat[0].Hour.Add(-48 * time.Hour)
	const currentSignals = `SELECT ss.story_id, ss.article_id, s.id AS source_id, ss.observed_at, s.created_at AS source_since,
		CASE WHEN s.participation_mode = 'editorial' THEN 'editorial' ELSE 'signal' END AS kind,
		CASE WHEN s.signal_group_id IS NOT NULL THEN 'group:' || s.signal_group_id WHEN s.owner_entity_id IS NOT NULL THEN 'owner:' || s.owner_entity_id ELSE 'source:' || s.id END AS participant_key
		FROM story_signals ss JOIN articles a ON a.id = ss.article_id JOIN sources s ON s.id = a.source_id LEFT JOIN publications p ON p.article_id = ss.article_id
		WHERE s.participation_mode <> 'isolated' AND coalesce(p.visibility, 'public') <> 'withdrawn'
		AND NOT EXISTS (SELECT 1 FROM grouping_overrides go WHERE go.article_id = ss.article_id AND go.mode = 'standalone')
		AND (s.participation_mode <> 'editorial' OR EXISTS (SELECT 1 FROM fact_articles fa JOIN facts f ON f.id = fa.fact_id
			WHERE fa.article_id = ss.article_id AND f.story_id = ss.story_id AND fa.role <> 'mention'
			AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')))`
	query := `WITH current_signals AS (` + currentSignals + `) SELECT participant_key, observed_at, source_since FROM current_signals
		WHERE story_id = ? AND observed_at > ? AND observed_at <= ?`
	return r.db.WithContext(ctx).Raw(query, storyID, since, now).Scan(&data.Signals).Error
}

var _ SiteStoryRepository = (*GormRepository)(nil)
