package health

import (
	"context"

	"gorm.io/gorm"
)

type Repository struct{ database *gorm.DB }

func NewRepository(database *gorm.DB) *Repository { return &Repository{database: database} }

func (r *Repository) Check(ctx context.Context) error {
	if r.database == nil {
		return gorm.ErrInvalidDB
	}
	sqlDB, err := r.database.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}
