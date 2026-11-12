package publiccontent

import (
	"context"
	"time"

	"aihot/backend-go/internal/topics"
)

type DiscoveryReports struct {
	Daily   bool
	Weekly  bool
	Monthly bool
}

func (r *GormRepository) DiscoveryReports(ctx context.Context) (DiscoveryReports, error) {
	var kinds []string
	if err := r.db.WithContext(ctx).Model(&ReportModel{}).Distinct("kind").Where("kind IN ?", []string{"daily", "weekly", "monthly"}).Pluck("kind", &kinds).Error; err != nil {
		return DiscoveryReports{}, err
	}
	result := DiscoveryReports{}
	for _, kind := range kinds {
		switch kind {
		case "daily":
			result.Daily = true
		case "weekly":
			result.Weekly = true
		case "monthly":
			result.Monthly = true
		}
	}
	return result, nil
}

func (r *GormRepository) ReadSitemap(ctx context.Context, now time.Time, definitions []topics.Definition, maxURLs int) (SitemapData, error) {
	data := SitemapData{}
	var selected struct {
		Latest *time.Time `gorm:"column:latest"`
	}
	if err := r.db.WithContext(ctx).Table("publications AS p").
		Select("max(p.timeline_at) AS latest").
		Where("p.visibility = 'public' AND p.selected AND p.visible_after <= ?", now).
		Scan(&selected).Error; err != nil {
		return SitemapData{}, err
	}
	data.LatestSelectedAt = selected.Latest

	var daily struct {
		Latest *time.Time `gorm:"column:latest"`
	}
	if err := r.db.WithContext(ctx).Table("reports").
		Select("max(generated_at) AS latest").
		Where("kind = 'daily'").Scan(&daily).Error; err != nil {
		return SitemapData{}, err
	}
	data.LatestDailyAt = daily.Latest

	if err := r.db.WithContext(ctx).Table("reports").
		Select("kind, key, generated_at").Order("kind ASC").Order("key DESC").
		Scan(&data.Reports).Error; err != nil {
		return SitemapData{}, err
	}

	encodedTopics, err := encodeSitemapTopics(definitions)
	if err != nil {
		return SitemapData{}, err
	}
	topicRows := make([]struct {
		Slug      string     `gorm:"column:slug"`
		Total     int        `gorm:"column:total"`
		Recent    int        `gorm:"column:recent"`
		ChangedAt *time.Time `gorm:"column:changed_at"`
	}, 0, len(definitions))
	err = r.db.WithContext(ctx).Raw(`
		WITH topic_config AS (
			SELECT * FROM jsonb_to_recordset(?::jsonb) AS t(slug text, tags text[], pattern text)
		), seats AS (
			SELECT t.slug, p.article_id, p.timeline_at, p.visible_after
			FROM topic_config t
			JOIN publications p ON p.visibility = 'public' AND p.selected AND p.seat AND p.visible_after <= ?
				AND p.tags && t.tags
				AND (t.pattern IS NULL OR p.title ~* t.pattern OR coalesce(p.original_title, '') ~* t.pattern
					OR (SELECT count(*) FROM unnest(p.tags) AS e(tag) WHERE e.tag LIKE 'entity:%') = 1)
		)
		SELECT t.slug, count(s.article_id)::int AS total,
			count(s.article_id) FILTER (WHERE s.timeline_at > ? - interval '30 days')::int AS recent,
			max(s.visible_after) AS changed_at
		FROM topic_config t LEFT JOIN seats s ON s.slug = t.slug
		GROUP BY t.slug ORDER BY t.slug`, encodedTopics, now, now).Scan(&topicRows).Error
	if err != nil {
		return SitemapData{}, err
	}
	data.Topics = make([]SitemapTopic, 0, len(topicRows))
	for _, row := range topicRows {
		data.Topics = append(data.Topics, SitemapTopic{
			Slug: row.Slug, Pages: sitemapPageCount(row.Total),
			Indexable: row.Total >= 50 || row.Total >= 20 && row.Recent > 0, ChangedAt: row.ChangedAt,
		})
	}

	var stories []SitemapStory
	err = r.db.WithContext(ctx).Raw(`
		SELECT st.public_id::text AS public_id, st.latest_at
		FROM stories st
		WHERE st.merged_into IS NULL AND EXISTS (
			SELECT 1 FROM facts f
			JOIN fact_articles fa ON fa.fact_id = f.id
			JOIN publications p ON p.article_id = fa.article_id
			WHERE f.story_id = st.id AND fa.role <> 'mention'
				AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
				AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
		)
		ORDER BY st.latest_at DESC NULLS LAST, st.id DESC LIMIT 500`, now).Scan(&stories).Error
	if err != nil {
		return SitemapData{}, err
	}
	data.Stories = stories

	limit := remainingSitemapItems(data, maxURLs)
	if limit == 0 {
		return data, nil
	}
	err = r.db.WithContext(ctx).Raw(`
		SELECT p.article_id AS id, p.updated_at
		FROM publications p
		JOIN sources s ON s.id = p.source_id
		WHERE p.visibility = 'public' AND s.participation_mode = 'editorial'
			AND (NOT p.selected OR p.visible_after <= ?) AND p.indexable
		ORDER BY p.timeline_at DESC, p.article_id DESC LIMIT ?`, now, limit).Scan(&data.Items).Error
	if err != nil {
		return SitemapData{}, err
	}
	return data, nil
}

var _ SitemapReader = (*GormRepository)(nil)
