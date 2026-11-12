package mpcollector

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"aihot/backend-go/internal/materials"
)

type memoryRepository struct {
	source       Source
	known        map[string]*KnownArticle
	stored       []materials.Input
	updatedRetry map[string]BodyRetry
	cleared      []string
	finished     string
	softFailure  bool
	found        int
	created      int
}

func (r *memoryRepository) DueSources(context.Context) ([]Source, error) {
	return []Source{r.source}, nil
}
func (r *memoryRepository) Begin(_ context.Context, id, _ string) (*Run, bool, error) {
	if id != r.source.ID || !r.source.Enabled {
		return nil, false, nil
	}
	return &Run{ID: 9, Source: r.source}, true, nil
}
func (r *memoryRepository) KnownArticle(_ context.Context, identity string) (*KnownArticle, error) {
	return r.known[identity], nil
}
func (r *memoryRepository) UpdateBodyRetry(_ context.Context, id string, retry BodyRetry) error {
	r.updatedRetry[id] = retry
	return nil
}
func (r *memoryRepository) ClearBodyRetry(_ context.Context, id string) error {
	r.cleared = append(r.cleared, id)
	return nil
}
func (r *memoryRepository) FinishSuccess(_ context.Context, _ *Run, found, created int, _ json.RawMessage) error {
	r.finished, r.found, r.created = "ok", found, created
	return nil
}
func (r *memoryRepository) FinishFailure(_ context.Context, _ *Run, found, created int, soft bool, _ string) error {
	r.finished, r.found, r.created, r.softFailure = "failed", found, created, soft
	return nil
}

type memoryMaterialRepository struct {
	created bool
	inputs  []materials.Input
}

func (r *memoryMaterialRepository) StorePrepared(_ context.Context, input materials.Input, _ materials.TimelineDecision, _ string) (materials.Result, error) {
	r.inputs = append(r.inputs, input)
	return materials.Result{ArticleID: fmt.Sprintf("article-%d", len(r.inputs)), Created: r.created}, nil
}

type fakeProvider struct {
	history    History
	articles   map[string]Article
	articleErr map[string]error
	identities []string
}

func pointer[T any](value T) *T { return &value }

func (p *fakeProvider) History(_ context.Context, _, _, _ string) (History, error) {
	return p.history, nil
}
func (p *fakeProvider) Article(_ context.Context, rawURL, _, identity string) (Article, error) {
	p.identities = append(p.identities, identity)
	if err := p.articleErr[rawURL]; err != nil {
		return Article{}, err
	}
	return p.articles[rawURL], nil
}

func TestDueSourceIDsUsesPerAccountInterval(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	repository := &memoryRepository{source: Source{ID: "mp-1", Enabled: true, IntervalMinutes: 10,
		Cursor: json.RawMessage(`{"lastCheckedAt":"2026-10-08T11:49:00Z"}`)}}
	service := NewService(repository, nil, &fakeProvider{})
	service.now = func() time.Time { return now }
	ids, err := service.DueSourceIDs(context.Background())
	if err != nil || len(ids) != 1 || ids[0] != "mp-1" {
		t.Fatalf("source older than interval was not due: ids=%v err=%v", ids, err)
	}
	repository.source.Cursor = json.RawMessage(`{"lastCheckedAt":"2026-10-08T11:50:00Z"}`)
	ids, err = service.DueSourceIDs(context.Background())
	if err != nil || len(ids) != 0 {
		t.Fatalf("source checked at interval boundary should wait: ids=%v err=%v", ids, err)
	}
}

func TestCheckAccountStoresBoundedFirstImportAndBody(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	repository := &memoryRepository{source: Source{ID: "mp-demo", Name: "Demo", Enabled: true, FirstCheck: true,
		IntervalMinutes: 60, Config: json.RawMessage(`{"ghid":"ghid-demo"}`)}, updatedRetry: map[string]BodyRetry{}, known: map[string]*KnownArticle{}}
	materialsRepository := &memoryMaterialRepository{created: true}
	posts := []Post{
		{URL: "https://mp.weixin.qq.com/s/old", Title: "old", PostTime: now.Add(-8 * 24 * time.Hour).Unix()},
		{URL: "https://mp.weixin.qq.com/s/recent?from=timeline", Title: "listed title", Digest: pointer("short intro"), Serial: pointer("serial-1"), PostTime: now.Add(-2 * time.Hour).Unix(), Position: pointer(3), Original: pointer(1), ItemShowType: pointer(0)},
	}
	provider := &fakeProvider{history: History{Posts: posts}, articles: map[string]Article{
		posts[1].URL: {Title: "provider title", Content: `<p>Paragraph &amp; body</p>`, Author: "publisher", Pubtime: "ignored"},
	}, articleErr: map[string]error{}}
	service := NewService(repository, materials.NewService(materialsRepository), provider)
	service.now = func() time.Time { return now }
	if err := service.CheckAccount(context.Background(), repository.source.ID); err != nil {
		t.Fatal(err)
	}
	if repository.finished != "ok" || repository.found != 2 || repository.created != 1 || len(materialsRepository.inputs) != 1 {
		t.Fatalf("check result repository=%+v stored=%d", repository, len(materialsRepository.inputs))
	}
	input := materialsRepository.inputs[0]
	if input.Title != "listed title" || input.Author != "publisher" || input.Excerpt != "short intro" || input.BodyStatus != "ok" || input.Backfill != "first-import" {
		t.Fatalf("stored source fields differ from the existing importer: %+v", input)
	}
	if input.BodyText != "Paragraph & body" || input.BodyHTML == "" || len(provider.identities) != 1 || provider.identities[0] != "serial-1" {
		t.Fatalf("stored body or paid identity is wrong: input=%+v identities=%v", input, provider.identities)
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(input.Raw, &raw); err != nil || raw["dajiala"]["position"] != float64(3) || raw["dajiala"]["itemShowType"] != float64(0) {
		t.Fatalf("Dajiala provenance was not preserved: %s (%v)", input.Raw, err)
	}
}

func TestCheckAccountKeepsRetryableBodyFailureForNewArticle(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	post := Post{URL: "https://mp.weixin.qq.com/s/retry", Title: "story", PostTime: now.Add(-time.Hour).Unix()}
	repository := &memoryRepository{source: Source{ID: "mp-demo", Enabled: true, IntervalMinutes: 60, Config: json.RawMessage(`{"wxid":"wxid-demo"}`)}, known: map[string]*KnownArticle{}, updatedRetry: map[string]BodyRetry{}}
	materialsRepository := &memoryMaterialRepository{created: true}
	provider := &fakeProvider{history: History{Posts: []Post{post}}, articles: map[string]Article{}, articleErr: map[string]error{
		post.URL: &DajialaError{Message: "Dajiala article detail rate limited", Retryable: true, Rejected: true},
	}}
	service := NewService(repository, materials.NewService(materialsRepository), provider)
	service.now = func() time.Time { return now }
	if err := service.CheckAccount(context.Background(), repository.source.ID); err != nil {
		t.Fatal(err)
	}
	if len(materialsRepository.inputs) != 1 || materialsRepository.inputs[0].BodyStatus != "none" || materialsRepository.inputs[0].BodyText != "" {
		t.Fatalf("article without a body was not retained: %#v", materialsRepository.inputs)
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(materialsRepository.inputs[0].Raw, &raw); err != nil {
		t.Fatal(err)
	}
	retry, ok := raw["dajiala"]["bodyRetry"].(map[string]any)
	if !ok || retry["attempts"] != float64(1) || repository.finished != "ok" {
		t.Fatalf("passing body failure must be retried on a later account check: raw=%s repository=%+v", materialsRepository.inputs[0].Raw, repository)
	}
}

func TestCheckAccountRetriesKnownBodyAndCapsNewReads(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	posts := make([]Post, 10)
	articles := make(map[string]Article)
	for i := range posts {
		posts[i] = Post{URL: fmt.Sprintf("https://mp.weixin.qq.com/s/%d", i), Title: fmt.Sprintf("story %d", i), Serial: pointer(fmt.Sprintf("serial-%d", i)), PostTime: now.Add(-time.Duration(i+1) * time.Minute).Unix()}
		articles[posts[i].URL] = Article{Content: "article body", Author: "publisher"}
	}
	repository := &memoryRepository{source: Source{ID: "mp-demo", Enabled: true, IntervalMinutes: 60, Config: json.RawMessage(`{"ghid":"ghid"}`)}, known: map[string]*KnownArticle{}, updatedRetry: map[string]BodyRetry{}}
	identity, ok := materials.IdentityKeyForURL(posts[0].URL)
	if !ok {
		t.Fatal("fixture URL did not normalize")
	}
	repository.known[identity] = &KnownArticle{ID: "existing", BodyStatus: "none", DiscoveredAt: now.Add(-time.Hour), Retry: &BodyRetry{Attempts: 1}}
	materialsRepository := &memoryMaterialRepository{created: false}
	provider := &fakeProvider{history: History{Posts: posts}, articles: articles, articleErr: map[string]error{}}
	service := NewService(repository, materials.NewService(materialsRepository), provider)
	service.now = func() time.Time { return now }
	if err := service.CheckAccount(context.Background(), repository.source.ID); err != nil {
		t.Fatal(err)
	}
	if len(provider.identities) != maxNewPerCheck || len(materialsRepository.inputs) != maxNewPerCheck {
		t.Fatalf("check should make at most %d body reads, got provider=%d materials=%d", maxNewPerCheck, len(provider.identities), len(materialsRepository.inputs))
	}
	if len(repository.cleared) != 1 || repository.cleared[0] != "existing" {
		t.Fatalf("successful retry did not clear its retry marker: %v", repository.cleared)
	}
}
