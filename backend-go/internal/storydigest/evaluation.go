package storydigest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/receipts"
)

type EvaluationModelClient interface {
	ModelClient
	Complete(context.Context, int64) error
}

type EvaluationOptions struct {
	Cases       string   `json:"cases"`
	Models      []string `json:"models"`
	Candidate   *string  `json:"candidate,omitempty"`
	MaxCalls    int      `json:"maxCalls"`
	Concurrency int      `json:"concurrency"`
}

type EvaluationPrompt struct {
	Version string `json:"version"`
	System  string `json:"-"`
}

type EvaluationCall struct {
	Title     *string `json:"title"`
	Digest    *string `json:"digest"`
	ReceiptID *int64  `json:"receiptId"`
	Reused    bool    `json:"reused"`
	Error     *string `json:"error"`
}

type EvaluationResult struct {
	CaseID     string          `json:"caseId"`
	StoryTitle string          `json:"storyTitle"`
	Input      string          `json:"input"`
	Reports    int             `json:"reports"`
	Live       EvaluationCall  `json:"live"`
	Candidate  *EvaluationCall `json:"candidate,omitempty"`
}

type EvaluationRun struct {
	Calls       int                           `json:"calls"`
	Prompts     map[string]EvaluationPrompt   `json:"prompts"`
	Models      map[string][]EvaluationResult `json:"models"`
	WallSeconds map[string]int64              `json:"-"`
}

type EvaluationService struct {
	models  EvaluationModelClient
	pack    *editorial.PromptPack
	live    EvaluationPrompt
	allowed map[string]bool
}

/** NewEvaluationService prepares the production prompt and model allowlist. */
func NewEvaluationService(models EvaluationModelClient, pack *editorial.PromptPack, live Prompts, siteName string, allowed []string) *EvaluationService {
	keys := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		keys[key] = true
	}
	return &EvaluationService{
		models: models,
		pack:   pack,
		live: EvaluationPrompt{
			Version: live.Version,
			System:  strings.ReplaceAll(live.System, "{{siteName}}", siteName),
		},
		allowed: keys,
	}
}

/** Run evaluates each case against the live and optional candidate prompt. */
func (s *EvaluationService) Run(ctx context.Context, options EvaluationOptions) (EvaluationRun, error) {
	rows, prompts, err := s.prepare(options)
	if err != nil {
		return EvaluationRun{}, err
	}
	run := EvaluationRun{
		Calls:       len(rows) * len(options.Models) * len(prompts),
		Prompts:     make(map[string]EvaluationPrompt, len(prompts)),
		Models:      make(map[string][]EvaluationResult, len(options.Models)),
		WallSeconds: make(map[string]int64, len(options.Models)),
	}
	for key, prompt := range prompts {
		run.Prompts[key] = prompt
	}
	for _, model := range options.Models {
		started := time.Now()
		results, err := s.evaluateModel(ctx, model, rows, prompts, options.Concurrency)
		if err != nil {
			return EvaluationRun{}, err
		}
		run.Models[model] = results
		run.WallSeconds[model] = int64(time.Since(started).Seconds() + 0.5)
	}
	return run, nil
}

/** prepare validates every case and the complete call budget before making any calls. */
func (s *EvaluationService) prepare(options EvaluationOptions) ([]EvaluationCase, map[string]EvaluationPrompt, error) {
	if s == nil || s.models == nil || s.pack == nil || s.live.Version == "" {
		return nil, nil, errors.New("story digest evaluation is unavailable")
	}
	if options.MaxCalls <= 0 || options.Concurrency <= 0 || len(options.Models) == 0 {
		return nil, nil, errors.New("evaluation requires models, a positive call limit and concurrency")
	}
	for _, key := range options.Models {
		if !s.allowed[key] {
			return nil, nil, fmt.Errorf("unknown model %s", key)
		}
	}
	rows, err := ParseEvaluationCases(options.Cases)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, errors.New("no digest evaluation cases")
	}
	prompts := map[string]EvaluationPrompt{"live": s.live}
	if options.Candidate != nil {
		system, err := s.pack.RenderText("candidate", *options.Candidate, nil)
		if err != nil {
			return nil, nil, err
		}
		if system == s.live.System {
			return nil, nil, errors.New("candidate renders the same as the live prompt")
		}
		hash := sha256.Sum256([]byte(system))
		prompts["candidate"] = EvaluationPrompt{Version: "candidate@" + hex.EncodeToString(hash[:])[:10], System: system}
	}
	if len(rows) > options.MaxCalls/len(options.Models)/len(prompts) {
		return nil, nil, errors.New("evaluation exceeds the model call limit")
	}
	return rows, prompts, nil
}

/** evaluateModel runs independent prompt comparisons with bounded concurrency. */
func (s *EvaluationService) evaluateModel(ctx context.Context, model string, rows []EvaluationCase, prompts map[string]EvaluationPrompt, concurrency int) ([]EvaluationResult, error) {
	type task struct {
		row int
		key string
	}
	results := make([]EvaluationResult, len(rows))
	tasks := make(chan task, len(rows)*len(prompts))
	for index, row := range rows {
		form := "首次撰写"
		if row.InputMode != nil && *row.InputMode == "corrected" {
			form = "更正后重写（不带上一版综述）"
		} else if row.Story.PreviousDigest != nil && *row.Story.PreviousDigest != "" {
			form = "增量更新（带上一版综述）"
		}
		results[index] = EvaluationResult{CaseID: row.CaseID, StoryTitle: row.Story.Title, Input: form, Reports: len(row.Reports)}
		tasks <- task{row: index, key: "live"}
		if _, ok := prompts["candidate"]; ok {
			tasks <- task{row: index, key: "candidate"}
		}
	}
	close(tasks)
	var workers sync.WaitGroup
	for i := 0; i < min(concurrency, len(rows)*len(prompts)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range tasks {
				if ctx.Err() != nil {
					return
				}
				call := s.evaluateOne(ctx, model, rows[task.row], prompts[task.key])
				if task.key == "live" {
					results[task.row].Live = call
				} else {
					results[task.row].Candidate = &call
				}
			}
		}()
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

/** evaluateOne performs one receipt-backed model request without changing the story. */
func (s *EvaluationService) evaluateOne(ctx context.Context, model string, row EvaluationCase, prompt EvaluationPrompt) EvaluationCall {
	input, err := BuildEvaluationInput(row)
	if err != nil {
		return evaluationFailure(err, nil)
	}
	result, err := s.models.Call(ctx, modelcall.Request{
		Capability:    "digest",
		ModelKey:      model,
		Purpose:       "eval_story_digest",
		Subject:       "story-digest-eval:" + row.CaseID,
		PromptVersion: prompt.Version,
		System:        prompt.System,
		User:          input,
		Temperature:   floatPointer(0.3),
		MaxTokens:     1200,
	})
	if err != nil {
		var unknown *receipts.OutcomeUnknownError
		var output *modelcall.OutputError
		if errors.As(err, &unknown) {
			return evaluationFailure(err, &unknown.ReceiptID)
		}
		if errors.As(err, &output) {
			return evaluationFailure(err, &output.ReceiptID)
		}
		return evaluationFailure(err, nil)
	}
	output, err := parseOutput(result.Completion.Text)
	if err != nil {
		if rejectErr := s.models.Reject(context.WithoutCancel(ctx), result.ReceiptID, "unusable story digest output"); rejectErr != nil {
			err = errors.Join(err, rejectErr)
		}
		return evaluationFailure(err, &result.ReceiptID)
	}
	if err := s.models.Complete(ctx, result.ReceiptID); err != nil {
		return evaluationFailure(err, &result.ReceiptID)
	}
	return EvaluationCall{Title: &output.Title, Digest: &output.Digest, ReceiptID: &result.ReceiptID, Reused: result.Reused}
}

/** evaluationFailure records a failed call without losing its receipt identity. */
func evaluationFailure(err error, receiptID *int64) EvaluationCall {
	message := err.Error()
	if runes := []rune(message); len(runes) > 500 {
		message = string(runes[:500])
	}
	return EvaluationCall{ReceiptID: receiptID, Error: &message}
}
