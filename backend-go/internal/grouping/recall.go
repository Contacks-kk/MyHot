package grouping

import (
	"context"
	"math"
	"sort"
	"time"
)

const (
	LexicalMinScore = 0.25
	RecallMinCosine = 0.6
	RecallTopFacts  = 10
)

type VectorInput struct {
	ID       string
	Text     string
	Revision *int
}

type EmbeddingStore interface {
	Available(context.Context) bool
	Ensure(context.Context, []VectorInput) (map[string][]float32, error)
}

type CachedEmbeddingStore interface {
	NeedsText(articleID string, revision int) bool
	Cached(articleIDs []string) map[string][]float32
}

type RecallRepository interface {
	RecallPool(context.Context, time.Time) ([]PoolRow, error)
	SelectedReadingPool(context.Context, time.Time) ([]ReadingRow, error)
	ReportTexts(context.Context, []string) (map[string]string, error)
	ArticleBodies(context.Context, []string) (map[string]*string, error)
}

type Recalled struct {
	FactID    int64
	StoryID   int64
	FactTitle string
	Score     float64
}

type ReadingContext struct {
	Report     Report
	SourceText *string
}

type RecallService struct {
	repository RecallRepository
	embeddings EmbeddingStore
	now        func() time.Time
}

func NewRecallService(repository RecallRepository, embeddings EmbeddingStore, now func() time.Time) *RecallService {
	if now == nil {
		now = time.Now
	}
	return &RecallService{repository: repository, embeddings: embeddings, now: now}
}

// RecallFacts finds recent live facts by report similarity and direct URL/reply/quote references.
// When embedding credentials are absent it uses the measured lexical threshold; a configured
// embedding provider error is returned so the worker can retry instead of silently changing modes.
func (s *RecallService) RecallFacts(ctx context.Context, queryID, queryText string, minScore float64, top int, boost []PoolRow) ([]Recalled, error) {
	if s == nil || s.repository == nil {
		return nil, ErrRecallUnavailable
	}
	if top <= 0 {
		return []Recalled{}, nil
	}
	pool, err := s.repository.RecallPool(ctx, s.now())
	if err != nil {
		return nil, err
	}
	pool = withoutArticle(pool, queryID)
	scores, err := s.similarReports(ctx, queryID, queryText, pool, minScore)
	if err != nil {
		return nil, err
	}
	best := make(map[int64]Recalled, len(pool)+len(boost))
	order := make([]int64, 0, len(pool)+len(boost))
	consider := func(row PoolRow, score float64) {
		previous, exists := best[row.FactID]
		if !exists || score > previous.Score {
			if !exists {
				order = append(order, row.FactID)
			}
			best[row.FactID] = Recalled{FactID: row.FactID, StoryID: row.StoryID, FactTitle: row.FactTitle, Score: score}
		}
	}
	for _, row := range pool {
		if score, ok := scores[row.ArticleID]; ok {
			consider(row, score)
		}
	}
	for _, row := range boost {
		consider(row, 1)
	}
	result := make([]Recalled, 0, len(best))
	for _, factID := range order {
		result = append(result, best[factID])
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Score > result[j].Score })
	if len(result) > top {
		result = result[:top]
	}
	return result, nil
}

func (s *RecallService) RecallSelectedBackground(ctx context.Context, queryID, queryText string, minScore float64, top int) ([]ReadingContext, error) {
	if s == nil || s.repository == nil {
		return nil, ErrRecallUnavailable
	}
	if top <= 0 {
		return []ReadingContext{}, nil
	}
	now := s.now()
	pool, err := s.repository.SelectedReadingPool(ctx, now)
	if err != nil {
		return nil, err
	}
	poolRows := make([]PoolRow, 0, len(pool))
	for _, row := range pool {
		if row.ArticleID != queryID {
			poolRows = append(poolRows, PoolRow{ArticleID: row.ArticleID, Revision: row.Revision})
		}
	}
	texts, err := s.reportTexts(ctx, poolRows)
	if err != nil {
		return nil, err
	}
	scores, err := s.scoreReports(ctx, queryID, queryText, poolRows, texts, minScore)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(pool, func(i, j int) bool {
		return scores[pool[i].ArticleID] > scores[pool[j].ArticleID]
	})
	limit := min(top, 3)
	selected := make([]ReadingRow, 0, limit)
	for _, row := range pool {
		if row.ArticleID == queryID {
			continue
		}
		if _, ok := scores[row.ArticleID]; !ok {
			continue
		}
		selected = append(selected, row)
		if len(selected) == limit {
			break
		}
	}
	if len(selected) == 0 {
		return []ReadingContext{}, nil
	}
	bodies, err := s.repository.ArticleBodies(ctx, idsForReading(selected))
	if err != nil {
		return nil, err
	}
	result := make([]ReadingContext, 0, len(selected))
	for _, row := range selected {
		body := bodies[row.ArticleID]
		if body != nil {
			limited := truncateUTF16(*body, 6000)
			body = &limited
		}
		result = append(result, ReadingContext{Report: Report{
			Title: row.Title, Summary: row.Summary, Source: row.Source, FirstParty: row.FirstParty,
			At: row.At, Scope: stringValue(row.Scope),
		}, SourceText: body})
	}
	return result, nil
}

func (s *RecallService) similarReports(ctx context.Context, queryID, queryText string, pool []PoolRow, minScore float64) (map[string]float64, error) {
	scores := make(map[string]float64)
	if len(pool) == 0 {
		return scores, nil
	}
	texts, err := s.reportTexts(ctx, pool)
	if err != nil {
		return nil, err
	}
	return s.scoreReports(ctx, queryID, queryText, pool, texts, minScore)
}

func (s *RecallService) reportTexts(ctx context.Context, pool []PoolRow) (map[string]string, error) {
	ids := uniqueArticleIDs(pool)
	cache, cached := s.embeddings.(CachedEmbeddingStore)
	if s.embeddings == nil || !s.embeddings.Available(ctx) || !cached {
		return s.repository.ReportTexts(ctx, ids)
	}
	uncached := make([]string, 0, len(ids))
	rows := make(map[string]PoolRow, len(pool))
	for _, row := range pool {
		rows[row.ArticleID] = row
	}
	for _, id := range ids {
		if row, ok := rows[id]; !ok || cache.NeedsText(id, row.Revision) {
			uncached = append(uncached, id)
		}
	}
	return s.repository.ReportTexts(ctx, uncached)
}

func (s *RecallService) scoreReports(ctx context.Context, queryID, queryText string, pool []PoolRow, texts map[string]string, minScore float64) (map[string]float64, error) {
	scores := make(map[string]float64)
	if len(pool) == 0 {
		return scores, nil
	}
	if s.embeddings == nil || !s.embeddings.Available(ctx) {
		for _, row := range pool {
			text, ok := texts[row.ArticleID]
			if !ok {
				continue
			}
			score := LexicalSimilarity(queryText, text)
			if score >= LexicalMinScore {
				scores[row.ArticleID] = score
			}
		}
		return scores, nil
	}
	inputs := make([]VectorInput, 0, len(pool)+1)
	inputs = append(inputs, VectorInput{ID: queryID, Text: queryText})
	seen := map[string]struct{}{queryID: {}}
	for _, row := range pool {
		if _, exists := seen[row.ArticleID]; exists {
			continue
		}
		if text, ok := texts[row.ArticleID]; ok {
			revision := row.Revision
			inputs = append(inputs, VectorInput{ID: row.ArticleID, Text: text, Revision: &revision})
			seen[row.ArticleID] = struct{}{}
		}
	}
	vectors, err := s.embeddings.Ensure(ctx, inputs)
	if err != nil {
		return nil, err
	}
	query := vectors[queryID]
	if len(query) == 0 {
		return scores, nil
	}
	if cache, ok := s.embeddings.(CachedEmbeddingStore); ok {
		for id, vector := range cache.Cached(uniqueArticleIDs(pool)) {
			vectors[id] = vector
		}
	}
	for _, row := range pool {
		vector := vectors[row.ArticleID]
		if len(vector) == 0 {
			continue
		}
		score := Cosine32(query, vector)
		if score >= minScore {
			scores[row.ArticleID] = score
		}
	}
	return scores, nil
}

func Cosine32(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for index := range a {
		x, y := float64(a[index]), float64(b[index])
		dot += x * y
		normA += x * x
		normB += y * y
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func withoutArticle(pool []PoolRow, articleID string) []PoolRow {
	result := make([]PoolRow, 0, len(pool))
	for _, row := range pool {
		if row.ArticleID != articleID {
			result = append(result, row)
		}
	}
	return result
}

func idsForReading(rows []ReadingRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ArticleID)
	}
	return ids
}
