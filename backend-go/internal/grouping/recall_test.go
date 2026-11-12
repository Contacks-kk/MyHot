package grouping

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type recallRepositoryStub struct {
	pool       []PoolRow
	reading    []ReadingRow
	texts      map[string]string
	bodies     map[string]*string
	poolErr    error
	readingErr error
	textErr    error
	bodyErr    error
}

func (r *recallRepositoryStub) RecallPool(context.Context, time.Time) ([]PoolRow, error) {
	return r.pool, r.poolErr
}
func (r *recallRepositoryStub) SelectedReadingPool(context.Context, time.Time) ([]ReadingRow, error) {
	return r.reading, r.readingErr
}
func (r *recallRepositoryStub) ReportTexts(_ context.Context, ids []string) (map[string]string, error) {
	if r.textErr != nil {
		return nil, r.textErr
	}
	result := make(map[string]string, len(ids))
	for _, id := range ids {
		if value, ok := r.texts[id]; ok {
			result[id] = value
		}
	}
	return result, nil
}
func (r *recallRepositoryStub) ArticleBodies(_ context.Context, ids []string) (map[string]*string, error) {
	if r.bodyErr != nil {
		return nil, r.bodyErr
	}
	result := make(map[string]*string, len(ids))
	for _, id := range ids {
		if value, ok := r.bodies[id]; ok {
			result[id] = value
		}
	}
	return result, nil
}

type embeddingStoreStub struct {
	available bool
	vectors   map[string][]float32
	err       error
	inputs    []VectorInput
}

func (s *embeddingStoreStub) Available(context.Context) bool { return s.available }
func (s *embeddingStoreStub) Ensure(_ context.Context, inputs []VectorInput) (map[string][]float32, error) {
	s.inputs = inputs
	return s.vectors, s.err
}

func TestRecallFactsUsesLexicalFallbackAndKeepsStableTies(t *testing.T) {
	repository := &recallRepositoryStub{
		pool: []PoolRow{
			{ArticleID: "first", FactID: 11, StoryID: 21, FactTitle: "first fact"},
			{ArticleID: "duplicate", FactID: 11, StoryID: 21, FactTitle: "duplicate fact"},
			{ArticleID: "second", FactID: 12, StoryID: 22, FactTitle: "second fact"},
			{ArticleID: "third", FactID: 14, StoryID: 24, FactTitle: "third fact"},
			{ArticleID: "query", FactID: 13, StoryID: 23},
		},
		texts: map[string]string{
			"first":     "甲乙丙丁",
			"duplicate": "甲乙丙戊",
			"second":    "甲乙丙己",
			"third":     "甲乙丙庚",
		},
	}
	service := NewRecallService(repository, nil, func() time.Time { return time.Unix(123, 0) })
	rows, err := service.RecallFacts(context.Background(), "query", "甲乙丙丁", .99, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].FactID != 11 || rows[1].FactID != 12 || rows[2].FactID != 14 || rows[1].Score != rows[2].Score {
		t.Fatalf("fallback score/order/dedup changed: %#v", rows)
	}
	if rows[0].FactTitle != "first fact" {
		t.Fatalf("same-fact representative changed: %#v", rows[0])
	}
}

func TestRecallFactsUsesEmbeddingsAndAlwaysIncludesBoosts(t *testing.T) {
	repository := &recallRepositoryStub{
		pool: []PoolRow{
			{ArticleID: "one", FactID: 1, StoryID: 10, FactTitle: "one"},
			{ArticleID: "two", FactID: 2, StoryID: 20, FactTitle: "two"},
		},
		texts: map[string]string{"one": "report one", "two": "report two"},
	}
	embeddings := &embeddingStoreStub{
		available: true,
		vectors: map[string][]float32{
			"query": {1, 0}, "one": {1, 0}, "two": {0, 1},
		},
	}
	service := NewRecallService(repository, embeddings, nil)
	rows, err := service.RecallFacts(context.Background(), "query", "query report", RecallMinCosine, 10, []PoolRow{{ArticleID: "quoted", FactID: 3, StoryID: 30, FactTitle: "quoted"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].FactID != 1 || rows[0].Score != 1 || rows[1].FactID != 3 || rows[1].Score != 1 {
		t.Fatalf("embedding recall/boost contract changed: %#v", rows)
	}
	if len(embeddings.inputs) != 3 || embeddings.inputs[0].ID != "query" {
		t.Fatalf("embedding input set changed: %#v", embeddings.inputs)
	}
}

func TestRecallReturnsConfiguredEmbeddingFailure(t *testing.T) {
	want := errors.New("provider unavailable")
	service := NewRecallService(&recallRepositoryStub{
		pool: []PoolRow{{ArticleID: "candidate", FactID: 1}}, texts: map[string]string{"candidate": "candidate report"},
	}, &embeddingStoreStub{available: true, err: want}, nil)
	_, err := service.RecallFacts(context.Background(), "query", "query report", RecallMinCosine, 10, nil)
	if !errors.Is(err, want) {
		t.Fatalf("provider failure was swallowed: %v", err)
	}
}

func TestRecallSelectedBackgroundSortsLimitsAndTruncatesBody(t *testing.T) {
	old := time.Unix(123, 0).UTC()
	pool := []ReadingRow{
		{ArticleID: "lower", Title: "lower", At: &old},
		{ArticleID: "best", Title: "best", At: &old},
		{ArticleID: "third", Title: "third", At: &old},
		{ArticleID: "fourth", Title: "fourth", At: &old},
		{ArticleID: "query", Title: "query", At: &old},
	}
	texts := map[string]string{
		"lower": "甲乙丙丁戊己", "best": "甲乙丙丁", "third": "甲乙丙丁戊", "fourth": "甲乙丙丁戊庚",
	}
	body := strings.Repeat("界", 6001)
	repository := &recallRepositoryStub{reading: pool, texts: texts, bodies: map[string]*string{"best": &body}}
	embeddings := &embeddingStoreStub{available: true, vectors: map[string][]float32{
		"query": {1, 0}, "lower": {.7, .71414286}, "best": {1, 0}, "third": {.9, .4358899}, "fourth": {.8, .6},
	}}
	service := NewRecallService(repository, embeddings, func() time.Time { return old })
	rows, err := service.RecallSelectedBackground(context.Background(), "query", "query report", .6, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Report.Title != "best" || rows[1].Report.Title != "third" || rows[2].Report.Title != "fourth" {
		t.Fatalf("selected background ordering/limit changed: %#v", rows)
	}
	if rows[0].SourceText == nil || utf16Length(*rows[0].SourceText) != 6000 {
		t.Fatalf("source body limit changed: len=%v", rows[0].SourceText)
	}
}

func TestCosine32RejectsDifferentDimensionsAndZeroVectors(t *testing.T) {
	if Cosine32([]float32{1, 0}, []float32{1}) != 0 || Cosine32([]float32{0}, []float32{1}) != 0 || Cosine32([]float32{1, 0}, []float32{1, 0}) != 1 {
		t.Fatal("cosine dimension/zero/vector behavior changed")
	}
}
