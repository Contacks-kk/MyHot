package editorial

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
)

type modelFixture struct {
	mu        sync.Mutex
	answers   map[string][]string
	calls     []modelcall.Request
	nextID    int64
	vision    bool
	callError map[string][]error
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

type imageFixture string

func (i imageFixture) FirstImage(context.Context, Input) (string, error) { return string(i), nil }

type providerRejectionError string

func (e providerRejectionError) Error() string        { return string(e) }
func (providerRejectionError) ProviderRejected() bool { return true }

func newPipelineFixture(t *testing.T, models *modelFixture) *AnalysisService {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	industry := filepath.Join(repositoryRoot, "shared", "industry")
	t.Setenv("INDUSTRY_VOCABULARY_FILE", filepath.Join(industry, "vocabulary.json"))
	t.Setenv("INDUSTRY_CATEGORIES_FILE", filepath.Join(industry, "categories.json"))
	t.Setenv("INDUSTRY_ENTITIES_FILE", filepath.Join(industry, "entities.json"))
	t.Setenv("INDUSTRY_SELECTION_FILE", filepath.Join(industry, "selection.json"))
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
	prompts, err := LoadPromptsFrom(filepath.Join(industry, "prompts"), "MyHOT")
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
