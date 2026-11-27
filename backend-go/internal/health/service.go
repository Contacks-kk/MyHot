package health

import (
	"context"

	"github.com/redis/go-redis/v9"
)

type Service struct{ repository *Repository }

func NewService(repository *Repository) *Service { return &Service{repository: repository} }

/** SetRedis forwards Redis health probing to the underlying repository. */
func (s *Service) SetRedis(client *redis.Client) {
	if s == nil || s.repository == nil {
		return
	}

	s.repository.SetRedis(client)
}

func (s *Service) Check(ctx context.Context) error { return s.repository.Check(ctx) }
