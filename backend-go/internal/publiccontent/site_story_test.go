package publiccontent

import (
	"context"
	"math"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/topics"
)

type siteStoryFixtureRepository struct {
	*fakeRepository
	lookup V1StoryLookup
	data   SiteStoryData
}

func (r *siteStoryFixtureRepository) ResolveV1Story(context.Context, string) (V1StoryLookup, error) {
	return r.lookup, nil
}

func (r *siteStoryFixtureRepository) LoadV1Story(context.Context, int64, time.Time) (V1StoryResponse, bool, error) {
	return V1StoryResponse{}, false, nil
}

func (r *siteStoryFixtureRepository) SiteStory(context.Context, int64, time.Time, []topics.Definition) (SiteStoryData, error) {
	return r.data, nil
}

func TestSiteStoryBuildsTimelineDevelopmentsHeatAndTopicOrder(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	old := now.Add(-50 * time.Hour)
	recent := now.Add(-30 * time.Hour)
	selected := true
	full, summary := "full", "summary"
	fullScore, lowScore := 91.0, 40.0
	repository := &siteStoryFixtureRepository{
		fakeRepository: &fakeRepository{},
		lookup:         V1StoryLookup{Kind: "found", ID: 42},
		data: SiteStoryData{
			Story: v1StoryDatabase{PublicID: "12345678-1234-4234-8234-123456789abc", Title: "Story title"},
			Reports: []SiteStoryReport{
				{ID: "latest-mentioned", Title: "Mention only", At: now.Add(-time.Hour), SourceID: "source-x", SourceName: "Signal source", FactID: 2, Role: "mention", Selected: true},
				{ID: "rep-full", Title: "Primary full", Summary: stringPointer("Full summary"), At: old, SourceID: "source-b", SourceName: "Publisher B", FactID: 1, Role: "primary", Selected: selected, SourceTier: "T2", BodyMode: full, Score: &fullScore, TimelineAt: old, FirstParty: true, TopicSlugs: []string{"openai"}},
				{ID: "rep-t1", Title: "Primary T1", Summary: stringPointer("T1 summary"), At: recent, SourceID: "source-a", SourceName: "Publisher A", FactID: 1, Role: "report", Selected: selected, SourceTier: "T1", BodyMode: summary, Score: &lowScore, TimelineAt: recent, FirstParty: true, TopicSlugs: []string{"openai", "ai"}},
				{ID: "first", Title: "First fact report", Summary: stringPointer("First summary"), At: now.Add(-60 * time.Hour), SourceID: "source-c", SourceName: "Publisher C", FactID: 2, Role: "primary", SourceTier: "T2", BodyMode: summary, TimelineAt: now.Add(-60 * time.Hour), TopicSlugs: []string{"ai"}},
			},
			Facts:   []SiteStoryFact{{ID: 1, PublicID: "fact-one", Title: "Development one"}, {ID: 2, PublicID: "fact-two", Title: "Development two"}},
			Latest:  &SiteStoryLatest{ID: "rep-t1", Title: "Latest eligible", At: recent},
			WhyHot:  SiteStoryWhyHot{Participants48h: 8, NewParticipants6h: 2, RecentReports24h: 3, ObservationComplete: true, Rank: intPointer(4)},
			Related: []v1StoryRelatedRecord{{PublicID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Title: "Related", Relation: "related"}},
			Heat:    []SiteStoryHeatPoint{{Hour: now.Add(-time.Hour), Heat: 9, Participants: 3}, {Hour: now, Heat: 10, Participants: 4}},
			Signals: []SiteStorySignal{
				{ParticipantKey: "kept", ObservedAt: now.Add(-time.Hour), SourceSince: now.Add(-100 * 24 * time.Hour)},
				{ParticipantKey: "kept", ObservedAt: now, SourceSince: now.Add(-100 * 24 * time.Hour)},
				{ParticipantKey: "late", ObservedAt: now, SourceSince: now.Add(-48 * time.Hour)},
			},
		},
	}
	service := NewService(repository, map[string]interests.Entity{})
	service.SetTopics([]topics.Definition{{Slug: "ai", Name: "AI", Position: 0}, {Slug: "openai", Name: "OpenAI", Position: 1}})
	got, err := service.SiteStory(context.Background(), repository.data.Story.PublicID, now)
	if err != nil || got.Kind != "found" || got.Story == nil {
		t.Fatalf("site story = %#v, %v", got, err)
	}
	story := got.Story
	if story.Status != "watching" || story.ReportCount != 4 || story.SourceCount != 4 || story.Latest == nil || *story.Latest != "Latest eligible" || story.LatestReport.ID != "rep-t1" {
		t.Fatalf("story summary = %#v", story)
	}
	if story.FirstReportAt == nil || *story.FirstReportAt != formatV1Time(now.Add(-60*time.Hour)) || story.WhyHot.Rank == nil || *story.WhyHot.Rank != 4 {
		t.Fatalf("dates and rank = %#v", story)
	}
	if len(story.Developments) != 2 || story.Developments[0].FactID != "fact-one" || story.Developments[0].Representative.ID != "rep-t1" {
		t.Fatalf("developments = %#v", story.Developments)
	}
	if len(story.OfficialReports) != 2 || len(story.Timeline) != 4 || story.Timeline[0].ID != "latest-mentioned" || len(story.Related) != 1 {
		t.Fatalf("report projections = official %v timeline %v related %v", story.OfficialReports, story.Timeline, story.Related)
	}
	if len(story.Topics) != 2 || story.Topics[0].Slug != "ai" || story.Topics[1].Slug != "openai" {
		t.Fatalf("topic order = %#v", story.Topics)
	}
	if len(story.Heat) != 2 || story.Heat[0].Participants != 1 || math.Abs(story.Heat[0].Heat-1) > 0.000001 || story.Heat[1].Participants != 1 {
		t.Fatalf("recomputed heat = %#v", story.Heat)
	}
	if story.Excerpt == nil || story.Excerpt.Text != "First summary" || story.Excerpt.SourceName != "Publisher C" {
		t.Fatalf("origin excerpt = %#v", story.Excerpt)
	}
}

func TestSiteStoryReturnsMergedAndMissingResolutionWithoutLoadingDetail(t *testing.T) {
	for _, lookup := range []V1StoryLookup{
		{Kind: "merged", Target: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{Kind: "not_found"},
	} {
		repository := &siteStoryFixtureRepository{fakeRepository: &fakeRepository{}, lookup: lookup}
		got, err := NewService(repository).SiteStory(context.Background(), "12345678-1234-4234-8234-123456789abc", time.Now())
		if err != nil || got.Kind != lookup.Kind || got.Target != lookup.Target || got.Story != nil {
			t.Errorf("story resolution = %#v, %v; want %#v", got, err, lookup)
		}
	}
}

func intPointer(value int) *int { return &value }
