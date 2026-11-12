package storydigest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
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
