package storylinks

import (
	"context"
	"testing"
)

type repositoryFixture struct {
	days       int
	confidence float64
	count      int
	added      int
}

func (r *repositoryFixture) LinkRelatedStories(_ context.Context, days int, confidence float64, count int) (int, error) {
	r.days, r.confidence, r.count = days, confidence, count
	return r.added, nil
}

func TestRunPreservesRelatedStoryThresholdsAndResult(t *testing.T) {
	repository := &repositoryFixture{added: 3}
	result, err := NewService(repository).Run(context.Background())
	if err != nil || result.Added != 3 {
		t.Fatalf("Run = %#v, %v", result, err)
	}
	if repository.days != 14 || repository.confidence != 0.8 || repository.count != 2 {
		t.Fatalf("related-story thresholds changed: %#v", repository)
	}
}
