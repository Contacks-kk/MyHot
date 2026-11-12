package publiccontent

import (
	"context"
	"encoding/json"
	"time"

	"aihot/backend-go/internal/topics"
	"gorm.io/gorm"
)

type siteItemTopicQuery struct {
	Slug     string   `json:"slug"`
	Tags     []string `json:"tags"`
	Pattern  *string  `json:"pattern"`
	Position int      `json:"position"`
}

func (r *GormRepository) SiteItem(ctx context.Context, id string, definitions []topics.Definition) (*SiteItemDetailRow, error) {
	query := make([]siteItemTopicQuery, 0, len(definitions))
	for _, definition := range definitions {
		query = append(query, siteItemTopicQuery{Slug: definition.Slug, Tags: definition.Tags, Pattern: definition.Pattern, Position: definition.Position})
	}
	encoded, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}
	var row SiteItemDetailRow
	err = r.db.WithContext(ctx).Table("publications AS p").
		Select(`p.article_id AS id, p.title, p.summary, p.reason, p.category, p.tags, p.score, p.selected, p.channel,
			p.published_at, p.timeline_at, p.body_mode, s.name AS source_name, a.x_post, qt.text_zh AS quoted_zh,
			p.original_title, p.url AS original_url, p.discovered_at, p.visibility, s.participation_mode AS source_mode,
			p.indexable, p.seat, p.fact_id, a.author, a.language, a.body_html, a.body_text, a.body_status,
			tr.body_html AS translation_html, tr.body_text AS translation_text, tr.complete AS translation_done,
			st.public_id::text AS story_public_id, st.title AS story_title,
			ARRAY(SELECT t.slug FROM jsonb_to_recordset(?::jsonb) AS t(slug text, tags text[], pattern text, position integer)
				WHERE p.tags && t.tags AND (t.pattern IS NULL OR p.title ~* t.pattern OR coalesce(p.original_title, '') ~* t.pattern
					OR (SELECT count(*) FROM unnest(p.tags) AS e(tag) WHERE e.tag LIKE 'entity:%') = 1)
				ORDER BY t.position) AS topic_slugs`, string(encoded)).
		Joins("JOIN sources AS s ON s.id = p.source_id").
		Joins("JOIN articles AS a ON a.id = p.article_id").
		Joins("LEFT JOIN stories AS st ON st.id = p.story_id AND st.merged_into IS NULL").
		Joins("LEFT JOIN translations AS tr ON tr.article_id = p.article_id AND tr.lang = 'zh' AND tr.revision >= a.revision").
		Joins("LEFT JOIN quote_translations AS qt ON p.channel = 'x' AND qt.tweet_id = substring(a.x_post->'quoted'->>'url' from '/status/([0-9]+)')").
		Where("p.article_id = ?", id).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *GormRepository) SiteItemRelatedStories(ctx context.Context, articleID string) ([]SiteItemStoryRef, error) {
	var stories []SiteItemStoryRef
	err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT st.public_id::text AS public_id, st.title
		FROM fact_articles fa JOIN facts f ON f.id = fa.fact_id JOIN stories st ON st.id = f.story_id
		WHERE fa.article_id = ? AND fa.role <> 'mention' AND st.merged_into IS NULL
		ORDER BY public_id LIMIT 6`, articleID).Scan(&stories).Error
	if stories == nil {
		stories = []SiteItemStoryRef{}
	}
	return stories, err
}

func (r *GormRepository) SiteItemGroup(ctx context.Context, factID int64, now time.Time) (*SiteItemGroup, error) {
	var result struct {
		FactID  string `gorm:"column:fact_id"`
		Reports int    `gorm:"column:reports"`
		Sources int    `gorm:"column:sources"`
	}
	err := r.db.WithContext(ctx).Raw(`SELECT f.public_id AS fact_id, count(p.article_id) AS reports, count(DISTINCT p.source_id) AS sources
		FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id
		WHERE f.id = ? AND fa.role <> 'mention'
		AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
		AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
		GROUP BY f.public_id`, factID, now).Scan(&result).Error
	if err != nil {
		return nil, err
	}
	if result.FactID == "" {
		return nil, nil
	}
	return &SiteItemGroup{FactID: result.FactID, ReportCount: result.Reports, AdditionalSourceCount: max(0, result.Sources-1)}, nil
}

func (r *GormRepository) SiteItemSeatHolder(ctx context.Context, factID int64, now time.Time) (*topics.SameEvent, error) {
	var holder topics.SameEvent
	result := r.db.WithContext(ctx).Table("publications AS p").
		Select("p.article_id AS id, p.title").
		Where("p.fact_id = ? AND p.visibility = 'public' AND p.selected AND p.seat AND p.visible_after <= ?", factID, now).
		Take(&holder)
	if result.Error == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return &holder, nil
}

var _ SiteItemRepository = (*GormRepository)(nil)
