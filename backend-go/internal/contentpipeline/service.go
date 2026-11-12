package contentpipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"aihot/backend-go/internal/articlebody"
	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/grouping"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
	"aihot/backend-go/internal/publication"
	"aihot/backend-go/internal/receipts"
)

var analysisRetryMinutes = []int{5, 10, 20, 40, 60, 120, 240, 360}

type ArticleRoute struct {
	ID                string
	Revision          int
	ProcessingState   string
	ParticipationMode string
	NeedsBody         bool
	Backfill          bool
	PublishedAt       *time.Time
	DiscoveredAt      time.Time
}

type AnalysisTask struct {
	ArticleID  string
	AttemptTag string
	Priority   int
}

type GroupingTask struct {
	ArticleID  string
	SignalOnly bool
	Priority   int
}

type FailureResult struct {
	RetryAt time.Time
	State   string
}

type Repository interface {
	PendingAnalysis(context.Context, int) ([]AnalysisTask, error)
	PendingGrouping(context.Context, int) ([]GroupingTask, error)
	Route(context.Context, string) (*ArticleRoute, error)
	CurrentAnalysis(context.Context, string, int) (string, bool, error)
	MarkSkipped(context.Context, string, int) error
	CompleteAnalysis(context.Context, string, int) error
	DeferAnalysis(context.Context, string, int, string, time.Duration) (time.Time, error)
	RecordAnalysisFailure(context.Context, string, int, string, bool) (FailureResult, error)
	FailAnalysis(context.Context, string, int, string, bool) error
}

type Analyzer interface {
	Analyze(context.Context, string, editorial.AnalysisOptions) (*editorial.AnalyzeResult, error)
}

type Grouper interface {
	Process(context.Context, string, bool) (grouping.Result, error)
}

type Publisher interface {
	Publish(context.Context, string, publication.PublishOptions) (*publication.Result, error)
}

type AnalysisOutcome struct {
	State        string
	RetryAfter   time.Duration
	NeedGrouping bool
	SignalOnly   bool
}

type Service struct {
	repository Repository
	analyzer   Analyzer
	grouper    Grouper
	publisher  Publisher
	now        func() time.Time
}

func NewService(repository Repository, analyzer Analyzer, grouper Grouper, publisher Publisher, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, analyzer: analyzer, grouper: grouper, publisher: publisher, now: now}
}

func (s *Service) Scan(ctx context.Context, limit int) ([]AnalysisTask, []GroupingTask, error) {
	if s == nil || s.repository == nil {
		return nil, nil, errors.New("shared content pipeline repository is unavailable")
	}
	analysis, err := s.repository.PendingAnalysis(ctx, limit)
	if err != nil {
		return nil, nil, err
	}
	grouping, err := s.repository.PendingGrouping(ctx, limit)
	if err != nil {
		return nil, nil, err
	}
	return analysis, grouping, nil
}

// Analyze is idempotent across a worker restart after the analysis row was saved. Publication and
// grouping are retried from durable article/analysis state without issuing another paid request.
func (s *Service) Analyze(ctx context.Context, articleID, attemptTag string) (AnalysisOutcome, error) {
	if s == nil || s.repository == nil || s.analyzer == nil || s.publisher == nil {
		return AnalysisOutcome{}, errors.New("shared content pipeline dependencies are incomplete")
	}
	route, err := s.repository.Route(ctx, articleID)
	if err != nil || route == nil {
		return AnalysisOutcome{State: "missing"}, err
	}
	if route.NeedsBody {
		return AnalysisOutcome{State: "waiting-body"}, nil
	}
	if route.ParticipationMode != "editorial" {
		if err := s.repository.MarkSkipped(ctx, articleID, route.Revision); err != nil {
			return AnalysisOutcome{}, err
		}
		if _, err := s.publisher.Publish(ctx, articleID, publication.PublishOptions{}); err != nil {
			return AnalysisOutcome{}, err
		}
		return AnalysisOutcome{State: "skipped", NeedGrouping: route.ParticipationMode == "hot_signal" && !isHistorical(*route), SignalOnly: true}, nil
	}

	relevance, hasAnalysis, err := s.repository.CurrentAnalysis(ctx, articleID, route.Revision)
	if err != nil {
		return AnalysisOutcome{}, err
	}
	if !hasAnalysis {
		if route.ProcessingState != "new" {
			return AnalysisOutcome{State: route.ProcessingState}, nil
		}
		result, analyzeErr := s.analyzer.Analyze(ctx, articleID, editorial.AnalysisOptions{AttemptTag: attemptTag})
		if analyzeErr != nil {
			return s.handleAnalysisFailure(ctx, *route, analyzeErr)
		}
		if result == nil {
			return AnalysisOutcome{State: "missing"}, nil
		}
		if result.Stale {
			return AnalysisOutcome{State: "stale"}, nil
		}
		if result.NeedsBody || result.Output == nil {
			return AnalysisOutcome{State: "waiting-body"}, nil
		}
		relevance = result.Output.Relevance
	}
	if _, err := s.publisher.Publish(ctx, articleID, publication.PublishOptions{}); err != nil {
		return AnalysisOutcome{}, err
	}
	if err := s.repository.CompleteAnalysis(ctx, articleID, route.Revision); err != nil {
		return AnalysisOutcome{}, err
	}
	return AnalysisOutcome{State: relevance, NeedGrouping: relevance == "pass"}, nil
}

func needsBody(candidate articlebody.Candidate, bodyPipelineEnabled bool) bool {
	if candidate.XPostPresent && !bodyPipelineEnabled {
		return false
	}
	return articlebody.ShouldExtract(candidate)
}

func taskPriority(historical bool, participationMode string) int {
	if historical {
		return 3
	}
	if participationMode == "hot_signal" {
		return 2
	}
	return 1
}

func (s *Service) Group(ctx context.Context, articleID string, signalOnly bool) (grouping.Result, error) {
	if s == nil || s.grouper == nil {
		return grouping.Result{}, errors.New("shared content grouping service is unavailable")
	}
	return s.grouper.Process(ctx, articleID, signalOnly)
}

func (s *Service) handleAnalysisFailure(ctx context.Context, route ArticleRoute, failure error) (AnalysisOutcome, error) {
	if ctx.Err() != nil {
		return AnalysisOutcome{}, failure
	}
	clean := context.WithoutCancel(ctx)
	message := failure.Error()
	var unknown *receipts.OutcomeUnknownError
	if errors.As(failure, &unknown) {
		message = fmt.Sprintf("receipt %d outcome unknown", unknown.ReceiptID)
		if err := s.repository.FailAnalysis(clean, route.ID, route.Revision, message, false); err != nil {
			return AnalysisOutcome{}, err
		}
		return AnalysisOutcome{State: "unknown-receipt"}, nil
	}
	if errors.Is(failure, receipts.ErrBusy) {
		retryAt, err := s.repository.DeferAnalysis(clean, route.ID, route.Revision, message, time.Minute)
		return AnalysisOutcome{State: "waiting", RetryAfter: retryDuration(s.now(), retryAt)}, err
	}
	var budget *receipts.BudgetExceededError
	if errors.As(failure, &budget) {
		delay := time.Duration(max(1, budget.RetryAfterSecond)) * time.Second
		retryAt, err := s.repository.DeferAnalysis(clean, route.ID, route.Revision, message, delay)
		return AnalysisOutcome{State: "waiting", RetryAfter: retryDuration(s.now(), retryAt)}, err
	}
	var rejected interface{ ProviderRejected() bool }
	if errors.As(failure, &rejected) && rejected.ProviderRejected() {
		var providerFailure *provider.Error
		if errors.As(failure, &providerFailure) && providerFailure.Retryable {
			return s.recordRetry(clean, route, message, false)
		}
		if err := s.repository.FailAnalysis(clean, route.ID, route.Revision, message, true); err != nil {
			return AnalysisOutcome{}, err
		}
		return AnalysisOutcome{State: "failed"}, nil
	}
	var output *modelcall.OutputError
	return s.recordRetry(clean, route, message, errors.As(failure, &output))
}

func (s *Service) recordRetry(ctx context.Context, route ArticleRoute, message string, outputFailure bool) (AnalysisOutcome, error) {
	result, err := s.repository.RecordAnalysisFailure(ctx, route.ID, route.Revision, message, outputFailure)
	if err != nil {
		return AnalysisOutcome{}, err
	}
	if result.State == "failed" {
		return AnalysisOutcome{State: "failed"}, nil
	}
	if result.State == "stale" {
		return AnalysisOutcome{State: "stale"}, nil
	}
	return AnalysisOutcome{State: "retrying", RetryAfter: retryDuration(s.now(), result.RetryAt)}, nil
}

func retryDuration(now, retryAt time.Time) time.Duration {
	delay := retryAt.Sub(now)
	if delay < time.Second {
		return time.Second
	}
	return delay
}

func analysisRetryDelay(attempt int) (time.Duration, bool) {
	if attempt < 1 || attempt > len(analysisRetryMinutes) {
		return 0, false
	}
	return time.Duration(analysisRetryMinutes[attempt-1]) * time.Minute, true
}

func isHistorical(route ArticleRoute) bool {
	return route.Backfill && (route.PublishedAt == nil || route.DiscoveredAt.Sub(*route.PublishedAt) > 48*time.Hour)
}
