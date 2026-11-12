package hotranking

import (
	"context"
	"testing"
	"time"

	"aihot/backend-go/internal/heatsnapshot"
	"aihot/backend-go/internal/interests"
)

type heatStub struct {
	rows       []heatsnapshot.HeatRow
	people     []heatsnapshot.Participant
	heatAt     time.Time
	storyCalls []int64
}

func (s *heatStub) HeatAt(_ context.Context, at time.Time) ([]heatsnapshot.HeatRow, error) {
	s.heatAt = at
	return s.rows, nil
}

func (s *heatStub) Participants(_ context.Context, storyID int64, _ time.Time) ([]heatsnapshot.Participant, error) {
	s.storyCalls = append(s.storyCalls, storyID)
	return append([]heatsnapshot.Participant(nil), s.people...), nil
}

type repositoryStub struct {
	reports    map[int64][]Report
	entries    []Entry
	candidates int
}

func (s *repositoryStub) Reports(_ context.Context, storyID int64, _ time.Time) ([]Report, error) {
	return append([]Report(nil), s.reports[storyID]...), nil
}

func (s *repositoryStub) Save(_ context.Context, _ time.Time, entries []Entry, candidates int) (int64, error) {
	s.entries = append([]Entry(nil), entries...)
	s.candidates = candidates
	return 91, nil
}

func TestComputeRanksByHeatAndChoosesTheMostReportedFact(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	firstReport := at.Add(-2 * time.Hour)
	latest := at.Add(-10 * time.Minute)
	owner := "openai"
	role := "organization"
	heat := &heatStub{
		rows: []heatsnapshot.HeatRow{
			{StoryID: 1, PublicID: "story-one", Title: "OpenAI launches a model", Participants: 4, Heat: 1.234, HeatPrevious: .5, HeatComparable: .9, HeatPreviousComparable: .2, Uncomparable: 1, Recent6Hours: 3, EditorialParticipants: 2, SignalParticipants: 2, FirstReportAt: &firstReport, LatestAt: &latest},
			{StoryID: 2, PublicID: "story-two", Title: "No evidence", Participants: 3, Heat: 9, EditorialParticipants: 1},
			{StoryID: 3, PublicID: "story-three", Title: "Signal only", Participants: 4, Heat: 8, SignalParticipants: 4},
		},
		people: []heatsnapshot.Participant{
			{Name: "Community", Kind: "signal", Tier: "T2", At: at.Add(-time.Minute)},
			{Name: "OpenAI Blog", Kind: "editorial", Tier: "T1", At: at.Add(-2 * time.Minute)},
			{Name: "Tech Review", Kind: "editorial", Tier: "T2", At: at.Add(-time.Hour)},
		},
	}
	repository := &repositoryStub{reports: map[int64][]Report{
		1: {
			{ID: "leak", URL: "https://example.org/leak", Title: "A leak", SourceID: "wire", SourceName: "Wire", FactID: 10, TimelineAt: at.Add(-time.Hour), At: at.Add(-time.Hour), SourceTier: "T2"},
			{ID: "launch-a", URL: "https://openai.com/launch", Title: "Launch", SourceID: "openai", SourceName: "OpenAI Blog", FactID: 11, TimelineAt: at.Add(-30 * time.Minute), At: at.Add(-30 * time.Minute), Selected: true, SourceTier: "T2", OwnerEntityID: &owner, PublisherRole: &role, FactSubject: stringPointer("OpenAI")},
			{ID: "launch-b", URL: "https://news.example/launch", Title: "Launch coverage", SourceID: "media", SourceName: "Tech Review", FactID: 11, TimelineAt: at.Add(-20 * time.Minute), At: at.Add(-20 * time.Minute), Selected: true, SourceTier: "T1"},
		},
	}}
	service := NewService(repository, heat, map[string]interests.Entity{
		"openai": {Name: "OpenAI", Aliases: []string{"Open AI"}},
	})

	got, err := service.Compute(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 91 || got.Entries != 1 || repository.candidates != 2 {
		t.Fatalf("result=%#v candidates=%d", got, repository.candidates)
	}
	if !heat.heatAt.Equal(at) || len(heat.storyCalls) != 1 || heat.storyCalls[0] != 1 {
		t.Fatalf("heat service inputs at=%s participants=%v", heat.heatAt, heat.storyCalls)
	}
	entry := repository.entries[0]
	if entry.Rank != 1 || entry.StoryPublicID != "story-one" || entry.Heat != 12.3 || entry.ReportCount != 3 {
		t.Fatalf("rank entry = %#v", entry)
	}
	if entry.RepresentativeItemID != "launch-b" {
		t.Fatalf("representative = %q, want selected T1 report from the most reported fact", entry.RepresentativeItemID)
	}
	if entry.Trend != "up" || entry.TrendPct == nil || *entry.TrendPct != 350 || len(entry.Badges) != 2 || entry.Badges[0] != "surge" || entry.Badges[1] != "new" {
		t.Fatalf("trend/badges = %#v, %#v", entry.Trend, entry.Badges)
	}
	if len(entry.SourceNames) != 2 || entry.SourceNames[0] != "OpenAI Blog" || entry.SourceNames[1] != "Tech Review" {
		t.Fatalf("source names = %#v", entry.SourceNames)
	}
	if len(entry.Participants) != 3 || entry.Participants[0].Name != "OpenAI Blog" {
		t.Fatalf("participants = %#v", entry.Participants)
	}
}

func TestChooseRepresentativeFallsBackToPublisherIdentityAndFullText(t *testing.T) {
	owner := "acme"
	org := "organization"
	person := "person"
	entities := map[string]interests.Entity{"acme": {Name: "Acme Corp", Aliases: []string{"Acme"}}}
	reports := []Report{
		{ID: "plain", OwnerEntityID: &owner, PublisherRole: &org, FactSubject: stringPointer("Acme"), BodyMode: "summary", TimelineAt: time.Unix(1, 0)},
		{ID: "full", OwnerEntityID: &owner, PublisherRole: &person, FactSubject: stringPointer("Acme Corp"), BodyMode: "full", TimelineAt: time.Unix(2, 0)},
	}
	if got := chooseRepresentative(reports, entities); got.ID != "plain" {
		t.Fatalf("representative = %q, want the verified organization publisher", got.ID)
	}
	if got := representativePriority(Report{OwnerEntityID: &owner, PublisherRole: &person, FactSubject: stringPointer("Acme Corp + unknown")}, entities); got != 3 {
		t.Fatalf("an unresolved fact subject should not confer representative priority: %d", got)
	}
}

func stringPointer(value string) *string { return &value }
