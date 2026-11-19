package storydigest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
	"aihot/backend-go/internal/receipts"
)

type fakeRepository struct {
	story      *Story
	reports    []DigestReport
	last       *SavedDigest
	saved      *SaveInput
	cleared    bool
	stale      []int64
	acceptSave bool
}

func (r *fakeRepository) Story(context.Context, int64) (*Story, error) { return r.story, nil }
func (r *fakeRepository) Reports(context.Context, int64, time.Time) ([]DigestReport, error) {
	return r.reports, nil
}
func (r *fakeRepository) LastDigest(context.Context, int64) (*SavedDigest, error) { return r.last, nil }
func (r *fakeRepository) Clear(context.Context, int64, int) (bool, error) {
	r.cleared = true
	return true, nil
}
func (r *fakeRepository) Save(_ context.Context, input SaveInput) (bool, error) {
	r.saved = &input
	return r.acceptSave, nil
}
func (r *fakeRepository) StaleStories(context.Context, int, time.Time) ([]int64, error) {
	return r.stale, nil
}

type fakeModel struct {
	request  modelcall.Request
	result   modelcall.Result
	err      error
	rejected int64
	reason   string
	called   int
}

func (m *fakeModel) Call(_ context.Context, request modelcall.Request) (modelcall.Result, error) {
	m.called++
	m.request = request
	return m.result, m.err
}
func (m *fakeModel) Reject(_ context.Context, id int64, reason string) error {
	m.rejected, m.reason = id, reason
	return nil
}

func TestComposeWritesDigestWithCurrentEvidenceAndNewReportMarkers(t *testing.T) {
	service, repository, model, now := fixture(t, `{"title":"事件总览","digest":"这件事目前由相关公司按计划推进，影响后续产品安排。"}`)
	service.now = func() time.Time { return now }
	result, err := service.Compose(context.Background(), 23, false)
	if err != nil || !result.Updated || result.Version != 5 {
		t.Fatalf("Compose = %#v, %v", result, err)
	}
	if model.called != 1 || model.request.Capability != "digest" || model.request.Purpose != "story_digest" || model.request.Subject != "story:23@1" {
		t.Fatalf("model request = %#v calls=%d", model.request, model.called)
	}
	if model.request.Temperature == nil || *model.request.Temperature != 0.3 || model.request.MaxTokens != 1200 {
		t.Fatalf("sampling = temperature:%v tokens:%d", model.request.Temperature, model.request.MaxTokens)
	}
	if !strings.Contains(model.request.User, "【新】报道 article-1") || !strings.Contains(model.request.User, "事实条件与来源证据：") || !strings.Contains(model.request.System, "事件概览") {
		t.Fatalf("request lost the digest contract: %s", model.request.User)
	}
	if repository.saved == nil || repository.saved.Version != 5 || repository.saved.StoryVersion != 4 || repository.saved.Digest != "这件事目前由相关公司按计划推进，影响后续产品安排。" || repository.saved.ReceiptID == nil || *repository.saved.ReceiptID != 91 {
		t.Fatalf("saved digest = %#v", repository.saved)
	}
}

func TestComposeReusesMatchingDigestWithoutAnotherPaidCall(t *testing.T) {
	service, repository, model, _ := fixture(t, `{"title":"ignored","digest":"unused model output"}`)
	hash, err := DigestInputsHash(repository.reports)
	if err != nil {
		t.Fatal(err)
	}
	repository.story.Digest = stringPointer("已有事件综述，当前证据没有变化。")
	repository.last = &SavedDigest{ArticleIDs: []string{"article-1"}, InputsHash: &hash, Digest: *repository.story.Digest}
	result, err := service.Compose(context.Background(), 23, false)
	if err != nil || result.Updated || model.called != 0 || repository.saved != nil {
		t.Fatalf("unchanged digest was rewritten: result=%#v calls=%d saved=%#v err=%v", result, model.called, repository.saved, err)
	}
}

func TestComposeRewritesCorrectedEvidenceWithoutNewMarkers(t *testing.T) {
	service, repository, model, _ := fixture(t, `{"title":"更新后的事件","digest":"这件事目前由相关公司按计划推进，影响后续产品安排。"}`)
	repository.last = &SavedDigest{ArticleIDs: []string{"article-1"}, Digest: "旧综述", InputsHash: stringPointer("old-hash")}
	if _, err := service.Compose(context.Background(), 23, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.request.User, "报道内容或事实证据经过更正") || strings.Contains(model.request.User, "【新】") {
		t.Fatalf("correction prompt = %s", model.request.User)
	}
}

func TestComposeClearsDigestWhenNoCurrentEvidenceRemains(t *testing.T) {
	service, repository, model, _ := fixture(t, `{"title":"ignored","digest":"unused model output"}`)
	repository.reports = nil
	result, err := service.Compose(context.Background(), 23, false)
	if err != nil || !result.Updated || !repository.cleared || model.called != 0 {
		t.Fatalf("empty digest outcome = %#v cleared=%t calls=%d err=%v", result, repository.cleared, model.called, err)
	}
}

func TestComposeRejectsUnusableOutputAndLeavesReceiptFailed(t *testing.T) {
	service, repository, model, _ := fixture(t, "not JSON")
	result, err := service.Compose(context.Background(), 23, false)
	if err == nil || result.Updated || model.rejected != 91 || model.reason != "unusable story digest output" || repository.saved != nil {
		t.Fatalf("invalid output was committed: result=%#v rejected=%d saved=%#v err=%v", result, model.rejected, repository.saved, err)
	}
}

func TestComposeDiscardsOutputWhenRepositoryRejectsStaleWrite(t *testing.T) {
	service, repository, model, _ := fixture(t, `{"title":"事件总览","digest":"这件事目前由相关公司按计划推进，影响后续产品安排。"}`)
	repository.acceptSave = false
	result, err := service.Compose(context.Background(), 23, false)
	if err != nil || result.Updated || model.called != 1 {
		t.Fatalf("stale write outcome = %#v calls=%d err=%v", result, model.called, err)
	}
}

func TestDigestInputHashMatchesCanonicalTypeScriptShape(t *testing.T) {
	report := DigestReport{ID: "article-1", FactID: 7, Title: "标题", Summary: stringPointer("摘要"), SourceName: "来源", FirstParty: true,
		At: time.Date(2026, 10, 8, 3, 4, 5, 123_456_000, time.UTC), FactSubject: stringPointer("主体"), FactAction: stringPointer("动作"),
		FactObject: stringPointer("对象"), FactConditions: stringPointer("仅限该地区"), Evidence: stringPointer("来源原话"),
		StructuredFact: json.RawMessage(`{"evidence":"原文证据","conditions":[{"text":"地区限制","quote":"该地区"}]}`)}
	got, err := DigestInputsHash([]DigestReport{report})
	if err != nil {
		t.Fatal(err)
	}
	const expected = "9321d823f10259f8675d23c398e081c2636d7f9809e450461ed9ec2422244b73"
	if got != expected {
		t.Fatalf("input hash = %s, want %s", got, expected)
	}
}

func TestParseOutputAcceptsFencedJSONAndEnforcesCodeUnitLimits(t *testing.T) {
	parsed, err := parseOutput("```json\n{\"title\":\"事件\",\"digest\":\"1234567890\"}\n```")
	if err != nil || parsed.Title != "事件" || parsed.Digest != "1234567890" {
		t.Fatalf("parsed = %#v, %v", parsed, err)
	}
	if _, err := parseOutput(`{"title":"","digest":"短"}`); err == nil {
		t.Fatal("short digest passed validation")
	}
}

/** TestEvaluationCasesMatchLiveInput checks strict snapshots, ordering and correction semantics. */
func TestEvaluationCasesMatchLiveInput(t *testing.T) {
	const row = `{"caseId":"one","story":{"title":"事件","previousDigest":"旧综述"},"knownArticleIds":["first"],"reports":[{"id":"later","publishedAt":"2026-09-02T09:00:00+08:00","source":"媒体","title":"后续","fact":{"id":2}},{"id":"first","publishedAt":"2026-09-01T09:00:00+08:00","source":"官方","firstParty":true,"title":"首报","fact":{"id":1}}]}`
	rows, err := ParseEvaluationCases("// comment\n" + row + "\n")
	if err != nil || len(rows) != 1 {
		t.Fatalf("parse evaluation case: rows=%#v err=%v", rows, err)
	}
	input, err := BuildEvaluationInput(rows[0])
	if err != nil || !strings.Contains(input, "上一版综述：旧综述") || !strings.Contains(input, "【新】报道 later") || strings.Contains(input, "【新】报道 first") || strings.Index(input, "报道 first") > strings.Index(input, "报道 later") {
		t.Fatalf("incremental input changed: %q, %v", input, err)
	}
	corrected := "corrected"
	rows[0].InputMode = &corrected
	input, err = BuildEvaluationInput(rows[0])
	if err != nil || !strings.Contains(input, "经过更正") || strings.Contains(input, "旧综述") || strings.Contains(input, "【新】") {
		t.Fatalf("corrected input changed: %q, %v", input, err)
	}
	for _, bad := range []string{
		row + "\n" + row,
		strings.Replace(row, `"caseId":"one"`, `"caseId":"one","unknown":1`, 1),
		strings.Replace(row, `"firstParty":true`, `"firstParty":null`, 1),
		strings.Replace(row, `"knownArticleIds":["first"]`, `"knownArticleIds":null`, 1),
		strings.Replace(row, `2026-09-01T09:00:00+08:00`, `invalid`, 1),
	} {
		if _, err := ParseEvaluationCases(bad); err == nil {
			t.Fatalf("invalid case was accepted: %s", bad)
		}
	}
}

/** TestEvaluationInputMatchesCompose verifies that snapshot data produces the live model request. */
func TestEvaluationInputMatchesCompose(t *testing.T) {
	service, repository, model, now := fixture(t, `{"title":"事件总览","digest":"这件事目前由相关公司按计划推进，影响后续产品安排。"}`)
	service.now = func() time.Time { return now }
	if _, err := service.Compose(context.Background(), repository.story.ID, false); err != nil {
		t.Fatal(err)
	}
	report := repository.reports[0]
	firstParty := report.FirstParty
	row := EvaluationCase{
		CaseID: "snapshot",
		Story: &EvaluationStory{
			Title:          repository.story.Title,
			PreviousDigest: repository.story.Digest,
		},
		Reports: []EvaluationReport{{
			ID:          report.ID,
			PublishedAt: report.At.Format(time.RFC3339Nano),
			Source:      report.SourceName,
			FirstParty:  &firstParty,
			Title:       report.Title,
			Summary:     report.Summary,
			Fact: &EvaluationFact{
				ID:         report.FactID,
				Subject:    report.FactSubject,
				Action:     report.FactAction,
				Object:     report.FactObject,
				Conditions: report.FactConditions,
				Evidence:   report.Evidence,
				Structured: report.StructuredFact,
			},
		}},
	}
	input, err := BuildEvaluationInput(row)
	if err != nil || input != model.request.User {
		t.Fatalf("snapshot differs from live request: snapshot=%q live=%q err=%v", input, model.request.User, err)
	}
}

/** TestEvaluationStorySnapshotPreservesEvidence verifies export normalization and rendering. */
func TestEvaluationStorySnapshotPreservesEvidence(t *testing.T) {
	ids, err := ParseEvaluationStoryIDs(" 00000000-0000-4000-8000-ABCDEF012345,00000000-0000-4000-8000-abcdef012345 ")
	if err != nil || len(ids) != 1 || ids[0] != "00000000-0000-4000-8000-abcdef012345" {
		t.Fatalf("unexpected public IDs: %#v, %v", ids, err)
	}
	if _, err := ParseEvaluationStoryIDs("bad-id"); err == nil {
		t.Fatal("malformed story ID accepted")
	}
	_, repository, _, _ := fixture(t, "")
	row := evaluationCaseFromStory(ids[0], repository.story.Title, true, repository.reports)
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseEvaluationCases(string(data))
	if err != nil || len(parsed) != 1 || *parsed[0].InputMode != "corrected" || len(parsed[0].KnownArticleIDs) != 0 || parsed[0].Reports[0].Fact.Evidence == nil || !strings.Contains(string(parsed[0].Reports[0].Fact.Structured), "公开可用") {
		t.Fatalf("snapshot lost evidence or mode: %s, %v", data, err)
	}
}

type evaluationModelStub struct {
	mu        sync.Mutex
	requests  []modelcall.Request
	completed []int64
}

/** Call returns deterministic fake results for live and candidate requests. */
func (m *evaluationModelStub) Call(_ context.Context, request modelcall.Request) (modelcall.Result, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	if strings.Contains(request.System, "candidate") {
		return modelcall.Result{}, &receipts.OutcomeUnknownError{ReceiptID: 42}
	}
	return modelcall.Result{ReceiptID: 41, Completion: provider.Completion{Text: `{"title":"标题","digest":"这是长度足够的评测综述内容。"}`}}, nil
}

/** Complete records successful fake receipts. */
func (m *evaluationModelStub) Complete(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed = append(m.completed, id)
	return nil
}

/** Reject satisfies the evaluation client without contacting an external provider. */
func (m *evaluationModelStub) Reject(context.Context, int64, string) error {
	return errors.New("unexpected rejection")
}

/** TestEvaluationBudgetAndReport keeps prompt comparisons and receipt failures isolated. */
func TestEvaluationBudgetAndReport(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "rules.md"), []byte("shared rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	pack, err := editorial.LoadPromptsFrom(directory, "Example")
	if err != nil {
		t.Fatal(err)
	}
	model := &evaluationModelStub{}
	service := NewEvaluationService(model, pack, Prompts{System: "live {{siteName}}", Version: "story-digest@1"}, "Example", []string{"allowed"})
	candidate := "candidate {{siteName}} {{> rules}}"
	options := EvaluationOptions{
		Cases:       `{"caseId":"one","story":{"title":"事件"},"reports":[{"id":"a","publishedAt":"2026-09-01T09:00:00+08:00","source":"媒体","title":"报道","fact":{"id":1}}]}`,
		Models:      []string{"allowed"},
		Candidate:   &candidate,
		MaxCalls:    1,
		Concurrency: 2,
	}
	if _, err := service.Run(context.Background(), options); err == nil || len(model.requests) != 0 {
		t.Fatalf("over-budget evaluation made model calls: err=%v calls=%d", err, len(model.requests))
	}
	options.MaxCalls = 2
	same := "live {{siteName}}"
	options.Candidate = &same
	if _, err := service.Run(context.Background(), options); err == nil || len(model.requests) != 0 {
		t.Fatalf("identical candidate made model calls: err=%v calls=%d", err, len(model.requests))
	}
	options.Candidate = &candidate
	options.Models = []string{"unknown"}
	if _, err := service.Run(context.Background(), options); err == nil || len(model.requests) != 0 {
		t.Fatalf("unknown model made calls: err=%v calls=%d", err, len(model.requests))
	}
	options.Models = []string{"allowed"}
	run, err := service.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	result := run.Models["allowed"][0]
	if run.Calls != 2 || len(model.requests) != 2 || len(model.completed) != 1 || result.Live.Digest == nil || result.Live.ReceiptID == nil || *result.Live.ReceiptID != 41 || result.Candidate == nil || result.Candidate.Error == nil || result.Candidate.ReceiptID == nil || *result.Candidate.ReceiptID != 42 {
		t.Fatalf("unexpected evaluation output: run=%#v requests=%d completed=%v", run, len(model.requests), model.completed)
	}
	if model.requests[0].User == "" || model.requests[0].User != model.requests[1].User || model.requests[0].Subject != model.requests[1].Subject || model.requests[0].PromptVersion == model.requests[1].PromptVersion {
		t.Fatalf("live and candidate prompts did not compare the same case: %#v", model.requests)
	}
	if run.Prompts["candidate"].Version == "" || run.Prompts["candidate"].System != "candidate Example shared rules" {
		t.Fatalf("candidate prompt was not rendered or versioned: %#v", run.Prompts)
	}
}

type evaluationUsageStub struct {
	requests [][]int64
}

/** EvaluationUsage records the requested IDs and returns deterministic usage. */
func (s *evaluationUsageStub) EvaluationUsage(_ context.Context, ids []int64) (receipts.EvaluationUsage, error) {
	s.requests = append(s.requests, append([]int64(nil), ids...))
	return receipts.EvaluationUsage{TokensIn: int64(len(ids) * 10), TokensOut: int64(len(ids) * 2), AvgLatencyMS: 7}, nil
}

/** TestEvaluationReportDeduplicatesReceiptUsage verifies the legacy comparison fields. */
func TestEvaluationReportDeduplicatesReceiptUsage(t *testing.T) {
	id := int64(41)
	failure := "unknown"
	caseResult := EvaluationResult{
		CaseID:     "one",
		StoryTitle: "事件",
		Input:      "首次撰写",
		Reports:    1,
		Live:       EvaluationCall{ReceiptID: &id, Reused: true},
		Candidate:  &EvaluationCall{Error: &failure},
	}
	run := EvaluationRun{
		Calls:       4,
		Prompts:     map[string]EvaluationPrompt{"live": {Version: "live@1"}, "candidate": {Version: "candidate@1"}},
		Models:      map[string][]EvaluationResult{"model": {caseResult, caseResult}},
		WallSeconds: map[string]int64{"model": 3},
	}
	usage := &evaluationUsageStub{}
	report, err := BuildEvaluationReport(context.Background(), usage, run, "cases.jsonl", "candidate.md", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	model := report.Models["model"]
	if report.Meta.Cases != "cases.jsonl" || report.Meta.Prompts["candidate"].File != "candidate.md" || model.Summary.Live.Calls != 2 || model.Summary.Live.Reused != 2 || model.Summary.Live.TokensIn != 10 || model.Summary.Candidate == nil || model.Summary.Candidate.Errors != 2 || model.Cases[0].Live.TokensOut != 2 || model.Summary.WallSeconds != 3 {
		t.Fatalf("unexpected comparison report: %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(encoded), `"tokensIn":10`) || !strings.Contains(string(encoded), `"avgLatencyMs":7`) {
		t.Fatalf("usage fields are missing from report JSON: %s, %v", encoded, err)
	}
	markdown := EvaluationMarkdown(report)
	for _, fragment := range []string{"候选提示词：candidate.md", "成功 2/2", "案例 `one`", "回执 #41", "生成失败：unknown"} {
		if !strings.Contains(markdown, fragment) {
			t.Fatalf("comparison markdown is missing %q: %s", fragment, markdown)
		}
	}
}

func fixture(t *testing.T, output string) (*Service, *fakeRepository, *fakeModel, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 8, 3, 10, 0, 0, time.UTC)
	repository := &fakeRepository{
		story: &Story{ID: 23, Title: "产品更新事件", Digest: stringPointer("旧摘要"), Version: 4, Origin: "model"},
		reports: []DigestReport{{StoryID: 23, ID: "article-1", Title: "产品更新", Summary: stringPointer("公司发布更新"), SourceName: "官方", FirstParty: true,
			At: now.Add(-time.Minute), FactID: 31, FactSubject: stringPointer("公司"), FactAction: stringPointer("发布"), FactObject: stringPointer("产品更新"),
			FactConditions: stringPointer("公开可用"), Evidence: stringPointer("官方公告原句"),
			StructuredFact: json.RawMessage(`{"evidence":"官方公告原句","conditions":[{"text":"公开可用","quote":"面向用户开放"}]}`)}},
		acceptSave: true,
	}
	model := &fakeModel{result: modelcall.Result{ReceiptID: 91, Completion: provider.Completion{Text: output}}}
	service := NewService(repository, model, Prompts{System: "你是 {{siteName}} 的事件编辑，写事件概览。", Version: "story-digest@test"}, Config{SiteName: "示例站"})
	return service, repository, model, now
}

func stringPointer(value string) *string { return &value }

var _ ModelClient = (*fakeModel)(nil)
