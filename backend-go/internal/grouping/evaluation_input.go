package grouping

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

/** ParseRelationGoldJSONL parses labelled pairs while preserving their input order. */
func ParseRelationGoldJSONL(text string) ([]EvaluationGoldRow, error) {
	rows := make([]EvaluationGoldRow, 0)
	seen := make(map[string]struct{})
	for index, raw := range strings.Split(text, "\n") {
		line := index + 1
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "//") {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
			return nil, fmt.Errorf("line %d: row must be a JSON object", line)
		}
		caseID, err := evaluationRequiredString(obj, "caseId")
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if _, ok := seen[caseID]; ok {
			return nil, fmt.Errorf("line %d: duplicate caseId %s", line, caseID)
		}
		seen[caseID] = struct{}{}
		a, err := parseEvaluationReport(obj["a"])
		if err != nil {
			return nil, fmt.Errorf("line %d: a.%w", line, err)
		}
		b, err := parseEvaluationReport(obj["b"])
		if err != nil {
			return nil, fmt.Errorf("line %d: b.%w", line, err)
		}
		var sampling *EvaluationSamplingContext
		if rawSampling, ok := obj["samplingContext"]; ok {
			var context map[string]json.RawMessage
			if err := json.Unmarshal(rawSampling, &context); err != nil || context == nil {
				return nil, fmt.Errorf("line %d: samplingContext must be an object", line)
			}
			split, err := evaluationOptionalString(context, "benchmarkSplit")
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			stratum, err := evaluationOptionalString(context, "samplingStratum")
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			sampling = &EvaluationSamplingContext{}
			if split != nil {
				sampling.BenchmarkSplit = *split
			}
			if stratum != nil {
				sampling.SamplingStratum = *stratum
			}
		}
		var gold struct {
			Relation Relation `json:"relation"`
		}
		if err := json.Unmarshal(obj["gold"], &gold); err != nil {
			return nil, fmt.Errorf("line %d: gold must be an object", line)
		}
		if _, ok := validRelations[gold.Relation]; !ok {
			return nil, fmt.Errorf("line %d: invalid gold.relation", line)
		}
		rows = append(rows, EvaluationGoldRow{
			CaseID:          caseID,
			A:               a,
			B:               b,
			SamplingContext: sampling,
			Gold:            EvaluationGold{Relation: gold.Relation},
		})
	}
	return rows, nil
}

/** parseEvaluationReport validates the model input fields for one labelled report. */
func parseEvaluationReport(raw json.RawMessage) (EvaluationGoldReport, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return EvaluationGoldReport{}, fmt.Errorf("report must be an object")
	}
	title, err := evaluationRequiredString(obj, "title")
	if err != nil {
		return EvaluationGoldReport{}, err
	}
	source, err := evaluationRequiredString(obj, "source")
	if err != nil {
		return EvaluationGoldReport{}, err
	}
	var firstParty bool
	if rawFirstParty, ok := obj["firstParty"]; ok {
		if string(rawFirstParty) == "null" || json.Unmarshal(rawFirstParty, &firstParty) != nil {
			return EvaluationGoldReport{}, fmt.Errorf("firstParty must be a boolean")
		}
	}
	publishedAt, err := evaluationOptionalString(obj, "publishedAt")
	if err != nil {
		return EvaluationGoldReport{}, err
	}
	if publishedAt != nil && *publishedAt != "" {
		if _, err := parseEvaluationDate(*publishedAt); err != nil {
			return EvaluationGoldReport{}, fmt.Errorf("publishedAt is not a supported date")
		}
	}
	summary, err := evaluationOptionalString(obj, "summary")
	if err != nil {
		return EvaluationGoldReport{}, err
	}
	var frame *Frame
	if rawFrame, ok := obj["frame"]; ok && string(rawFrame) != "null" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawFrame, &fields); err != nil || fields == nil {
			return EvaluationGoldReport{}, fmt.Errorf("frame must be an object")
		}
		frame = &Frame{}
		for key, target := range map[string]**string{"subject": &frame.Subject, "action": &frame.Action, "object": &frame.Object, "occurredAt": &frame.OccurredAt} {
			value, err := evaluationOptionalString(fields, key)
			if err != nil {
				return EvaluationGoldReport{}, err
			}
			*target = value
		}
	}
	return EvaluationGoldReport{
		Title:       title,
		Source:      source,
		FirstParty:  firstParty,
		PublishedAt: publishedAt,
		Summary:     summary,
		Frame:       frame,
	}, nil
}

/** evaluationRequiredString reads a nonempty JSON string without changing its content. */
func evaluationRequiredString(obj map[string]json.RawMessage, key string) (string, error) {
	value, err := evaluationOptionalString(obj, key)
	if err != nil || value == nil || strings.TrimSpace(*value) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", key)
	}
	return *value, nil
}

/** evaluationOptionalString accepts a JSON string, null, or a missing property. */
func evaluationOptionalString(obj map[string]json.RawMessage, key string) (*string, error) {
	raw, ok := obj[key]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%s must be a string or null", key)
	}
	return &value, nil
}

/** parseEvaluationDate accepts standard ISO dates used by the gold datasets. */
func parseEvaluationDate(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date %q", value)
}

/** Report converts validated gold data into the production relation judge input. */
func (r EvaluationGoldReport) Report() (Report, error) {
	var published *time.Time
	if r.PublishedAt != nil && *r.PublishedAt != "" {
		parsed, err := parseEvaluationDate(*r.PublishedAt)
		if err != nil {
			return Report{}, err
		}
		published = &parsed
	}
	return Report{
		Title:      r.Title,
		Source:     r.Source,
		FirstParty: r.FirstParty,
		At:         published,
		Summary:    r.Summary,
		Frame:      r.Frame,
	}, nil
}
