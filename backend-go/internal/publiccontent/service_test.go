package publiccontent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
)

type fakeRepository struct {
	ids               []string
	rows              []Record
	members           []FollowupMember
	followupPublicID  string
	followupRequested time.Time
	groupQuery        GroupReportsQuery
	groupRequested    time.Time
	groupReports      []GroupReportRow
	err               error
}

func (r *fakeRepository) Availability(_ context.Context, ids []string) ([]Record, error) {
	r.ids = append([]string(nil), ids...)
	return r.rows, r.err
}

func (r *fakeRepository) StoryFollowups(_ context.Context, publicID string, now time.Time) ([]FollowupMember, error) {
	r.followupPublicID = publicID
	r.followupRequested = now
	return r.members, r.err
}

func (r *fakeRepository) GroupReports(_ context.Context, query GroupReportsQuery, now time.Time) ([]GroupReportRow, error) {
	r.groupQuery = query
	r.groupRequested = now
	return r.groupReports, r.err
}

func TestItemAvailabilityFiltersAndKeepsPublicScope(t *testing.T) {
	repository := &fakeRepository{rows: []Record{
		{ID: "visible", Visibility: "public", SourceMode: "editorial", SourceName: "OpenAI（官网 RSS）"},
		{ID: "short", Visibility: "summary-only", SourceMode: "editorial", SourceName: "Short"},
		{ID: "withdrawn", Visibility: "withdrawn", SourceMode: "editorial", SourceName: "Hidden"},
		{ID: "heat", Visibility: "public", SourceMode: "hot_signal", SourceName: "Heat"},
	}}
	service := NewService(repository)
	got, err := service.ItemAvailability(context.Background(), []string{"visible", "visible", "bad/id", "short", "withdrawn", "heat", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ItemAvailability{
		"visible":   {Status: "public", SourceName: "OpenAI"},
		"short":     {Status: "summary-only"},
		"withdrawn": {Status: "unavailable"},
		"heat":      {Status: "unavailable"},
		"missing":   {Status: "unavailable"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("availability = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(repository.ids, []string{"visible", "short", "withdrawn", "heat", "missing"}) {
		t.Fatalf("repository IDs = %#v", repository.ids)
	}
}

func TestItemAvailabilityCapsAtFiveHundredAndEmptyDoesNotQuery(t *testing.T) {
	repository := &fakeRepository{}
	service := NewService(repository)
	if got, err := service.ItemAvailability(context.Background(), []string{"../bad"}); err != nil || len(got) != 0 || repository.ids != nil {
		t.Fatalf("invalid-only request = %#v, %v, queried=%#v", got, err, repository.ids)
	}
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = "item_" + string(rune('a'+i%26)) + string(rune('A'+i/26))
	}
	if _, err := service.ItemAvailability(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if len(repository.ids) != 500 {
		t.Fatalf("repository received %d IDs, want 500", len(repository.ids))
	}
}

func TestPublicSourceNameMatchesSiteFormatting(t *testing.T) {
	for input, want := range map[string]string{
		"OpenAI（官网动态）（RSS）":        "OpenAI",
		"X：Sam Altman (@sama)（热点）": "Sam Altman",
		"X（说明）":                    "X",
		"（仅备注）":                    "（仅备注）",
	} {
		if got := publicSourceName(input); got != want {
			t.Errorf("publicSourceName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStoryFollowupsChoosesRepresentativeOrdersAndCaps(t *testing.T) {
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	organizationRole := "organization"
	owner := "openai"
	score := 98.0
	lowScore := 20.0
	members := []FollowupMember{
		{FactID: 1, FactPublicID: "fact-00", ID: "generic", Title: "Generic title", SourceName: "News desk", BodyMode: "full", Score: &score, TimelineAt: now.Add(-time.Minute), SortAt: now.Add(-time.Hour), SourceTier: "T2"},
		{FactID: 1, FactPublicID: "fact-00", ID: "official", Title: "Official title", SourceName: "OpenAI（官网 RSS）", BodyMode: "summary", Score: &lowScore, TimelineAt: now.Add(-2 * time.Minute), SortAt: now.Add(-time.Hour), SourceTier: "T2", PublisherRole: &organizationRole, OwnerEntityID: &owner, FactSubject: stringPtr("ChatGPT")},
	}
	for i := 1; i < 10; i++ {
		members = append(members, FollowupMember{
			FactID: int64(i + 1), FactPublicID: fmt.Sprintf("fact-%02d", i), ID: fmt.Sprintf("article-%02d", i),
			Title: fmt.Sprintf("Title %d", i), SourceName: "Source", BodyMode: "full", TimelineAt: now,
			SortAt: now.Add(-time.Duration(i+1) * time.Hour),
		})
	}
	repository := &fakeRepository{members: members}
	entities := map[string]interests.Entity{"openai": {Name: "OpenAI", Aliases: []string{"ChatGPT"}}}
	service := NewService(repository, entities)
	got, err := service.StoryFollowups(context.Background(), "12345678-1234-4234-8234-123456789abc", now)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.More || len(got.Items) != followupLimit {
		t.Fatalf("response = %#v, want %d items and more=true", got, followupLimit)
	}
	if got.Items[0].FactID != "fact-00" || got.Items[0].Representative.ID != "official" {
		t.Fatalf("latest representative = %#v", got.Items[0])
	}
	if got.Items[0].Representative.Source.Name != "OpenAI" || got.Items[0].Representative.TimelineAt != "2026-10-07T07:58:00.000Z" {
		t.Fatalf("representative wire fields = %#v", got.Items[0].Representative)
	}
	if got.Items[1].FactID != "fact-01" || got.Items[7].FactID != "fact-07" {
		t.Fatalf("fact ordering/cap = %#v", got.Items)
	}
	if repository.followupPublicID != "12345678-1234-4234-8234-123456789abc" || !repository.followupRequested.Equal(now) {
		t.Fatalf("repository call = (%q, %s)", repository.followupPublicID, repository.followupRequested)
	}
}

func TestStoryFollowupsRejectsInvalidAndEmptyStories(t *testing.T) {
	repository := &fakeRepository{members: []FollowupMember{{FactID: 1, FactPublicID: "fact-1", ID: "article"}}}
	service := NewService(repository)
	if got, err := service.StoryFollowups(context.Background(), "not-a-uuid", time.Now()); err != nil || got != nil || repository.followupPublicID != "" {
		t.Fatalf("invalid public ID = %#v, %v; repository ID=%q", got, err, repository.followupPublicID)
	}
	repository.members = nil
	if got, err := service.StoryFollowups(context.Background(), "12345678-1234-4234-8234-123456789abc", time.Now()); err != nil || got != nil {
		t.Fatalf("empty story = %#v, %v", got, err)
	}
}

func TestGroupReportsMapsOnlyReportListFields(t *testing.T) {
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.FixedZone("local", 8*60*60))
	repository := &fakeRepository{groupReports: []GroupReportRow{{
		ID: "article-1", Title: "Report", TimelineAt: now, URL: "https://example.invalid/report", SourceName: "X：Sam Altman (@sama)（热点）",
	}}}
	service := NewService(repository)
	got, err := service.GroupReports(context.Background(), GroupReportsQuery{FactPublicID: "fact-1", Channel: "all", Tag: "Agent"}, now)
	if err != nil {
		t.Fatal(err)
	}
	wantTime := "2026-10-07T00:00:00.000Z"
	if got == nil || got.FactID != "fact-1" || len(got.Reports) != 1 || got.Reports[0].Source.Name != "Sam Altman" || got.Reports[0].TimelineAt != wantTime || got.Reports[0].OriginalURL != "https://example.invalid/report" {
		t.Fatalf("group response = %#v", got)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != `{"factId":"fact-1","reports":[{"id":"article-1","title":"Report","source":{"name":"Sam Altman"},"timelineAt":"2026-10-07T00:00:00.000Z","originalUrl":"https://example.invalid/report"}]}` {
		t.Fatalf("group response wire contract = %s", wire)
	}
	if repository.groupQuery.Tag != "Agent" || repository.groupQuery.FactPublicID != "fact-1" || !repository.groupRequested.Equal(now) {
		t.Fatalf("repository query = %#v at %s", repository.groupQuery, repository.groupRequested)
	}
	repository.groupReports = nil
	if got, err := service.GroupReports(context.Background(), GroupReportsQuery{FactPublicID: "fact-1"}, now); err != nil || got != nil {
		t.Fatalf("empty group = %#v, %v", got, err)
	}
}

func stringPtr(value string) *string { return &value }
