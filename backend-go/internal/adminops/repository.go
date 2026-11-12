package adminops

import (
	"context"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type Repository interface {
	NavCounts(context.Context) (NavCounts, error)
	Audit(context.Context, AuditFilter) (AuditResult, error)
}

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) NavCounts(ctx context.Context) (NavCounts, error) {
	var counts NavCounts
	err := r.db.WithContext(ctx).Raw(`
		SELECT (SELECT count(*)::int FROM feedback WHERE status = 'new') AS feedback,
		       (SELECT count(*)::int FROM sources WHERE enabled AND health = 'failing') AS sources,
		       (SELECT count(*)::int FROM receipts WHERE status = 'unknown') +
	       (SELECT count(*)::int FROM deliveries WHERE status = 'unknown') AS runs`).Scan(&counts).Error
	return counts, err
}

func (r *GormRepository) Audit(ctx context.Context, filter AuditFilter) (AuditResult, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	query := r.db.WithContext(ctx).Model(&model.AdminAuditLog{})
	if filter.Subject != nil {
		query = query.Where("subject = ?", *filter.Subject)
	}
	if filter.Action != nil {
		query = query.Where("action LIKE ?", *filter.Action+"%")
	}
	var rows []AuditRow
	err := query.Select("id, created_at, actor, action, subject, reason, before, after").
		Order("created_at DESC").Limit(100).Offset((filter.Page - 1) * 100).Find(&rows).Error
	if rows == nil {
		rows = []AuditRow{}
	}
	return AuditResult{Page: filter.Page, Rows: rows}, err
}
