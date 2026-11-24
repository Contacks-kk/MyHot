package publiccontent

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

/** RecommendationPublications projects publication visibility and fields suitable for recommendation cards. */
func RecommendationPublications(db *gorm.DB) *gorm.DB {
	return db.Table("publications AS p").
		Select(`p.article_id,p.title,p.summary,p.score,
			(p.visibility='withdrawn' OR p.visible_after>now()
			  OR (p.visibility='public' AND p.selected AND p.visible_after IS NULL)) AS blocked,
			(p.visibility='public' AND (p.visible_after<=now() OR (p.visible_after IS NULL AND NOT p.selected))
			  AND p.eligible AND ps.participation_mode='editorial') AS public_detail`).
		Joins("JOIN sources ps ON ps.id=p.source_id")
}

func (r *GormRepository) Availability(ctx context.Context, ids []string) ([]Record, error) {
	var rows []Record
	err := r.db.WithContext(ctx).Table("publications AS p").
		Select("p.article_id AS id, p.visibility, s.participation_mode AS source_mode, s.name AS source_name").
		Joins("JOIN sources s ON s.id = p.source_id").
		Where("p.article_id IN ?", ids).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) StoryFollowups(ctx context.Context, publicID string, now time.Time) ([]FollowupMember, error) {
	var rows []FollowupMember
	err := r.db.WithContext(ctx).Table("stories AS st").
		Select(`p.fact_id, f.public_id::text AS fact_public_id, p.article_id AS id, p.title, s.name AS source_name,
			p.body_mode, p.score, p.timeline_at, p.sort_at, s.tier AS source_tier,
			s.config->>'publisherRole' AS publisher_role, s.owner_entity_id, f.subject AS fact_subject`).
		Joins("JOIN publications p ON p.story_id = st.id").
		Joins("JOIN sources s ON s.id = p.source_id").
		Joins("JOIN facts f ON f.id = p.fact_id").
		Where(`st.public_id = ? AND st.merged_into IS NULL AND p.visibility = 'public' AND p.selected AND p.visible_after <= ?
			AND EXISTS (SELECT 1 FROM fact_articles fa WHERE fa.fact_id = p.fact_id AND fa.article_id = p.article_id
				AND fa.role <> 'mention' AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite'))`, publicID, now).
		Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) GroupReports(ctx context.Context, query GroupReportsQuery, now time.Time) ([]GroupReportRow, error) {
	var rows []GroupReportRow
	db := r.db.WithContext(ctx).Table("facts AS f").
		Select("p.article_id AS id, p.title, p.timeline_at, p.url, s.name AS source_name").
		Joins("JOIN fact_articles fa ON fa.fact_id = f.id").
		Joins("JOIN publications p ON p.article_id = fa.article_id").
		Joins("JOIN sources s ON s.id = p.source_id").
		Where("f.public_id = ?", query.FactPublicID).
		Where(`fa.role <> 'mention' AND NOT EXISTS (
			SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite'
		)`).
		Where(`p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)`, now)
	switch query.Channel {
	case "news", "x":
		db = db.Where("p.channel = ?", query.Channel)
	case "firstParty":
		db = db.Where("s.tier = 'T1'")
	}
	if query.Category != "" {
		db = db.Where("p.category = ?", query.Category)
	}
	if query.Tag != "" {
		db = db.Where("p.tags @> ARRAY[?]::text[]", query.Tag)
	}
	err := db.Order("p.timeline_at DESC").Order("p.article_id ASC").Limit(groupReportsLimit).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) HotTopics(ctx context.Context, now time.Time) ([]HotTopicRecord, error) {
	var rows []struct {
		Rank                 int             `gorm:"column:rank"`
		Entry                json.RawMessage `gorm:"column:entry"`
		StoryPublicID        string          `gorm:"column:story_public_id"`
		StoryTitle           string          `gorm:"column:story_title"`
		RepresentativeURL    string          `gorm:"column:representative_url"`
		RepresentativeSource *string         `gorm:"column:representative_source"`
		LatestAt             time.Time       `gorm:"column:latest_at"`
	}
	const query = `
WITH ranking AS (
  SELECT entry.ordinality::int AS rank, entry.value AS entry,
         (entry.value->>'storyId')::bigint AS story_id,
         entry.value->>'representativeItemId' AS representative_id
  FROM hot_rankings h
  CROSS JOIN LATERAL jsonb_array_elements(h.entries) WITH ORDINALITY AS entry(value, ordinality)
  WHERE h.id = (SELECT id FROM hot_rankings WHERE published ORDER BY computed_at DESC LIMIT 1)
), current_reports AS (
  SELECT DISTINCT ON (f.story_id, p.article_id)
         f.story_id, p.article_id, st.public_id::text AS story_public_id, st.title AS story_title,
         p.url AS representative_url, s.name AS representative_source, latest.latest_at
  FROM ranking h
  JOIN stories st ON st.id = h.story_id
  JOIN facts f ON f.story_id = st.id
  JOIN fact_articles fa ON fa.fact_id = f.id
  JOIN publications p ON p.article_id = fa.article_id
  JOIN sources s ON s.id = p.source_id
  JOIN LATERAL (
    SELECT max(coalesce(p2.published_at, p2.discovered_at)) AS latest_at
    FROM facts f2
    JOIN fact_articles fa2 ON fa2.fact_id = f2.id
    JOIN publications p2 ON p2.article_id = fa2.article_id
    WHERE f2.story_id = st.id
      AND fa2.role <> 'mention'
      AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p2.analysis_id AND evidence_an.output->>'scope' = 'composite')
      AND p2.visibility = 'public' AND p2.eligible AND (NOT p2.selected OR p2.visible_after <= ?)
  ) latest ON latest.latest_at IS NOT NULL
  WHERE p.article_id = h.representative_id
    AND fa.role <> 'mention'
    AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
    AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
  ORDER BY f.story_id, p.article_id, (fa.role = 'primary') DESC, f.id
)
SELECT h.rank, h.entry, c.story_public_id, c.story_title, c.representative_url, c.representative_source, c.latest_at
FROM ranking h
JOIN current_reports c ON c.story_id = h.story_id AND c.article_id = h.representative_id
ORDER BY h.rank`
	err := r.db.WithContext(ctx).Raw(query, now, now).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]HotTopicRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, HotTopicRecord{
			Rank: row.Rank, Entry: row.Entry, StoryPublicID: row.StoryPublicID, StoryTitle: row.StoryTitle,
			RepresentativeURL: row.RepresentativeURL, RepresentativeSource: row.RepresentativeSource, LatestAt: row.LatestAt,
		})
	}
	return result, nil
}
