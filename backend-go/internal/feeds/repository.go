package feeds

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

type ItemQuery struct {
	Kind         string
	CategoryKeys []string
	Now          time.Time
}

type Item struct {
	ID                  string          `gorm:"column:id"`
	Title               string          `gorm:"column:title"`
	Summary             *string         `gorm:"column:summary"`
	URL                 string          `gorm:"column:url"`
	Category            *string         `gorm:"column:category"`
	PublishedAt         *time.Time      `gorm:"column:published_at"`
	SourceName          string          `gorm:"column:source_name"`
	Channel             string          `gorm:"column:channel"`
	Syndicate           bool            `gorm:"column:syndicate"`
	Language            *string         `gorm:"column:language"`
	XPost               json.RawMessage `gorm:"column:x_post"`
	ZHText              *string         `gorm:"column:zh_text"`
	QuotedZH            *string         `gorm:"column:quoted_zh"`
	BodyText            *string         `gorm:"column:body_text"`
	BodyHTML            *string         `gorm:"column:body_html"`
	TranslationHTML     *string         `gorm:"column:tr_html"`
	TranslationComplete *bool           `gorm:"column:tr_complete"`
}

type ItemRepository interface {
	Items(context.Context, ItemQuery) ([]Item, error)
}

type GormRepository struct{ db *gorm.DB }

func NewGormRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) Items(ctx context.Context, query ItemQuery) ([]Item, error) {
	var rows []Item
	db := r.db.WithContext(ctx).Table("publications AS p").
		Select("p.article_id AS id, p.title, p.summary, p.url, p.category, p.published_at, s.name AS source_name").
		Joins("JOIN sources s ON s.id = p.source_id")
	if query.Kind == "selected-full" {
		db = db.Select(`p.article_id AS id, p.title, p.summary, p.url, p.category, p.published_at, s.name AS source_name,
			p.channel, p.syndicate, a.language, a.x_post,
			CASE WHEN p.channel = 'x' THEN tr.body_text END AS zh_text, qt.text_zh AS quoted_zh,
			left(a.body_text, 400) AS body_text, a.body_html, tr.body_html AS tr_html, tr.complete AS tr_complete`).
			Joins("LEFT JOIN articles a ON a.id = p.article_id AND p.syndicate").
			Joins("LEFT JOIN translations tr ON tr.article_id = p.article_id AND tr.lang = 'zh' AND tr.revision >= a.revision").
			Joins("LEFT JOIN quote_translations qt ON p.channel = 'x' AND qt.tweet_id = substring(a.x_post->'quoted'->>'url' from '/status/([0-9]+)')")
	}
	if query.Kind == "all" {
		db = db.Where(`p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
			AND coalesce(p.published_at, p.discovered_at) > ?::timestamptz - interval '7 days'
			AND coalesce(p.published_at, p.discovered_at) <= ?`, query.Now, query.Now, query.Now)
	} else {
		db = db.Where("p.visibility = 'public' AND p.selected AND p.seat AND p.visible_after <= ?", query.Now)
		if query.CategoryKeys != nil {
			if len(query.CategoryKeys) == 0 {
				return []Item{}, nil
			}
			db = db.Where("p.category IN ?", query.CategoryKeys).
				Where("coalesce(p.published_at, p.discovered_at) >= ?", query.Now.Add(-7*24*time.Hour))
		}
	}
	err := db.Order("coalesce(p.published_at, p.discovered_at) DESC").Order("p.article_id DESC").Limit(50).Scan(&rows).Error
	return rows, err
}

var _ ItemRepository = (*GormRepository)(nil)
