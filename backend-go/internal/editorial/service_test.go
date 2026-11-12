package editorial

import (
	"context"
	"errors"
	"testing"

	"aihot/backend-go/internal/interests"
)

type repositoryFixture struct {
	input  *Input
	loaded int
	saved  int
	run    AnalysisRun
	output AnalysisOutput
	err    error
}

func (r *repositoryFixture) LoadInput(context.Context, string) (*Input, error) {
	r.loaded++
	return r.input, r.err
}

func (r *repositoryFixture) SaveAnalysis(_ context.Context, _ Input, run AnalysisRun, output AnalysisOutput, version string) (AnalyzeResult, error) {
	r.saved++
	r.run, r.output = run, output
	if version == "" {
		return AnalyzeResult{}, errors.New("prompt version is empty")
	}
	return AnalyzeResult{AnalysisID: int64Pointer(9), Output: &output, Reused: analysisReused(run)}, r.err
}

func TestWaitsForPageMatchesBodyQueueRouting(t *testing.T) {
	input := Input{URL: "https://publisher.example/story", BodyStatus: "pending", Source: Source{Kind: "rss"}}
	if !WaitsForPage(input) {
		t.Fatal("pending article with a fetchable URL must wait for page extraction")
	}
	body := "body"
	input.BodyText = &body
	if WaitsForPage(input) {
		t.Fatal("article with body text must be analyzed")
	}
	input.BodyText = nil
	input.Source.Kind = "mp_account"
	if WaitsForPage(input) {
		t.Fatal("WeChat account articles are not fetched as ordinary pages")
	}
	input.Source.Kind = "rss"
	input.XPost = &XPost{Text: "post"}
	if WaitsForPage(input) {
		t.Fatal("X posts are analyzed from their post material")
	}
}

func TestProcessingServiceWaitsForBodyBeforeModelOrPersistence(t *testing.T) {
	input := &Input{ID: "article", URL: "https://publisher.example/story", BodyStatus: "pending", Source: Source{Kind: "rss"}}
	repository := &repositoryFixture{input: input}
	service := &ProcessingService{repository: repository, analysis: &AnalysisService{}}
	result, err := service.Analyze(context.Background(), input.ID, AnalysisOptions{})
	if err != nil || result == nil || !result.NeedsBody || result.AnalysisID != nil || result.Output != nil || repository.saved != 0 {
		t.Fatalf("body wait result=%#v err=%v saved=%d", result, err, repository.saved)
	}
}

func TestProcessingServiceCommitsNormalizedRunThroughRepository(t *testing.T) {
	body := "普通消费电子商品的发布和价格信息。"
	input := &Input{ID: "article", URL: "https://publisher.example/story", BodyStatus: "ok", BodyText: &body, Source: Source{Kind: "rss"}}
	models := &modelFixture{answers: map[string][]string{
		"prefilter_article": {`{"label":"BLOCK","reason":"明确无关"}`},
	}, callError: map[string][]error{}}
	analysis := newPipelineFixture(t, models)
	repository := &repositoryFixture{input: input}
	service := NewProcessingService(repository, analysis, map[string]interests.Entity{})
	result, err := service.Analyze(context.Background(), input.ID, AnalysisOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.AnalysisID == nil || *result.AnalysisID != 9 || result.Output == nil || result.Output.Relevance != "block" || repository.saved != 1 {
		t.Fatalf("analysis result=%#v saved=%d", result, repository.saved)
	}
}

func int64Pointer(value int64) *int64 { return &value }
