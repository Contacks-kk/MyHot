package grouping

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type BatchResult struct {
	Query     string
	Decisions []Decision
	Selection SelectionValue
}

type SelectionValue struct {
	AddsValue bool
	Reason    string
}

type PairResult struct {
	A          string
	B          string
	Relation   Relation
	Difference string
	Confidence float64
}

type SignalResult struct {
	Decisions []Decision
}

var validRelations = map[Relation]struct{}{
	RelationSameOccurrence: {}, RelationSameStory: {}, RelationUnrelated: {}, RelationRoundup: {},
}

// ParseBatchResult applies the same defaults and bounds as the TypeScript BatchSchema. In
// particular, query/note use catch("") while a malformed or incomplete decision is rejected.
func ParseBatchResult(data []byte, candidateCount int) (BatchResult, error) {
	root, err := jsonObject(data)
	if err != nil {
		return BatchResult{}, err
	}
	result := BatchResult{Query: catchString(root["query"], 400)}
	var rawDecisions []json.RawMessage
	if err := json.Unmarshal(root["decisions"], &rawDecisions); err != nil || rawDecisions == nil {
		return BatchResult{}, errors.New("decisions: expected array")
	}
	result.Decisions = make([]Decision, 0, len(rawDecisions))
	for _, raw := range rawDecisions {
		item, err := jsonObject(raw)
		if err != nil {
			return BatchResult{}, fmt.Errorf("decision: %w", err)
		}
		id, err := requiredString(item, "id")
		if err != nil {
			return BatchResult{}, fmt.Errorf("decision id: %w", err)
		}
		relation, err := requiredRelation(item, "relation")
		if err != nil {
			return BatchResult{}, fmt.Errorf("decision relation: %w", err)
		}
		confidence, err := requiredConfidence(item, "confidence")
		if err != nil {
			return BatchResult{}, fmt.Errorf("decision confidence: %w", err)
		}
		result.Decisions = append(result.Decisions, Decision{ID: id, Relation: relation, Confidence: confidence, Note: catchString(item["note"], 400)})
	}
	selection, err := jsonObject(root["selection"])
	if err != nil {
		return BatchResult{}, fmt.Errorf("selection: %w", err)
	}
	if raw, ok := selection["addsValue"]; !ok || json.Unmarshal(raw, &result.Selection.AddsValue) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return BatchResult{}, errors.New("selection addsValue: expected boolean")
	}
	reason, err := requiredString(selection, "reason")
	if err != nil {
		return BatchResult{}, fmt.Errorf("selection reason: %w", err)
	}
	reason = strings.TrimFunc(reason, isJavaScriptWhitespace)
	if reason == "" || utf16Length(reason) > 400 {
		return BatchResult{}, errors.New("selection reason: expected 1 to 400 characters")
	}
	result.Selection.Reason = reason
	if !CompleteDecisions(result.Decisions, candidateCount) {
		return BatchResult{}, errors.New("identity answer must cover every candidate exactly once")
	}
	return result, nil
}

func ParsePairResult(data []byte) (PairResult, error) {
	root, err := jsonObject(data)
	if err != nil {
		return PairResult{}, err
	}
	relation, err := requiredRelation(root, "relation")
	if err != nil {
		return PairResult{}, fmt.Errorf("relation: %w", err)
	}
	confidence, err := requiredConfidence(root, "confidence")
	if err != nil {
		return PairResult{}, fmt.Errorf("confidence: %w", err)
	}
	return PairResult{
		A: catchString(root["a"], 400), B: catchString(root["b"], 400), Relation: relation,
		Difference: catchString(root["difference"], 400), Confidence: confidence,
	}, nil
}

func ParseSignalResult(data []byte, candidateCount int) (SignalResult, error) {
	root, err := jsonObject(data)
	if err != nil {
		return SignalResult{}, err
	}
	var rawDecisions []json.RawMessage
	if err := json.Unmarshal(root["decisions"], &rawDecisions); err != nil || rawDecisions == nil {
		return SignalResult{}, errors.New("decisions: expected array")
	}
	result := SignalResult{Decisions: make([]Decision, 0, len(rawDecisions))}
	for _, raw := range rawDecisions {
		item, err := jsonObject(raw)
		if err != nil {
			return SignalResult{}, fmt.Errorf("decision: %w", err)
		}
		id, err := requiredString(item, "id")
		if err != nil {
			return SignalResult{}, fmt.Errorf("decision id: %w", err)
		}
		relation, err := requiredRelation(item, "relation")
		if err != nil {
			return SignalResult{}, fmt.Errorf("decision relation: %w", err)
		}
		confidence, err := requiredConfidence(item, "confidence")
		if err != nil {
			return SignalResult{}, fmt.Errorf("decision confidence: %w", err)
		}
		result.Decisions = append(result.Decisions, Decision{ID: id, Relation: relation, Confidence: confidence})
	}
	if !CompleteDecisions(result.Decisions, candidateCount) {
		return SignalResult{}, errors.New("identity answer must cover every candidate exactly once")
	}
	return result, nil
}

func jsonObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var result map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, errors.New("expected object")
	}
	return result, nil
}

func requiredString(object map[string]json.RawMessage, field string) (string, error) {
	raw, ok := object[field]
	if !ok {
		return "", errors.New("required")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("expected string")
	}
	return value, nil
}

func requiredRelation(object map[string]json.RawMessage, field string) (Relation, error) {
	value, err := requiredString(object, field)
	if err != nil {
		return "", err
	}
	relation := Relation(value)
	if _, ok := validRelations[relation]; !ok {
		return "", errors.New("invalid relation")
	}
	return relation, nil
}

func requiredConfidence(object map[string]json.RawMessage, field string) (float64, error) {
	raw, ok := object[field]
	if !ok {
		return 0, errors.New("required")
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || value < 0 || value > 1 {
		return 0, errors.New("expected number between 0 and 1")
	}
	return value, nil
}

func catchString(raw json.RawMessage, limit int) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || utf16Length(value) > limit {
		return ""
	}
	return value
}

func utf16Length(value string) int {
	length := 0
	for _, char := range value {
		if char > 0xFFFF {
			length += 2
		} else {
			length++
		}
	}
	return length
}
