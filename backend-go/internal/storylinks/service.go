// Package storylinks maintains reciprocal links between live stories whose reports
// repeatedly identify the same event without merging the stories.
package storylinks

import "context"

type Repository interface {
	LinkRelatedStories(context.Context, int, float64, int) (int, error)
}

type Result struct {
	Added int `json:"added"`
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) Run(ctx context.Context) (Result, error) {
	added, err := s.repository.LinkRelatedStories(ctx, 14, 0.8, 2)
	if err != nil {
		return Result{}, err
	}
	return Result{Added: added}, nil
}

var _ Repository = (*GormRepository)(nil)
