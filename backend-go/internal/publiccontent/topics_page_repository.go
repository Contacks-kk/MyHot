package publiccontent

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"aihot/backend-go/internal/topics"
)

type topicFilterConfig struct {
	Tags    []string `json:"tags"`
	Pattern *string  `json:"pattern"`
}

func topicFilterJSON(definition topics.Definition) (string, error) {
	tags := definition.Tags
	if tags == nil {
		tags = []string{}
	}
	encoded, err := json.Marshal([]topicFilterConfig{{Tags: tags, Pattern: definition.Pattern}})
	return string(encoded), err
}

func (r *GormRepository) Page(ctx context.Context, definition topics.Definition, page int, now time.Time) (topics.PageData, error) {
	encoded, err := topicFilterJSON(definition)
	if err != nil {
		return topics.PageData{}, err
	}
	var stats struct {
		Total       int
		Recent      int
		LatestTitle *string
		LatestAt    *time.Time
	}
	err = r.db.WithContext(ctx).Raw(`
		WITH topic_filter AS (
			SELECT * FROM jsonb_to_recordset(?::jsonb) AS t(tags text[], pattern text)
		), seats AS (
			SELECT p.article_id, p.title, p.timeline_at
			FROM publications p CROSS JOIN topic_filter t
			WHERE p.visibility = 'public' AND p.selected AND p.seat AND p.visible_after <= ?
				AND p.tags && t.tags
				AND (t.pattern IS NULL OR p.title ~* t.pattern OR coalesce(p.original_title, '') ~* t.pattern
					OR (SELECT count(*) FROM unnest(p.tags) AS e(tag) WHERE e.tag LIKE 'entity:%') = 1)
		)
		SELECT count(*)::int AS total,
			count(*) FILTER (WHERE timeline_at > ?::timestamptz - interval '30 days')::int AS recent,
			(SELECT title FROM seats ORDER BY timeline_at DESC, article_id DESC LIMIT 1) AS latest_title,
			(SELECT timeline_at FROM seats ORDER BY timeline_at DESC, article_id DESC LIMIT 1) AS latest_at
		FROM seats`, encoded, now, now).Scan(&stats).Error
	if err != nil {
		return topics.PageData{}, err
	}
	var poolTotal int
	err = r.db.WithContext(ctx).Raw(`
		WITH topic_filter AS (
			SELECT * FROM jsonb_to_recordset(?::jsonb) AS t(tags text[], pattern text)
		)
		SELECT count(*)::int FROM publications p CROSS JOIN topic_filter t
		WHERE p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
			AND p.tags && t.tags
			AND (t.pattern IS NULL OR p.title ~* t.pattern OR coalesce(p.original_title, '') ~* t.pattern
				OR (SELECT count(*) FROM unnest(p.tags) AS e(tag) WHERE e.tag LIKE 'entity:%') = 1)`, encoded, now).Scan(&poolTotal).Error
	if err != nil {
		return topics.PageData{}, err
	}
	offset := math.MaxInt
	if page <= math.MaxInt/20+1 {
		offset = (page - 1) * 20
	}
	var rows []topics.PageItemRow
	err = r.db.WithContext(ctx).Raw(`
		WITH topic_filter AS (
			SELECT * FROM jsonb_to_recordset(?::jsonb) AS t(tags text[], pattern text)
		)
		SELECT p.article_id AS id, p.title, p.summary,
			CASE WHEN p.selected THEN p.reason END AS reason, p.category, p.tags, p.score, p.channel,
			p.published_at, p.timeline_at, p.body_mode, s.name AS source_name, a.x_post,
			qt.text_zh AS quoted_zh
		FROM publications p CROSS JOIN topic_filter t
		JOIN sources s ON s.id = p.source_id
		JOIN articles a ON a.id = p.article_id
		LEFT JOIN quote_translations qt ON p.channel = 'x' AND qt.tweet_id = substring(a.x_post->'quoted'->>'url' from '/status/([0-9]+)')
		WHERE p.visibility = 'public' AND p.selected AND p.seat AND p.visible_after <= ?
			AND p.tags && t.tags
			AND (t.pattern IS NULL OR p.title ~* t.pattern OR coalesce(p.original_title, '') ~* t.pattern
				OR (SELECT count(*) FROM unnest(p.tags) AS e(tag) WHERE e.tag LIKE 'entity:%') = 1)
		ORDER BY p.timeline_at DESC, p.article_id DESC LIMIT 20 OFFSET ?`, encoded, now, offset).Scan(&rows).Error
	if err != nil {
		return topics.PageData{}, err
	}
	if rows == nil {
		rows = []topics.PageItemRow{}
	}
	data := topics.PageData{Total: stats.Total, Recent: stats.Recent, PoolTotal: poolTotal, Items: rows}
	if stats.LatestTitle != nil && stats.LatestAt != nil {
		data.Latest = &topics.Latest{Title: *stats.LatestTitle, At: stats.LatestAt.UTC().Format("2006-01-02T15:04:05.000Z")}
	}
	return data, nil
}

var _ topics.PageRepository = (*GormRepository)(nil)
