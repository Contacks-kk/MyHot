package editorial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
	"aihot/backend-go/internal/receipts"
)

type modelFixture struct {
	mu        sync.Mutex
	answers   map[string][]string
	calls     []modelcall.Request
	nextID    int64
	vision    bool
	callError map[string][]error
}

/** TestSelectionGoldSamplingAndInputs preserves the legacy sample and source conversion. */
func TestSelectionGoldSamplingAndInputs(t *testing.T) {
	const template = `{"caseId":"%s","material":{"title":"标题","originalTitle":"Original","publishedAt":"2026-09-30T09:00:00+08:00","sourceName":"Source","bodyZh":"翻译","bodyOriginal":"original body"},"sourceFacts":{"sourceKind":"rss","sourceTier":"T1","firstParty":true},"gold":{"decision":"select"}}`
	var lines []string
	for _, id := range []string{"a", "b", "c", "d"} {
		lines = append(lines, fmt.Sprintf(template, id))
	}
	rows, err := ParseSelectionGold("// comment\n" + strings.Join(lines, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	sample := SampleSelectionGold(rows, "all", 3, 7)
	if len(sample) != 3 || sample[0].CaseID != "a" || sample[1].CaseID != "c" || sample[2].CaseID != "b" {
		t.Fatalf("seeded sample changed: %#v", sample)
	}
	input, err := SelectionInput(rows[0])
	if err != nil || input.Title != "Original" || input.BodyText == nil || *input.BodyText != "original body" || input.Source.Tier != "T1" || !input.Source.FirstParty || input.PublishedAt == nil {
		t.Fatalf("RSS selection input changed: %#v, %v", input, err)
	}
	rows[0].SourceFacts.SourceKind = "x_search"
	input, err = SelectionInput(rows[0])
	if err != nil || input.BodyText != nil || input.XPost == nil || input.XPost.Text != "original body" || input.XPost.AuthorName != "Source" {
		t.Fatalf("X selection input changed: %#v, %v", input, err)
	}
	if _, err := ParseSelectionGold(strings.Replace(lines[0], `"select"`, `"invalid"`, 1)); err == nil {
		t.Fatal("invalid gold decision accepted")
	}
}

/** TestSelectionEvaluationSharesScoresAndKeepsTierThresholds checks the paid-request boundary. */
func TestSelectionEvaluationSharesScoresAndKeepsTierThresholds(t *testing.T) {
	models := &modelFixture{answers: map[string][]string{
		"prefilter_article": {`{"label":"PASS","reason":"relevant"}`},
		"score_article":     {`{"attentionScore":65}`},
	}}
	service := newPipelineFixture(t, models)
	service.selection.Thresholds = map[string]int{"T1": 60, "T2": 70}
	const text = `{"caseId":"one","material":{"title":"Event","sourceName":"Source 1","bodyOriginal":"the same substantial article body"},"sourceFacts":{"sourceKind":"rss","sourceTier":"T1"},"gold":{"decision":"select"}}
{"caseId":"two","material":{"title":"Event","sourceName":"Source 2","bodyOriginal":"the same substantial article body"},"sourceFacts":{"sourceKind":"rss","sourceTier":"T2"},"gold":{"decision":"reject"}}`
	rows, err := ParseSelectionGold(text)
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.EvaluateSelection(context.Background(), "score-model", rows, 2)
	if err != nil || len(results) != 2 || results[0].Output == nil || results[1].Output == nil || !results[0].Output.Selected || results[1].Output.Selected {
		t.Fatalf("selection decisions = %#v, %v", results, err)
	}
	if len(results[0].ReceiptIDs) != 3 || len(results[1].ReceiptIDs) != 3 || results[0].ReceiptIDs[1] != results[1].ReceiptIDs[1] || results[0].ReceiptIDs[2] != results[1].ReceiptIDs[2] {
		t.Fatalf("score receipts were not shared: %#v", results)
	}
	models.mu.Lock()
	defer models.mu.Unlock()
	if len(models.calls) != 4 {
		t.Fatalf("model calls = %d; want two prefilters and two shared scores", len(models.calls))
	}
}

/** TestSelectionEvaluationSharesFailedScores keeps failures and their receipt usage. */
func TestSelectionEvaluationSharesFailedScores(t *testing.T) {
	models := &modelFixture{answers: map[string][]string{
		"prefilter_article": {`{"label":"PASS","reason":"relevant"}`},
		"score_article":     {`not JSON`},
	}}
	service := newPipelineFixture(t, models)
	const row = `{"caseId":"one","material":{"title":"Event","sourceName":"Source","bodyOriginal":"the same substantial article body"},"sourceFacts":{"sourceKind":"rss","sourceTier":"T1"},"gold":{"decision":"select"}}`
	rows, err := ParseSelectionGold(row + "\n" + strings.Replace(row, `"one"`, `"two"`, 1))
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.EvaluateSelection(context.Background(), "score-model", rows, 2)
	if err != nil || results[0].Error == nil || results[1].Error == nil || results[0].Output != nil || results[1].Output != nil || len(results[0].ReceiptIDs) != 2 || len(results[1].ReceiptIDs) != 2 || results[0].ReceiptIDs[1] != results[1].ReceiptIDs[1] {
		t.Fatalf("shared failure lost results or receipts: %#v, %v", results, err)
	}
	models.mu.Lock()
	defer models.mu.Unlock()
	if len(models.calls) != 3 {
		t.Fatalf("failed score was repeated: %d calls", len(models.calls))
	}
}

type selectionUsageStub struct {
	ids []int64
}

/** EvaluationUsage records the unique receipt IDs requested by a report. */
func (s *selectionUsageStub) EvaluationUsage(_ context.Context, ids []int64) (receipts.EvaluationUsage, error) {
	s.ids = append([]int64(nil), ids...)
	return receipts.EvaluationUsage{TokensIn: 20, TokensOut: 5, AvgLatencyMS: 10}, nil
}

/** TestSelectionReportKeepsDecisiveMetricsAndUniqueUsage checks the SelectBench projection. */
func TestSelectionReportKeepsDecisiveMetricsAndUniqueUsage(t *testing.T) {
	const fixture = `{"caseId":"%s","material":{"title":"Event","sourceName":"Source"},"sourceFacts":{"sourceKind":"rss"},"gold":{"decision":"%s"}}`
	rows, err := ParseSelectionGold(fmt.Sprintf(fixture, "one", "select") + "\n" + fmt.Sprintf(fixture, "two", "reject") + "\n" + fmt.Sprintf(fixture, "three", "either") + "\n" + fmt.Sprintf(fixture, "four", "select"))
	if err != nil {
		t.Fatal(err)
	}
	score := 65
	results := []SelectionEvaluationResult{
		{Row: rows[0], Output: &AnalysisOutput{Selected: true, Relevance: "pass", Score: &score}, ReceiptIDs: []int64{1, 2}},
		{Row: rows[1], Output: &AnalysisOutput{Selected: true, Relevance: "pass", Score: &score}, ReceiptIDs: []int64{3, 2}},
		{Row: rows[2], Output: &AnalysisOutput{Selected: false, Relevance: "block"}},
		{Row: rows[3], Error: stringPointer("failed"), ReceiptIDs: []int64{4}},
	}
	usage := &selectionUsageStub{}
	report, err := BuildSelectionModelReport(context.Background(), usage, "model", results, 64, 66, time.Second)
	if err != nil || report.Summary.TP != 1 || report.Summary.FP != 1 || report.Summary.Either != 1 || report.Summary.Errors != 1 || report.Summary.Decisive != 2 || report.Summary.Precision != 0.5 || report.Summary.F1 != 0.667 || len(usage.ids) != 4 || len(report.Mistakes) != 1 {
		t.Fatalf("unexpected selection report: %#v, ids=%v, err=%v", report, usage.ids, err)
	}
	if len(report.Sweep) != 2 || report.Sweep[0].Selected != 1 || report.Sweep[1].Selected != 0 || report.Cases[0].ReceiptID == nil || *report.Cases[0].ReceiptID != 1 || report.Cases[3].Decision != nil {
		t.Fatalf("unexpected threshold sweep or cases: %#v", report)
	}
}

/** TestSelectionEvaluationRunPreservesModelOrder checks persisted report ordering and metadata. */
func TestSelectionEvaluationRunPreservesModelOrder(t *testing.T) {
	models := &modelFixture{answers: map[string][]string{
		"prefilter_article": {`{"label":"PASS","reason":"relevant"}`, `{"label":"PASS","reason":"relevant"}`},
		"score_article":     {`{"attentionScore":65}`, `{"attentionScore":65}`},
	}}
	analysis := newPipelineFixture(t, models)
	service := NewSelectionEvaluationService(analysis, &selectionUsageStub{}, []string{"second", "first"})
	const gold = `{"caseId":"one","material":{"title":"Event","sourceName":"Source","bodyOriginal":"the same substantial article body"},"sourceFacts":{"sourceKind":"rss","sourceTier":"T1"},"gold":{"decision":"select"}}`
	report, err := service.Run(context.Background(), SelectionEvaluationOptions{
		Gold: gold, Models: []string{"second", "first"}, Limit: 1, Split: "all", Concurrency: 1,
		Seed: 7, SweepStart: 40, SweepEnd: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	second := strings.Index(string(encoded), `"second"`)
	first := strings.Index(string(encoded), `"first"`)
	if second < 0 || first < 0 || second > first || report.Meta.N != 1 || report.Meta.PromptVersion == "" {
		t.Fatalf("report order or metadata changed: %s", encoded)
	}
	var decoded SelectionEvaluationRun
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if names := decoded.ModelNames(); len(names) != 2 || names[0] != "second" || names[1] != "first" {
		t.Fatalf("model order was lost on audit reload: %v", names)
	}
}

/** TestSelectionEvaluationReportFileNameKeepsReportsInsideEvalDirectory protects split-derived paths. */
func TestSelectionEvaluationReportFileNameKeepsReportsInsideEvalDirectory(t *testing.T) {
	if got := SelectionEvaluationReportFileName("../../../../outside", 1, 123); got != "selection-outside-1-123.json" {
		t.Fatalf("unsafe split filename = %q", got)
	}
	if got := SelectionEvaluationReportFileName("../", 0, 123); got != "selection-all-0-123.json" {
		t.Fatalf("empty split filename = %q", got)
	}
	if got := SelectionEvaluationReportFileName(strings.Repeat("a", 81), 2, 456); got != "selection-"+strings.Repeat("a", 80)+"-2-456.json" {
		t.Fatalf("long split filename was not capped: %q", got)
	}
}

func (f *modelFixture) Call(_ context.Context, request modelcall.Request) (modelcall.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, request)
	if errors := f.callError[request.Purpose]; len(errors) > 0 {
		err := errors[0]
		f.callError[request.Purpose] = errors[1:]
		if err != nil {
			return modelcall.Result{}, err
		}
	}
	answers := f.answers[request.Purpose]
	if len(answers) == 0 {
		return modelcall.Result{}, errors.New("no model answer fixture")
	}
	answer := answers[0]
	if len(answers) > 1 {
		f.answers[request.Purpose] = answers[1:]
	}
	f.nextID++
	model := request.ModelKey
	if model == "" {
		model = request.Capability
	}
	return modelcall.Result{Capability: request.Capability, Model: model, ReceiptID: f.nextID, Completion: provider.Completion{Text: answer}}, nil
}

func (f *modelFixture) ResolveKey(_ context.Context, capability string) (string, error) {
	return capability + "-fixture", nil
}
func (f *modelFixture) SupportsVision(_ context.Context, _ string) (bool, error) {
	return f.vision, nil
}
func (f *modelFixture) Complete(context.Context, int64) error       { return nil }
func (f *modelFixture) Reject(context.Context, int64, string) error { return nil }

type drainingModelFixture struct {
	mu               sync.Mutex
	calls            map[string]int
	cached           map[string]modelcall.Result
	nextReceipt      int64
	structureStarted chan struct{}
	releaseStructure chan struct{}
	firstScoreFailed chan struct{}
}

/** Call simulates a model service that drains a parallel structure request after a score failure. */
func (f *drainingModelFixture) Call(ctx context.Context, request modelcall.Request) (modelcall.Result, error) {
	key := request.Purpose + ":" + request.AttemptTag
	f.mu.Lock()
	if cached, ok := f.cached[key]; ok {
		cached.Reused = true
		f.mu.Unlock()
		return cached, nil
	}
	f.calls[key]++
	callNumber := f.calls[key]
	f.mu.Unlock()

	if request.Purpose == "structure_article" && callNumber == 1 {
		close(f.structureStarted)
		select {
		case <-f.releaseStructure:
		case <-ctx.Done():
			return modelcall.Result{}, ctx.Err()
		}
	}
	if request.Purpose == "score_article" && strings.HasSuffix(request.AttemptTag, "score-1") && callNumber == 1 {
		close(f.firstScoreFailed)
		return modelcall.Result{}, errors.New("fake first score failure")
	}

	answer := map[string]string{
		"prefilter_article":  `{"label":"PASS","reason":"有明确事件"}`,
		"structure_article":  `{"scope":"single","category":"ai-models","tags":["模型发布"],"subjects":["openai"],"fact":{"title":"OpenAI 发布模型","evidence":"OpenAI 发布 GPT-6。","conditions":[]}}`,
		"understand_article": `{"itemType":"model_release","authorRole":"principal","tags":[],"editorialJudgment":"官方发布","titleZh":"OpenAI 发布 GPT-6","summaryZh":"OpenAI 发布 GPT-6 模型。"}`,
	}
	if request.Purpose == "score_article" {
		answer[request.Purpose] = `{"attentionScore":61}`
		if strings.HasSuffix(request.AttemptTag, "score-2") {
			answer[request.Purpose] = `{"attentionScore":60}`
		}
	}
	text, ok := answer[request.Purpose]
	if !ok {
		return modelcall.Result{}, fmt.Errorf("unexpected model purpose %q", request.Purpose)
	}
	f.mu.Lock()
	f.nextReceipt++
	result := modelcall.Result{Capability: request.Capability, Model: "fake-model", ReceiptID: f.nextReceipt,
		Completion: provider.Completion{Text: text}}
	f.cached[key] = result
	f.mu.Unlock()
	return result, nil
}

/** ResolveKey supplies the deterministic score model used by pipeline retry tests. */
func (*drainingModelFixture) ResolveKey(context.Context, string) (string, error) {
	return "fake-score-model", nil
}

/** SupportsVision keeps test analyses on the text-only model path. */
func (*drainingModelFixture) SupportsVision(context.Context, string) (bool, error) { return false, nil }

/** Reject accepts parsed fake responses without mutating the test fixture. */
func (*drainingModelFixture) Reject(context.Context, int64, string) error { return nil }

/** TestAnalysisRetryDrainsStructureBeforeWriting reuses completed work after the failed score retry. */
func TestAnalysisRetryDrainsStructureBeforeWriting(t *testing.T) {
	models := &drainingModelFixture{
		calls: make(map[string]int), cached: make(map[string]modelcall.Result),
		structureStarted: make(chan struct{}), releaseStructure: make(chan struct{}), firstScoreFailed: make(chan struct{}),
	}
	service := newPipelineFixture(t, models)
	input := pipelineInput("OpenAI 发布 GPT-6。")
	options := AnalysisOptions{AttemptTag: "shutdown-retry"}
	type outcome struct {
		run AnalysisRun
		err error
	}
	first := make(chan outcome, 1)
	go func() {
		run, err := service.Run(context.Background(), input, options)
		first <- outcome{run: run, err: err}
	}()
	select {
	case <-models.structureStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("parallel structure request did not start")
	}
	select {
	case <-models.firstScoreFailed:
	case <-time.After(5 * time.Second):
		t.Fatal("first score request did not fail")
	}
	select {
	case result := <-first:
		t.Fatalf("analysis returned before the in-flight structure request drained: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}
	models.mu.Lock()
	writingBeforeDrain := models.calls["understand_article:shutdown-retry:understand"] + models.calls["summarize_article:shutdown-retry:summarize"]
	models.mu.Unlock()
	if writingBeforeDrain != 0 {
		t.Fatalf("writing started while structure was still in flight: %d calls", writingBeforeDrain)
	}
	close(models.releaseStructure)
	failed := <-first
	if failed.err == nil {
		t.Fatal("first analysis attempt unexpectedly succeeded")
	}

	second, err := service.Run(context.Background(), input, options)
	secondOutput := NormalizeAnalysis(second, service.entities)
	if err != nil || second.Structure == nil || !second.Structure.Reused || second.Prefilter.Label != "PASS" || second.Writing == nil || !secondOutput.Selected {
		t.Fatalf("retry failed to reuse drained structure and finish: run=%#v err=%v", second, err)
	}
	models.mu.Lock()
	defer models.mu.Unlock()
	if models.calls["structure_article:shutdown-retry:structure"] != 1 || models.calls["score_article:shutdown-retry:score-1"] != 2 || models.calls["score_article:shutdown-retry:score-2"] != 1 || models.calls["understand_article:shutdown-retry:understand"] != 1 {
		t.Fatalf("retry repeated or skipped model stages: %#v", models.calls)
	}
}

func TestAnalysisPipelineRunsPrefilterScoreStructureAndUnderstand(t *testing.T) {
	models := &modelFixture{answers: map[string][]string{
		"prefilter_article":  {`{"label":"PASS","reason":"有明确事件"}`},
		"score_article":      {`{"attentionScore":61}`, `{"attentionScore":60}`},
		"structure_article":  {`{"scope":"single","category":"ai-models","tags":["模型发布","Agent"],"subjects":["openai"],"fact":{"title":"OpenAI 发布模型","evidence":"OpenAI 发布 GPT-6。","conditions":[]}}`},
		"understand_article": {`{"itemType":"model_release","authorRole":"principal","tags":[],"editorialJudgment":"官方发布","titleZh":"OpenAI 发布 GPT-6","summaryZh":"OpenAI 发布 GPT-6 模型。"}`},
	}, callError: map[string][]error{}}
	service := newPipelineFixture(t, models)
	body := "OpenAI 发布 GPT-6。"
	input := pipelineInput(body)
	run, err := service.Run(context.Background(), input, AnalysisOptions{AttemptTag: "admin-rerun-9"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Prefilter.Label != "PASS" || run.Scores == nil || run.Scores.Threshold != 60 || len(run.Scores.Values) != 2 || run.Writing == nil || run.Writing.Kind != "understand" || run.Structure == nil {
		t.Fatalf("analysis run = %#v", run)
	}
	if len(run.Prefilter.Model) == 0 || run.Prefilter.ReceiptID != 1 || len(run.Writing.ReceiptIDs) != 1 {
		t.Fatalf("receipt metadata = %#v", run)
	}
	output := NormalizeAnalysis(run, service.entities)
	if output.Relevance != "pass" || !output.Selected || output.Score == nil || *output.Score != 60 || !contains(output.Tags, "OpenAI") {
		t.Fatalf("normalized analysis = %#v", output)
	}
	models.mu.Lock()
	defer models.mu.Unlock()
	if len(models.calls) != 5 {
		t.Fatalf("model calls = %d, want prefilter + 2 scores + structure + understand", len(models.calls))
	}
	var scores int
	for _, call := range models.calls {
		if call.Purpose == "score_article" {
			scores++
			if !strings.HasPrefix(call.AttemptTag, "admin-rerun-9:score-") {
				t.Errorf("score attempt tag = %q", call.AttemptTag)
			}
		}
		if call.Subject != "article:article-1@1" {
			t.Errorf("call subject = %q", call.Subject)
		}
	}
	if scores != 2 {
		t.Fatalf("score calls = %d", scores)
	}
}

func TestAnalysisPipelineConvertsUnsupportedBlockAndStopsKnownBlock(t *testing.T) {
	models := &modelFixture{answers: map[string][]string{
		"prefilter_article": {`{"label":"BLOCK","reason":"文本证据不足"}`},
		"score_article":     {`{"attentionScore":10}`, `{"attentionScore":10}`},
		"structure_article": {`{"scope":"unknown","category":null,"tags":[],"subjects":[],"fact":null}`},
	}}
	service := newPipelineFixture(t, models)
	empty := pipelineInput("")
	empty.Title = "含糊标题"
	run, err := service.Run(context.Background(), empty, AnalysisOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Prefilter.Label != "UNKNOWN" || run.Structure == nil || run.Writing == nil || run.Writing.Kind != "none" {
		t.Fatalf("missing-evidence block should continue as unknown: %#v", run)
	}
	models = &modelFixture{answers: map[string][]string{"prefilter_article": {`{"label":"BLOCK","reason":"明确无关"}`}}}
	service = newPipelineFixture(t, models)
	input := pipelineInput("这篇文章详细说明了普通科技设备的上市活动和价格。")
	run, err = service.Run(context.Background(), input, AnalysisOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Prefilter.Label != "BLOCK" || run.Structure != nil || run.Scores != nil || run.Writing != nil {
		t.Fatalf("known block must stop after prefilter: %#v", run)
	}
	if len(models.calls) != 1 || models.calls[0].Purpose != "prefilter_article" {
		t.Fatalf("known block made extra model calls: %#v", models.calls)
	}
}

/** TestAnalysisPipelineStopsWritingWhenStructureFails prevents a second paid writing request. */
func TestAnalysisPipelineStopsWritingWhenStructureFails(t *testing.T) {
	models := &modelFixture{answers: map[string][]string{
		"prefilter_article": {`{"label":"PASS","reason":"有明确事件"}`},
		"score_article":     {`{"attentionScore":61}`, `{"attentionScore":60}`},
		"structure_article": {"not JSON"},
	}}
	service := newPipelineFixture(t, models)
	if _, err := service.Run(context.Background(), pipelineInput("OpenAI 发布 GPT-6。"), AnalysisOptions{}); err == nil {
		t.Fatal("invalid structure unexpectedly completed")
	}
	models.mu.Lock()
	defer models.mu.Unlock()
	var structure, writing int
	for _, call := range models.calls {
		if call.Purpose == "structure_article" {
			structure++
		}
		if call.Purpose == "understand_article" || call.Purpose == "summarize_article" {
			writing++
		}
	}
	if structure != 1 || writing != 0 {
		t.Fatalf("failed structure led to wrong follow-up calls: structure=%d writing=%d calls=%#v", structure, writing, models.calls)
	}
}

func TestAnalysisPipelineSummarizesLowScoreArticlesAndRetriesUnderstandWithoutImage(t *testing.T) {
	models := &modelFixture{vision: true, answers: map[string][]string{
		"prefilter_article":  {`{"label":"PASS","reason":"相关"}`},
		"score_article":      {`{"attentionScore":20}`, `{"attentionScore":30}`},
		"structure_article":  {`{"scope":"single","category":"industry","tags":["行业动态"],"subjects":[],"fact":null}`},
		"understand_article": {`{"itemType":"industry_event","authorRole":"principal","tags":[],"editorialJudgment":"","titleZh":"标题","summaryZh":"摘要"}`},
		"summarize_article":  {"title_zh: OpenAI 更新产品\nsummary_zh: OpenAI 更新产品，面向开发者发布新的 API。"},
	}, callError: map[string][]error{}}
	service := newPipelineFixture(t, models)
	body := "OpenAI 发布了一项面向开发者的新 API，提供了更快的调用速度和更低的价格。"
	input := pipelineInput(body)
	input.Source.Kind = "rss"
	input.Media = []Media{{Kind: "image", URL: "https://images.example.test/chart.png"}}
	service.images = imageFixture("data:image/png;base64,aGVsbG8=")
	models.callError["understand_article"] = []error{providerRejectionError("unsupported image format"), nil}
	// Raise the score over the understand floor, so the image-capable understanding path runs.
	models.answers["score_article"] = []string{`{"attentionScore":51}`, `{"attentionScore":52}`}
	run, err := service.Run(context.Background(), input, AnalysisOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Writing == nil || run.Writing.Kind != "understand" {
		t.Fatalf("understand should recover without the rejected image: %#v", run.Writing)
	}
	imageCalls := 0
	understandCalls := 0
	for _, call := range models.calls {
		if call.Purpose == "understand_article" {
			understandCalls++
			if len(call.Images) > 0 {
				imageCalls++
			}
		}
	}
	if understandCalls != 2 || imageCalls != 1 {
		t.Fatalf("understand retry calls=%d image calls=%d", understandCalls, imageCalls)
	}
}

func TestAnalysisPipelineUsesSummaryPromptBelowUnderstandFloor(t *testing.T) {
	models := &modelFixture{answers: map[string][]string{
		"prefilter_article": {`{"label":"PASS","reason":"相关"}`},
		"score_article":     {`{"attentionScore":20}`, `{"attentionScore":30}`},
		"structure_article": {`{"scope":"single","category":"industry","tags":["行业动态"],"subjects":[],"fact":null}`},
		"summarize_article": {"title_zh: OpenAI 更新产品\nsummary_zh: OpenAI 更新产品，面向开发者发布了新的 API。"},
	}}
	service := newPipelineFixture(t, models)
	body := "OpenAI 面向开发者发布了全新的 API，并介绍了产品功能、价格与服务可用情况。"
	run, err := service.Run(context.Background(), pipelineInput(body), AnalysisOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Scores == nil || run.Writing == nil || run.Writing.Kind != "summarize" || run.Writing.Model == nil || *run.Writing.Model != "summarize" {
		t.Fatalf("low-scoring article should use summary writing: %#v", run.Writing)
	}
}

/** TestAnalysisPipelineKeepsShortChinesePostsAndTranslatesFilteredWriting verifies both writing fallbacks. */
func TestAnalysisPipelineKeepsShortChinesePostsAndTranslatesFilteredWriting(t *testing.T) {
	t.Run("short Chinese X post", func(t *testing.T) {
		const text = "今天把智能体接进了工作流，效果不错。"
		models := &modelFixture{answers: map[string][]string{
			"prefilter_article":  {`{"label":"PASS","reason":"有明确事件"}`},
			"score_article":      {`{"attentionScore":61}`, `{"attentionScore":60}`},
			"structure_article":  {`{"scope":"single","category":"ai-models","tags":[],"subjects":[],"fact":null}`},
			"understand_article": {`{"itemType":"model_release","authorRole":"principal","tags":[],"editorialJudgment":"","titleZh":"` + text + `","summaryZh":"` + text + `"}`},
		}}
		service := newPipelineFixture(t, models)
		input := pipelineInput(text)
		input.Source.Kind = "x_search"
		input.XPost = &XPost{Text: text}
		run, err := service.Run(context.Background(), input, AnalysisOptions{})
		if err != nil || run.Writing == nil || run.Writing.TitleZh != text || run.Writing.SummaryZh != text {
			t.Fatalf("short Chinese post copy = %#v, err=%v", run.Writing, err)
		}
		for _, call := range models.calls {
			if call.Purpose == "summarize_article" {
				t.Fatal("short Chinese post started a translation request")
			}
		}
	})

	t.Run("content filtered understanding", func(t *testing.T) {
		models := &modelFixture{answers: map[string][]string{
			"prefilter_article": {`{"label":"PASS","reason":"有明确事件"}`},
			"score_article":     {`{"attentionScore":61}`, `{"attentionScore":60}`},
			"structure_article": {`{"scope":"single","category":"ai-models","tags":[],"subjects":[],"fact":null}`},
			"summarize_article": {"title_zh: OpenAI 发布 GPT-6\nsummary_zh: OpenAI 发布 GPT-6，并公布了评测结果和价格。"},
		}, callError: map[string][]error{
			"understand_article": {&provider.Error{Message: "contentFilter"}},
		}}
		service := newPipelineFixture(t, models)
		input := pipelineInput("OpenAI released GPT-6 with benchmark and pricing details. " + strings.Repeat("The release explains product availability. ", 8))
		run, err := service.Run(context.Background(), input, AnalysisOptions{})
		if err != nil || run.Writing == nil || run.Writing.Kind != "summarize" || run.Writing.Model == nil || *run.Writing.Model != "summarize" {
			t.Fatalf("filtered understanding fallback = %#v, err=%v", run.Writing, err)
		}
		var understandCalls, summarizeCalls int
		for _, call := range models.calls {
			if call.Purpose == "understand_article" {
				understandCalls++
			}
			if call.Purpose == "summarize_article" {
				summarizeCalls++
			}
		}
		if understandCalls != 1 || summarizeCalls != 1 {
			t.Fatalf("filtered writing calls = understand:%d summarize:%d", understandCalls, summarizeCalls)
		}
	})
}

type imageFixture string

func (i imageFixture) FirstImage(context.Context, Input) (string, error) { return string(i), nil }

type providerRejectionError string

func (e providerRejectionError) Error() string        { return string(e) }
func (providerRejectionError) ProviderRejected() bool { return true }

func newPipelineFixture(t *testing.T, models ModelClient) *AnalysisService {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	configDir := filepath.Join(repositoryRoot, "backend-go", "config")
	t.Setenv("INDUSTRY_VOCABULARY_FILE", filepath.Join(repositoryRoot, "backend-go", "config", "vocabulary.json"))
	t.Setenv("INDUSTRY_CATEGORIES_FILE", filepath.Join(repositoryRoot, "backend-go", "config", "categories.json"))
	t.Setenv("INDUSTRY_ENTITIES_FILE", filepath.Join(repositoryRoot, "backend-go", "config", "entities.json"))
	t.Setenv("INDUSTRY_SELECTION_FILE", filepath.Join(repositoryRoot, "backend-go", "config", "selection.json"))
	vocabulary, err := LoadVocabulary()
	if err != nil {
		t.Fatal(err)
	}
	categories, err := interests.LoadCategories()
	if err != nil {
		t.Fatal(err)
	}
	entities, err := interests.LoadEntityEntries()
	if err != nil {
		t.Fatal(err)
	}
	selection, err := LoadSelectionRules()
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := LoadPromptsFrom(filepath.Join(configDir, "prompts"), "MyHOT")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAnalysisService(models, prompts, vocabulary, categories, entities, selection, nil,
		func() time.Time { return time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func pipelineInput(body string) Input {
	return Input{ID: "article-1", Revision: 1, Title: "OpenAI 发布 GPT-6", URL: "https://openai.com/blog/gpt-6",
		BodyText: &body, BodyStatus: "ok", Source: Source{Name: "OpenAI 官方博客", Kind: "rss", Tier: "T1", FirstParty: true}}
}
