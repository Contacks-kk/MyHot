package health

import "context"

type Service struct{ repository *Repository }

func NewService(repository *Repository) *Service { return &Service{repository: repository} }

func (s *Service) Check(ctx context.Context) error { return s.repository.Check(ctx) }
