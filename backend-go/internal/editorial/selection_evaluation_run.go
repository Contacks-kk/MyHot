package editorial

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SelectionEvaluationOptions struct {
	Gold        string   `json:"gold"`
	Models      []string `json:"models"`
	Limit       int      `json:"n"`
	Split       string   `json:"split"`
	Concurrency int      `json:"concurrency"`
	Seed        int64    `json:"seed"`
	SweepStart  int      `json:"sweepStart"`
	SweepEnd    int      `json:"sweepEnd"`
	Label       string   `json:"label,omitempty"`
	NoImport    bool     `json:"noImport,omitempty"`
}

type SelectionEvaluationService struct {
	analysis *AnalysisService
	usage    SelectionUsageRepository
	models   map[string]struct{}
}

/** NewSelectionEvaluationService binds production selection rules and allowed model keys. */
func NewSelectionEvaluationService(analysis *AnalysisService, usage SelectionUsageRepository, models []string) *SelectionEvaluationService {
	allowed := make(map[string]struct{}, len(models))
	for _, model := range models {
		allowed[model] = struct{}{}
	}
	return &SelectionEvaluationService{analysis: analysis, usage: usage, models: allowed}
}

/** SelectionEvaluationReportFileName returns a safe report basename for the requested split. */
func SelectionEvaluationReportFileName(split string, count int, timestamp int64) string {
	name := strings.Trim(strings.Map(func(character rune) rune {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			return character
		}
		return '-'
	}, split), "-")
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" {
		name = "all"
	}
	return fmt.Sprintf("selection-%s-%d-%d.json", name, count, timestamp)
}

/** Run samples the gold set and builds the legacy-compatible multi-model report. */
func (s *SelectionEvaluationService) Run(ctx context.Context, options SelectionEvaluationOptions) (SelectionEvaluationRun, error) {
	if s == nil || s.analysis == nil || s.usage == nil {
		return SelectionEvaluationRun{}, errors.New("selection evaluation is unavailable")
	}
	if options.Limit <= 0 || options.Concurrency <= 0 || len(options.Models) == 0 {
		return SelectionEvaluationRun{}, errors.New("evaluation requires a positive sample size, concurrency and models")
	}
	for _, model := range options.Models {
		if strings.TrimSpace(model) == "" {
			return SelectionEvaluationRun{}, errors.New("evaluation model cannot be empty")
		}
		if _, ok := s.models[model]; !ok {
			return SelectionEvaluationRun{}, fmt.Errorf("unknown model %s", model)
		}
	}
	rows, err := ParseSelectionGold(options.Gold)
	if err != nil {
		return SelectionEvaluationRun{}, err
	}
	sample := SampleSelectionGold(rows, options.Split, options.Limit, uint32(options.Seed))
	report := SelectionEvaluationRun{
		Meta: SelectionEvaluationMeta{
			Split:         options.Split,
			N:             len(sample),
			Seed:          options.Seed,
			PromptVersion: s.analysis.Versions.Selection,
		},
		Models: make(map[string]SelectionModelReport, len(options.Models)),
	}
	for _, model := range options.Models {
		if err := ctx.Err(); err != nil {
			return SelectionEvaluationRun{}, err
		}
		started := time.Now()
		results, err := s.analysis.EvaluateSelection(ctx, model, sample, options.Concurrency)
		if err != nil {
			return SelectionEvaluationRun{}, err
		}
		modelReport, err := BuildSelectionModelReport(ctx, s.usage, model, results, options.SweepStart, options.SweepEnd, time.Since(started))
		if err != nil {
			return SelectionEvaluationRun{}, err
		}
		report.AddModel(model, modelReport)
	}
	report.Meta.CreatedAt = time.Now().UTC()
	return report, nil
}
