// Package grouping contains the deterministic rules used to connect reports to facts and stories.
// Database access and model calls belong to the grouping Service and Repository built around these
// rules.
package grouping

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Relation string

const (
	RelationSameOccurrence Relation = "SAME_OCCURRENCE"
	RelationSameStory      Relation = "SAME_STORY"
	RelationUnrelated      Relation = "UNRELATED"
	RelationRoundup        Relation = "ROUNDUP"

	TieMinConfidence         = 0.8
	StoryReviewMinConfidence = 0.75
)

var ErrRecallUnavailable = errors.New("grouping recall is unavailable")
var ErrEmbeddingUnavailable = errors.New("embedding calls are unavailable")

type Frame struct {
	Subject    *string `json:"subject,omitempty"`
	Action     *string `json:"action,omitempty"`
	Object     *string `json:"object,omitempty"`
	OccurredAt *string `json:"occurredAt,omitempty"`
}

type Report struct {
	Title      string
	Source     string
	FirstParty bool
	At         *time.Time
	Summary    *string
	SourceText *string
	Scope      string
	Frame      *Frame
}

type Candidate struct {
	FactID         int64
	StoryID        int64
	FactTitle      string
	Members        int
	StoryRoot      bool
	Score          float64
	Report         Report
	Selected       bool
	SelectedReport *Report
}

type Verdict struct {
	Relation   Relation
	Confidence float64
	Note       string
}

type Decision struct {
	ID         string
	Relation   Relation
	Confidence float64
	Note       string
}

// CompleteDecisions accepts each positional candidate key exactly once. A missing, repeated, or
// out-of-range answer is a failed judgement; callers must not interpret it as UNRELATED.
func CompleteDecisions(decisions []Decision, count int) bool {
	if count < 0 || len(decisions) != count {
		return false
	}
	seen := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		index, ok := candidateIndex(decision.ID)
		if !ok || index < 1 || index > count {
			return false
		}
		if _, exists := seen[decision.ID]; exists {
			return false
		}
		seen[decision.ID] = struct{}{}
	}
	return true
}

// VerdictsByFact maps the validated positional decisions back to the recalled facts.
func VerdictsByFact(decisions []Decision, candidates []Candidate) (map[int64]Verdict, error) {
	if !CompleteDecisions(decisions, len(candidates)) {
		return nil, fmt.Errorf("identity answer must cover every candidate exactly once")
	}
	result := make(map[int64]Verdict, len(decisions))
	for _, decision := range decisions {
		index, _ := candidateIndex(decision.ID)
		candidate := candidates[index-1]
		if _, exists := result[candidate.FactID]; !exists {
			result[candidate.FactID] = Verdict{Relation: decision.Relation, Confidence: decision.Confidence, Note: decision.Note}
		}
	}
	return result, nil
}

// SameOccurrence returns the most confident same-event fact first; recall score breaks confidence
// ties, and input order breaks exact ties (matching stable Array.sort behavior).
func SameOccurrence(candidates []Candidate, verdicts map[int64]Verdict) []Candidate {
	result := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if verdicts[candidate.FactID].Relation == RelationSameOccurrence {
			result = append(result, candidate)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := verdicts[result[i].FactID], verdicts[result[j].FactID]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		return result[i].Score > result[j].Score
	})
	return result
}

// StoryForDevelopment selects the closest root occurrence for a direct development. Developments
// never attach through another development.
func StoryForDevelopment(candidates []Candidate, verdicts map[int64]Verdict) *Candidate {
	var best *Candidate
	for i := range candidates {
		candidate := &candidates[i]
		if !candidate.StoryRoot || verdicts[candidate.FactID].Relation != RelationSameStory {
			continue
		}
		if best == nil || candidate.Score > best.Score {
			best = candidate
		}
	}
	return best
}

// SignalTarget first prefers a same-occurrence fact, then accepts only a sufficiently confident
// direct reaction to a story.
func SignalTarget(candidates []Candidate, verdicts map[int64]Verdict, minimum ...float64) *Candidate {
	minReactionConfidence := TieMinConfidence
	if len(minimum) > 0 {
		minReactionConfidence = minimum[0]
	}
	if same := SameOccurrence(candidates, verdicts); len(same) > 0 {
		return &same[0]
	}
	var best *Candidate
	for i := range candidates {
		candidate := &candidates[i]
		verdict := verdicts[candidate.FactID]
		if verdict.Relation != RelationSameStory || verdict.Confidence < minReactionConfidence {
			continue
		}
		if best == nil || candidate.Score > best.Score {
			best = candidate
		}
	}
	return best
}

func FirmlyTied(relation Relation, confidence float64, minimum ...float64) bool {
	minConfidence := TieMinConfidence
	if len(minimum) > 0 {
		minConfidence = minimum[0]
	}
	return (relation == RelationSameOccurrence || relation == RelationSameStory) && confidence >= minConfidence
}

func ReportText(title string, summary *string) string {
	return title + "。" + truncateUTF16(stringValue(summary), 300)
}

// LexicalSimilarity is the no-embedding fallback: shared Unicode character bigrams divided by
// the smaller bigram set. Whitespace is removed before bigrams are formed.
func LexicalSimilarity(a, b string) float64 {
	left, right := characterBigrams(a), characterBigrams(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	intersection := 0
	for gram := range left {
		if _, ok := right[gram]; ok {
			intersection++
		}
	}
	denominator := min(len(left), len(right))
	return float64(intersection) / float64(denominator)
}

func characterBigrams(value string) map[string]struct{} {
	chars := make([]rune, 0, len(value))
	for _, char := range value {
		if !isJavaScriptWhitespace(char) {
			chars = append(chars, char)
		}
	}
	if len(chars) < 2 {
		return nil
	}
	grams := make(map[string]struct{}, len(chars)-1)
	for i := 0; i+1 < len(chars); i++ {
		grams[string(chars[i:i+2])] = struct{}{}
	}
	return grams
}

func isJavaScriptWhitespace(char rune) bool {
	return char >= '\u0009' && char <= '\u000D' || char == '\u0020' || char == '\u00A0' ||
		char == '\u1680' || char >= '\u2000' && char <= '\u200A' || char == '\u2028' ||
		char == '\u2029' || char == '\u202F' || char == '\u205F' || char == '\u3000' || char == '\uFEFF'
}

func candidateIndex(id string) (int, bool) {
	if len(id) < 2 || id[0] != 'C' || id[1] == '0' {
		return 0, false
	}
	value := 0
	for _, char := range id[1:] {
		if char < '0' || char > '9' {
			return 0, false
		}
		value = value*10 + int(char-'0')
		if value > 1_000_000 {
			return 0, false
		}
	}
	return value, value > 0
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// truncateUTF16 mirrors JavaScript String.slice(0, limit) for normal Unicode scalar boundaries.
func truncateUTF16(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var result strings.Builder
	units := 0
	for _, char := range value {
		width := 1
		if char > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			break
		}
		result.WriteRune(char)
		units += width
	}
	return result.String()
}
