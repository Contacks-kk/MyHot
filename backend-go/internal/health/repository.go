package health

import (
	"context"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Repository struct {
	database *gorm.DB
	redis    *redis.Client
}

/** NewRepository creates a health repository checking PostgreSQL connectivity. */
func NewRepository(database *gorm.DB) *Repository {
	return &Repository{
		database: database,
	}
}

/** SetRedis attaches the Redis client verified during API readiness checks. */
func (r *Repository) SetRedis(client *redis.Client) {
	if r == nil {
		return
	}

	r.redis = client
}

/** Check verifies PostgreSQL and any attached Redis connection. */
func (r *Repository) Check(ctx context.Context) error {
	if r.database == nil {
		return gorm.ErrInvalidDB
	}
	sqlDB, err := r.database.DB()
	if err != nil {
		return err
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return err
	}
	if r.redis != nil {
		return r.redis.Ping(ctx).Err()
	}
	return nil
}
