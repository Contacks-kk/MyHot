package adminops

import (
	"context"
	"encoding/json"
	"time"
)

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

type NavCounts struct {
	Feedback int `json:"feedback"`
	Sources  int `json:"sources"`
	Runs     int `json:"runs"`
}

type AuditFilter struct {
	Subject *string
	Action  *string
	Page    int
}

type AuditRow struct {
	ID        int64           `json:"id"`
	CreatedAt time.Time       `json:"created_at"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Subject   *string         `json:"subject"`
	Reason    *string         `json:"reason"`
	Before    json.RawMessage `json:"before"`
	After     json.RawMessage `json:"after"`
}

type AuditResult struct {
	Page int        `json:"page"`
	Rows []AuditRow `json:"rows"`
}

func (s *Service) NavCounts(ctx context.Context) (NavCounts, error) {
	return s.repository.NavCounts(ctx)
}

func (s *Service) Audit(ctx context.Context, filter AuditFilter) (AuditResult, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	return s.repository.Audit(ctx, filter)
}
