package grouping

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/tagging"
	"aihot/backend-go/internal/topics"
)

type workflowStubRepository struct {
	start            GroupingStart
	article          *ArticleInput
	analysis         *AnalysisInput
	signals          []SignalRevisitRow
	waitingTweet     string
	waitingSince     time.Time
	rematchSince     time.Time
	sameURL          *PoolRow
	referenced       []PoolRow
	relatedIDs       []string
	candidates       []Candidate
	roots            []StoryRoot
	redirects        map[int64]int64
	emptyStories     []int64
	manual           ManualState
	membership       *CurrentMembership
	commits          []CommitInput
	failures         int
	commitManual     bool
	commitResult     CommitResult
	failureRevision  int
	failureReceiptID *int64
	failureCause     error
}

func (r *workflowStubRepository) BeginGrouping(context.Context, string) (GroupingStart, bool, error) {
	return r.start, r.article != nil, nil
}
func (r *workflowStubRepository) ArticleForGrouping(context.Context, string) (*ArticleInput, error) {
	return r.article, nil
}
func (r *workflowStubRepository) LatestAnalysis(context.Context, string) (*AnalysisInput, error) {
	return r.analysis, nil
}
func (r *workflowStubRepository) ManualDecision(context.Context, string) (ManualState, error) {
	return r.manual, nil
}
func (r *workflowStubRepository) CurrentMembership(context.Context, string) (*CurrentMembership, error) {
	return r.membership, nil
}
func (r *workflowStubRepository) RelatedPosts(_ context.Context, _, _ string, xIDs []string) (*PoolRow, []PoolRow, error) {
	r.relatedIDs = append([]string(nil), xIDs...)
	return r.sameURL, append([]PoolRow(nil), r.referenced...), nil
}
func (r *workflowStubRepository) CandidateViews(context.Context, []Recalled, map[string]interests.Entity, time.Time) ([]Candidate, error) {
	return append([]Candidate{}, r.candidates...), nil
}

/** WaitingSignals returns the eligible fake replies and records the query window. */
func (r *workflowStubRepository) WaitingSignals(_ context.Context, tweetID string, since time.Time) ([]SignalRevisitRow, error) {
	r.waitingTweet, r.waitingSince = tweetID, since
	return append([]SignalRevisitRow(nil), r.signals...), nil
}

/** UnattachedSignals returns the fake semantic candidates and records the query window. */
func (r *workflowStubRepository) UnattachedSignals(_ context.Context, since time.Time) ([]SignalRevisitRow, error) {
	r.rematchSince = since
	return append([]SignalRevisitRow(nil), r.signals...), nil
}

/** StoryRoots returns the configured fake roots to the grouping tests. */
func (r *workflowStubRepository) StoryRoots(context.Context, []int64) ([]StoryRoot, error) {
	return append([]StoryRoot(nil), r.roots...), nil
}

/** LiveStoryID applies the redirect fixture used by story-consolidation tests. */
func (r *workflowStubRepository) LiveStoryID(_ context.Context, id int64) (int64, bool, error) {
	if target, ok := r.redirects[id]; ok {
		return target, true, nil
	}
	return id, true, nil
}

/** EmptyStories returns the configured stale story IDs to the redirect tests. */
func (r *workflowStubRepository) EmptyStories(context.Context, string, int64) ([]int64, error) {
	return append([]int64(nil), r.emptyStories...), nil
}
func (r *workflowStubRepository) CommitGrouping(_ context.Context, input CommitInput) (CommitResult, error) {
	r.commits = append(r.commits, input)
	if r.commitManual {
		return CommitResult{Manual: ManualState{Found: true}}, nil
	}
	return r.commitResult, nil
}

type signalTaskEnqueuerStub struct {
	articleIDs []string
}

/** EnqueueSignalGroups records signal IDs selected for the River queue. */
func (e *signalTaskEnqueuerStub) EnqueueSignalGroups(_ context.Context, articleIDs []string) error {
	e.articleIDs = append(e.articleIDs, articleIDs...)
	return nil
}

/** FailGrouping captures the receipt and error associated with an unresolved decision. */
func (r *workflowStubRepository) FailGrouping(_ context.Context, _ string, revision int, receiptID *int64, cause error, _ bool) error {
	r.failures++
	r.failureRevision = revision
	r.failureReceiptID = receiptID
	r.failureCause = cause
	return nil
}

type storyMergeStub struct {
	from, into int64
	reason     string
	actor      string
	merged     bool
	calls      int
}

/** MergeStories records the approved merge request for assertions. */
func (m *storyMergeStub) MergeStories(_ context.Context, from, into int64, reason, actor string) (map[string]int, bool, error) {
	m.calls++
	m.from, m.into, m.reason, m.actor = from, into, reason, actor
	return nil, m.merged, nil
}

func TestProcessManualDecisionWinsBeforeModelWork(t *testing.T) {
	repository := &workflowStubRepository{
		start:   GroupingStart{Revision: 3},
		article: &ArticleInput{ID: "article-1", Revision: 3, DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
		manual:  ManualState{Found: true},
	}
	service := NewService(repository, nil, nil, nil, nil, WorkflowConfig{})
	result, err := service.Process(context.Background(), "article-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "manual" || len(repository.commits) != 1 {
		t.Fatalf("result=%+v commits=%+v", result, repository.commits)
	}
	commit := repository.commits[0]
	if commit.Verdict != "manual" || commit.Selection == nil || commit.Selection.Reason != "人工确认的归属" || !commit.SkipDecision {
		t.Fatalf("manual commit=%+v", commit)
	}
}

func TestProcessUnsupportedMaterialRemovesAutomaticMembership(t *testing.T) {
	repository := &workflowStubRepository{
		start:    GroupingStart{Revision: 4},
		article:  &ArticleInput{ID: "article-2", Revision: 4, DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
		analysis: &AnalysisInput{InputRevision: 4, Relevance: "pass", Output: map[string]any{"scope": "unknown"}},
	}
	service := NewService(repository, nil, nil, nil, nil, WorkflowConfig{})
	result, err := service.Process(context.Background(), "article-2", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "standalone" || len(repository.commits) != 1 {
		t.Fatalf("result=%+v commits=%+v", result, repository.commits)
	}
	commit := repository.commits[0]
	if !commit.RemoveAutomatic || !commit.SkipDecision || commit.Selection == nil || commit.Selection.AddsValue {
		t.Fatalf("unsupported commit=%+v", commit)
	}
}

/** TestProcessRetriesAfterIncompleteBatchAnswer keeps an unusable judgement unresolved. */
func TestProcessRetriesAfterIncompleteBatchAnswer(t *testing.T) {
	model := &judgeModelStub{outputs: []string{
		`{"decisions":[],"selection":{"addsValue":true,"reason":"fixture"}}`,
		`{"decisions":[{"id":"C1","relation":"SAME_OCCURRENCE","confidence":1}],"selection":{"addsValue":true,"reason":"fixture"}}`,
	}}
	repository := &workflowStubRepository{
		start:   GroupingStart{Revision: 1},
		article: &ArticleInput{ID: "retry-article", Revision: 1, Title: "A report", DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
		analysis: &AnalysisInput{InputRevision: 1, Relevance: "pass", Output: map[string]any{
			"scope": "single",
			"fact":  map[string]any{"title": "A fact", "subject": "Company", "action": "released", "object": "product"},
		}},
		candidates: []Candidate{{FactID: 7, StoryID: 11, FactTitle: "A fact", Score: 0.9}},
	}
	recall := NewRecallService(&recallRepositoryStub{}, nil, nil)
	service := NewService(repository, recall, newJudgeServiceForTest(t, model), nil, nil, WorkflowConfig{})

	_, err := service.Process(context.Background(), repository.article.ID, false)
	var outputError *JudgementOutputError
	if !errors.As(err, &outputError) || outputError.ReceiptID != 71 {
		t.Fatalf("first grouping error=%v, want unusable response with receipt 71", err)
	}
	if repository.failures != 1 || repository.failureRevision != 1 || repository.failureReceiptID == nil || *repository.failureReceiptID != 71 || len(repository.commits) != 0 {
		t.Fatalf("failed grouping state lost its receipt or wrote a decision: repository=%+v", repository)
	}

	result, err := service.Process(context.Background(), repository.article.ID, false)
	if err != nil || result.Verdict != "same-fact" || len(repository.commits) != 1 {
		t.Fatalf("retry result=%+v commits=%+v err=%v", result, repository.commits, err)
	}
	if len(model.rejected) != 1 || model.rejected[0] != 71 || len(model.complete) != 1 || model.complete[0] != 71 {
		t.Fatalf("retry receipt transitions rejected=%v completed=%v", model.rejected, model.complete)
	}
}

/** TestProcessSameURLPreservesSelectionValueDecision verifies representative reuse does not bypass selection rules. */
func TestProcessSameURLPreservesSelectionValueDecision(t *testing.T) {
	tests := []struct {
		name             string
		wantSameURL      bool
		existingSelected bool
		modelAddsValue   bool
		wantSelected     bool
		wantModelCall    bool
		wantVerdict      string
	}{
		{name: "selected same-URL representative needs no model call", wantSameURL: true, existingSelected: true, wantSelected: true, wantVerdict: "same-url"},
		{name: "unselected same-URL report can be selected for new value", wantSameURL: true, modelAddsValue: true, wantSelected: true, wantModelCall: true, wantVerdict: "same-url"},
		{name: "unselected same-URL report cannot be revived without new value", wantSameURL: true, modelAddsValue: false, wantSelected: false, wantModelCall: true, wantVerdict: "same-url"},
		{name: "same-fact representative stays eligible without new value", existingSelected: true, modelAddsValue: false, wantSelected: true, wantModelCall: true, wantVerdict: "same-fact"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := &judgeModelStub{}
			if test.wantModelCall {
				value := "false"
				if test.modelAddsValue {
					value = "true"
				}
				model.outputs = []string{`{"query":"same URL report","decisions":[{"id":"C1","relation":"SAME_OCCURRENCE","confidence":1},{"id":"C2","relation":"UNRELATED","confidence":1}],"selection":{"addsValue":` + value + `,"reason":"value decision fixture"}}`}
			}
			repository := &workflowStubRepository{
				start:   GroupingStart{Revision: 1},
				article: &ArticleInput{ID: "same-url-article", Revision: 1, Title: "Same URL report", URL: "https://source.example.invalid/report", DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
				analysis: &AnalysisInput{InputRevision: 1, Relevance: "pass", Output: map[string]any{
					"scope": "single",
					"fact":  map[string]any{"title": "Existing fact", "subject": "Acme", "action": "announced", "object": "a product"},
				}},
				candidates: []Candidate{
					{FactID: 17, StoryID: 23, FactTitle: "Existing fact", Score: 1, Selected: test.existingSelected},
					{FactID: 29, StoryID: 31, FactTitle: "Previously selected coverage", Score: 0.8, Selected: true},
				},
			}
			if test.wantSameURL {
				repository.sameURL = &PoolRow{ArticleID: "existing-report", FactID: 17, StoryID: 23, FactTitle: "Existing fact"}
			}
			recall := NewRecallService(&recallRepositoryStub{}, nil, nil)
			service := NewService(repository, recall, newJudgeServiceForTest(t, model), nil, nil, WorkflowConfig{})

			result, err := service.Process(context.Background(), repository.article.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			if result.Verdict != test.wantVerdict || len(repository.commits) != 1 {
				t.Fatalf("result=%+v commits=%+v", result, repository.commits)
			}
			commit := repository.commits[0]
			if commit.Selection == nil || commit.Selection.AddsValue != test.wantSelected {
				t.Fatalf("same-URL selection=%+v, want addsValue=%t", commit.Selection, test.wantSelected)
			}
			if (len(model.requests) > 0) != test.wantModelCall {
				t.Fatalf("model calls=%d, want call=%t", len(model.requests), test.wantModelCall)
			}
		})
	}
}

/** TestCompositeWithoutCandidatesStaysOutsideTheStoryGraph verifies empty composite handling. */
func TestCompositeWithoutCandidatesStaysOutsideTheStoryGraph(t *testing.T) {
	repository := &workflowStubRepository{
		start:   GroupingStart{Revision: 1},
		article: &ArticleInput{ID: "composite-1", Revision: 1, Title: "Two unrelated updates", DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
		analysis: &AnalysisInput{InputRevision: 1, Relevance: "pass", Composite: true, Output: map[string]any{
			"scope": "composite",
			"fact":  nil,
		}},
	}
	recall := NewRecallService(&recallRepositoryStub{}, nil, nil)
	service := NewService(repository, recall, nil, nil, nil, WorkflowConfig{})

	result, err := service.Process(context.Background(), repository.article.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "roundup" || len(repository.commits) != 1 {
		t.Fatalf("grouping result=%+v commits=%+v", result, repository.commits)
	}
	commit := repository.commits[0]
	if commit.Verdict != "roundup" || !commit.RemoveAutomatic || len(commit.MentionFacts) != 0 {
		t.Fatalf("empty composite was not left outside the story graph: %+v", commit)
	}
	if commit.FactID != nil || commit.StoryID != nil || commit.CreateFact || commit.CreateStory || commit.AddMembership || commit.Signal {
		t.Fatalf("empty composite created event identity: %+v", commit)
	}
}

/** TestCompositeMentionsRelatedFactsWithoutCreatingIdentity verifies composite mention semantics. */
func TestCompositeMentionsRelatedFactsWithoutCreatingIdentity(t *testing.T) {
	model := &judgeModelStub{outputs: []string{`{"query":"combined updates","decisions":[{"id":"C1","relation":"SAME_OCCURRENCE","confidence":0.95},{"id":"C2","relation":"SAME_OCCURRENCE","confidence":0.99}],"selection":{"addsValue":true,"reason":"independent update"}}`}}
	repository := &workflowStubRepository{
		start:   GroupingStart{Revision: 1},
		article: &ArticleInput{ID: "composite-2", Revision: 1, Title: "Two product updates", DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
		analysis: &AnalysisInput{InputRevision: 1, Relevance: "pass", Composite: true, Output: map[string]any{
			"scope": "composite",
			"fact": map[string]any{"title": "Two product updates", "conditions": []any{
				map[string]any{"text": "仅预览用户", "quote": "Preview users only."},
			}},
		}},
		candidates: []Candidate{{FactID: 11, StoryID: 21, FactTitle: "first product"}, {FactID: 12, StoryID: 22, FactTitle: "second product"}},
	}
	service := NewService(repository, NewRecallService(&recallRepositoryStub{}, nil, nil), newJudgeServiceForTest(t, model), nil, nil, WorkflowConfig{})

	result, err := service.Process(context.Background(), repository.article.ID, false)
	if err != nil || result.Verdict != "roundup" || len(repository.commits) != 1 {
		t.Fatalf("composite grouping = %+v commits=%+v err=%v", result, repository.commits, err)
	}
	commit := repository.commits[0]
	if commit.FactID != nil || commit.StoryID != nil || commit.CreateFact || commit.CreateStory || commit.AddMembership || !commit.RemoveAutomatic {
		t.Fatalf("composite created or retained automatic identity: %+v", commit)
	}
	if len(commit.MentionFacts) != 2 || commit.MentionFacts[0] != 11 || commit.MentionFacts[1] != 12 || len(commit.Candidates) != 2 || commit.Candidates[1].Relation != RelationSameOccurrence {
		t.Fatalf("composite mentions or candidate decisions = mentions:%v candidates:%+v", commit.MentionFacts, commit.Candidates)
	}
	if commit.Conditions == nil || *commit.Conditions != "仅预览用户" || len(model.requests) != 1 || len(model.complete) != 1 || model.complete[0] != 71 {
		t.Fatalf("composite conditions or single batch receipt changed: conditions=%v requests=%d completed=%v", commit.Conditions, len(model.requests), model.complete)
	}
}

/** TestRoundupCandidateDoesNotReclassifySingleReport preserves its own fact and grounded conditions. */
func TestRoundupCandidateDoesNotReclassifySingleReport(t *testing.T) {
	model := &judgeModelStub{outputs: []string{`{"query":"a product launch","decisions":[{"id":"C1","relation":"ROUNDUP","confidence":0.99}],"selection":{"addsValue":true,"reason":"new report"}}`}}
	repository := &workflowStubRepository{
		start:   GroupingStart{Revision: 1},
		article: &ArticleInput{ID: "roundup-single", Revision: 1, Title: "Product launch", DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
		analysis: &AnalysisInput{InputRevision: 1, Relevance: "pass", Output: map[string]any{
			"scope": "single",
			"fact": map[string]any{"title": "Product launch", "subject": "Company", "action": "launched", "object": "product", "evidence": "A new product.", "conditions": []any{
				map[string]any{"text": "仅预览用户", "quote": "Preview users only."},
			}},
		}},
		candidates: []Candidate{{FactID: 13, StoryID: 23, FactTitle: "existing update", Score: 0.7}},
	}
	service := NewService(repository, NewRecallService(&recallRepositoryStub{}, nil, nil), newJudgeServiceForTest(t, model), nil, nil, WorkflowConfig{})

	result, err := service.Process(context.Background(), repository.article.ID, false)
	if err != nil || result.Verdict != "new-story" || len(repository.commits) != 1 {
		t.Fatalf("single-report grouping = %+v commits=%+v err=%v", result, repository.commits, err)
	}
	commit := repository.commits[0]
	if !commit.CreateStory || !commit.CreateFact || !commit.AddMembership || commit.FactID != nil || commit.StoryID != nil {
		t.Fatalf("roundup candidate changed single-report identity: %+v", commit)
	}
	if commit.Conditions == nil || *commit.Conditions != "仅预览用户" || commit.Evidence == nil || *commit.Evidence != "A new product.\nPreview users only." {
		t.Fatalf("grounded fact conditions were not retained: conditions=%v evidence=%v", commit.Conditions, commit.Evidence)
	}
}

/** TestProcessKeepsRecentBackfillsLiveAndArchivesOldBackfills verifies the historical grouping boundary. */
func TestProcessKeepsRecentBackfillsLiveAndArchivesOldBackfills(t *testing.T) {
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name           string
		publishedAt    *time.Time
		wantVerdict    string
		wantHistorical bool
	}{
		{
			name:           "old backfill",
			publishedAt:    timePointer(now.Add(-30 * 24 * time.Hour)),
			wantVerdict:    "historical",
			wantHistorical: true,
		},
		{
			name:        "recent backfill",
			publishedAt: timePointer(now.Add(-time.Hour)),
			wantVerdict: "new-story",
		},
		{
			name:           "unknown publication time",
			wantVerdict:    "historical",
			wantHistorical: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			commitResult := CommitResult{}
			if !test.wantHistorical {
				commitResult = CommitResult{
					FactID:  int64Pointer(11),
					StoryID: int64Pointer(21),
				}
			}
			repository := &workflowStubRepository{
				start: GroupingStart{Revision: 1},
				article: &ArticleInput{ID: "backfill-1", Revision: 1, Title: "Archive record", Backfill: true,
					PublishedAt: test.publishedAt, DiscoveredAt: now, ParticipationMode: "editorial"},
				analysis: &AnalysisInput{InputRevision: 1, Relevance: "pass", Output: map[string]any{
					"scope": "single", "fact": map[string]any{"title": "New product", "subject": "Company", "action": "released", "object": "product"},
				}},
				commitResult: commitResult,
			}
			recall := NewRecallService(&recallRepositoryStub{}, nil, func() time.Time { return now })
			service := NewService(repository, recall, nil, nil, nil, WorkflowConfig{Now: func() time.Time { return now }})
			result, err := service.Process(context.Background(), repository.article.ID, false)
			if err != nil || result.Verdict != test.wantVerdict || len(repository.commits) != 1 {
				t.Fatalf("grouping result=%+v commits=%+v err=%v", result, repository.commits, err)
			}
			commit := repository.commits[0]
			if commit.Verdict == "historical" != test.wantHistorical {
				t.Fatalf("historical state mismatch: %+v", commit)
			}
			if test.wantHistorical {
				if !commit.RemoveAutomatic || !commit.SkipDecision || commit.AddMembership {
					t.Fatalf("historical material was not archived without an event: %+v", commit)
				}
			} else if !commit.AddMembership || !commit.CreateStory || !commit.CreateFact {
				t.Fatalf("recent backfill did not found a live event: %+v", commit)
			}
		})
	}
}

/** TestGroupSignalUsesPostReferenceBeforeSemanticRecall preserves direct X reply and quote matching. */
func TestGroupSignalUsesPostReferenceBeforeSemanticRecall(t *testing.T) {
	now := time.Now().UTC()
	repository := &workflowStubRepository{
		referenced: []PoolRow{{
			FactID:    9,
			StoryID:   19,
			FactTitle: "referenced fact",
		}},
		commitResult: CommitResult{
			FactID:  int64Pointer(9),
			StoryID: int64Pointer(19),
		},
	}
	embeddings := &embeddingStoreStub{available: true}
	service := NewService(repository, NewRecallService(&recallRepositoryStub{}, embeddings, nil), nil, embeddings, nil, WorkflowConfig{})
	replyTo := "789012"
	quoteURL := "https://x.com/example/status/123456"
	result, err := service.groupSignal(context.Background(), ArticleInput{
		ID:       "signal-1",
		Revision: 1,
		Title:    "reaction",
		URL:      "https://x.com/example/status/654321",
		XPost: XPost{
			ReplyTo: &replyTo,
			Quoted: &struct {
				URL string `json:"url"`
			}{URL: quoteURL},
		},
		ParticipationMode: "hot_signal",
	}, now)
	if err != nil || result.Verdict != "signal-native" || result.StoryID == nil || *result.StoryID != 19 {
		t.Fatalf("direct reference did not attach to its story: result=%+v err=%v", result, err)
	}
	if len(embeddings.inputs) != 0 || len(repository.commits) != 1 || repository.commits[0].Verdict != "signal-native" || !repository.commits[0].Signal {
		t.Fatalf("direct reference performed semantic work or lost the signal commit: embeddings=%+v commits=%+v", embeddings.inputs, repository.commits)
	}
	if len(repository.relatedIDs) != 2 || repository.relatedIDs[0] != replyTo || repository.relatedIDs[1] != "123456" {
		t.Fatalf("reply/quote identities were not both supplied: %v", repository.relatedIDs)
	}
	if len(repository.commits[0].Candidates) != 1 || repository.commits[0].Candidates[0].ID != 9 || repository.commits[0].Candidates[0].Relation != RelationSameStory {
		t.Fatalf("direct reference audit candidate changed: %+v", repository.commits[0].Candidates)
	}
}

/** TestGroupSignalAutoAttachesNearIdenticalReportWithoutModel preserves the high-cosine fast path. */
func TestGroupSignalAutoAttachesNearIdenticalReportWithoutModel(t *testing.T) {
	now := time.Now().UTC()
	repository := &workflowStubRepository{
		candidates: []Candidate{{
			FactID:    10,
			StoryID:   20,
			FactTitle: "fact",
			Score:     1,
		}},
		commitResult: CommitResult{
			FactID:  int64Pointer(10),
			StoryID: int64Pointer(20),
		},
	}
	embeddings := &embeddingStoreStub{available: true, vectors: map[string][]float32{"signal-2": {1, 0}, "report-2": {1, 0}}}
	recallRepository := &recallRepositoryStub{
		pool: []PoolRow{{
			ArticleID: "report-2",
			FactID:    10,
			StoryID:   20,
			FactTitle: "fact",
		}},
		texts: map[string]string{"report-2": "matching report"},
	}
	service := NewService(repository, NewRecallService(recallRepository, embeddings, func() time.Time { return now }), nil,
		embeddings, nil, WorkflowConfig{Now: func() time.Time { return now }})
	result, err := service.groupSignal(context.Background(), ArticleInput{
		ID:                "signal-2",
		Revision:          1,
		Title:             "matching report",
		URL:               "https://example.invalid/signal-2",
		ParticipationMode: "hot_signal",
	}, now)
	if err != nil || result.Verdict != "signal" || result.StoryID == nil || *result.StoryID != 20 {
		t.Fatalf("near-identical signal did not attach: result=%+v err=%v", result, err)
	}
	if len(embeddings.inputs) != 2 || len(repository.commits) != 1 || repository.commits[0].Verdict != "signal" || !repository.commits[0].Signal {
		t.Fatalf("high-cosine signal called the model or lost its commit: inputs=%+v commits=%+v", embeddings.inputs, repository.commits)
	}
}

/** TestGroupSignalRecordsUnmatchedWhenNoFactExists keeps decided posts distinguishable from pending ones. */
func TestGroupSignalRecordsUnmatchedWhenNoFactExists(t *testing.T) {
	now := time.Now().UTC()
	repository := &workflowStubRepository{}
	embeddings := &embeddingStoreStub{available: true}
	service := NewService(repository, NewRecallService(&recallRepositoryStub{}, embeddings, nil), nil, embeddings, nil, WorkflowConfig{})
	result, err := service.groupSignal(context.Background(), ArticleInput{
		ID:                "signal-empty",
		Revision:          1,
		Title:             "a post with no matching report",
		URL:               "https://example.invalid/signal-empty",
		ParticipationMode: "hot_signal",
	}, now)
	if err != nil || result.Verdict != "signal-unmatched" || len(repository.commits) != 1 {
		t.Fatalf("unmatched signal result=%+v commits=%+v err=%v", result, repository.commits, err)
	}
	commit := repository.commits[0]
	if commit.Verdict != "signal-unmatched" || commit.Signal || commit.SkipDecision {
		t.Fatalf("unmatched signal was not recorded as a completed no-match decision: %+v", commit)
	}
}

func TestCommitRechecksManualDecisionThatArrivesDuringProcessing(t *testing.T) {
	repository := &workflowStubRepository{
		start:        GroupingStart{Revision: 8},
		article:      &ArticleInput{ID: "article-6", Revision: 8, DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
		analysis:     &AnalysisInput{InputRevision: 8, Relevance: "pass", Output: map[string]any{"scope": "unknown"}},
		commitManual: true,
	}
	service := NewService(repository, nil, nil, nil, nil, WorkflowConfig{})
	result, err := service.Process(context.Background(), "article-6", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "manual" || len(repository.commits) != 1 || repository.failures != 0 {
		t.Fatalf("result=%+v commits=%+v failures=%d", result, repository.commits, repository.failures)
	}
}

/** TestConsolidateStoryRootsRequiresBothModelsAndKeepsTheEarliestRoot verifies automatic merge gates. */
func TestConsolidateStoryRootsRequiresBothModelsAndKeepsTheEarliestRoot(t *testing.T) {
	anchorTime := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	otherTime := anchorTime.Add(time.Hour)
	anchor := StoryRoot{StoryID: 10, At: anchorTime, Report: Report{Title: "anchor", Source: "a"}}
	other := StoryRoot{StoryID: 20, At: otherTime, Report: Report{Title: "other", Source: "b"}}
	for _, test := range []struct {
		name          string
		review        string
		wantMerge     bool
		wantFinalID   int64
		wantMergeCall int
	}{
		{name: "review disagrees", review: `{"a":"other","b":"anchor","relation":"UNRELATED","difference":"different facts","confidence":0.98}`, wantFinalID: 20},
		{name: "review confirms", review: `{"a":"other","b":"anchor","relation":"SAME_STORY","difference":"same story","confidence":0.82}`, wantMerge: true, wantFinalID: 10, wantMergeCall: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundup := StoryRoot{StoryID: 5, At: anchorTime.Add(-time.Hour), Roundup: true, Report: Report{Title: "roundup"}}
			repository := &workflowStubRepository{roots: []StoryRoot{other, roundup, anchor}}
			if test.wantMerge {
				repository.redirects = map[int64]int64{other.StoryID: anchor.StoryID}
			}
			model := &judgeModelStub{outputs: []string{
				`{"a":"anchor","b":"other","relation":"SAME_OCCURRENCE","difference":"same event","confidence":0.9}`,
				test.review,
			}}
			merger := &storyMergeStub{merged: test.wantMerge}
			service := NewService(repository, nil, newJudgeServiceForTest(t, model), nil, nil, WorkflowConfig{})
			service.SetStoryMerger(merger)
			consolidations, finalStoryID, err := service.consolidateStoryRoots(context.Background(), other.StoryID, []int64{other.StoryID, anchor.StoryID})
			if err != nil || finalStoryID != test.wantFinalID || len(consolidations) != 1 {
				t.Fatalf("consolidations=%#v finalStoryID=%d err=%v", consolidations, finalStoryID, err)
			}
			if merger.calls != test.wantMergeCall {
				t.Fatalf("merge calls=%d, want %d", merger.calls, test.wantMergeCall)
			}
			if test.wantMerge {
				if !consolidations[0].Merge || merger.from != other.StoryID || merger.into != anchor.StoryID || merger.actor != "grouping" || merger.reason == "" {
					t.Fatalf("confirmed merge lost its direction or audit reason: result=%#v merger=%#v", consolidations[0], merger)
				}
			} else if consolidations[0].Merge {
				t.Fatalf("one-model agreement merged stories: %#v", consolidations[0])
			}
		})
	}
}

/** TestRedirectEmptyStoriesMergesFormerStoryAddressesIntoTheCurrentStory verifies redirect targets and audit reasons. */
func TestRedirectEmptyStoriesMergesFormerStoryAddressesIntoTheCurrentStory(t *testing.T) {
	repository := &workflowStubRepository{emptyStories: []int64{20}}
	merger := &storyMergeStub{merged: true}
	service := NewService(repository, nil, nil, nil, nil, WorkflowConfig{})
	service.SetStoryMerger(merger)
	redirected, err := service.redirectEmptyStories(context.Background(), "article-1", 10)
	if err != nil || len(redirected) != 1 || redirected[0] != 20 {
		t.Fatalf("redirected=%v err=%v", redirected, err)
	}
	if merger.from != 20 || merger.into != 10 || merger.actor != "grouping" || merger.reason == "" {
		t.Fatalf("redirect merge lost its target or audit reason: %#v", merger)
	}
}

/** TestProcessRevisitsDiscussionSignalsAfterFoundingStory preserves late-arrival matching behavior. */
func TestProcessRevisitsDiscussionSignalsAfterFoundingStory(t *testing.T) {
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	body := "a reaction to the newly announced product"
	article := &ArticleInput{ID: "report-1", Revision: 2, Title: "Product announced", BodyText: &body,
		DiscoveredAt: now, ParticipationMode: "editorial", XPost: XPost{TweetID: "tweet-123"}}
	repository := &workflowStubRepository{
		start:   GroupingStart{Revision: 2},
		article: article,
		analysis: &AnalysisInput{InputRevision: 2, Relevance: "pass", Title: stringPointer("产品发布"),
			Summary: stringPointer("公司发布一款新产品"), Output: map[string]any{"scope": "single", "fact": map[string]any{
				"title": "产品发布", "subject": "公司", "action": "发布", "object": "产品", "occurredAt": "2026-11-17"}}},
		commitResult: CommitResult{FactID: int64Pointer(11), StoryID: int64Pointer(21)},
		signals:      []SignalRevisitRow{{ArticleID: "early-reaction", Revision: 1, Title: "Reaction", BodyText: &body}},
	}
	embeddings := &embeddingStoreStub{available: true, vectors: map[string][]float32{
		"report-1": {1, 0}, "early-reaction": {.8, .6},
	}}
	recallRepository := &recallRepositoryStub{}
	service := NewService(repository, NewRecallService(recallRepository, embeddings, func() time.Time { return now }), nil,
		embeddings, nil, WorkflowConfig{Now: func() time.Time { return now }})
	queue := &signalTaskEnqueuerStub{}
	service.SetSignalTaskEnqueuer(queue)

	result, err := service.Process(context.Background(), article.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "new-story" || result.ReclaimedSignals != 1 || result.RematchedSignals != 1 {
		t.Fatalf("grouping result lost signal revisit counts: %+v", result)
	}
	if repository.waitingTweet != "tweet-123" || !repository.waitingSince.Equal(now.Add(-signalWaitWindow)) {
		t.Fatalf("waiting signal query used wrong identity or window: tweet=%q since=%s", repository.waitingTweet, repository.waitingSince)
	}
	if !repository.rematchSince.Equal(now.Add(-signalRevisitWindow)) {
		t.Fatalf("semantic signal query used wrong window: %s", repository.rematchSince)
	}
	if len(queue.articleIDs) != 2 || queue.articleIDs[0] != "early-reaction" || queue.articleIDs[1] != "early-reaction" {
		t.Fatalf("signal revisit tasks were not scheduled through both paths: %#v", queue.articleIDs)
	}
}

func TestProcessSupersededRevisionIsNotCommitted(t *testing.T) {
	repository := &workflowStubRepository{
		start:   GroupingStart{Revision: 1},
		article: &ArticleInput{ID: "article-3", Revision: 2, DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
	}
	service := NewService(repository, nil, nil, nil, nil, WorkflowConfig{})
	_, err := service.Process(context.Background(), "article-3", false)
	if !errors.Is(err, ErrSuperseded) {
		t.Fatalf("error=%v, want %v", err, ErrSuperseded)
	}
	if len(repository.commits) != 0 || repository.failures != 1 {
		t.Fatalf("commits=%d failures=%d", len(repository.commits), repository.failures)
	}
}

/** timePointer builds a timestamp fixture without sharing mutable test state. */
func timePointer(value time.Time) *time.Time { return &value }

func TestProcessKeepsExistingMembershipBeforeCheckingMissingAnalysis(t *testing.T) {
	repository := &workflowStubRepository{
		start:      GroupingStart{Revision: 5, WasComplete: true},
		article:    &ArticleInput{ID: "article-4", Revision: 5, DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
		membership: &CurrentMembership{FactID: 12, StoryID: 20},
	}
	service := NewService(repository, nil, nil, nil, nil, WorkflowConfig{})
	result, err := service.Process(context.Background(), "article-4", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "kept" || len(repository.commits) != 1 {
		t.Fatalf("result=%+v commits=%+v", result, repository.commits)
	}
	commit := repository.commits[0]
	if commit.Verdict != "kept" || commit.FactID == nil || *commit.FactID != 12 || commit.StoryID == nil || *commit.StoryID != 20 || commit.Selection != nil {
		t.Fatalf("kept commit=%+v", commit)
	}
}

func TestProcessRechecksSelectionForKeptMembershipOnlyWhenItWasUnset(t *testing.T) {
	for _, test := range []struct {
		name          string
		start         GroupingStart
		wantVerdict   string
		wantSelection bool
	}{
		{name: "already complete", start: GroupingStart{Revision: 6, WasComplete: true}, wantVerdict: "kept"},
		{name: "unfinished with unset value", start: GroupingStart{Revision: 6, SelectionAddsValue: nil}, wantVerdict: "standalone", wantSelection: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &workflowStubRepository{
				start:      test.start,
				article:    &ArticleInput{ID: "article-5", Revision: 6, DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
				membership: &CurrentMembership{FactID: 13, StoryID: 21},
			}
			service := NewService(repository, nil, nil, nil, nil, WorkflowConfig{})
			result, err := service.Process(context.Background(), "article-5", false)
			if err != nil {
				t.Fatal(err)
			}
			if result.Verdict != test.wantVerdict || len(repository.commits) != 1 {
				t.Fatalf("result=%+v commits=%+v", result, repository.commits)
			}
			commit := repository.commits[0]
			if commit.RemoveAutomatic || (commit.Selection != nil) != test.wantSelection {
				t.Fatalf("commit=%+v", commit)
			}
		})
	}
}

func TestFactFrameKeepsOccurrenceEvidenceAndConditionsSeparate(t *testing.T) {
	output := map[string]any{"fact": map[string]any{
		"title": "事实标题", "subject": "公司", "action": "发布", "object": "产品", "occurredAt": "2026-10-09",
		"evidence": "官方公告", "conditions": []any{
			map[string]any{"text": "先向部分用户开放", "quote": "limited rollout"},
			map[string]any{"text": "后续扩大范围", "quote": "expanding access"},
		},
	}}
	frame, title, evidence, conditions := factFrame(output, "备用标题")
	if frame == nil || title != "事实标题" || frame.OccurredAt == nil || *frame.OccurredAt != "2026-10-09" {
		t.Fatalf("frame=%+v title=%q", frame, title)
	}
	if evidence == nil || *evidence != "官方公告\nlimited rollout\nexpanding access" {
		t.Fatalf("evidence=%v", evidence)
	}
	if conditions == nil || *conditions != "先向部分用户开放；后续扩大范围" {
		t.Fatalf("conditions=%v", conditions)
	}
}

func TestReferencedStatusIDsUsesReplyAndQuotedPost(t *testing.T) {
	replyTo := "112233"
	post := XPost{ReplyTo: &replyTo, Quoted: &struct {
		URL string `json:"url"`
	}{URL: "https://x.com/example/status/445566?ref=share"}}
	got := referencedStatusIDs(post)
	if len(got) != 2 || got[0] != "112233" || got[1] != "445566" {
		t.Fatalf("referenced ids=%v", got)
	}
}

/** TestProcessUsesSelectedReadingAsCoverageWithoutTurningItIntoAFact verifies reading context affects selection only. */
func TestProcessUsesSelectedReadingAsCoverageWithoutTurningItIntoAFact(t *testing.T) {
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	summary := "The prior report covered the launch."
	body := "The original report explains the previously disclosed features."
	readingRepository := &recallRepositoryStub{
		reading: []ReadingRow{{
			ArticleID:  "selected-1",
			Revision:   1,
			Title:      "Prior selected release",
			Summary:    &summary,
			Source:     "Acme",
			FirstParty: true,
			At:         timePointer(now.Add(-time.Hour)),
		}},
		texts:  map[string]string{"selected-1": "Acme launched the Atlas product."},
		bodies: map[string]*string{"selected-1": &body},
	}
	embeddings := &embeddingStoreStub{available: true, vectors: map[string][]float32{
		"query-1":    {1, 0},
		"selected-1": {1, 0},
	}}
	model := &judgeModelStub{outputs: []string{
		`{"query":"current launch","decisions":[],"selection":{"addsValue":false,"reason":"The selected report already covered this."}}`,
	}}
	repository := &workflowStubRepository{
		start: GroupingStart{Revision: 1},
		article: &ArticleInput{
			ID:                "query-1",
			Revision:          1,
			Title:             "Atlas launch update",
			URL:               "https://source.example.invalid/atlas-update",
			DiscoveredAt:      now,
			SourceID:          "source-1",
			SourceName:        "Acme",
			ParticipationMode: "editorial",
		},
		analysis: &AnalysisInput{InputRevision: 1, Relevance: "pass", Output: map[string]any{
			"scope": "single",
			"fact":  map[string]any{"title": "Atlas update", "subject": "Acme", "action": "announced", "object": "an update"},
		}},
		commitManual: true,
	}
	recall := NewRecallService(readingRepository, embeddings, func() time.Time { return now })
	service := NewService(repository, recall, newJudgeServiceForTest(t, model), embeddings, nil, WorkflowConfig{Now: func() time.Time { return now }})
	if _, err := service.Process(context.Background(), "query-1", false); err != nil {
		t.Fatal(err)
	}
	if len(repository.commits) != 1 {
		t.Fatalf("grouping commits = %#v", repository.commits)
	}
	commit := repository.commits[0]
	if commit.Selection == nil || commit.Selection.AddsValue || len(commit.Candidates) != 0 || !commit.CreateStory || !commit.CreateFact {
		t.Fatalf("selected reading became an identity/fact candidate or lost its coverage judgement: %+v", commit)
	}
	if len(model.requests) != 1 || !strings.Contains(model.requests[0].User, "Prior selected release") || !strings.Contains(model.requests[0].User, body) || strings.Contains(model.requests[0].User, "候选 C1") {
		t.Fatalf("selected reading prompt did not stay separate from identity candidates: %#v", model.requests)
	}
	if len(model.complete) != 1 || model.complete[0] != 71 {
		t.Fatalf("selected reading judgement receipt was not completed: %#v", model.complete)
	}
}

/** TestProcessKeepsIndependentSourceEvidenceInNewFactJudgement protects novel evidence omitted from summaries. */
func TestProcessKeepsIndependentSourceEvidenceInNewFactJudgement(t *testing.T) {
	evidence := "Acme's original benchmark recorded a new 42% latency reduction on the production workload."
	model := &judgeModelStub{outputs: []string{`{"query":"benchmark update","decisions":[{"id":"C1","relation":"SAME_STORY","confidence":0.95}],"selection":{"addsValue":true,"reason":"new independently measured result"}}`}}
	repository := &workflowStubRepository{
		start: GroupingStart{Revision: 1},
		article: &ArticleInput{ID: "benchmark-update", Revision: 1, Title: "Acme reports a new benchmark", BodyText: &evidence,
			DiscoveredAt: time.Now(), ParticipationMode: "editorial"},
		analysis: &AnalysisInput{InputRevision: 1, Relevance: "pass", Summary: stringPointer("Acme shared a product update."), Output: map[string]any{
			"scope": "single",
			"fact":  map[string]any{"title": "Acme product update", "subject": "Acme", "action": "announced", "object": "an update"},
		}},
		candidates: []Candidate{{FactID: 12, StoryID: 20, FactTitle: "Earlier product update", Score: 0.9, StoryRoot: true, Selected: true}},
	}
	service := NewService(repository, NewRecallService(&recallRepositoryStub{}, nil, nil), newJudgeServiceForTest(t, model), nil, nil, WorkflowConfig{})

	result, err := service.Process(context.Background(), repository.article.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "new-fact-in-story" || len(repository.commits) != 1 {
		t.Fatalf("result=%+v commits=%+v", result, repository.commits)
	}
	commit := repository.commits[0]
	if commit.StoryID == nil || *commit.StoryID != 20 || !commit.CreateFact || commit.CreateStory || !commit.AddMembership || commit.Selection == nil || !commit.Selection.AddsValue {
		t.Fatalf("independent value did not create a selected fact in the existing story: %+v", commit)
	}
	if len(model.requests) != 1 || !strings.Contains(model.requests[0].User, evidence) {
		t.Fatalf("source evidence was omitted from the value judgement: requests=%#v", model.requests)
	}
}

/** A revised article is rejudged against selected coverage while its confirmed membership stays fixed. */
func TestProcessRechecksSelectionForNewRevisionWithoutChangingMembership(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	summary := "The selected report covered the original release."
	readingRepository := &recallRepositoryStub{
		reading: []ReadingRow{{
			ArticleID:  "selected-article",
			Revision:   1,
			Title:      "Selected original release",
			Summary:    &summary,
			Source:     "Acme",
			FirstParty: true,
			At:         timePointer(now.Add(-time.Hour)),
		}},
		texts: map[string]string{"selected-article": "Acme introduced the Atlas product."},
	}
	embeddings := &embeddingStoreStub{available: true, vectors: map[string][]float32{
		"revised-article":  {1, 0},
		"selected-article": {1, 0},
	}}
	model := &judgeModelStub{outputs: []string{
		`{"query":"revised release","decisions":[],"selection":{"addsValue":false,"reason":"The selected report already covered it."}}`,
	}}
	repository := &workflowStubRepository{
		start:   GroupingStart{Revision: 2, SelectionAddsValue: nil},
		article: &ArticleInput{ID: "revised-article", Revision: 2, Title: "Atlas release revision", URL: "https://source.example.invalid/atlas", DiscoveredAt: now, SourceID: "source-1", ParticipationMode: "editorial"},
		analysis: &AnalysisInput{InputRevision: 2, Relevance: "pass", Output: map[string]any{
			"scope": "single",
			"fact":  map[string]any{"title": "Atlas release", "subject": "Acme", "action": "announced", "object": "the Atlas product"},
		}},
		membership: &CurrentMembership{FactID: 12, StoryID: 20},
	}
	recall := NewRecallService(readingRepository, embeddings, func() time.Time { return now })
	service := NewService(repository, recall, newJudgeServiceForTest(t, model), embeddings, nil, WorkflowConfig{Now: func() time.Time { return now }})

	result, err := service.Process(context.Background(), "revised-article", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "kept" || len(repository.commits) != 1 {
		t.Fatalf("revision grouping = %+v, commits=%+v", result, repository.commits)
	}
	commit := repository.commits[0]
	if commit.FactID == nil || *commit.FactID != 12 || commit.StoryID == nil || *commit.StoryID != 20 || commit.CreateFact || commit.AddMembership {
		t.Fatalf("confirmed membership changed: %+v", commit)
	}
	if commit.Selection == nil || commit.Selection.AddsValue || len(commit.Candidates) != 0 {
		t.Fatalf("new revision did not keep its independent low-increment judgement: %+v", commit)
	}
	if len(model.requests) != 1 || !strings.Contains(model.requests[0].User, "Selected original release") || len(model.complete) != 1 {
		t.Fatalf("revised value judgment did not reuse the selected-reading model path and receipt: requests=%#v complete=%#v", model.requests, model.complete)
	}
}

/** TestTaggingCleanTag preserves the tag input guardrails. */
func TestTaggingCleanTag(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		valid    bool
	}{
		{input: "  Agent  ", expected: "Agent", valid: true},
		{input: "具身智能", expected: "具身智能", valid: true},
		{input: "最新", valid: false},
		{input: "AI", valid: false},
		{input: "a", valid: false},
		{input: "这是一个超级无敌巨长的不合理的标签名字", valid: false},
		{input: "DeepSeek-V3", expected: "DeepSeek-V3", valid: true},
	}
	for _, test := range tests {
		actual, valid := tagging.CleanTag(test.input)
		if valid != test.valid {
			t.Errorf("CleanTag(%q) valid = %v, expected %v", test.input, valid, test.valid)
		}
		if valid && actual != test.expected {
			t.Errorf("CleanTag(%q) = %q, expected %q", test.input, actual, test.expected)
		}
	}
}

/** TestTaggingExtractRules preserves entity and topic matches in story labels. */
func TestTaggingExtractRules(t *testing.T) {
	tag := "DeepSeek"
	entities := map[string]interests.Entity{
		"deepseek": {
			Name:       "DeepSeek",
			DisplayTag: &tag,
			Aliases:    []string{"深度求索", "DeepSeek"},
		},
		"openai": {
			Name:    "OpenAI",
			Aliases: []string{"ChatGPT", "OpenAI"},
		},
	}
	definitions := []topics.Definition{
		{Slug: "agent", Name: "AI Agent", Group: "field", Aliases: []string{"Agent", "智能体"}},
	}
	engine := tagging.NewEngine(entities, definitions)
	results := engine.ExtractRules([]tagging.ArticleSample{
		{Title: "DeepSeek 发布最新开源推理模型架构", Excerpt: "深度求索团队今天宣布推出全新架构，在 Agent 场景表现优异。"},
		{Title: "构建多智能体系统：Agent 协作范式探讨", Excerpt: "分析当前大模型在多 Agent 协同工作时的任务编排与执行效率。"},
	})
	if len(results) == 0 {
		t.Fatal("expected extracted tags, got none")
	}
	foundDeepSeek, foundAgent := false, false
	for _, result := range results {
		if result.ID == "deepseek" {
			foundDeepSeek = true
		}
		if result.ID == "agent" {
			foundAgent = true
		}
	}
	if !foundDeepSeek || !foundAgent {
		t.Fatalf("expected DeepSeek and agent tags, got %#v", results)
	}
}
