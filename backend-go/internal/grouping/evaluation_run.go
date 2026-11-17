package grouping

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type RelationEvaluationOptions struct {
	Gold        string    `json:"gold"`
	Models      []string  `json:"models"`
	Split       string    `json:"split"`
	Limit       int       `json:"n"`
	Concurrency int       `json:"concurrency"`
	Seed        int64     `json:"seed"`
	Thresholds  []float64 `json:"thresholds"`
}

type RelationEvaluationMeta struct {
	Split         string    `json:"split"`
	N             int       `json:"n"`
	Seed          int64     `json:"seed"`
	Thresholds    []float64 `json:"thresholds"`
	PromptVersion string    `json:"promptVersion"`
	CreatedAt     time.Time `json:"createdAt"`
}

type RelationEvaluationRun struct {
	Meta   RelationEvaluationMeta                   `json:"meta"`
	Models map[string]RelationEvaluationModelReport `json:"models"`
}

type RelationRunJudge interface {
	EvaluationPairJudge
	EvaluationPromptVersion() (string, error)
}

type RelationEvaluationService struct {
	judge  RelationRunJudge
	usage  EvaluationUsageRepository
	models map[string]struct{}
}

/** NewRelationEvaluationService prepares the model judge and receipt usage repository. */
func NewRelationEvaluationService(judge RelationRunJudge, usage EvaluationUsageRepository, models []string) *RelationEvaluationService {
	allowed := make(map[string]struct{}, len(models))
	for _, model := range models {
		allowed[model] = struct{}{}
	}
	return &RelationEvaluationService{
		judge:  judge,
		usage:  usage,
		models: allowed,
	}
}

/** Run evaluates the sampled cases with each requested configured model. */
func (s *RelationEvaluationService) Run(ctx context.Context, options RelationEvaluationOptions) (RelationEvaluationRun, error) {
	if s == nil || s.judge == nil || s.usage == nil {
		return RelationEvaluationRun{}, errors.New("relation evaluation is unavailable")
	}
	if options.Limit <= 0 || options.Concurrency < 0 || len(options.Models) == 0 || len(options.Thresholds) == 0 {
		return RelationEvaluationRun{}, errors.New("evaluation requires a positive sample size, models and thresholds")
	}
	for _, threshold := range options.Thresholds {
		if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
			return RelationEvaluationRun{}, errors.New("thresholds must be between zero and one")
		}
	}
	for _, model := range options.Models {
		if strings.TrimSpace(model) == "" {
			return RelationEvaluationRun{}, errors.New("evaluation model cannot be empty")
		}
		if _, ok := s.models[model]; !ok {
			return RelationEvaluationRun{}, fmt.Errorf("unknown model %s", model)
		}
	}
	rows, err := ParseRelationGoldJSONL(options.Gold)
	if err != nil {
		return RelationEvaluationRun{}, err
	}
	sample := SampleRelationGold(rows, options.Split, options.Limit, uint32(options.Seed))
	if len(sample) == 0 {
		return RelationEvaluationRun{}, fmt.Errorf("no cases for split %s", options.Split)
	}
	version, err := s.judge.EvaluationPromptVersion()
	if err != nil {
		return RelationEvaluationRun{}, err
	}
	run := RelationEvaluationRun{
		Meta: RelationEvaluationMeta{
			Split:         options.Split,
			N:             len(sample),
			Seed:          options.Seed,
			Thresholds:    append([]float64(nil), options.Thresholds...),
			PromptVersion: version,
		},
		Models: make(map[string]RelationEvaluationModelReport, len(options.Models)),
	}
	for _, model := range options.Models {
		started := time.Now()
		concurrency := options.Concurrency
		if concurrency == 0 {
			concurrency = 6
		}
		result, err := EvaluateRelationSetConcurrent(ctx, s.judge, model, sample, options.Thresholds, concurrency)
		if err != nil {
			return RelationEvaluationRun{}, err
		}
		report, err := BuildRelationEvaluationModelReport(ctx, s.usage, model, time.Since(started), result)
		if err != nil {
			return RelationEvaluationRun{}, err
		}
		run.Models[model] = report
	}
	run.Meta.CreatedAt = time.Now().UTC()
	return run, nil
}
