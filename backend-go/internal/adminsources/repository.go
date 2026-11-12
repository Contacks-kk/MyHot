package adminsources

import (
	"context"
	"encoding/json"
	"errors"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) List(ctx context.Context, filter Filter) (ListResult, error) {
	query := r.db.WithContext(ctx).Table("sources AS s").Select(`
		s.id, s.name, s.kind, s.tier, s.participation_mode, s.enabled, s.health, s.fail_count, s.interval_minutes,
		s.last_ok_at, s.last_fetch_at, s.last_error, (s.tier = 'T1') AS first_party, s.next_fetch_at,
		(SELECT count(*)::int FROM articles a WHERE a.source_id = s.id AND a.discovered_at > now() - interval '7 days') AS items_7d,
		coalesce(selected.n, 0) AS selected_30d`).
		Joins(`LEFT JOIN (
			SELECT source_id, count(*)::int AS n FROM publications
			WHERE selected AND discovered_at > now() - interval '30 days' GROUP BY source_id
		) selected ON selected.source_id = s.id`)
	if filter.Query != nil {
		query = query.Where("(s.name ILIKE ? OR s.id ILIKE ? OR s.config::text ILIKE ?)", *filter.Query, *filter.Query, *filter.Query)
	}
	if filter.Kind != nil {
		query = query.Where("s.kind = ?", *filter.Kind)
	}
	if filter.Health != nil {
		query = query.Where("s.health = ?", *filter.Health)
	}
	if filter.Mode != nil {
		query = query.Where("s.participation_mode = ?", *filter.Mode)
	}
	if filter.Enabled != nil {
		query = query.Where("s.enabled = ?", *filter.Enabled)
	}
	var rows []Row
	if err := query.Order("s.enabled DESC, CASE s.health WHEN 'failing' THEN 0 WHEN 'degraded' THEN 1 ELSE 2 END, s.name, s.id").
		Limit(100).Offset((filter.Page - 1) * 100).Scan(&rows).Error; err != nil {
		return ListResult{}, err
	}
	var totals Totals
	err := r.db.WithContext(ctx).Raw(`SELECT count(*)::int AS total, count(*) FILTER (WHERE enabled)::int AS enabled,
		count(*) FILTER (WHERE health = 'failing')::int AS failing, count(*) FILTER (WHERE health = 'degraded')::int AS degraded FROM sources`).Scan(&totals).Error
	return ListResult{Rows: rows, Totals: totals}, err
}

func (r *GormRepository) Detail(ctx context.Context, id string) (*Detail, error) {
	var source Source
	result := r.db.WithContext(ctx).Table("sources").Select(`id, name, kind, config, tags, (tier = 'T1') AS first_party, owner_entity_id,
		tier, participation_mode, signal_group_id, interval_minutes, site_fulltext, syndicate_fulltext, enabled, health, fail_count,
		last_fetch_at, last_ok_at, last_error, cursor, next_fetch_at, created_at, updated_at`).
		Where("id = ?", id).Take(&source)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	var runs []FetchRun
	err := r.db.WithContext(ctx).Table("fetch_runs").Select("id, started_at, finished_at, status, found_count, new_count, error, detail").
		Where("source_id = ?", id).Order("started_at DESC").Limit(30).Scan(&runs).Error
	if err != nil {
		return nil, err
	}
	var items []Item
	err = r.db.WithContext(ctx).Table("articles AS a").Select(`a.id, a.title, a.url, a.discovered_at, a.published_at, a.processing_state,
		p.selected, p.visibility, p.title AS title_zh`).
		Joins("LEFT JOIN publications p ON p.article_id = a.id").Where("a.source_id = ?", id).
		Order("a.discovered_at DESC").Limit(30).Scan(&items).Error
	if err != nil {
		return nil, err
	}
	var stats Stats
	err = r.db.WithContext(ctx).Table("articles AS a").Select(`count(*)::int AS total,
		count(*) FILTER (WHERE a.discovered_at > now() - interval '7 days')::int AS last7d,
		(SELECT count(*)::int FROM publications p WHERE p.source_id = ? AND p.selected) AS selected`, id).
		Where("a.source_id = ?", id).Scan(&stats).Error
	if err != nil {
		return nil, err
	}
	var history []AuditEntry
	err = r.db.WithContext(ctx).Model(&model.AdminAuditLog{}).Select("created_at, actor, action, reason, before, after").
		Where("subject = ?", "source:"+id).Order("created_at DESC").Limit(20).Scan(&history).Error
	if err != nil {
		return nil, err
	}
	var setting model.AppSetting
	republish := map[string]any(nil)
	settingResult := r.db.WithContext(ctx).Select("value").First(&setting, "key = ?", "republish.source:"+id)
	if settingResult.Error != nil && !errors.Is(settingResult.Error, gorm.ErrRecordNotFound) {
		return nil, settingResult.Error
	}
	if settingResult.RowsAffected > 0 && len(setting.Value) > 0 {
		if err := json.Unmarshal(setting.Value, &republish); err != nil {
			return nil, err
		}
	}
	if runs == nil {
		runs = []FetchRun{}
	}
	if items == nil {
		items = []Item{}
	}
	if history == nil {
		history = []AuditEntry{}
	}
	return &Detail{Source: source, Runs: runs, Items: items, Stats: stats, History: history, Republish: republish}, nil
}
