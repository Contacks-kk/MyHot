package publiccontent

import (
	"context"
	"encoding/json"
	"time"

	"aihot/backend-go/internal/topics"
)

// TopicSummaries reads topic membership only from currently visible selected
// seats, alongside the other site publication reads owned by this repository.
func (r *GormRepository) Summaries(ctx context.Context, definitions []topics.Definition, now time.Time) ([]topics.SummaryRow, error) {
	configuration := make([]struct {
		Slug     string   `json:"slug"`
		Position int      `json:"position"`
		Tags     []string `json:"tags"`
		Pattern  *string  `json:"pattern"`
	}, 0, len(definitions))
	for _, topic := range definitions {
		configuration = append(configuration, struct {
			Slug     string   `json:"slug"`
			Position int      `json:"position"`
			Tags     []string `json:"tags"`
			Pattern  *string  `json:"pattern"`
		}{Slug: topic.Slug, Position: topic.Position, Tags: topic.Tags, Pattern: topic.Pattern})
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		return nil, err
	}
	var rows []topics.SummaryRow
	err = r.db.WithContext(ctx).Raw(`
		WITH topic_config AS (
			SELECT * FROM jsonb_to_recordset(?::jsonb) AS t(slug text, position integer, tags text[], pattern text)
		)
		SELECT t.slug, p.article_id AS id, p.title, p.timeline_at AS at
		FROM topic_config t
		LEFT JOIN publications p ON p.visibility = 'public' AND p.selected AND p.seat AND p.visible_after <= ?
			AND p.tags && t.tags
			AND (t.pattern IS NULL OR p.title ~* t.pattern OR coalesce(p.original_title, '') ~* t.pattern
				OR (SELECT count(*) FROM unnest(p.tags) AS e(tag) WHERE e.tag LIKE 'entity:%') = 1)
		ORDER BY t.position, p.timeline_at DESC, p.article_id DESC`, string(encoded), now).Scan(&rows).Error
	return rows, err
}

var _ topics.Repository = (*GormRepository)(nil)
