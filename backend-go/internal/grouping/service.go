package grouping

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/publication"
)

const (
	confirmBelowCosine = 0.85
	signalMinCosine    = 0.72
	signalAutoCosine   = 0.92
	groupStaleMillis   = 48 * time.Hour
)

var statusIDPattern = regexp.MustCompile(`/status/([0-9]+)`)

type WorkflowRepository interface {
	BeginGrouping(context.Context, string) (GroupingStart, bool, error)
	ArticleForGrouping(context.Context, string) (*ArticleInput, error)
	LatestAnalysis(context.Context, string) (*AnalysisInput, error)
	ManualDecision(context.Context, string) (ManualState, error)
	CurrentMembership(context.Context, string) (*CurrentMembership, error)
	RelatedPosts(context.Context, string, string, []string) (*PoolRow, []PoolRow, error)
	CandidateViews(context.Context, []Recalled, map[string]interests.Entity, time.Time) ([]Candidate, error)
	CommitGrouping(context.Context, CommitInput) (CommitResult, error)
	FailGrouping(context.Context, string, int, *int64, error, bool) error
}

type ArticlePublisher interface {
	Publish(context.Context, string, publication.PublishOptions) (*publication.Result, error)
}

type WorkflowConfig struct {
	Entities map[string]interests.Entity
	Now      func() time.Time
}

type Service struct {
	repository WorkflowRepository
	recall     *RecallService
	judge      *JudgeService
	embeddings EmbeddingStore
	publisher  ArticlePublisher
	config     WorkflowConfig
}

type Result struct {
	Verdict string
	FactID  *int64
	StoryID *int64
}

func NewService(repository WorkflowRepository, recall *RecallService, judge *JudgeService, embeddings EmbeddingStore, publisher ArticlePublisher, cfg WorkflowConfig) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{repository: repository, recall: recall, judge: judge, embeddings: embeddings, publisher: publisher, config: cfg}
}

// Process runs a single durable grouping decision. Model calls happen before the write transaction;
// CommitGrouping re-locks the article, checks its revision and honors any manual decision made while
// a provider was answering.
func (s *Service) Process(ctx context.Context, articleID string, signalOnly bool) (Result, error) {
	if s == nil || s.repository == nil {
		return Result{}, errors.New("grouping service is unavailable")
	}
	start, found, err := s.repository.BeginGrouping(ctx, articleID)
	if err != nil || !found {
		if err != nil {
			return Result{}, err
		}
		return Result{Verdict: "skipped"}, nil
	}
	if !start.WasComplete {
		if err := s.publish(ctx, articleID); err != nil {
			return Result{}, s.fail(ctx, articleID, start.Revision, err)
		}
	}
	result, err := s.decide(ctx, articleID, signalOnly, start)
	if err != nil {
		return Result{}, s.fail(ctx, articleID, start.Revision, err)
	}
	if err := s.publish(ctx, articleID); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) decide(ctx context.Context, articleID string, signalOnly bool, start GroupingStart) (Result, error) {
	article, err := s.repository.ArticleForGrouping(ctx, articleID)
	if err != nil {
		return Result{}, err
	}
	if article == nil {
		return Result{Verdict: "skipped"}, nil
	}
	if article.Revision != start.Revision {
		return Result{}, ErrSuperseded
	}
	observedAt := article.DiscoveredAt
	if article.PublishedAt != nil {
		observedAt = *article.PublishedAt
	}
	manual, err := s.repository.ManualDecision(ctx, articleID)
	if err != nil {
		return Result{}, err
	}
	if manual.Found {
		committed, err := s.repository.CommitGrouping(ctx, CommitInput{Article: *article, Verdict: "manual", Selection: &SelectionValue{AddsValue: true, Reason: "人工确认的归属"}, SkipDecision: true, ObservedAt: observedAt})
		return resultFromCommit("manual", committed), err
	}
	analysis, err := s.repository.LatestAnalysis(ctx, articleID)
	if err != nil {
		return Result{}, err
	}
	if analysis != nil && analysis.InputRevision != article.Revision && article.ParticipationMode == "editorial" {
		return Result{}, ErrSuperseded
	}
	composite := analysis != nil && analysis.Composite
	unsupported := analysis != nil && valueString(analysis.Output["scope"]) == "unknown" && analysis.Output["fact"] == nil
	historical := article.Backfill && (article.PublishedAt == nil || article.DiscoveredAt.Sub(*article.PublishedAt) > groupStaleMillis)
	needsReset := composite || unsupported || historical
	if historical {
		committed, err := s.repository.CommitGrouping(ctx, CommitInput{Article: *article, Verdict: "historical", Selection: &SelectionValue{AddsValue: true, Reason: "历史资料按原文时间归档"}, RemoveAutomatic: true, SkipDecision: true, ObservedAt: observedAt})
		return resultFromCommit("historical", committed), err
	}
	if signalOnly || article.ParticipationMode != "editorial" {
		return s.groupSignal(ctx, *article, observedAt)
	}
	if unsupported {
		committed, err := s.repository.CommitGrouping(ctx, CommitInput{Article: *article, Verdict: "standalone", Selection: &SelectionValue{AddsValue: false, Reason: "材料不足以确认当前消息"}, RemoveAutomatic: true, SkipDecision: true, ObservedAt: observedAt})
		return resultFromCommit("standalone", committed), err
	}
	kept, err := s.repository.CurrentMembership(ctx, articleID)
	if err != nil {
		return Result{}, err
	}
	recheckSelection := !start.WasComplete && start.SelectionAddsValue == nil
	if kept != nil && !recheckSelection {
		committed, err := s.repository.CommitGrouping(ctx, CommitInput{Article: *article, Verdict: "kept", FactID: &kept.FactID, StoryID: &kept.StoryID, SkipDecision: true, ObservedAt: observedAt})
		return resultFromCommit("kept", committed), err
	}
	if analysis == nil || analysis.Relevance != "pass" {
		committed, err := s.repository.CommitGrouping(ctx, CommitInput{Article: *article, Verdict: "standalone", Selection: &SelectionValue{AddsValue: false, Reason: "未通过内容分析"}, RemoveAutomatic: needsReset, SkipDecision: true, ObservedAt: observedAt})
		return resultFromCommit("standalone", committed), err
	}

	query := reportFor(*article, analysis, observedAt, composite)
	queryText := ReportText(query.Title, query.Summary)
	frame, frameTitle, evidence, conditions := factFrame(analysis.Output, query.Title)
	newFactTitle := truncateUTF16(frameTitle, 60)
	xIDs := referencedStatusIDs(article.XPost)
	sameURL, referenced, err := s.repository.RelatedPosts(ctx, article.ID, article.URL, xIDs)
	if err != nil {
		return Result{}, err
	}
	selectedSameURL := false
	if sameURL != nil && !composite && kept == nil {
		views, viewErr := s.repository.CandidateViews(ctx, []Recalled{{FactID: sameURL.FactID, StoryID: sameURL.StoryID, FactTitle: sameURL.FactTitle, Score: 1}}, s.config.Entities, s.config.Now())
		if viewErr != nil {
			return Result{}, viewErr
		}
		selectedSameURL = len(views) > 0 && views[0].Selected
	}
	var candidates []Candidate
	var reading []ReadingContext
	if !selectedSameURL {
		if s.recall == nil {
			return Result{}, ErrRecallUnavailable
		}
		boost := append([]PoolRow(nil), referenced...)
		if sameURL != nil {
			boost = append(boost, *sameURL)
		}
		recalled, recallErr := s.recall.RecallFacts(ctx, article.ID, queryText, RecallMinCosine, RecallTopFacts, boost)
		if recallErr != nil {
			return Result{}, recallErr
		}
		candidates, err = s.repository.CandidateViews(ctx, recalled, s.config.Entities, s.config.Now())
		if err != nil {
			return Result{}, err
		}
		reading, err = s.recall.RecallSelectedBackground(ctx, article.ID, queryText, RecallMinCosine, 3)
		if err != nil {
			return Result{}, err
		}
	}
	selection := SelectionValue{AddsValue: true, Reason: "没有已公开精选的相关报道"}
	verdicts := map[int64]Verdict{}
	receiptIDs := make([]int64, 0, 2)
	if len(candidates) > 0 || len(reading) > 0 {
		judged, judgeErr := s.judge.JudgeBatch(ctx, article.ID, query, candidates, reading)
		if judgeErr != nil {
			return Result{}, judgeErr
		}
		verdicts, selection = judged.Verdicts, judged.Selection
		receiptIDs = append(receiptIDs, judged.ReceiptID)
	}
	if composite {
		mentions := make([]int64, 0)
		for _, candidate := range candidates {
			verdict := verdicts[candidate.FactID]
			if verdict.Relation != RelationUnrelated && verdict.Confidence >= TieMinConfidence {
				mentions = append(mentions, candidate.FactID)
			}
		}
		commit := baseCommit(*article, observedAt, "roundup", selection, candidates, verdicts, receiptIDs)
		commit.RemoveAutomatic, commit.MentionFacts, commit.Conditions = needsReset, mentions, conditions
		committed, err := s.repository.CommitGrouping(ctx, commit)
		if err == nil {
			err = s.completeReceipts(ctx, receiptIDs)
		}
		return resultFromCommit("roundup", committed), err
	}
	if kept != nil {
		if containsSelectedFact(candidates, kept.FactID) {
			selection = SelectionValue{AddsValue: true, Reason: "同一新闻的代表报道候选"}
		}
		commit := baseCommit(*article, observedAt, "kept", selection, candidates, verdicts, receiptIDs)
		commit.FactID, commit.StoryID = &kept.FactID, &kept.StoryID
		committed, err := s.repository.CommitGrouping(ctx, commit)
		if err == nil {
			err = s.completeReceipts(ctx, receiptIDs)
		}
		return resultFromCommit("kept", committed), err
	}

	verdict, factID, storyID := "new-story", (*int64)(nil), (*int64)(nil)
	if sameURL != nil {
		verdict, factID, storyID = "same-url", &sameURL.FactID, &sameURL.StoryID
	} else {
		for _, candidate := range SameOccurrence(candidates, verdicts) {
			if candidate.Score >= confirmBelowCosine {
				id := candidate.FactID
				factID = &id
				break
			}
			review, reviewErr := s.judge.ConfirmMerge(ctx, article.ID, query, candidate)
			if reviewErr != nil {
				return Result{}, reviewErr
			}
			receiptIDs = append(receiptIDs, review.ReceiptID)
			if review.Relation == RelationSameOccurrence {
				id := candidate.FactID
				factID = &id
				break
			}
			if review.Relation == RelationSameStory && candidate.StoryRoot {
				id := candidate.StoryID
				storyID = &id
				break
			}
		}
		if factID != nil {
			verdict = "same-fact"
			for _, candidate := range candidates {
				if candidate.FactID == *factID {
					id := candidate.StoryID
					storyID = &id
					break
				}
			}
		} else if storyID != nil {
			verdict = "new-fact-in-story"
		} else if development := StoryForDevelopment(candidates, verdicts); development != nil {
			verdict = "new-fact-in-story"
			id := development.StoryID
			storyID = &id
		}
	}
	if factID != nil && (selectedSameURL || containsSelectedFact(candidates, *factID)) {
		selection = SelectionValue{AddsValue: true, Reason: "同一新闻的代表报道候选"}
	}
	commit := baseCommit(*article, observedAt, verdict, selection, candidates, verdicts, receiptIDs)
	if sameURL != nil {
		commit.Candidates = append(commit.Candidates, DecisionCandidate{ID: sameURL.FactID, Score: 1, Relation: RelationSameOccurrence, Confidence: floatPointer(1)})
	}
	commit.FactID, commit.StoryID = factID, storyID
	commit.CreateStory, commit.CreateFact = storyID == nil, factID == nil
	commit.AddMembership, commit.FactTitle, commit.Frame, commit.Evidence, commit.Conditions = true, newFactTitle, frame, evidence, conditions
	committed, err := s.repository.CommitGrouping(ctx, commit)
	if err == nil {
		err = s.completeReceipts(ctx, receiptIDs)
	}
	return resultFromCommit(verdict, committed), err
}

func (s *Service) groupSignal(ctx context.Context, article ArticleInput, observedAt time.Time) (Result, error) {
	if s.recall == nil {
		return Result{}, ErrRecallUnavailable
	}
	_, referenced, err := s.repository.RelatedPosts(ctx, article.ID, article.URL, referencedStatusIDs(article.XPost))
	if err != nil {
		return Result{}, err
	}
	if len(referenced) > 0 {
		row := referenced[0]
		candidate := DecisionCandidate{ID: row.FactID, Score: 1, Relation: RelationSameStory, Confidence: floatPointer(1)}
		commit := CommitInput{Article: article, Verdict: "signal-native", FactID: &row.FactID, StoryID: &row.StoryID,
			Candidates: []DecisionCandidate{candidate}, Signal: true, ObservedAt: observedAt}
		written, err := s.repository.CommitGrouping(ctx, commit)
		return resultFromCommit("signal-native", written), err
	}
	if s.embeddings == nil || !s.embeddings.Available(ctx) {
		written, err := s.repository.CommitGrouping(ctx, CommitInput{Article: article, Verdict: "signal-unmatched", SkipDecision: true, ObservedAt: observedAt})
		return resultFromCommit("signal-unmatched", written), err
	}
	queryText := ReportText(article.Title, article.BodyText)
	recalled, err := s.recall.RecallFacts(ctx, article.ID, queryText, signalMinCosine, 4, nil)
	if err != nil {
		return Result{}, err
	}
	if len(recalled) == 0 {
		written, err := s.repository.CommitGrouping(ctx, CommitInput{Article: article, Verdict: "signal-unmatched", ObservedAt: observedAt})
		return resultFromCommit("signal-unmatched", written), err
	}
	candidates, err := s.repository.CandidateViews(ctx, recalled, s.config.Entities, s.config.Now())
	if err != nil {
		return Result{}, err
	}
	if len(candidates) == 0 {
		written, err := s.repository.CommitGrouping(ctx, CommitInput{Article: article, Verdict: "signal-unmatched", SkipDecision: true, ObservedAt: observedAt})
		return resultFromCommit("signal-unmatched", written), err
	}
	verdicts := map[int64]Verdict{}
	receipts := []int64{}
	var target *Candidate
	if recalled[0].Score >= signalAutoCosine {
		for i := range candidates {
			if candidates[i].FactID == recalled[0].FactID {
				target = &candidates[i]
				break
			}
		}
	} else {
		var summary *string
		if article.BodyText != nil {
			value := truncateUTF16(*article.BodyText, 300)
			summary = &value
		}
		query := Report{Title: article.Title, Source: article.SourceName, At: &observedAt, Summary: summary}
		judged, judgeErr := s.judge.JudgeSignal(ctx, article.ID, query, candidates)
		if judgeErr != nil {
			return Result{}, judgeErr
		}
		verdicts, target = judged.Verdicts, SignalTarget(candidates, judged.Verdicts)
		receipts = append(receipts, judged.ReceiptID)
	}
	verdict := "signal-unmatched"
	commit := CommitInput{Article: article, Verdict: verdict, ObservedAt: observedAt}
	if target != nil {
		verdict = "signal"
		commit.Verdict, commit.FactID, commit.StoryID, commit.Signal = verdict, &target.FactID, &target.StoryID, true
	}
	commit.Candidates = decisionCandidates(candidates, verdicts)
	if len(receipts) > 0 {
		commit.ReceiptID = intPointer64(receipts[0])
	}
	written, err := s.repository.CommitGrouping(ctx, commit)
	if err == nil {
		err = s.completeReceipts(ctx, receipts)
	}
	return resultFromCommit(verdict, written), err
}

func (s *Service) fail(ctx context.Context, articleID string, revision int, cause error) error {
	var receiptID *int64
	var output *JudgementOutputError
	if errors.As(cause, &output) {
		receiptID = intPointer64(output.ReceiptID)
	}
	var callOutput *modelcall.OutputError
	if errors.As(cause, &callOutput) {
		receiptID = intPointer64(callOutput.ReceiptID)
	}
	var embeddingOutput *EmbeddingOutputError
	if errors.As(cause, &embeddingOutput) {
		receiptID = intPointer64(embeddingOutput.ReceiptID)
	}
	cleanup := context.WithoutCancel(ctx)
	_ = s.repository.FailGrouping(cleanup, articleID, revision, receiptID, cause, ctx.Err() != nil)
	_ = s.publish(cleanup, articleID)
	return cause
}

func (s *Service) publish(ctx context.Context, articleID string) error {
	if s.publisher == nil {
		return nil
	}
	_, err := s.publisher.Publish(ctx, articleID, publication.PublishOptions{})
	return err
}

func (s *Service) completeReceipts(ctx context.Context, ids []int64) error {
	if s.judge == nil {
		return nil
	}
	for _, id := range ids {
		if err := s.judge.CompleteReceipt(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func reportFor(article ArticleInput, analysis *AnalysisInput, at time.Time, composite bool) Report {
	title := article.Title
	var summary *string
	if analysis != nil {
		if analysis.Title != nil && strings.TrimSpace(*analysis.Title) != "" {
			title = *analysis.Title
		}
		summary = analysis.Summary
	}
	scope := "unknown"
	if composite {
		scope = "composite"
	} else if analysis != nil && valueString(analysis.Output["scope"]) == "single" {
		scope = "single"
	}
	var frame *Frame
	if analysis != nil {
		fact, _ := analysis.Output["fact"].(map[string]any)
		frame = toFrame(fact)
	}
	return Report{Title: title, Source: article.SourceName, FirstParty: article.FirstParty, At: &at, Summary: summary, SourceText: article.BodyText, Scope: scope, Frame: frame}
}

func factFrame(output map[string]any, fallbackTitle string) (*Frame, string, *string, *string) {
	fact, _ := output["fact"].(map[string]any)
	if fact == nil {
		return nil, fallbackTitle, nil, nil
	}
	title := valueString(fact["title"])
	if title == "" {
		title = fallbackTitle
	}
	evidenceParts := []string{}
	if evidence := valueString(fact["evidence"]); evidence != "" {
		evidenceParts = append(evidenceParts, evidence)
	}
	conditions := []string{}
	if rows, ok := fact["conditions"].([]any); ok {
		for _, item := range rows {
			if row, ok := item.(map[string]any); ok {
				if text := valueString(row["text"]); text != "" {
					conditions = append(conditions, text)
				}
				if quote := valueString(row["quote"]); quote != "" {
					evidenceParts = append(evidenceParts, quote)
				}
			}
		}
	}
	var evidence *string
	if len(evidenceParts) > 0 {
		value := strings.Join(evidenceParts, "\n")
		evidence = &value
	}
	var conditionText *string
	if len(conditions) > 0 {
		value := strings.Join(conditions, "；")
		conditionText = &value
	}
	return toFrame(fact), title, evidence, conditionText
}

func toFrame(fact map[string]any) *Frame {
	if fact == nil {
		return nil
	}
	return &Frame{Subject: optionalString(fact["subject"]), Action: optionalString(fact["action"]), Object: optionalString(fact["object"]), OccurredAt: optionalString(fact["occurredAt"])}
}

func baseCommit(article ArticleInput, at time.Time, verdict string, selection SelectionValue, candidates []Candidate, verdicts map[int64]Verdict, receiptIDs []int64) CommitInput {
	commit := CommitInput{Article: article, Verdict: verdict, Selection: &selection, Candidates: decisionCandidates(candidates, verdicts), ObservedAt: at,
		FirstParty: article.FirstParty, SignalGroupID: article.SignalGroupID}
	if len(receiptIDs) > 0 {
		commit.ReceiptID = intPointer64(receiptIDs[0])
	}
	return commit
}

func decisionCandidates(candidates []Candidate, verdicts map[int64]Verdict) []DecisionCandidate {
	result := make([]DecisionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		verdict := verdicts[candidate.FactID]
		var confidence *float64
		if _, ok := verdicts[candidate.FactID]; ok {
			value := verdict.Confidence
			confidence = &value
		}
		result = append(result, DecisionCandidate{ID: candidate.FactID, Score: math.Round(candidate.Score*1000) / 1000, Relation: verdict.Relation, Confidence: confidence})
	}
	return result
}

func containsSelectedFact(candidates []Candidate, factID int64) bool {
	for _, candidate := range candidates {
		if candidate.FactID == factID && candidate.Selected {
			return true
		}
	}
	return false
}

func referencedStatusIDs(post XPost) []string {
	ids := make([]string, 0, 2)
	if post.ReplyTo != nil && *post.ReplyTo != "" {
		ids = append(ids, *post.ReplyTo)
	}
	if post.Quoted != nil {
		if match := statusIDPattern.FindStringSubmatch(post.Quoted.URL); len(match) == 2 {
			ids = append(ids, match[1])
		}
	}
	return ids
}

func resultFromCommit(verdict string, commit CommitResult) Result {
	if commit.Manual.Found {
		return Result{Verdict: "manual", FactID: commit.Manual.FactID}
	}
	return Result{Verdict: verdict, FactID: commit.FactID, StoryID: commit.StoryID}
}

func optionalString(value any) *string {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return nil
	}
	return &text
}

func valueString(value any) string {
	text, _ := value.(string)
	return text
}

func intPointer64(value int64) *int64 { return &value }
