package contentpipeline

import (
	"context"
	"testing"
	"time"

	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/grouping"
	"aihot/backend-go/internal/publication"
	"aihot/backend-go/internal/receipts"
)

type pipelineRepositoryStub struct {
	route         *ArticleRoute
	relevance     string
	hasAnalysis   bool
	markedSkipped int
	completed     int
	deferred      int
	failures      int
	failed        int
	failureResult FailureResult
	failureErr    error
	deferRetryAt  time.Time
}

func (r *pipelineRepositoryStub) PendingAnalysis(context.Context, int) ([]AnalysisTask, error) {
	return []AnalysisTask{}, nil
}
func (r *pipelineRepositoryStub) PendingGrouping(context.Context, int) ([]GroupingTask, error) {
	return []GroupingTask{}, nil
}
func (r *pipelineRepositoryStub) Route(context.Context, string) (*ArticleRoute, error) {
	return r.route, nil
}
func (r *pipelineRepositoryStub) CurrentAnalysis(context.Context, string, int) (string, bool, error) {
	return r.relevance, r.hasAnalysis, nil
}
func (r *pipelineRepositoryStub) MarkSkipped(context.Context, string, int) error {
	r.markedSkipped++
	return nil
}
func (r *pipelineRepositoryStub) CompleteAnalysis(context.Context, string, int) error {
	r.completed++
	return nil
}
func (r *pipelineRepositoryStub) DeferAnalysis(context.Context, string, int, string, time.Duration) (time.Time, error) {
	r.deferred++
	return r.deferRetryAt, r.failureErr
}
func (r *pipelineRepositoryStub) RecordAnalysisFailure(context.Context, string, int, string, bool) (FailureResult, error) {
	r.failures++
	return r.failureResult, r.failureErr
}
func (r *pipelineRepositoryStub) FailAnalysis(context.Context, string, int, string, bool) error {
	r.failed++
	return r.failureErr
}

type analyzerStub struct {
	result *editorial.AnalyzeResult
	err    error
	calls  int
}

func (a *analyzerStub) Analyze(context.Context, string, editorial.AnalysisOptions) (*editorial.AnalyzeResult, error) {
	a.calls++
	return a.result, a.err
}

type grouperStub struct{}

func (grouperStub) Process(context.Context, string, bool) (grouping.Result, error) {
	return grouping.Result{}, nil
}

type publisherStub struct {
	calls int
	err   error
}

func (p *publisherStub) Publish(context.Context, string, publication.PublishOptions) (*publication.Result, error) {
	p.calls++
	return &publication.Result{}, p.err
}

func TestAnalyzeSavesThenPublishesAndRequestsGrouping(t *testing.T) {
	repository := &pipelineRepositoryStub{route: &ArticleRoute{ID: "item-1", Revision: 2, ProcessingState: "new", ParticipationMode: "editorial"}}
	analyzer := &analyzerStub{result: &editorial.AnalyzeResult{Output: &editorial.AnalysisOutput{Relevance: "pass"}}}
	publisher := &publisherStub{}
	service := NewService(repository, analyzer, grouperStub{}, publisher, func() time.Time { return time.Unix(100, 0) })

	outcome, err := service.Analyze(context.Background(), "item-1", "rerun-1")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != "pass" || !outcome.NeedGrouping || analyzer.calls != 1 || publisher.calls != 1 || repository.completed != 1 {
		t.Fatalf("pipeline outcome=%#v analyzer=%d publisher=%d completed=%d", outcome, analyzer.calls, publisher.calls, repository.completed)
	}
}

func TestAnalyzeReusesCurrentAnalysisAfterRestart(t *testing.T) {
	repository := &pipelineRepositoryStub{
		route:     &ArticleRoute{ID: "item-1", Revision: 3, ProcessingState: "analyzed", ParticipationMode: "editorial"},
		relevance: "pass", hasAnalysis: true,
	}
	analyzer := &analyzerStub{}
	publisher := &publisherStub{}
	service := NewService(repository, analyzer, grouperStub{}, publisher, nil)

	outcome, err := service.Analyze(context.Background(), "item-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.NeedGrouping || analyzer.calls != 0 || publisher.calls != 1 || repository.completed != 1 {
		t.Fatalf("saved analysis was not reused: outcome=%#v analyzer=%d publisher=%d", outcome, analyzer.calls, publisher.calls)
	}
}

func TestAnalyzeDefersBodyAndSettlesSignalWithoutModel(t *testing.T) {
	t.Run("body pending", func(t *testing.T) {
		repository := &pipelineRepositoryStub{route: &ArticleRoute{ID: "item-1", Revision: 1, ProcessingState: "new", ParticipationMode: "editorial", NeedsBody: true}}
		analyzer := &analyzerStub{result: &editorial.AnalyzeResult{NeedsBody: true}}
		publisher := &publisherStub{}
		service := NewService(repository, analyzer, grouperStub{}, publisher, nil)
		outcome, err := service.Analyze(context.Background(), "item-1", "")
		if err != nil || outcome.State != "waiting-body" || analyzer.calls != 0 || publisher.calls != 0 || repository.completed != 0 {
			t.Fatalf("body wait outcome=%#v err=%v", outcome, err)
		}
	})
	t.Run("hot signal", func(t *testing.T) {
		repository := &pipelineRepositoryStub{route: &ArticleRoute{ID: "item-2", Revision: 1, ParticipationMode: "hot_signal"}}
		analyzer := &analyzerStub{}
		publisher := &publisherStub{}
		service := NewService(repository, analyzer, grouperStub{}, publisher, nil)
		outcome, err := service.Analyze(context.Background(), "item-2", "")
		if err != nil || !outcome.NeedGrouping || !outcome.SignalOnly || analyzer.calls != 0 || repository.markedSkipped != 1 || publisher.calls != 1 {
			t.Fatalf("signal settlement outcome=%#v analyzer=%d publisher=%d err=%v", outcome, analyzer.calls, publisher.calls, err)
		}
	})
}

func TestSharedPipelineTaskPriorityKeepsLiveAheadOfSignalsAndHistory(t *testing.T) {
	if live, signal, history := taskPriority(false, "editorial"), taskPriority(false, "hot_signal"), taskPriority(true, "editorial"); live != 1 || signal != 2 || history != 3 {
		t.Fatalf("task priorities = live:%d signal:%d history:%d", live, signal, history)
	}
}

func TestAnalyzeFailurePolicyKeepsBusyCallOutOfFailureCount(t *testing.T) {
	now := time.Unix(100, 0)
	retryAt := now.Add(time.Minute)
	repository := &pipelineRepositoryStub{
		route:        &ArticleRoute{ID: "item-1", Revision: 1, ProcessingState: "new", ParticipationMode: "editorial"},
		deferRetryAt: retryAt,
	}
	analyzer := &analyzerStub{err: receipts.ErrBusy}
	service := NewService(repository, analyzer, grouperStub{}, &publisherStub{}, func() time.Time { return now })

	outcome, err := service.Analyze(context.Background(), "item-1", "")
	if err != nil || outcome.State != "waiting" || outcome.RetryAfter != time.Minute || repository.deferred != 1 || repository.failures != 0 {
		t.Fatalf("busy receipt outcome=%#v deferred=%d failures=%d err=%v", outcome, repository.deferred, repository.failures, err)
	}
}

func TestAnalysisRetryWindowsMatchLegacyBackoff(t *testing.T) {
	want := []time.Duration{5, 10, 20, 40, 60, 120, 240, 360}
	for i, minutes := range want {
		got, ok := analysisRetryDelay(i + 1)
		if !ok || got != minutes*time.Minute {
			t.Fatalf("attempt %d retry = %s, want %s", i+1, got, minutes*time.Minute)
		}
	}
	if _, ok := analysisRetryDelay(len(want) + 1); ok {
		t.Fatal("retry schedule allowed an extra attempt")
	}
}
