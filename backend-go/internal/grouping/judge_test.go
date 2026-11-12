package grouping

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
)

type judgeModelStub struct {
	requests []modelcall.Request
	outputs  []string
	rejected []int64
	complete []int64
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
	return nil
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
	directory := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "shared", "industry", "prompts"))
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
