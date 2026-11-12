package publiccontent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type v1StoryFakeRepository struct {
	*fakeRepository
	lookup      V1StoryLookup
	lookupID    string
	response    V1StoryResponse
	found       bool
	storyID     int64
	requestedAt time.Time
	err         error
}

func (r *v1StoryFakeRepository) ResolveV1Story(_ context.Context, publicID string) (V1StoryLookup, error) {
	r.lookupID = publicID
	return r.lookup, r.err
}

func (r *v1StoryFakeRepository) LoadV1Story(_ context.Context, storyID int64, now time.Time) (V1StoryResponse, bool, error) {
	r.storyID, r.requestedAt = storyID, now
	return r.response, r.found, r.err
}

func TestV1StoryResolutionAndAbsoluteLinks(t *testing.T) {
	repository := &v1StoryFakeRepository{
		fakeRepository: &fakeRepository{},
		lookup:         V1StoryLookup{Kind: "found", ID: 31},
		found:          true,
		response: V1StoryResponse{SchemaVersion: 1, Story: V1StoryBody{
			PublicID: "12345678-1234-4234-8234-123456789abc", Reports: []V1StoryReport{{ID: "article_1"}},
			Storyline: []V1RelatedStory{{PublicID: "story-1", Relation: "storyline"}},
			Related:   []V1RelatedStory{{PublicID: "story-2", Relation: "related"}},
		}},
	}
	service := NewService(repository)
	invalid, err := service.ResolveV1Story(context.Background(), "not-a-uuid")
	if err != nil || invalid.Kind != "not_found" || repository.lookupID != "" {
		t.Fatalf("invalid lookup = %#v, err=%v, repo id=%q", invalid, err, repository.lookupID)
	}
	lookup, err := service.ResolveV1Story(context.Background(), repository.response.Story.PublicID)
	if err != nil || lookup.Kind != "found" || lookup.ID != 31 || repository.lookupID != repository.response.Story.PublicID {
		t.Fatalf("lookup = %#v, err=%v", lookup, err)
	}
	now := time.Now()
	response, found, err := service.V1Story(context.Background(), lookup.ID, now, "https://myhot.example/")
	if err != nil || !found || repository.storyID != 31 || !repository.requestedAt.Equal(now.UTC().Truncate(time.Millisecond)) {
		t.Fatalf("story = %#v, found=%v err=%v repo=%d at=%s", response, found, err, repository.storyID, repository.requestedAt)
	}
	if response.Story.Links.AIHot != "https://myhot.example/story/12345678-1234-4234-8234-123456789abc" || response.Story.Reports[0].Links.AIHot != "https://myhot.example/items/article_1" {
		t.Fatalf("story links = %#v", response.Story)
	}
	if response.Story.Storyline[0].Links.API != "https://myhot.example/api/v1/stories/story-1" || response.Story.Related[0].Links.AIHot != "https://myhot.example/story/story-2" {
		t.Fatalf("neighbor links = %#v %#v", response.Story.Storyline, response.Story.Related)
	}

	repository.lookup = V1StoryLookup{Kind: "merged", Target: "abcdefab-cdef-4abc-8def-abcdefabcdef"}
	merged, err := service.ResolveV1Story(context.Background(), repository.response.Story.PublicID)
	if err != nil || merged.Kind != "merged" || merged.Target != repository.lookup.Target {
		t.Fatalf("merged lookup = %#v, err=%v", merged, err)
	}
	repository.lookup = V1StoryLookup{Kind: "not_found"}
	missing, err := service.ResolveV1Story(context.Background(), repository.response.Story.PublicID)
	if err != nil || missing.Kind != "not_found" {
		t.Fatalf("missing lookup = %#v, err=%v", missing, err)
	}
}

func TestBuildV1StoryUsesPrimaryReportsForFirstDateAndMapsLegacyStatus(t *testing.T) {
	first := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	latest := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	reports := []v1StoryReportRecord{
		{ID: "mention", At: latest.Add(time.Hour), SourceID: "source-1", Role: "mention"},
		{ID: "latest", Title: "Latest primary", At: latest, SourceID: "source-2", Role: "primary"},
		{ID: "first", Title: "First primary", At: first, SourceID: "source-1", Role: "primary"},
	}
	got := buildV1Story(v1StoryDatabase{PublicID: "story-id", Title: "Story"}, reports, nil, nil, now)
	if got.Story.Status != "active" || got.Story.ReportCount != 3 || got.Story.SourceCount != 2 || got.Story.FirstReportAt != formatV1Time(first) || got.Story.LatestAt != formatV1Time(latest) || got.Story.Latest != "Latest primary" {
		t.Fatalf("active story = %#v", got.Story)
	}
	if len(got.Story.Reports) != 3 || got.Story.Storyline == nil || got.Story.Related == nil {
		t.Fatalf("empty-list/report wire shape = %#v", got.Story)
	}
	settled := buildV1Story(v1StoryDatabase{PublicID: "story-id", Title: "Story"}, reports, nil, nil, now.Add(4*24*time.Hour))
	if settled.Story.Status != "settled" {
		t.Fatalf("settled status = %s", settled.Story.Status)
	}
}

func TestCurrentStoryDigestChecksSavedEvidenceAndStableFingerprint(t *testing.T) {
	rows := []v1StoryDigestRecord{{
		ID: "article_1", Title: "Story evidence", Summary: stringPtr("summary"), SourceName: "Source", FirstParty: true,
		At: time.Date(2026, 10, 7, 0, 0, 0, 987_000_000, time.UTC), FactID: 3,
		FactSubject: stringPtr("subject"), FactAction: stringPtr("action"), FactObject: nil, FactConditions: stringPtr("condition"),
		Evidence: stringPtr("quoted evidence"), StructuredFact: json.RawMessage(`{"conditions":[{"text":"condition","quote":"quoted"}],"evidence":"extracted"}`),
	}}
	hash := storyDigestHash(append([]v1StoryDigestRecord(nil), rows...))
	if hash != "70485fe4ce62516eddbf5492cfc1894bc47300ab048d44a8f89b91b9fe61ccaa" {
		t.Fatalf("digest hash = %s, want TypeScript stableJson hash", hash)
	}
	updated := time.Date(2026, 10, 7, 1, 2, 3, 456_000_000, time.UTC)
	digest := "saved digest"
	story := v1StoryDatabase{Digest: &digest, SavedDigest: &digest, SavedHash: &hash, SavedArticleIDs: json.RawMessage(`["article_1"]`), DigestUpdated: &updated}
	gotDigest, gotUpdated := currentStoryDigest(story, rows)
	if gotDigest == nil || *gotDigest != digest || gotUpdated == nil || *gotUpdated != formatV1Time(updated) {
		t.Fatalf("current digest = %v, %v", gotDigest, gotUpdated)
	}
	story.SavedHash = stringPtr("stale")
	if digest, updated := currentStoryDigest(story, rows); digest != nil || updated != nil {
		t.Fatalf("stale digest escaped: %v, %v", digest, updated)
	}
	story.SavedHash = &hash
	story.SavedArticleIDs = json.RawMessage(`[]`)
	if digest, _ := currentStoryDigest(story, rows); digest != nil {
		t.Fatalf("empty digest evidence escaped: %v", digest)
	}
	if storyDigestHash(nil) == "" {
		t.Fatal("empty evidence hash should still be deterministic")
	}
}
