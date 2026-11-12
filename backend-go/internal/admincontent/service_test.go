package admincontent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type commandRepository struct {
	visibility VisibilityInput
	override   OverrideInput
	step       string
	requestID  string
	actor      string
	calls      int
}

func (r *commandRepository) SetVisibility(_ context.Context, _ string, input VisibilityInput, actor string) (*PublishResult, bool, error) {
	r.visibility, r.actor, r.calls = input, actor, r.calls+1
	return &PublishResult{ArticleID: "article-1"}, true, nil
}
func (r *commandRepository) OverrideFields(_ context.Context, _ string, input OverrideInput, actor string) (*PublishResult, bool, error) {
	r.override, r.actor, r.calls = input, actor, r.calls+1
	return &PublishResult{ArticleID: "article-1"}, true, nil
}
func (r *commandRepository) Rerun(_ context.Context, _, step, requestID, actor string) (*RerunResult, bool, error) {
	r.step, r.requestID, r.actor, r.calls = step, requestID, actor, r.calls+1
	return &RerunResult{}, true, nil
}
func (*commandRepository) Detach(context.Context, string, string, string) (map[string]int, bool, error) {
	return map[string]int{"detached": 1}, true, nil
}
func (*commandRepository) MergeStories(context.Context, int64, int64, string, string) (map[string]int, bool, error) {
	return map[string]int{"moved": 1}, true, nil
}

type memoryRepository struct {
	calls      []string
	id         string
	urls       []string
	identities []string
	title      string
	rows       []Row
}

func (r *memoryRepository) ByID(_ context.Context, id string) ([]Row, error) {
	r.calls = append(r.calls, "id")
	r.id = id
	return r.rows, nil
}
func (r *memoryRepository) ByURL(_ context.Context, urls, identities []string) ([]Row, error) {
	r.calls = append(r.calls, "url")
	r.urls, r.identities = urls, identities
	return r.rows, nil
}
func (r *memoryRepository) ByTitle(_ context.Context, title string) ([]Row, error) {
	r.calls = append(r.calls, "title")
	r.title = title
	return r.rows, nil
}
func TestSearchPrefersExactIDsAndSkipsEmptyQueries(t *testing.T) {
	repo := &memoryRepository{rows: []Row{{ID: "article-1"}}}
	service := NewService(repo)
	rows, err := service.Search(context.Background(), " article-1 ")
	if err != nil || len(rows) != 1 || repo.id != "article-1" || len(repo.calls) != 1 || repo.calls[0] != "id" {
		t.Fatalf("exact ID lookup did not short circuit: rows=%#v calls=%v err=%v", rows, repo.calls, err)
	}
	repo.calls = nil
	rows, err = service.Search(context.Background(), "  ")
	if err != nil || len(rows) != 0 || rows == nil || len(repo.calls) != 0 {
		t.Fatalf("empty search should return a stable empty result: rows=%#v calls=%v err=%v", rows, repo.calls, err)
	}
}

func TestSearchNormalizesArticleURLsAndKeepsFragmentIdentity(t *testing.T) {
	repo := &memoryRepository{}
	_, err := NewService(repo).Search(context.Background(), "http://www.example.com/a?utm_source=fixture&b=2#part")
	if err != nil || len(repo.calls) != 2 || repo.calls[0] != "url" || repo.calls[1] != "title" {
		t.Fatalf("URL lookup fallback changed: calls=%v err=%v", repo.calls, err)
	}
	if len(repo.urls) != 2 || repo.urls[0] != "http://www.example.com/a?utm_source=fixture&b=2#part" || repo.urls[1] != "https://example.com/a?b=2" {
		t.Fatalf("URL candidates changed: %#v", repo.urls)
	}
	if len(repo.identities) != 2 || repo.identities[0] != "url:https://example.com/a?b=2" || repo.identities[1] != "url:https://example.com/a?b=2#part" {
		t.Fatalf("normalized material identities changed: %#v", repo.identities)
	}
}

func TestTwitterStatusUsesMaterialIdentity(t *testing.T) {
	urls, identities, ok := contentURLKeys("https://mobile.x.com/example/status/123456?ref=share")
	if !ok || len(urls) != 1 || len(identities) != 1 || identities[0] != "x:123456" {
		t.Fatalf("X URL identity changed: urls=%#v identities=%#v ok=%v", urls, identities, ok)
	}
}

func TestContentCommandsValidateBeforeRepositoryAndKeepRequestIdentity(t *testing.T) {
	repo := &commandRepository{}
	service := NewService(&memoryRepository{})
	service.SetCommands(repo, []string{"ai", "business"})
	ctx := context.Background()
	if _, _, err := service.SetVisibility(ctx, "article-1", VisibilityInput{Visibility: "hidden", Reason: "reviewed", Version: 0}, "admin:41"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unsupported visibility accepted: %v", err)
	}
	if _, _, err := service.OverrideFields(ctx, "article-1", OverrideInput{Fields: map[string]json.RawMessage{"category": json.RawMessage(`"unknown"`)}, Reason: "reviewed"}, "admin:41"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unknown category accepted: %v", err)
	}
	if _, _, err := service.Rerun(ctx, "article-1", "analyze", "bad", "admin:41"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("invalid idempotency key accepted: %v", err)
	}
	if repo.calls != 0 {
		t.Fatalf("invalid commands reached the repository: calls=%d", repo.calls)
	}
	if _, _, err := service.Rerun(ctx, "article-1", "analyze", "admin-request-123", "admin:41"); err != nil {
		t.Fatal(err)
	}
	if repo.calls != 1 || repo.step != "analyze" || repo.requestID != "admin-request-123" || repo.actor != "admin:41" {
		t.Fatalf("valid idempotent command was not preserved: %#v", repo)
	}
}
