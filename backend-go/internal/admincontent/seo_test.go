package admincontent

import (
	"context"
	"errors"
	"testing"
)

type seoRepository struct {
	input  SEOInput
	actor  string
	result *PublishResult
	err    error
}

func (r *seoRepository) ByID(context.Context, string) ([]Row, error)              { return nil, nil }
func (r *seoRepository) ByURL(context.Context, []string, []string) ([]Row, error) { return nil, nil }
func (r *seoRepository) ByTitle(context.Context, string) ([]Row, error)           { return nil, nil }
func (r *seoRepository) SetSEO(_ context.Context, _ string, input SEOInput, actor string) (*PublishResult, error) {
	r.input, r.actor = input, actor
	return r.result, r.err
}

func TestSetSEOValidatesReasonAndDelegatesAuditActor(t *testing.T) {
	repository := &seoRepository{result: &PublishResult{ArticleID: "article-1", Changed: true}}
	service := NewService(repository)
	if _, err := service.SetSEO(context.Background(), "article-1", SEOInput{Indexed: true, Reason: " \t"}, "admin:1"); !errors.Is(err, ErrInvalidSEO) {
		t.Fatalf("blank reason error = %v", err)
	}
	if repository.actor != "" {
		t.Fatalf("invalid input reached repository: actor=%q", repository.actor)
	}
	input := SEOInput{Indexed: true, Reason: " reviewed with source owner "}
	result, err := service.SetSEO(context.Background(), "article-1", input, "admin:1")
	if err != nil || result == nil || !result.Changed || repository.input != input || repository.actor != "admin:1" {
		t.Fatalf("SEO request was not preserved: result=%#v input=%#v actor=%q err=%v", result, repository.input, repository.actor, err)
	}
}

func TestSetSEORequiresRepositorySupport(t *testing.T) {
	service := NewService(chainRepository{})
	_, err := service.SetSEO(context.Background(), "article-1", SEOInput{Reason: "reviewed"}, "admin:1")
	if !errors.Is(err, ErrSEORepositoryUnavailable) {
		t.Fatalf("missing SEO repository error = %v", err)
	}
}
