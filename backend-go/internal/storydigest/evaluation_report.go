package storydigest

import (
	"context"
	"errors"
	"time"

	"aihot/backend-go/internal/receipts"
)

type EvaluationUsageRepository interface {
	EvaluationUsage(context.Context, []int64) (receipts.EvaluationUsage, error)
}

type EvaluationReportCall struct {
	EvaluationCall
	receipts.EvaluationUsage
}

type EvaluationReportCase struct {
	CaseID     string                `json:"caseId"`
	StoryTitle string                `json:"storyTitle"`
	Input      string                `json:"input"`
	Reports    int                   `json:"reports"`
	Live       EvaluationReportCall  `json:"live"`
	Candidate  *EvaluationReportCall `json:"candidate,omitempty"`
}

type EvaluationPromptSummary struct {
	Calls     int `json:"calls"`
	Succeeded int `json:"succeeded"`
	Errors    int `json:"errors"`
	Reused    int `json:"reused"`
	receipts.EvaluationUsage
}

type EvaluationModelSummary struct {
	Live        EvaluationPromptSummary  `json:"live"`
	Candidate   *EvaluationPromptSummary `json:"candidate,omitempty"`
	WallSeconds int64                    `json:"wallSeconds"`
}

type EvaluationModelReport struct {
	Summary EvaluationModelSummary `json:"summary"`
	Cases   []EvaluationReportCase `json:"cases"`
}

type EvaluationReportPrompt struct {
	Version string `json:"version"`
	File    string `json:"file,omitempty"`
}

type EvaluationReportMeta struct {
	CreatedAt time.Time                         `json:"createdAt"`
	Cases     string                            `json:"cases"`
	Calls     int                               `json:"calls"`
	Prompts   map[string]EvaluationReportPrompt `json:"prompts"`
}

type EvaluationComparisonReport struct {
	Meta   EvaluationReportMeta             `json:"meta"`
	Models map[string]EvaluationModelReport `json:"models"`
}

/** BuildEvaluationReport adds recorded receipt usage to each case and model summary. */
func BuildEvaluationReport(ctx context.Context, repository EvaluationUsageRepository, run EvaluationRun, casesFile, candidateFile string, created time.Time) (EvaluationComparisonReport, error) {
	if repository == nil {
		return EvaluationComparisonReport{}, errors.New("evaluation usage repository is unavailable")
	}
	report := EvaluationComparisonReport{
		Meta: EvaluationReportMeta{
			CreatedAt: created.UTC(),
			Cases:     casesFile,
			Calls:     run.Calls,
			Prompts:   make(map[string]EvaluationReportPrompt, len(run.Prompts)),
		},
		Models: make(map[string]EvaluationModelReport, len(run.Models)),
	}
	for key, prompt := range run.Prompts {
		entry := EvaluationReportPrompt{Version: prompt.Version}
		if key == "candidate" {
			entry.File = candidateFile
		}
		report.Meta.Prompts[key] = entry
	}
	for model, results := range run.Models {
		entry, err := buildEvaluationModelReport(ctx, repository, results, run.WallSeconds[model])
		if err != nil {
			return EvaluationComparisonReport{}, err
		}
		report.Models[model] = entry
	}
	return report, nil
}

/** buildEvaluationModelReport preserves each call and summarizes distinct receipts. */
func buildEvaluationModelReport(ctx context.Context, repository EvaluationUsageRepository, results []EvaluationResult, wallSeconds int64) (EvaluationModelReport, error) {
	report := EvaluationModelReport{Cases: make([]EvaluationReportCase, 0, len(results))}
	liveCalls := make([]EvaluationCall, 0, len(results))
	candidateCalls := make([]EvaluationCall, 0, len(results))
	for _, result := range results {
		live, err := evaluationReportCall(ctx, repository, result.Live)
		if err != nil {
			return EvaluationModelReport{}, err
		}
		item := EvaluationReportCase{CaseID: result.CaseID, StoryTitle: result.StoryTitle, Input: result.Input, Reports: result.Reports, Live: live}
		liveCalls = append(liveCalls, result.Live)
		if result.Candidate != nil {
			candidate, err := evaluationReportCall(ctx, repository, *result.Candidate)
			if err != nil {
				return EvaluationModelReport{}, err
			}
			item.Candidate = &candidate
			candidateCalls = append(candidateCalls, *result.Candidate)
		}
		report.Cases = append(report.Cases, item)
	}
	var err error
	report.Summary.Live, err = summarizeEvaluationCalls(ctx, repository, liveCalls)
	if err != nil {
		return EvaluationModelReport{}, err
	}
	if len(candidateCalls) > 0 {
		candidate, err := summarizeEvaluationCalls(ctx, repository, candidateCalls)
		if err != nil {
			return EvaluationModelReport{}, err
		}
		report.Summary.Candidate = &candidate
	}
	report.Summary.WallSeconds = wallSeconds
	return report, nil
}

/** evaluationReportCall loads usage for a single receipt when one exists. */
func evaluationReportCall(ctx context.Context, repository EvaluationUsageRepository, call EvaluationCall) (EvaluationReportCall, error) {
	result := EvaluationReportCall{EvaluationCall: call}
	if call.ReceiptID == nil {
		return result, nil
	}
	usage, err := repository.EvaluationUsage(ctx, []int64{*call.ReceiptID})
	result.EvaluationUsage = usage
	return result, err
}

/** summarizeEvaluationCalls counts outcomes and loads each receipt only once. */
func summarizeEvaluationCalls(ctx context.Context, repository EvaluationUsageRepository, calls []EvaluationCall) (EvaluationPromptSummary, error) {
	summary := EvaluationPromptSummary{Calls: len(calls)}
	ids := make([]int64, 0, len(calls))
	seen := make(map[int64]bool)
	for _, call := range calls {
		if call.Error == nil {
			summary.Succeeded++
		} else {
			summary.Errors++
		}
		if call.Reused {
			summary.Reused++
		}
		if call.ReceiptID != nil && !seen[*call.ReceiptID] {
			ids = append(ids, *call.ReceiptID)
			seen[*call.ReceiptID] = true
		}
	}
	usage, err := repository.EvaluationUsage(ctx, ids)
	summary.EvaluationUsage = usage
	return summary, err
}
