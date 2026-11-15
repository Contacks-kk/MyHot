package adminlabels

import (
	"context"
)

// Service provides administrative inspection and diagnostics for labels and associated hotspots.
type Service struct {
	repository Repository
}

/** NewService creates a new Service instance using the given Repository. */
func NewService(repository Repository) *Service {
	return &Service{
		repository: repository,
	}
}

/** Overview returns stable label metrics, candidate promotion labels, and summary statistics. */
func (s *Service) Overview(ctx context.Context) (OverviewResult, error) {
	return s.repository.Overview(ctx)
}

/** Hotspots retrieves publications associated with a label, with fuzzy search and score ranking. */
func (s *Service) Hotspots(ctx context.Context, filter HotspotsFilter) (HotspotsResult, error) {
	return s.repository.Hotspots(ctx, filter)
}
