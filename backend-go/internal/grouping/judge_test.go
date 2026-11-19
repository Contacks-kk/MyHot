package grouping

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
)

type judgeModelStub struct {
	requests    []modelcall.Request
	outputs     []string
	rejected    []int64
	complete    []int64
	completeErr error
}

type evaluationUsageStub struct {
	ids []int64
}

type concurrentEvaluationStub struct {
	mu      sync.Mutex
	active  int
	peak    int
	calls   int
	started chan struct{}
	release chan struct{}
}

/** EvaluatePair records concurrency and waits for the test to release its fake model call. */
func (s *concurrentEvaluationStub) EvaluatePair(ctx context.Context, _, _ string, _, _ Report) (EvaluationPairJudgement, error) {
	s.mu.Lock()
	s.active++
	s.calls++
	s.peak = max(s.peak, s.active)
	s.mu.Unlock()
	s.started <- struct{}{}
	select {
	case <-ctx.Done():
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		return EvaluationPairJudgement{}, ctx.Err()
	case <-s.release:
	}
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	return EvaluationPairJudgement{
		PairResult: PairResult{Relation: RelationSameStory, Confidence: 0.9},
		ReceiptID:  71,
	}, nil
}

/** EvaluationUsage records the receipt IDs requested by the service. */
func (r *evaluationUsageStub) EvaluationUsage(_ context.Context, ids []int64) (EvaluationUsage, error) {
	r.ids = append([]int64(nil), ids...)
	return EvaluationUsage{TokensIn: 15, TokensOut: 8, AvgLatencyMS: 123}, nil
}

func (m *judgeModelStub) Call(_ context.Context, request modelcall.Request) (modelcall.Result, error) {
	m.requests = append(m.requests, request)
	if len(m.outputs) == 0 {
		return modelcall.Result{}, errors.New("no fixture response")
	}
	output := m.outputs[0]
	m.outputs = m.outputs[1:]
	return modelcall.Result{ReceiptID: 71, Completion: provider.Completion{Text: output}}, nil
}
func (m *judgeModelStub) Complete(_ context.Context, id int64) error {
	m.complete = append(m.complete, id)
	return m.completeErr
}
func (m *judgeModelStub) Reject(_ context.Context, id int64, _ string) error {
	m.rejected = append(m.rejected, id)
	return nil
}

func newJudgeServiceForTest(t *testing.T, model *judgeModelStub) *JudgeService {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	directory := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "backend-go", "config", "prompts"))
	prompts, err := editorial.LoadPromptsFrom(directory, "Example site")
	if err != nil {
		t.Fatal(err)
	}
	return NewJudgeService(model, prompts)
}

func TestJudgeServiceUsesVersionedPromptsAndRejectsNoCoverageOverride(t *testing.T) {
	model := &judgeModelStub{outputs: []string{`{"query":"summary","decisions":[{"id":"C1","relation":"SAME_OCCURRENCE","confidence":0.91,"note":"same event"}],"selection":{"addsValue":false,"reason":"covered"}}`}}
	service := newJudgeServiceForTest(t, model)
	candidate := Candidate{FactID: 12, StoryID: 22, FactTitle: "fact title", Members: 2, Score: .86,
		Report: Report{Title: "candidate title", Source: "source", Summary: stringPointer("candidate summary")}}
	result, err := service.JudgeBatch(context.Background(), "article-1", Report{Title: "query title", Source: "publisher", Summary: stringPointer("query summary")}, []Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := model.requests[0]
	version, _ := service.prompts.Version("group-batch")
	if result.ReceiptID != 71 || result.Verdicts[12].Relation != RelationSameOccurrence || result.Verdicts[12].Confidence != .91 || !result.Selection.AddsValue || result.Selection.Reason != "没有已公开精选的相关报道" {
		t.Fatalf("batch judgement contract changed: %#v", result)
	}
	if request.Capability != "group" || request.Purpose != "group_article" || request.Subject != "article:article-1" || request.PromptVersion != version || request.Temperature == nil || *request.Temperature != 0 || request.MaxTokens != 520 {
		t.Fatalf("batch model request changed: %#v", request)
	}
	if !strings.Contains(request.System, "你是新闻事件编辑") || !strings.Contains(request.User, "候选 C1") || !strings.Contains(request.User, "尚无公开精选") {
		t.Fatalf("rendered prompt lost its report context: system=%q user=%q", request.System, request.User)
	}
}

func TestJudgeServiceUsesSignalAndReviewCapabilities(t *testing.T) {
	model := &judgeModelStub{outputs: []string{
		`{"decisions":[{"id":"C1","relation":"SAME_STORY","confidence":0.88}]}`,
		`{"a":"A event","b":"B development","relation":"SAME_STORY","difference":"later response","confidence":0.82}`,
	}}
	service := newJudgeServiceForTest(t, model)
	candidate := Candidate{FactID: 8, StoryID: 4, Report: Report{Title: "candidate", Source: "source"}}
	signal, err := service.JudgeSignal(context.Background(), "signal-1", Report{Title: "social post", Source: "feed"}, []Candidate{candidate})
	if err != nil || signal.Verdicts[8].Relation != RelationSameStory || signal.ReceiptID != 71 {
		t.Fatalf("signal judgement = %#v err=%v", signal, err)
	}
	review, err := service.ConfirmMerge(context.Background(), "article-1", Report{Title: "query", Source: "source"}, candidate)
	if err != nil || review.Relation != RelationSameStory || review.Confidence != .82 || review.Difference != "later response" {
		t.Fatalf("pair review = %#v err=%v", review, err)
	}
	if model.requests[0].Capability != "group" || model.requests[0].Purpose != "group_signal" || model.requests[0].MaxTokens != 210 {
		t.Fatalf("signal model request = %#v", model.requests[0])
	}
	if model.requests[1].Capability != "groupReview" || model.requests[1].Purpose != "group_review" || model.requests[1].Subject != "article:article-1:fact:8" || model.requests[1].MaxTokens != 400 {
		t.Fatalf("review model request = %#v", model.requests[1])
	}
}

/** TestJudgeServiceComparesStoryRootsWithAReversedReview verifies both paid decisions and receipt completion. */
func TestJudgeServiceComparesStoryRootsWithAReversedReview(t *testing.T) {
	model := &judgeModelStub{outputs: []string{
		`{"a":"anchor event","b":"other event","relation":"SAME_STORY","difference":"same launch","confidence":0.91}`,
		`{"a":"other event","b":"anchor event","relation":"SAME_OCCURRENCE","difference":"same date","confidence":0.86}`,
	}}
	service := newJudgeServiceForTest(t, model)
	anchor := StoryRoot{StoryID: 10, Report: Report{Title: "anchor event", Source: "source-a"}}
	other := StoryRoot{StoryID: 20, Report: Report{Title: "other event", Source: "source-b"}}
	first, err := service.CompareStoryRoots(context.Background(), anchor, other, false)
	if err != nil || first.Relation != RelationSameStory || first.Confidence != 0.91 {
		t.Fatalf("first story comparison = %#v, err=%v", first, err)
	}
	second, err := service.CompareStoryRoots(context.Background(), anchor, other, true)
	if err != nil || second.Relation != RelationSameOccurrence || second.Confidence != 0.86 {
		t.Fatalf("review story comparison = %#v, err=%v", second, err)
	}
	if len(model.requests) != 2 || model.requests[0].Capability != "group" || model.requests[0].Purpose != "group_story" ||
		model.requests[0].Subject != "story:10:20" || model.requests[0].User != pairPrompt(anchor.Report, other.Report) ||
		model.requests[1].Capability != "groupReview" || model.requests[1].Purpose != "group_story_review" ||
		model.requests[1].Subject != "story:20:10" || model.requests[1].User != pairPrompt(other.Report, anchor.Report) {
		t.Fatalf("story comparison requests did not preserve their order: %#v", model.requests)
	}
	if len(model.complete) != 2 || model.complete[0] != 71 || model.complete[1] != 71 {
		t.Fatalf("story comparison receipts were not completed: %#v", model.complete)
	}
}

/** TestEvaluatePairUsesProductionPromptAndReceipt verifies model selection and failure handling. */
func TestEvaluatePairUsesProductionPromptAndReceipt(t *testing.T) {
	model := &judgeModelStub{outputs: []string{
		`{"a":"first","b":"second","relation":"SAME_STORY","difference":"later","confidence":0.82}`,
		`{"relation":"UNKNOWN","confidence":0.8}`,
	}}
	service := newJudgeServiceForTest(t, model)
	a := Report{Title: "first", Source: "source-a"}
	b := Report{Title: "second", Source: "source-b"}
	result, err := service.EvaluatePair(context.Background(), "case-1", "model-a", a, b)
	if err != nil || result.Relation != RelationSameStory || result.A != "first" || result.B != "second" || result.ReceiptID != 71 {
		t.Fatalf("evaluation result = %#v err=%v", result, err)
	}
	request := model.requests[0]
	if request.ModelKey != "model-a" || request.Capability != "groupReview" || request.Purpose != "eval_relation_pair" || request.Subject != "relation-gold:case-1" || request.User != pairPrompt(a, b) || request.MaxTokens != 400 || len(model.complete) != 1 || model.complete[0] != 71 {
		t.Fatalf("evaluation request = %#v, completed = %#v", request, model.complete)
	}
	_, err = service.EvaluatePair(context.Background(), "case-2", "model-a", a, b)
	var outputError *JudgementOutputError
	if !errors.As(err, &outputError) || outputError.ReceiptID != 71 || len(model.rejected) != 1 || model.rejected[0] != 71 || len(model.complete) != 1 {
		t.Fatalf("invalid evaluation should reject its receipt: err=%v rejected=%v completed=%v", err, model.rejected, model.complete)
	}
	model.outputs = []string{`{"a":"first","b":"second","relation":"SAME_STORY","difference":"later","confidence":0.82}`}
	model.completeErr = errors.New("receipt unavailable")
	result, err = service.EvaluatePair(context.Background(), "case-3", "model-a", a, b)
	if err == nil || result.ReceiptID != 71 {
		t.Fatalf("failed receipt completion must retain the receipt identity: result=%#v err=%v", result, err)
	}
}

/** TestEvaluateRelationSetSharesIdenticalPrompts verifies cases, errors and metrics use one model call per prompt. */
func TestEvaluateRelationSetSharesIdenticalPrompts(t *testing.T) {
	model := &judgeModelStub{outputs: []string{
		`{"a":"first","b":"second","relation":"SAME_STORY","difference":"later","confidence":0.82}`,
		`{"relation":"UNKNOWN","confidence":0.8}`,
	}}
	service := newJudgeServiceForTest(t, model)
	rows := []EvaluationGoldRow{
		{CaseID: "one", A: EvaluationGoldReport{Title: "first", Source: "source"}, B: EvaluationGoldReport{Title: "second", Source: "source"}, Gold: EvaluationGold{Relation: RelationSameStory}},
		{CaseID: "duplicate", A: EvaluationGoldReport{Title: "first", Source: "source"}, B: EvaluationGoldReport{Title: "second", Source: "source"}, Gold: EvaluationGold{Relation: RelationUnrelated}},
		{CaseID: "invalid", A: EvaluationGoldReport{Title: "other", Source: "source"}, B: EvaluationGoldReport{Title: "second", Source: "source"}, Gold: EvaluationGold{Relation: RelationUnrelated}},
		{CaseID: "invalid-duplicate", A: EvaluationGoldReport{Title: "other", Source: "source"}, B: EvaluationGoldReport{Title: "second", Source: "source"}, Gold: EvaluationGold{Relation: RelationUnrelated}},
	}
	report, err := EvaluateRelationSet(context.Background(), service, "model-a", rows, []float64{0.75, 0.8})
	if err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 2 || len(model.complete) != 1 || len(model.rejected) != 1 || len(report.Cases) != 4 || report.Reused != 1 || !report.Cases[1].Reused || report.Cases[2].Error == nil || report.Cases[2].ReceiptID == nil || *report.Cases[2].ReceiptID != 71 || report.Cases[3].Error == nil || report.Cases[3].ReceiptID == nil || *report.Cases[3].ReceiptID != *report.Cases[2].ReceiptID || report.Cases[3].Reused {
		t.Fatalf("unexpected evaluation cases: %#v, requests=%d completed=%v rejected=%v", report, len(model.requests), model.complete, model.rejected)
	}
	if report.Metrics.Evaluated != 2 || report.Metrics.Errors != 2 || report.Metrics.Accuracy != 0.5 || len(report.StoryThresholds) != 2 || report.StoryThresholds[0].TP != 1 || report.StoryThresholds[0].FP != 1 {
		t.Fatalf("unexpected evaluation metrics: %#v", report)
	}
	usageRepository := &evaluationUsageStub{}
	usage, err := RelationEvaluationUsage(context.Background(), usageRepository, report)
	if err != nil || !reflect.DeepEqual(usageRepository.ids, []int64{71}) || usage.TokensIn != 15 || usage.TokensOut != 8 || usage.AvgLatencyMS != 123 {
		t.Fatalf("evaluation usage = %#v, ids = %#v, err = %v", usage, usageRepository.ids, err)
	}
	modelReport, err := BuildRelationEvaluationModelReport(context.Background(), usageRepository, "model-a", 1500*time.Millisecond, report)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(modelReport)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Summary struct {
			Model       string `json:"model"`
			TokensIn    int64  `json:"tokensIn"`
			WallSeconds int64  `json:"wallSeconds"`
		} `json:"summary"`
		ConfusionMatrix map[string]map[string]int `json:"confusionMatrix"`
		Cases           []json.RawMessage         `json:"cases"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Summary.Model != "model-a" || decoded.Summary.TokensIn != 15 || decoded.Summary.WallSeconds != 2 || decoded.ConfusionMatrix["UNRELATED"]["SAME_STORY"] != 1 || len(decoded.Cases) != 4 {
		t.Fatalf("serialized model report = %s, err=%v", encoded, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EvaluateRelationSet(ctx, service, "model-a", rows, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled evaluation = %v", err)
	}
}

/** TestRelationEvaluationRunBuildsModelReport verifies validation precedes calls and results contain report metadata. */
func TestRelationEvaluationRunBuildsModelReport(t *testing.T) {
	model := &judgeModelStub{outputs: []string{`{"a":"first","b":"second","relation":"SAME_STORY","difference":"later","confidence":0.82}`}}
	service := NewRelationEvaluationService(newJudgeServiceForTest(t, model), &evaluationUsageStub{}, []string{"model-a"})
	options := RelationEvaluationOptions{
		Gold:       `{"caseId":"one","a":{"title":"first","source":"source"},"b":{"title":"second","source":"source"},"gold":{"relation":"SAME_STORY"}}`,
		Models:     []string{"model-a", "missing"},
		Split:      "all",
		Limit:      1,
		Seed:       -1,
		Thresholds: []float64{0.75, 0.8},
	}
	if _, err := service.Run(context.Background(), options); err == nil || len(model.requests) != 0 {
		t.Fatalf("unknown model should fail before any calls: err=%v calls=%d", err, len(model.requests))
	}
	options.Models = []string{"model-a"}
	run, err := service.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	report := run.Models["model-a"]
	if run.Meta.N != 1 || run.Meta.Seed != -1 || run.Meta.Split != "all" || run.Meta.PromptVersion == "" || run.Meta.CreatedAt.IsZero() || report.Summary.N != 1 || report.Summary.TokensIn != 15 || report.Summary.Accuracy != 1 || len(report.Cases) != 1 || len(report.StoryThresholds) != 2 {
		t.Fatalf("unexpected relation evaluation run: %#v", run)
	}
}

/** TestConcurrentEvaluationBoundsCalls verifies overlapping unique pairs and duplicate reuse. */
func TestConcurrentEvaluationBoundsCalls(t *testing.T) {
	judge := &concurrentEvaluationStub{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	rows := []EvaluationGoldRow{
		{CaseID: "a", A: EvaluationGoldReport{Title: "first", Source: "source"}, B: EvaluationGoldReport{Title: "second", Source: "source"}, Gold: EvaluationGold{Relation: RelationSameStory}},
		{CaseID: "duplicate", A: EvaluationGoldReport{Title: "first", Source: "source"}, B: EvaluationGoldReport{Title: "second", Source: "source"}, Gold: EvaluationGold{Relation: RelationUnrelated}},
		{CaseID: "b", A: EvaluationGoldReport{Title: "other", Source: "source"}, B: EvaluationGoldReport{Title: "second", Source: "source"}, Gold: EvaluationGold{Relation: RelationSameStory}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type outcome struct {
		report RelationEvaluationReport
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		report, err := EvaluateRelationSetConcurrent(ctx, judge, "model-a", rows, []float64{0.8}, 2)
		done <- outcome{report: report, err: err}
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-judge.started:
		case <-ctx.Done():
			t.Fatal("distinct model requests did not overlap")
		}
	}
	close(judge.release)
	result := <-done
	if result.err != nil || judge.calls != 2 || judge.peak != 2 || len(result.report.Cases) != 3 || result.report.Cases[0].CaseID != "a" || result.report.Cases[1].CaseID != "duplicate" || !result.report.Cases[1].Reused || result.report.Cases[2].CaseID != "b" {
		t.Fatalf("concurrent result = %#v, calls=%d peak=%d err=%v", result.report, judge.calls, judge.peak, result.err)
	}
}

/** TestConcurrentEvaluationSharesFailedDuplicateAndRetry preserves receipt-backed retry behavior. */
func TestConcurrentEvaluationSharesFailedDuplicateAndRetry(t *testing.T) {
	model := &judgeModelStub{outputs: []string{
		"not JSON",
		`{"a":"first","b":"second","relation":"SAME_STORY","difference":"later","confidence":0.82}`,
	}}
	service := newJudgeServiceForTest(t, model)
	rows := []EvaluationGoldRow{
		{
			CaseID: "retry-1",
			A:      EvaluationGoldReport{Title: "first", Source: "source"},
			B:      EvaluationGoldReport{Title: "second", Source: "source"},
			Gold:   EvaluationGold{Relation: RelationSameStory},
		},
		{
			CaseID: "retry-2",
			A:      EvaluationGoldReport{Title: "first", Source: "source"},
			B:      EvaluationGoldReport{Title: "second", Source: "source"},
			Gold:   EvaluationGold{Relation: RelationSameStory},
		},
	}
	failed, err := EvaluateRelationSetConcurrent(context.Background(), service, "model-a", rows, []float64{0.8}, 1)
	if err != nil || len(model.requests) != 1 || len(model.rejected) != 1 || failed.Metrics.Errors != 2 || failed.Cases[0].ReceiptID == nil || failed.Cases[1].ReceiptID == nil || *failed.Cases[0].ReceiptID != *failed.Cases[1].ReceiptID {
		t.Fatalf("failed duplicate evaluation = %#v requests=%d rejected=%v err=%v", failed, len(model.requests), model.rejected, err)
	}
	usage := &evaluationUsageStub{}
	retried, err := EvaluateRelationSetConcurrent(context.Background(), service, "model-a", rows, []float64{0.8}, 1)
	if err != nil || len(model.requests) != 2 || len(model.complete) != 1 || retried.Metrics.Errors != 0 || retried.Reused != 1 || !retried.Cases[1].Reused || retried.Cases[0].ReceiptID == nil || *retried.Cases[0].ReceiptID != *failed.Cases[0].ReceiptID {
		t.Fatalf("retried duplicate evaluation = %#v requests=%d complete=%v err=%v", retried, len(model.requests), model.complete, err)
	}
	report, err := BuildRelationEvaluationModelReport(context.Background(), usage, "model-a", time.Second, retried)
	if err != nil || len(usage.ids) != 1 || usage.ids[0] != *retried.Cases[0].ReceiptID || report.Summary.Evaluated != 2 {
		t.Fatalf("retried report lost duplicate receipt accounting: report=%#v usage=%v err=%v", report, usage.ids, err)
	}
}

func TestJudgeServiceRejectsIncompleteModelAnswer(t *testing.T) {
	model := &judgeModelStub{outputs: []string{`{"query":"q","decisions":[],"selection":{"addsValue":true,"reason":"valid"}}`}}
	service := newJudgeServiceForTest(t, model)
	_, err := service.JudgeBatch(context.Background(), "article", Report{Title: "query"}, []Candidate{{FactID: 1}}, nil)
	var outputError *JudgementOutputError
	if !errors.As(err, &outputError) || outputError.ReceiptID != 71 || len(model.rejected) != 1 || model.rejected[0] != 71 {
		t.Fatalf("incomplete judgement was not rejected: err=%v rejected=%v", err, model.rejected)
	}
}

func TestDescribeReportUsesBeijingTimeAndBoundedText(t *testing.T) {
	at := time.Date(2026, 10, 9, 0, 30, 0, 0, time.UTC)
	long := strings.Repeat("😀", 181)
	text := describeReport(Report{Title: "title", Source: "source", FirstParty: true, At: &at, Summary: &long}, "new", "extra", 360)
	if !strings.Contains(text, "2026-10-09 08:30") || !strings.Contains(text, "摘要："+strings.Repeat("😀", 180)) || strings.Contains(text, strings.Repeat("😀", 181)) {
		t.Fatalf("report prompt timestamp/truncation changed: %q", text)
	}
}

func stringPointer(value string) *string { return &value }
