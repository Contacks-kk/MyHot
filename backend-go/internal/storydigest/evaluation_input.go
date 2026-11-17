package storydigest

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type EvaluationCase struct {
	CaseID          string             `json:"caseId"`
	Story           *EvaluationStory   `json:"story"`
	InputMode       *string            `json:"inputMode,omitempty"`
	KnownArticleIDs []string           `json:"knownArticleIds"`
	Reports         []EvaluationReport `json:"reports"`
}

type EvaluationStory struct {
	Title          string  `json:"title"`
	PreviousDigest *string `json:"previousDigest"`
}

type EvaluationReport struct {
	ID          string          `json:"id"`
	PublishedAt string          `json:"publishedAt"`
	Source      string          `json:"source"`
	FirstParty  *bool           `json:"firstParty,omitempty"`
	Title       string          `json:"title"`
	Summary     *string         `json:"summary"`
	Fact        *EvaluationFact `json:"fact"`
}

type EvaluationFact struct {
	ID         int64           `json:"id"`
	Subject    *string         `json:"subject"`
	Action     *string         `json:"action"`
	Object     *string         `json:"object"`
	Conditions *string         `json:"conditions"`
	Evidence   *string         `json:"evidence"`
	Structured json.RawMessage `json:"structured"`
}

/** ParseEvaluationCases reads the strict JSONL snapshot used by story-digest evaluations. */
func ParseEvaluationCases(text string) ([]EvaluationCase, error) {
	rows := make([]EvaluationCase, 0)
	seen := make(map[string]bool)
	for index, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "//") {
			continue
		}
		var row EvaluationCase
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&row); err != nil {
			return nil, fmt.Errorf("line %d: %w", index+1, err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("line %d: expected one JSON object", index+1)
		}
		if err := validateEvaluationNulls([]byte(raw)); err != nil {
			return nil, fmt.Errorf("line %d: %w", index+1, err)
		}
		if err := row.validate(); err != nil {
			return nil, fmt.Errorf("line %d: %w", index+1, err)
		}
		if seen[row.CaseID] {
			return nil, fmt.Errorf("line %d: duplicate caseId %s", index+1, row.CaseID)
		}
		seen[row.CaseID] = true
		rows = append(rows, row)
	}
	return rows, nil
}

/** validateEvaluationNulls distinguishes optional values from explicit invalid nulls. */
func validateEvaluationNulls(raw []byte) error {
	var row struct {
		InputMode json.RawMessage `json:"inputMode"`
		KnownIDs  json.RawMessage `json:"knownArticleIds"`
		Reports   []struct {
			FirstParty json.RawMessage `json:"firstParty"`
		} `json:"reports"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return err
	}
	if string(row.InputMode) == "null" || string(row.KnownIDs) == "null" {
		return fmt.Errorf("inputMode and knownArticleIds cannot be null")
	}
	for _, report := range row.Reports {
		if string(report.FirstParty) == "null" {
			return fmt.Errorf("report firstParty cannot be null")
		}
	}
	return nil
}

/** validate checks required snapshot fields before any model work is scheduled. */
func (row EvaluationCase) validate() error {
	if row.CaseID == "" || row.Story == nil || row.Story.Title == "" || len(row.Reports) == 0 {
		return fmt.Errorf("caseId, story title and reports are required")
	}
	if row.InputMode != nil && *row.InputMode != "incremental" && *row.InputMode != "corrected" {
		return fmt.Errorf("inputMode must be incremental or corrected")
	}
	for _, id := range row.KnownArticleIDs {
		if id == "" {
			return fmt.Errorf("knownArticleIds cannot contain an empty id")
		}
	}
	for _, report := range row.Reports {
		if report.ID == "" || report.Source == "" || report.Title == "" || report.Fact == nil || report.Fact.ID <= 0 {
			return fmt.Errorf("report id, source, title and positive fact id are required")
		}
		if _, err := time.Parse(time.RFC3339Nano, report.PublishedAt); err != nil {
			return fmt.Errorf("report %s has an invalid publishedAt", report.ID)
		}
	}
	return nil
}

/** BuildEvaluationInput renders a snapshot with the same ordering and formatter as the live digest. */
func BuildEvaluationInput(row EvaluationCase) (string, error) {
	if err := row.validate(); err != nil {
		return "", err
	}
	reports := make([]DigestReport, 0, len(row.Reports))
	for _, report := range row.Reports {
		at, _ := time.Parse(time.RFC3339Nano, report.PublishedAt)
		firstParty := report.FirstParty != nil && *report.FirstParty
		reports = append(reports, DigestReport{
			ID:             report.ID,
			Title:          report.Title,
			Summary:        report.Summary,
			SourceName:     report.Source,
			FirstParty:     firstParty,
			At:             at,
			FactID:         report.Fact.ID,
			FactSubject:    report.Fact.Subject,
			FactAction:     report.Fact.Action,
			FactObject:     report.Fact.Object,
			FactConditions: report.Fact.Conditions,
			Evidence:       report.Fact.Evidence,
			StructuredFact: report.Fact.Structured,
		})
	}
	sort.SliceStable(reports, func(i, j int) bool { return reports[i].At.Before(reports[j].At) })
	corrected := row.InputMode != nil && *row.InputMode == "corrected"
	input, _, err := BuildInput(Story{Title: row.Story.Title, Digest: row.Story.PreviousDigest}, reports, corrected, row.KnownArticleIDs)
	return input, err
}
