package grouping

import (
	"strings"
	"testing"
)

func TestCompleteDecisionsRequiresEveryCandidateExactlyOnce(t *testing.T) {
	tests := []struct {
		name      string
		decisions []Decision
		count     int
		want      bool
	}{
		{name: "empty candidate set", decisions: []Decision{}, count: 0, want: true},
		{name: "answers may arrive out of order", decisions: []Decision{{ID: "C2"}, {ID: "C1"}}, count: 2, want: true},
		{name: "missing answer", decisions: []Decision{{ID: "C1"}}, count: 2},
		{name: "duplicate answer", decisions: []Decision{{ID: "C1"}, {ID: "C1"}}, count: 2},
		{name: "out of range", decisions: []Decision{{ID: "C1"}, {ID: "C3"}}, count: 2},
		{name: "zero index", decisions: []Decision{{ID: "C0"}}, count: 1},
		{name: "leading zero", decisions: []Decision{{ID: "C01"}}, count: 1},
		{name: "wrong prefix case", decisions: []Decision{{ID: "c1"}}, count: 1},
		{name: "negative count", decisions: []Decision{}, count: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CompleteDecisions(test.decisions, test.count); got != test.want {
				t.Fatalf("CompleteDecisions() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestVerdictsByFactUsesCandidatePosition(t *testing.T) {
	candidates := []Candidate{{FactID: 41}, {FactID: 99}}
	got, err := VerdictsByFact([]Decision{
		{ID: "C2", Relation: RelationSameStory, Confidence: .91, Note: "development"},
		{ID: "C1", Relation: RelationSameOccurrence, Confidence: .88, Note: "same launch"},
	}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if got[41] != (Verdict{Relation: RelationSameOccurrence, Confidence: .88, Note: "same launch"}) ||
		got[99] != (Verdict{Relation: RelationSameStory, Confidence: .91, Note: "development"}) {
		t.Fatalf("verdicts mapped incorrectly: %#v", got)
	}
	if _, err := VerdictsByFact([]Decision{{ID: "C2"}}, candidates); err == nil {
		t.Fatal("incomplete model answer was accepted")
	}
}

func TestSameOccurrenceSortsConfidenceThenSimilarityStably(t *testing.T) {
	candidates := []Candidate{
		{FactID: 1, Score: .99},
		{FactID: 2, Score: .8},
		{FactID: 3, Score: .8},
		{FactID: 4, Score: 1},
	}
	verdicts := map[int64]Verdict{
		1: {Relation: RelationSameOccurrence, Confidence: .7},
		2: {Relation: RelationSameOccurrence, Confidence: .9},
		3: {Relation: RelationSameOccurrence, Confidence: .9},
		4: {Relation: RelationSameStory, Confidence: 1},
	}
	got := SameOccurrence(candidates, verdicts)
	if len(got) != 3 || got[0].FactID != 2 || got[1].FactID != 3 || got[2].FactID != 1 {
		t.Fatalf("unexpected occurrence order: %#v", got)
	}
}

func TestDevelopmentAndSignalTargetsRespectRootAndRelationPriority(t *testing.T) {
	candidates := []Candidate{
		{FactID: 1, Score: .5, StoryRoot: false},
		{FactID: 2, Score: .73, StoryRoot: true},
		{FactID: 3, Score: .91, StoryRoot: true},
	}
	verdicts := map[int64]Verdict{
		1: {Relation: RelationSameStory, Confidence: .99},
		2: {Relation: RelationSameStory, Confidence: .8},
		3: {Relation: RelationSameStory, Confidence: .81},
	}
	if got := StoryForDevelopment(candidates, verdicts); got == nil || got.FactID != 3 {
		t.Fatalf("development should select the closest root, got %#v", got)
	}
	if got := SignalTarget(candidates, verdicts); got == nil || got.FactID != 3 {
		t.Fatalf("signal should select the closest firmly related root, got %#v", got)
	}

	verdicts[1] = Verdict{Relation: RelationSameOccurrence, Confidence: .1}
	if got := SignalTarget(candidates, verdicts); got == nil || got.FactID != 1 {
		t.Fatalf("same occurrence must take priority over a more similar development, got %#v", got)
	}
	if got := SignalTarget(candidates, map[int64]Verdict{1: {Relation: RelationSameStory, Confidence: .79}}); got != nil {
		t.Fatalf("below-threshold reaction was accepted: %#v", got)
	}
	if got := SignalTarget(candidates, map[int64]Verdict{1: {Relation: RelationSameStory, Confidence: .4}}, .3); got == nil || got.FactID != 1 {
		t.Fatalf("explicit reaction threshold was ignored: %#v", got)
	}
}

func TestFirmlyTiedThreshold(t *testing.T) {
	if !FirmlyTied(RelationSameOccurrence, .8) || !FirmlyTied(RelationSameStory, .8) {
		t.Fatal("default tie threshold should be inclusive")
	}
	if FirmlyTied(RelationUnrelated, 1) || FirmlyTied(RelationRoundup, 1) || FirmlyTied(RelationSameStory, .79) {
		t.Fatal("unrelated, roundup, or low-confidence relation was treated as a tie")
	}
	if !FirmlyTied(RelationSameStory, .5, .5) || FirmlyTied(RelationSameStory, .5, .51) {
		t.Fatal("explicit tie threshold was not applied")
	}
}

func TestReportTextAndLexicalSimilarity(t *testing.T) {
	long := strings.Repeat("中", 300) + "尾"
	got := ReportText("标题", &long)
	if len([]rune(got)) != 303 || strings.HasSuffix(got, "尾") {
		t.Fatalf("summary was not limited to 300 UTF-16 code units: runeCount=%d suffix=%q", len([]rune(got)), got[len(got)-6:])
	}
	if got := LexicalSimilarity("人工 智能发布", "人工智能发布"); got != 1 {
		t.Fatalf("whitespace should not affect lexical bigrams, got %v", got)
	}
	if got := LexicalSimilarity("甲\u0085乙", "甲乙"); got != 0 {
		t.Fatalf("a non-JavaScript whitespace rune was removed, got %v", got)
	}
	if got := LexicalSimilarity("甲乙丙", "乙丙丁戊"); got != .5 {
		t.Fatalf("shared bigrams should divide by the smaller set, got %v", got)
	}
	if got := LexicalSimilarity("甲", "甲乙"); got != 0 {
		t.Fatalf("single-character text has no bigrams, got %v", got)
	}
}
