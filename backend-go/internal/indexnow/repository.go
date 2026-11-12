package indexnow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/model"
	"aihot/backend-go/internal/topics"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) Watermark(ctx context.Context) (time.Time, bool, error) {
	var setting model.AppSetting
	result := r.db.WithContext(ctx).Select("value").First(&setting, "key = ?", "indexnow.watermark")
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return time.Time{}, false, nil
	}
	if result.Error != nil {
		return time.Time{}, false, result.Error
	}
	var value struct {
		Since time.Time `json:"since"`
	}
	if err := json.Unmarshal(setting.Value, &value); err != nil {
		return time.Time{}, false, err
	}
	return value.Since, true, nil
}

func (r *GormRepository) Changes(ctx context.Context, since, now time.Time, definitions []topics.Definition, storyLimit int) (Changes, error) {
	changes := Changes{Reports: []Report{}, Stories: []Story{}, Topics: []Topic{}}
	if err := r.db.WithContext(ctx).Table("reports").Select("kind, key").
		Where("generated_at > ? AND generated_at <= ?", since, now).Scan(&changes.Reports).Error; err != nil {
		return Changes{}, err
	}
	if changes.Reports == nil {
		changes.Reports = []Report{}
	}
	if storyLimit > 0 {
		err := r.db.WithContext(ctx).Raw(`
			SELECT st.public_id::text AS public_id, st.created_at
			FROM stories st
			WHERE st.merged_into IS NULL AND st.created_at > ? AND st.created_at <= ? AND EXISTS (
				SELECT 1 FROM facts f
				JOIN fact_articles fa ON fa.fact_id = f.id
				JOIN publications p ON p.article_id = fa.article_id
				WHERE f.story_id = st.id AND fa.role <> 'mention'
					AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
					AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
			)
			ORDER BY st.created_at, st.id LIMIT ?`, since, now, now, storyLimit).Scan(&changes.Stories).Error
		if err != nil {
			return Changes{}, err
		}
	}
	if len(definitions) > 0 {
		topics, err := r.topicChanges(ctx, definitions, now)
		if err != nil {
			return Changes{}, err
		}
		for _, topic := range topics {
			if topic.ChangedAt == nil || !topic.ChangedAt.After(since) || topic.ChangedAt.After(now) {
				continue
			}
			if topic.Total >= 50 || topic.Total >= 20 && topic.Recent > 0 {
				changes.Topics = append(changes.Topics, topic)
			}
		}
	}
	return changes, nil
}

func (r *GormRepository) Items(ctx context.Context, since, now time.Time, limit int) ([]Item, error) {
	items := []Item{}
	if limit <= 0 {
		return items, nil
	}
	err := r.db.WithContext(ctx).Table("publications AS p").Select("p.article_id AS id, p.updated_at").
		Where("p.visibility = 'public' AND p.indexable AND p.updated_at > ? AND p.updated_at <= ?", since, now).
		Order("p.updated_at ASC").Order("p.article_id ASC").Limit(limit).Scan(&items).Error
	return items, err
}

func (r *GormRepository) topicChanges(ctx context.Context, definitions []topics.Definition, now time.Time) ([]Topic, error) {
	type topicConfig struct {
		Slug    string   `json:"slug"`
		Tags    []string `json:"tags"`
		Pattern *string  `json:"pattern"`
	}
	configuration := make([]topicConfig, 0, len(definitions))
	for _, definition := range definitions {
		tags := definition.Tags
		if tags == nil {
			tags = []string{}
		}
		configuration = append(configuration, topicConfig{Slug: definition.Slug, Tags: tags, Pattern: definition.Pattern})
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		return nil, err
	}
	var rows []Topic
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
		GROUP BY t.slug ORDER BY t.slug`, string(encoded), now, now).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	bySlug := make(map[string]Topic, len(rows))
	for _, row := range rows {
		bySlug[row.Slug] = row
	}
	ordered := make([]Topic, 0, len(definitions))
	for _, definition := range definitions {
		if topic, ok := bySlug[definition.Slug]; ok {
			ordered = append(ordered, topic)
		}
	}
	return ordered, nil
}

func (r *GormRepository) SaveWatermark(ctx context.Context, watermark time.Time) error {
	value, err := json.Marshal(struct {
		Since string `json:"since"`
	}{Since: watermark.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	updatedBy := "worker"
	setting := model.AppSetting{Key: "indexnow.watermark", Value: value, UpdatedBy: &updatedBy}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.Assignments(map[string]any{
		"value": value, "updated_at": gorm.Expr("now()"),
	})}).Create(&setting).Error
}

var _ Repository = (*GormRepository)(nil)
