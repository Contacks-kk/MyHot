package grouping

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"aihot/backend-go/internal/modelcall"
)

type EvaluationPairJudge interface {
	EvaluatePair(context.Context, string, string, Report, Report) (EvaluationPairJudgement, error)
}

type EvaluationUsageRepository interface {
	EvaluationUsage(context.Context, []int64) (EvaluationUsage, error)
}

type RelationEvaluationCase struct {
	CaseID     string    `json:"caseId"`
	Stratum    *string   `json:"stratum"`
	Gold       Relation  `json:"gold"`
	Decision   *Relation `json:"decision"`
	Confidence *float64  `json:"confidence"`
	A          *string   `json:"a"`
	B          *string   `json:"b"`
	Difference *string   `json:"difference"`
	ReceiptID  *int64    `json:"receiptId"`
	Reused     bool      `json:"reused"`
	Error      *string   `json:"error"`
}

type RelationEvaluationReport struct {
	Metrics         EvaluationMetrics        `json:"metrics"`
	StoryThresholds []EvaluationTieMetrics   `json:"storyThresholds"`
	Cases           []RelationEvaluationCase `json:"cases"`
	Reused          int                      `json:"reused"`
}

type RelationEvaluationSummary struct {
	Model        string  `json:"model"`
	N            int     `json:"n"`
	Evaluated    int     `json:"evaluated"`
	Errors       int     `json:"errors"`
	Accuracy     float64 `json:"accuracy"`
	MacroF1      float64 `json:"macroF1"`
	Reused       int     `json:"reused"`
	TokensIn     int64   `json:"tokensIn"`
	TokensOut    int64   `json:"tokensOut"`
	AvgLatencyMS int64   `json:"avgLatencyMs"`
	WallSeconds  int64   `json:"wallSeconds"`
}

type RelationEvaluationModelReport struct {
	Summary         RelationEvaluationSummary           `json:"summary"`
	ConfusionMatrix map[Relation]map[Relation]int       `json:"confusionMatrix"`
	PerClass        map[Relation]EvaluationClassMetrics `json:"perClass"`
	StoryThresholds []EvaluationTieMetrics              `json:"storyThresholds"`
	Cases           []RelationEvaluationCase            `json:"cases"`
}

type relationEvaluationCall struct {
	result EvaluationPairJudgement
	err    error
}

type cachedEvaluationJudge struct {
	calls map[string]relationEvaluationCall
}

/** EvaluatePair returns a previously computed judgement for a report pair. */
func (j cachedEvaluationJudge) EvaluatePair(_ context.Context, _, _ string, a, b Report) (EvaluationPairJudgement, error) {
	call, ok := j.calls[pairPrompt(a, b)]
	if !ok {
		return EvaluationPairJudgement{}, errors.New("evaluation pair result is missing")
	}
	return call.result, call.err
}

/** EvaluateRelationSetConcurrent evaluates distinct prompts with bounded concurrency and stable result order. */
func EvaluateRelationSetConcurrent(ctx context.Context, judge EvaluationPairJudge, modelKey string, rows []EvaluationGoldRow, thresholds []float64, concurrency int) (RelationEvaluationReport, error) {
	if judge == nil || concurrency <= 0 {
		return RelationEvaluationReport{}, errors.New("evaluation requires a judge and positive concurrency")
	}
	type request struct {
		caseID string
		prompt string
		a      Report
		b      Report
		call   relationEvaluationCall
	}
	requests := make([]request, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		a, err := row.A.Report()
		if err != nil {
			return RelationEvaluationReport{}, fmt.Errorf("case %s: %w", row.CaseID, err)
		}
		b, err := row.B.Report()
		if err != nil {
			return RelationEvaluationReport{}, fmt.Errorf("case %s: %w", row.CaseID, err)
		}
		prompt := pairPrompt(a, b)
		if _, ok := seen[prompt]; ok {
			continue
		}
		seen[prompt] = struct{}{}
		requests = append(requests, request{
			caseID: row.CaseID,
			prompt: prompt,
			a:      a,
			b:      b,
		})
	}
	indices := make(chan int, len(requests))
	for index := range requests {
		indices <- index
	}
	close(indices)
	var workers sync.WaitGroup
	for i := 0; i < min(concurrency, len(requests)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range indices {
				if ctx.Err() != nil {
					return
				}
				item := &requests[index]
				item.call.result, item.call.err = judge.EvaluatePair(ctx, item.caseID, modelKey, item.a, item.b)
			}
		}()
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return RelationEvaluationReport{}, err
	}
	calls := make(map[string]relationEvaluationCall, len(requests))
	for _, item := range requests {
		calls[item.prompt] = item.call
	}
	return EvaluateRelationSet(ctx, cachedEvaluationJudge{calls: calls}, modelKey, rows, thresholds)
}

/** RelationEvaluationUsage loads usage once for each receipt referenced in a report. */
func RelationEvaluationUsage(ctx context.Context, repository EvaluationUsageRepository, report RelationEvaluationReport) (EvaluationUsage, error) {
	if repository == nil {
		return EvaluationUsage{}, errors.New("evaluation usage repository is unavailable")
	}
	ids := make([]int64, 0, len(report.Cases))
	seen := make(map[int64]struct{})
	for _, item := range report.Cases {
		if item.ReceiptID == nil {
			continue
		}
		if _, exists := seen[*item.ReceiptID]; exists {
			continue
		}
		seen[*item.ReceiptID] = struct{}{}
		ids = append(ids, *item.ReceiptID)
	}
	return repository.EvaluationUsage(ctx, ids)
}

/** BuildRelationEvaluationModelReport assembles the legacy-compatible per-model report. */
func BuildRelationEvaluationModelReport(ctx context.Context, repository EvaluationUsageRepository, model string, elapsed time.Duration, report RelationEvaluationReport) (RelationEvaluationModelReport, error) {
	usage, err := RelationEvaluationUsage(ctx, repository, report)
	if err != nil {
		return RelationEvaluationModelReport{}, err
	}
	return RelationEvaluationModelReport{
		Summary: RelationEvaluationSummary{
			Model:        model,
			N:            report.Metrics.SampleSize,
			Evaluated:    report.Metrics.Evaluated,
			Errors:       report.Metrics.Errors,
			Accuracy:     report.Metrics.Accuracy,
			MacroF1:      report.Metrics.MacroF1,
			Reused:       report.Reused,
			TokensIn:     usage.TokensIn,
			TokensOut:    usage.TokensOut,
			AvgLatencyMS: usage.AvgLatencyMS,
			WallSeconds:  int64(math.Round(elapsed.Seconds())),
		},
		ConfusionMatrix: report.Metrics.ConfusionMatrix,
		PerClass:        report.Metrics.PerClass,
		StoryThresholds: report.StoryThresholds,
		Cases:           report.Cases,
	}, nil
}

/** EvaluateRelationSet runs one model against labelled pairs and builds case and metric results. */
func EvaluateRelationSet(ctx context.Context, judge EvaluationPairJudge, modelKey string, rows []EvaluationGoldRow, thresholds []float64) (RelationEvaluationReport, error) {
	if judge == nil {
		return RelationEvaluationReport{}, errors.New("relation evaluation judge is unavailable")
	}
	report := RelationEvaluationReport{
		Cases:           make([]RelationEvaluationCase, 0, len(rows)),
		StoryThresholds: make([]EvaluationTieMetrics, 0, len(thresholds)),
	}
	calls := make(map[string]relationEvaluationCall)
	predictions := make([]EvaluationPrediction, 0, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return RelationEvaluationReport{}, err
		}
		a, err := row.A.Report()
		if err != nil {
			return RelationEvaluationReport{}, fmt.Errorf("case %s: %w", row.CaseID, err)
		}
		b, err := row.B.Report()
		if err != nil {
			return RelationEvaluationReport{}, fmt.Errorf("case %s: %w", row.CaseID, err)
		}
		prompt := pairPrompt(a, b)
		call, shared := calls[prompt]
		if !shared {
			call.result, call.err = judge.EvaluatePair(ctx, row.CaseID, modelKey, a, b)
			calls[prompt] = call
		}
		if err := ctx.Err(); err != nil {
			return RelationEvaluationReport{}, err
		}
		item := RelationEvaluationCase{
			CaseID: row.CaseID,
			Gold:   row.Gold.Relation,
		}
		if row.SamplingContext != nil && row.SamplingContext.SamplingStratum != "" {
			item.Stratum = &row.SamplingContext.SamplingStratum
		}
		if call.err != nil {
			message := truncateUTF16(call.err.Error(), 300)
			item.Error = &message
			var invalid *JudgementOutputError
			var modelError *modelcall.OutputError
			switch {
			case errors.As(call.err, &invalid):
				item.ReceiptID = &invalid.ReceiptID
			case errors.As(call.err, &modelError):
				item.ReceiptID = &modelError.ReceiptID
			case call.result.ReceiptID != 0:
				item.ReceiptID = &call.result.ReceiptID
			}
		} else {
			item.Decision = &call.result.Relation
			item.Confidence = &call.result.Confidence
			item.A = &call.result.A
			item.B = &call.result.B
			item.Difference = &call.result.Difference
			item.ReceiptID = &call.result.ReceiptID
			item.Reused = shared || call.result.Reused
			if item.Reused {
				report.Reused++
			}
			predictions = append(predictions, EvaluationPrediction{
				CaseID:     row.CaseID,
				Gold:       row.Gold.Relation,
				Relation:   call.result.Relation,
				Confidence: call.result.Confidence,
				Stratum:    item.Stratum,
			})
		}
		report.Cases = append(report.Cases, item)
	}
	metrics, err := EvaluateRelations(predictions, len(rows))
	if err != nil {
		return RelationEvaluationReport{}, err
	}
	report.Metrics = metrics
	for _, threshold := range thresholds {
		report.StoryThresholds = append(report.StoryThresholds, EvaluateStoryTies(predictions, threshold))
	}
	return report, nil
}
