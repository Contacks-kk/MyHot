package grouping

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

/** TestSampleRelationGoldMatchesLegacySeed verifies filtering, order, and sample limits. */
func TestSampleRelationGoldMatchesLegacySeed(t *testing.T) {
	rows := make([]EvaluationGoldRow, 0, 12)
	for i := 0; i < 12; i++ {
		split := "development"
		if i >= 8 {
			split = "holdout"
		}
		rows = append(rows, EvaluationGoldRow{
			CaseID: fmt.Sprintf("case-%d", i),
			SamplingContext: &EvaluationSamplingContext{
				BenchmarkSplit: split,
			},
		})
	}
	selected := SampleRelationGold(rows, "development", 5, 11)
	ids := make([]string, 0, len(selected))
	for _, row := range selected {
		ids = append(ids, row.CaseID)
	}
	if want := []string{"case-3", "case-6", "case-0", "case-1", "case-2"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("sample = %#v, want %#v", ids, want)
	}
	if got := SampleRelationGold(rows, "holdout", 20, 11); len(got) != 4 {
		t.Fatalf("holdout sample size = %d", len(got))
	}
	if got := SampleRelationGold(rows, "all", -1, 11); len(got) != 0 {
		t.Fatalf("negative limit sample size = %d", len(got))
	}
}

/** TestSafeEvaluationReportNamePartMatchesLegacy protects split-derived report names. */
func TestSafeEvaluationReportNamePartMatchesLegacy(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "development", want: "development"},
		{input: "../../holdout\\windows", want: "holdout-windows"},
		{input: "  中文 split / 2026  ", want: "split-2026"},
		{input: "../..", want: "all"},
		{input: strings.Repeat("x", 81), want: strings.Repeat("x", 80)},
	}
	for _, test := range tests {
		if got := SafeEvaluationReportNamePart(test.input); got != test.want {
			t.Errorf("SafeEvaluationReportNamePart(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

/** TestParseRelationGoldJSONL validates gold labels and report fields using legacy fixtures. */
func TestParseRelationGoldJSONL(t *testing.T) {
	fixture, err := os.ReadFile("../../config/evaluations/relation-gold.example.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := ParseRelationGoldJSONL(string(fixture)); err != nil || len(rows) == 0 {
		t.Fatalf("example gold set: rows=%d err=%v", len(rows), err)
	}
	valid := `{"caseId":"same","a":{"title":"A","source":"Source","publishedAt":"2026-09-01T09:00:00+08:00"},"b":{"title":"B","source":"Source","firstParty":true,"frame":{"subject":"Example"}},"samplingContext":{"benchmarkSplit":"development","samplingStratum":"same"},"gold":{"relation":"SAME_OCCURRENCE"}}`
	rows, err := ParseRelationGoldJSONL("// header\n\n" + valid + "\n")
	if err != nil || len(rows) != 1 || rows[0].CaseID != "same" || rows[0].A.FirstParty || !rows[0].B.FirstParty || rows[0].B.Frame == nil || *rows[0].B.Frame.Subject != "Example" || rows[0].Gold.Relation != RelationSameOccurrence {
		t.Fatalf("parsed rows = %#v, err=%v", rows, err)
	}
	report, err := rows[0].A.Report()
	if err != nil || report.At == nil || report.At.UTC().Format(time.RFC3339) != "2026-09-01T01:00:00Z" || report.Title != "A" {
		t.Fatalf("judge report = %#v, err=%v", report, err)
	}
	for _, test := range []struct {
		name string
		text string
		want string
	}{
		{name: "duplicate", text: valid + "\n" + valid, want: "duplicate caseId"},
		{name: "relation", text: strings.Replace(valid, "SAME_OCCURRENCE", "MAYBE", 1), want: "gold.relation"},
		{name: "date", text: strings.Replace(valid, "2026-09-01T09:00:00+08:00", "not-a-date", 1), want: "publishedAt"},
		{name: "boolean null", text: strings.Replace(valid, `"firstParty":true`, `"firstParty":null`, 1), want: "firstParty"},
		{name: "invalid sampling", text: strings.Replace(valid, `"samplingContext":{"benchmarkSplit":"development","samplingStratum":"same"}`, `"samplingContext":null`, 1), want: "samplingContext"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseRelationGoldJSONL(test.text); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

/** TestEvaluationMetricsPreserveLegacyReport verifies multiclass and threshold report semantics. */
func TestEvaluationMetricsPreserveLegacyReport(t *testing.T) {
	if evaluationRound(0.0045) != 0.004 || evaluationRound(0.0125) != 0.013 {
		t.Fatal("metric rounding differs from the legacy JavaScript report")
	}
	predictions := []EvaluationPrediction{
		{CaseID: "1", Gold: RelationSameOccurrence, Relation: RelationSameOccurrence, Confidence: 0.9},
		{CaseID: "2", Gold: RelationSameOccurrence, Relation: RelationSameStory, Confidence: 0.9},
		{CaseID: "3", Gold: RelationSameStory, Relation: RelationSameStory, Confidence: 0.9},
		{CaseID: "4", Gold: RelationUnrelated, Relation: RelationRoundup, Confidence: 0.8},
		{CaseID: "5", Gold: RelationRoundup, Relation: RelationRoundup, Confidence: 0.8},
	}
	metrics, err := EvaluateRelations(predictions, 6)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.SampleSize != 6 || metrics.Evaluated != 5 || metrics.Errors != 1 || metrics.Accuracy != 0.6 || metrics.MacroF1 != 0.5 || metrics.ConfusionMatrix[RelationSameOccurrence][RelationSameStory] != 1 {
		t.Fatalf("unexpected evaluation report: %#v", metrics)
	}
	if got := metrics.PerClass[RelationSameOccurrence]; got != (EvaluationClassMetrics{Precision: 1, Recall: 0.5, F1: 0.667, Support: 2}) {
		t.Fatalf("unexpected class metrics: %#v", got)
	}
	ties := EvaluateStoryTies([]EvaluationPrediction{
		{Gold: RelationSameOccurrence, Relation: RelationSameOccurrence, Confidence: 0.9},
		{Gold: RelationSameStory, Relation: RelationSameStory, Confidence: 0.77},
		{Gold: RelationUnrelated, Relation: RelationSameStory, Confidence: 0.9},
		{Gold: RelationRoundup, Relation: RelationRoundup, Confidence: 0.99},
	}, 0.75)
	if ties != (EvaluationTieMetrics{Threshold: 0.75, TP: 2, FP: 1, TN: 1, Precision: 0.667, Recall: 1, F1: 0.8, Accuracy: 0.75}) {
		t.Fatalf("unexpected tie metrics: %#v", ties)
	}
}

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
