package grouping

import (
	"context"
	"errors"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
)

type workflowStubRepository struct {
	start        GroupingStart
	article      *ArticleInput
	analysis     *AnalysisInput
	manual       ManualState
	membership   *CurrentMembership
	commits      []CommitInput
	failures     int
	commitManual bool
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
func (r *workflowStubRepository) RelatedPosts(context.Context, string, string, []string) (*PoolRow, []PoolRow, error) {
	return nil, nil, nil
}
func (r *workflowStubRepository) CandidateViews(context.Context, []Recalled, map[string]interests.Entity, time.Time) ([]Candidate, error) {
	return []Candidate{}, nil
}
func (r *workflowStubRepository) CommitGrouping(_ context.Context, input CommitInput) (CommitResult, error) {
	r.commits = append(r.commits, input)
	if r.commitManual {
		return CommitResult{Manual: ManualState{Found: true}}, nil
	}
	return CommitResult{}, nil
}
func (r *workflowStubRepository) FailGrouping(context.Context, string, int, *int64, error, bool) error {
	r.failures++
	return nil
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
