package publiccontent

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"
)

type siteHotFixtureRepository struct {
	*fakeRepository
	ranking *SiteHotRanking
	details SiteHotDetails
	err     error
}

func (r *siteHotFixtureRepository) SiteHotRanking(context.Context, time.Time) (*SiteHotRanking, error) {
	return r.ranking, r.err
}

func (r *siteHotFixtureRepository) SiteHotDetails(context.Context, []SiteHotRankingRecord, time.Time, time.Time) (SiteHotDetails, error) {
	return r.details, r.err
}

func TestSiteHotBuildsWebsitePayloadWithSignedImagesAndHourlySpark(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	computedAt := now.Add(-37 * time.Minute)
	faceURL := "https://images.example.invalid/tier-15.png"
	coverURL := "https://images.example.invalid/cover.png"
	entry := json.RawMessage(`{"heat":12.5,"trend":"up","trendPct":23.4,"badges":["rising"],"participantCount":4,"sourceCount":3,"sourceNames":["Example（news）","Example"],"participants":[{"name":"Signal","kind":"signal","tier":"T1"},{"name":"Tier 2","kind":"editorial","tier":"T2"},{"name":"Tier 1","kind":"editorial","tier":"T1"},{"name":"Tier 1.5","kind":"editorial","tier":"T1_5"}]}`)
	repository := &siteHotFixtureRepository{
		fakeRepository: &fakeRepository{},
		ranking: &SiteHotRanking{ComputedAt: computedAt, Entries: []SiteHotRankingRecord{{
			ComputedAt: computedAt, Rank: 1, StoryID: 42, Entry: entry,
			StoryPublicID: "story-public-id", StoryTitle: "Current title",
		}}},
		details: SiteHotDetails{
			Sparks: map[int64][]SiteHotSparkRecord{42: {{StoryID: 42, Hour: time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC), Heat: 9.5}}},
			Faces:  []SiteHotFaceRecord{{Name: "Tier 2", URL: stringPointer("https://images.example.invalid/tier-2.png")}, {Name: "Tier 1.5", URL: &faceURL}},
			Texts:  map[int64]SiteHotTexts{42: {Summary: stringPointer("Verified digest"), Latest: stringPointer("Latest turn")}},
			Covers: map[int64]SiteHotCoverRecord{42: {StoryID: 42, URL: coverURL, Width: floatPointer(800), Height: floatPointer(450)}},
		},
	}
	service := NewService(repository)
	got, err := service.SiteHot(context.Background(), now, "test-signing-secret")
	if err != nil {
		t.Fatal(err)
	}
	if got.ComputedAt == nil || *got.ComputedAt != formatV1Time(computedAt) || got.WindowHours != 48 || len(got.Entries) != 1 {
		t.Fatalf("hot response metadata = %#v", got)
	}
	item := got.Entries[0]
	if item.Story.Title != "Current title" || item.Heat != 12.5 || item.Trend != "up" || item.Summary == nil || *item.Summary != "Verified digest" || item.Latest == nil || *item.Latest != "Latest turn" {
		t.Fatalf("hot entry = %#v", item)
	}
	if len(item.Spark) != 25 || item.Spark[24] == nil || *item.Spark[24] != 9.5 {
		t.Fatalf("spark slots = %#v", item.Spark)
	}
	if len(item.Participants) != 4 || item.Participants[0].Name != "Tier 1" || item.Participants[1].Name != "Tier 1.5" || item.Participants[2].Name != "Tier 2" || item.Participants[3].Name != "Signal" {
		t.Fatalf("participant ordering = %#v", item.Participants)
	}
	if string(item.Participants[0].IconURL) != "null" || item.Participants[3].IconURL != nil {
		t.Fatalf("visible null and signal face fields changed: %#v", item.Participants)
	}
	if got := siteHotProxyURL(t, item.Participants[1].IconURL); got != faceURL {
		t.Fatalf("participant image URL = %q", got)
	}
	if item.Participants[1].IconSrcSet == "" || item.Cover == nil || item.Cover.SrcSet == "" || item.Cover.Width == nil || *item.Cover.Width != 800 {
		t.Fatalf("responsive images missing: participant=%#v cover=%#v", item.Participants[1], item.Cover)
	}
	if got := siteHotProxyURL(t, json.RawMessage(mustJSON(t, item.Cover.URL))); got != coverURL {
		t.Fatalf("cover image URL = %q", got)
	}
	if len(item.SourceNames) != 1 || item.SourceNames[0] != "Example" {
		t.Fatalf("public source names = %#v", item.SourceNames)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" {
		t.Fatal("site hot response encoded to an empty body")
	}
}

func TestSiteHotEmptyRankingAndErrors(t *testing.T) {
	service := NewService(&siteHotFixtureRepository{fakeRepository: &fakeRepository{}})
	empty, err := service.SiteHot(context.Background(), time.Now(), "secret")
	if err != nil || empty.ComputedAt != nil || empty.Entries == nil || empty.WindowHours != 48 {
		t.Fatalf("empty hot response = %#v, %v", empty, err)
	}
	repository := &siteHotFixtureRepository{fakeRepository: &fakeRepository{}, err: errors.New("database unavailable")}
	if _, err := NewService(repository).SiteHot(context.Background(), time.Now(), "secret"); err == nil {
		t.Fatal("database error was hidden")
	}
	repository = &siteHotFixtureRepository{
		fakeRepository: &fakeRepository{},
		ranking:        &SiteHotRanking{ComputedAt: time.Now(), Entries: []SiteHotRankingRecord{{Entry: json.RawMessage(`{`)}}},
	}
	if _, err := NewService(repository).SiteHot(context.Background(), time.Now(), "secret"); err == nil {
		t.Fatal("malformed saved ranking was accepted")
	}
}

func siteHotProxyURL(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("image URL JSON = %s: %v", raw, err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query().Get("u")
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
