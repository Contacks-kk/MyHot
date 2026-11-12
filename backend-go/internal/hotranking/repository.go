package hotranking

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) Reports(ctx context.Context, storyID int64, at time.Time) ([]Report, error) {
	var rows []Report
	err := r.db.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (p.article_id)
			p.article_id AS id, p.url, p.title, s.id AS source_id, s.name AS source_name,
			p.selected, p.score, p.timeline_at, p.body_mode, f.id AS fact_id,
			coalesce(p.published_at, p.discovered_at) AS at, s.tier AS source_tier,
			s.config->>'publisherRole' AS publisher_role, s.owner_entity_id, f.subject AS fact_subject
		FROM facts f
		JOIN fact_articles fa ON fa.fact_id = f.id
		JOIN publications p ON p.article_id = fa.article_id
		JOIN sources s ON s.id = p.source_id
		WHERE f.story_id = ? AND fa.role <> 'mention'
			AND NOT EXISTS (SELECT 1 FROM analyses evidence_an
				WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
			AND p.visibility = 'public' AND p.eligible
			AND (NOT p.selected OR p.visible_after <= ?)
		ORDER BY p.article_id, (fa.role = 'primary') DESC, f.id`, storyID, at).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) Save(ctx context.Context, at time.Time, entries []Entry, candidates int) (int64, error) {
	encodedEntries, err := json.Marshal(entries)
	if err != nil {
		return 0, err
	}
	evidence, err := json.Marshal(map[string]int{
		"windowHours": windowHours, "halfLifeHours": halfLifeHours,
		"minParticipants": minimumPeople, "candidates": candidates,
	})
	if err != nil {
		return 0, err
	}
	var row struct {
		ID int64 `gorm:"column:id"`
	}
	if err := r.db.WithContext(ctx).Raw(`
		INSERT INTO hot_rankings (computed_at, rule_version, entries, evidence, published)
		VALUES (?, ?, ?::jsonb, ?::jsonb, true)
		RETURNING id`, at, ruleVersion, string(encodedEntries), string(evidence)).Scan(&row).Error; err != nil {
		return 0, err
	}
	if err := r.db.WithContext(ctx).Exec(`DELETE FROM hot_rankings WHERE computed_at < now() - interval '30 days'`).Error; err != nil {
		return row.ID, err
	}
	return row.ID, nil
}

var _ Repository = (*GormRepository)(nil)
