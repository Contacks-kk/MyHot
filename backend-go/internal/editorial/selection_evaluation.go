package editorial

import (
	"context"
	"errors"
	"sync"

	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/receipts"
)

type SelectionEvaluationResult struct {
	Row        SelectionGoldRow
	Output     *AnalysisOutput
	ReceiptIDs []int64
	Error      *string
}

type selectionRecordedClient struct {
	ModelClient
	receipts []int64
}

/** Call remembers every observable receipt, including unknown outcomes. */
func (c *selectionRecordedClient) Call(ctx context.Context, request modelcall.Request) (modelcall.Result, error) {
	result, err := c.ModelClient.Call(ctx, request)
	if err == nil && result.ReceiptID != 0 {
		c.receipts = append(c.receipts, result.ReceiptID)
	}
	var unknown *receipts.OutcomeUnknownError
	var output *modelcall.OutputError
	if errors.As(err, &unknown) {
		c.receipts = append(c.receipts, unknown.ReceiptID)
	} else if errors.As(err, &output) {
		c.receipts = append(c.receipts, output.ReceiptID)
	}
	return result, err
}

type sharedSelectionScore struct {
	done     chan struct{}
	scores   *ScoreResult
	receipts []int64
	err      error
}

type selectionScoreCache struct {
	mu      sync.Mutex
	entries map[string]*sharedSelectionScore
}

/** EvaluateSelection runs prefilters independently and shares matching score requests. */
func (s *AnalysisService) EvaluateSelection(ctx context.Context, model string, rows []SelectionGoldRow, concurrency int) ([]SelectionEvaluationResult, error) {
	if s == nil || s.models == nil || model == "" || concurrency <= 0 {
		return nil, errors.New("selection evaluation requires a model and positive concurrency")
	}
	cache := &selectionScoreCache{entries: make(map[string]*sharedSelectionScore)}
	results := make([]SelectionEvaluationResult, len(rows))
	indices := make(chan int, len(rows))
	for index := range rows {
		indices <- index
	}
	close(indices)
	var workers sync.WaitGroup
	for index := 0; index < min(concurrency, len(rows)); index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range indices {
				if ctx.Err() != nil {
					return
				}
				results[index] = s.evaluateSelectionCase(ctx, model, rows[index], cache)
			}
		}()
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

/** evaluateSelectionCase applies the live prefilter and the case-specific threshold. */
func (s *AnalysisService) evaluateSelectionCase(ctx context.Context, model string, row SelectionGoldRow, cache *selectionScoreCache) SelectionEvaluationResult {
	result := SelectionEvaluationResult{Row: row}
	input, err := SelectionInput(row)
	if err != nil {
		return failedSelection(result, err)
	}
	client := &selectionRecordedClient{ModelClient: s.models}
	service := *s
	service.models = client
	prefilter, err := service.runPrefilter(ctx, input, AnalysisOptions{})
	result.ReceiptIDs = append(result.ReceiptIDs, client.receipts...)
	if err != nil {
		return failedSelection(result, err)
	}
	run := AnalysisRun{Prefilter: prefilter}
	threshold, scoreable := s.selection.Threshold(input.Source.Tier)
	if prefilter.Label != "BLOCK" && scoreable {
		scores, ids, err := cache.score(ctx, s, input, model)
		result.ReceiptIDs = append(result.ReceiptIDs, ids...)
		if err != nil {
			return failedSelection(result, err)
		}
		if scores != nil {
			copy := *scores
			copy.Threshold = threshold
			run.Scores = &copy
		}
	}
	output := NormalizeAnalysis(run, s.entities)
	result.Output = &output
	return result
}

/** score reuses a single successful or failed score request for identical prompts. */
func (c *selectionScoreCache) score(ctx context.Context, service *AnalysisService, input Input, model string) (*ScoreResult, []int64, error) {
	key := BuildScoreInput(input)
	c.mu.Lock()
	entry, ok := c.entries[key]
	if !ok {
		entry = &sharedSelectionScore{done: make(chan struct{})}
		c.entries[key] = entry
	}
	c.mu.Unlock()
	if ok {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-entry.done:
			return entry.scores, entry.receipts, entry.err
		}
	}
	client := &selectionRecordedClient{ModelClient: service.models}
	copy := *service
	copy.models = client
	entry.scores, entry.err = copy.runScores(ctx, input, AnalysisOptions{ScoreModel: model})
	entry.receipts = client.receipts
	close(entry.done)
	return entry.scores, entry.receipts, entry.err
}

/** failedSelection records a bounded error alongside partial receipt evidence. */
func failedSelection(result SelectionEvaluationResult, err error) SelectionEvaluationResult {
	message := []rune(err.Error())
	if len(message) > 200 {
		message = message[:200]
	}
	text := string(message)
	result.Error = &text
	return result
}
