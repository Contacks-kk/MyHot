package adminselectbench

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Repository interface {
	List(context.Context) ([]RunListEntry, error)
	Run(context.Context, string, Filters) (*RunCases, error)
	Import(context.Context, ImportData, string) error
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

type RunSummary struct {
	ID            string          `json:"id"`
	Label         string          `json:"label"`
	Split         *string         `json:"split"`
	SampleSize    int             `json:"sample_size"`
	Seed          *int64          `json:"seed"`
	PromptVersion *string         `json:"prompt_version"`
	Models        []string        `json:"models"`
	Summary       json.RawMessage `json:"summary"`
	CreatedAt     time.Time       `json:"created_at"`
	ImportedBy    *string         `json:"imported_by"`
}

type RunListEntry struct {
	ID            string          `json:"id"`
	Label         string          `json:"label"`
	Split         *string         `json:"split"`
	SampleSize    int             `json:"sample_size"`
	PromptVersion *string         `json:"prompt_version"`
	Models        []string        `json:"models"`
	Summary       json.RawMessage `json:"summary"`
	CreatedAt     time.Time       `json:"created_at"`
	ImportedBy    *string         `json:"imported_by"`
	Cases         int             `json:"cases"`
}

type Decision struct {
	Decision  *string `json:"decision"`
	Score     *int    `json:"score"`
	Relevance *string `json:"relevance"`
	Category  *string `json:"category"`
	Reason    *string `json:"reason"`
	Error     *string `json:"error"`
	ReceiptID *int64  `json:"receiptId"`
}

type CaseRow struct {
	CaseID  string              `json:"case_id"`
	Title   string              `json:"title"`
	Stratum *string             `json:"stratum"`
	Gold    string              `json:"gold"`
	ByModel map[string]Decision `json:"by_model"`
}

type Stratum struct {
	Stratum *string `json:"stratum"`
	N       int     `json:"n"`
}

type RunCases struct {
	Run    RunSummary `json:"run"`
	Rows   []CaseRow  `json:"rows"`
	Strata []Stratum  `json:"strata"`
}

type Filters struct {
	Model    string
	Outcome  string
	Stratum  *string
	Disagree bool
}

type ImportData struct {
	ID         string
	Label      string
	Split      *string
	SampleSize int
	Seed       *int64
	Prompt     *string
	Models     []string
	Summary    json.RawMessage
	Results    []ImportResult
}

type ImportResult struct {
	Model     string
	CaseID    string
	Title     string
	Stratum   *string
	Gold      string
	Decision  *string
	Score     *int
	Relevance *string
	Category  *string
	Reason    *string
	ReceiptID *int64
	Error     *string
}

type report struct {
	Meta   map[string]any         `json:"meta"`
	Models map[string]modelReport `json:"models"`
}

type modelReport struct {
	Summary map[string]any `json:"summary"`
	Sweep   []any          `json:"sweep"`
	Cases   []reportCase   `json:"cases"`
}

type reportCase struct {
	CaseID    string  `json:"caseId"`
	Title     string  `json:"title"`
	Stratum   *string `json:"stratum"`
	Gold      string  `json:"gold"`
	Decision  *string `json:"decision"`
	Score     *int    `json:"score"`
	Relevance *string `json:"relevance"`
	Category  *string `json:"category"`
	Reason    *string `json:"reason"`
	ReceiptID *int64  `json:"receiptId"`
	Error     *string `json:"error"`
}

func (s *Service) List(ctx context.Context) ([]RunListEntry, error) {
	runs, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	if runs == nil {
		runs = []RunListEntry{}
	}
	return runs, nil
}

func (s *Service) Run(ctx context.Context, id string, filters Filters) (*RunCases, error) {
	return s.repository.Run(ctx, id, filters)
}

func (s *Service) Import(ctx context.Context, label string, raw json.RawMessage, actor string) (string, error) {
	var input report
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", err
	}
	names := orderedModelNames(raw, input.Models)
	if len(names) == 0 {
		return "", errors.New("report has no model summaries")
	}

	data := ImportData{Label: label, Models: names}
	var randomID [3]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return "", err
	}
	data.ID = fmt.Sprintf("sb-%s-%s", time.Now().UTC().Format("20060102"), hex.EncodeToString(randomID[:]))
	if value, ok := input.Meta["split"].(string); ok {
		data.Split = stringPtr(value)
	}
	if value, ok := numericInt(input.Meta["n"]); ok {
		data.SampleSize = value
	} else if first, ok := input.Models[names[0]].Summary["n"]; ok {
		data.SampleSize, _ = numericInt(first)
	}
	if value, ok := numericInt64(input.Meta["seed"]); ok {
		data.Seed = &value
	}
	if value, ok := input.Meta["promptVersion"].(string); ok {
		data.Prompt = stringPtr(value)
	}
	if strings.TrimSpace(label) == "" {
		data.Label = "导入的对比运行"
	}

	summary := make(map[string]any, len(names))
	for _, name := range names {
		item := input.Models[name]
		withSweep := make(map[string]any, len(item.Summary)+1)
		for key, value := range item.Summary {
			withSweep[key] = value
		}
		if item.Sweep == nil {
			item.Sweep = []any{}
		}
		withSweep["sweep"] = item.Sweep
		summary[name] = withSweep
		for _, result := range item.Cases {
			data.Results = append(data.Results, ImportResult{Model: name, CaseID: result.CaseID, Title: result.Title, Stratum: result.Stratum, Gold: result.Gold, Decision: result.Decision, Score: result.Score, Relevance: result.Relevance, Category: result.Category, Reason: result.Reason, ReceiptID: result.ReceiptID, Error: result.Error})
		}
	}
	encodedSummary, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	data.Summary = encodedSummary
	if err := s.repository.Import(ctx, data, actor); err != nil {
		return "", err
	}
	return data.ID, nil
}

func numericInt(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

func numericInt64(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) {
		return 0, false
	}
	return int64(number), true
}

func orderedModelNames(raw json.RawMessage, models map[string]modelReport) []string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil
		}
		key, ok := token.(string)
		if !ok {
			return nil
		}
		if key != "models" {
			var discard json.RawMessage
			if err := decoder.Decode(&discard); err != nil {
				return nil
			}
			continue
		}
		start, err := decoder.Token()
		if err != nil || start != json.Delim('{') {
			return nil
		}
		var names []string
		for decoder.More() {
			modelToken, err := decoder.Token()
			if err != nil {
				return nil
			}
			name, ok := modelToken.(string)
			if !ok {
				return nil
			}
			var discard json.RawMessage
			if err := decoder.Decode(&discard); err != nil {
				return nil
			}
			if models[name].Summary != nil {
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}

func stringPtr(value string) *string { return &value }
