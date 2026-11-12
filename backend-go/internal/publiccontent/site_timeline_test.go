package publiccontent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/topics"
)

type siteTimelineFixtureRepository struct {
	*fakeRepository
	anchors []SiteTimelineAnchor
	facts   SiteTimelineFactData
	items   []topics.PageItemRow
	days    map[string]int
	query   SiteTimelineQuery
}

func (r *siteTimelineFixtureRepository) SiteTimelineAnchors(_ context.Context, query SiteTimelineQuery, _ time.Time, after *siteTimelineCursor, limit int) ([]SiteTimelineAnchor, error) {
	r.query = query
	rows := make([]SiteTimelineAnchor, 0, limit)
	for _, anchor := range r.anchors {
		if after != nil && !(anchor.AnchorAt.Before(time.UnixMilli(after.Anchor).UTC()) || (anchor.AnchorAt.Equal(time.UnixMilli(after.Anchor).UTC()) && anchor.Key < after.GroupKey)) {
			continue
		}
		rows = append(rows, anchor)
		if len(rows) == limit {
			break
		}
	}
	return rows, nil
}

func (r *siteTimelineFixtureRepository) SiteTimelineFacts(context.Context, SiteTimelineQuery, time.Time, []int64) (SiteTimelineFactData, error) {
	return r.facts, nil
}

func (r *siteTimelineFixtureRepository) SiteTimelineItems(_ context.Context, _ SiteTimelineQuery, _ time.Time, ids []string) ([]topics.PageItemRow, error) {
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	rows := make([]topics.PageItemRow, 0, len(ids))
	for _, row := range r.items {
		if wanted[row.ID] {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (r *siteTimelineFixtureRepository) SiteTimelineDayCounts(context.Context, SiteTimelineQuery, time.Time, []string) (map[string]int, error) {
	return r.days, nil
}

func TestSiteTimelineBuildsCursorAndKeepsFilteredCardContract(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	first := now.Add(-time.Minute).Truncate(time.Millisecond)
	second := first.Add(-time.Minute)
	repository := &siteTimelineFixtureRepository{
		fakeRepository: &fakeRepository{},
		anchors:        []SiteTimelineAnchor{{Key: "aarticle-1", AnchorAt: first}, {Key: "aarticle-2", AnchorAt: second}},
		items:          []topics.PageItemRow{{ID: "article-1", Title: "First selected article", Channel: "news", BodyMode: "summary", Tags: []string{"Agent"}, TimelineAt: first}},
		days:           map[string]int{"2026-10-08": 2},
	}
	category, tag := "ai-models", "Agent"
	query := SiteTimelineQuery{Channel: "news", Category: &category, Tag: &tag, Limit: 1}
	got, err := NewService(repository).SiteTimeline(context.Background(), query, now, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cards) != 1 || got.Cards[0].Key != "aarticle-1" || got.Cards[0].Item.Title != "First selected article" || got.Cards[0].Group != nil {
		t.Fatalf("timeline cards = %#v", got.Cards)
	}
	if got.NextCursor == nil || got.Filters.Channel != "news" || got.Filters.Category == nil || *got.Filters.Category != category || got.Filters.Tag == nil || *got.Filters.Tag != tag || !reflect.DeepEqual(got.DayCounts, repository.days) {
		t.Fatalf("timeline metadata = %#v", got)
	}
	if got.Cards[0].Item.SameEvent != nil {
		t.Fatalf("timeline added a field the TypeScript timeline does not return: %#v", got.Cards[0].Item.SameEvent)
	}
	decoded, err := decodeSiteTimelineCursor(*got.NextCursor)
	if err != nil || decoded.Anchor != first.UnixMilli() || decoded.GroupKey != "aarticle-1" || decoded.Binding != siteTimelineBindingHash(query) {
		t.Fatalf("next cursor = %#v, %v", decoded, err)
	}
}

func TestSiteTimelineKeepsRepresentativeAndFactGroupCounts(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	anchor := now.Add(-time.Minute)
	organization, person := "organization", "person"
	owner := "openai"
	repository := &siteTimelineFixtureRepository{
		fakeRepository: &fakeRepository{},
		anchors:        []SiteTimelineAnchor{{Key: "f42", AnchorAt: anchor}},
		facts: SiteTimelineFactData{
			Members: []SiteTimelineMember{
				{FactID: 42, ArticleID: "person-report", SourceID: "source-person", SourceTier: "T2", PublisherRole: &person, OwnerEntityID: &owner, FactSubject: stringPointer("OpenAI"), Item: topics.PageItemRow{ID: "person-report", Title: "Person", BodyMode: "full", TimelineAt: anchor}},
				{FactID: 42, ArticleID: "official-report", SourceID: "source-official", SourceTier: "T2", PublisherRole: &organization, OwnerEntityID: &owner, FactSubject: stringPointer("OpenAI"), Item: topics.PageItemRow{ID: "official-report", Title: "Official", BodyMode: "summary", TimelineAt: anchor}},
			},
			Pool:  []SiteTimelinePoolReport{{FactID: 42, ArticleID: "official-report", SourceID: "source-official"}, {FactID: 42, ArticleID: "other-report", SourceID: "source-other"}, {FactID: 42, ArticleID: "third-report", SourceID: "source-third"}},
			Facts: []SiteTimelineFact{{ID: 42, PublicID: "public-fact-42"}},
		},
		items: []topics.PageItemRow{{ID: "official-report", Title: "Official", Channel: "news", BodyMode: "summary", TimelineAt: anchor}},
	}
	entities := map[string]interests.Entity{"openai": {Name: "OpenAI", Aliases: []string{"Open AI"}}}
	got, err := NewService(repository, entities).SiteTimeline(context.Background(), SiteTimelineQuery{Channel: "news", Limit: 20}, now, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cards) != 1 || got.Cards[0].Item.ID != "official-report" {
		t.Fatalf("representative card = %#v", got.Cards)
	}
	want := &SiteTimelineGroup{FactID: "public-fact-42", ReportCount: 3, AdditionalSourceCount: 2}
	if !reflect.DeepEqual(got.Cards[0].Group, want) {
		t.Fatalf("fact group = %#v, want %#v", got.Cards[0].Group, want)
	}
}

func TestSiteTimelineRejectsMalformedAndFilterMismatchedCursors(t *testing.T) {
	now := time.Now().UTC()
	repository := &siteTimelineFixtureRepository{fakeRepository: &fakeRepository{}}
	query := SiteTimelineQuery{Channel: "all", Limit: 20}
	for _, test := range []struct {
		name   string
		cursor string
		want   string
	}{
		{name: "unknown prefix", cursor: "other.e30=", want: "unknown cursor format"},
		{name: "malformed data", cursor: "tl2.%%%", want: "malformed cursor"},
		{name: "different filter", cursor: encodeSiteTimelineCursor(siteTimelineCursor{Anchor: now.UnixMilli(), GroupKey: "aarticle-1", Binding: "wrong"}), want: "cursor does not match this query"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cursor := test.cursor
			_, err := NewService(repository).SiteTimeline(context.Background(), SiteTimelineQuery{Channel: query.Channel, Limit: query.Limit, Cursor: &cursor}, now, "", true)
			var invalid SiteTimelineInvalidCursorError
			if !errors.As(err, &invalid) || invalid.Error() != test.want {
				t.Fatalf("cursor error = %T %v, want %q", err, err, test.want)
			}
		})
	}
}

type sitePoolFixtureRepository struct {
	*fakeRepository
	data      SitePoolData
	holders   map[int64]SitePoolHolder
	query     SitePoolQuery
	terms     []string
	entityTag string
}

func (r *sitePoolFixtureRepository) SitePoolPage(_ context.Context, query SitePoolQuery, _ time.Time, terms []string, entityTag string, _, _ int) (SitePoolData, error) {
	r.query = query
	r.terms = append([]string(nil), terms...)
	r.entityTag = entityTag
	return r.data, nil
}

func (r *sitePoolFixtureRepository) SitePoolSeatHolders(context.Context, []int64, time.Time) (map[int64]SitePoolHolder, error) {
	return r.holders, nil
}

func TestSitePoolSearchProjectsFiltersPageAndSeatHolder(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	factID := int64(42)
	updated := now.Add(-time.Minute)
	selected := true
	repository := &sitePoolFixtureRepository{
		fakeRepository: &fakeRepository{},
		data: SitePoolData{
			Rows:  []SitePoolRow{{Item: topics.PageItemRow{ID: "report", Title: "OpenAI launches", Summary: stringPointer("Summary"), Reason: stringPointer("Reason"), Channel: "news", BodyMode: "summary", TimelineAt: now}, Selected: true, Seat: false, FactID: &factID}},
			Total: 80, TodayCount: 12, UpdatedAt: &updated,
		},
		holders: map[int64]SitePoolHolder{factID: {ID: "seat", Title: "Selected seat"}},
	}
	definitions := []topics.Definition{{Slug: "openai", Name: "OpenAI / ChatGPT", EntityID: "openai", Aliases: []string{"Chat GPT"}}}
	service := NewService(repository)
	service.SetTopics(definitions)
	category, tag, search := "ai-models", "Agent", "  CHAT GPT  "
	got, err := service.SitePool(context.Background(), SitePoolQuery{Channel: "news", Category: &category, Tag: &tag, Search: &search, Tab: "relevance", Page: 2}, now, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repository.terms, []string{"chat", "gpt"}) || repository.entityTag != "entity:openai" {
		t.Fatalf("search projection = terms %v, entity tag %q", repository.terms, repository.entityTag)
	}
	if repository.query.Page != 2 || repository.query.Tab != "relevance" || repository.query.Search == nil || *repository.query.Search != "CHAT GPT" {
		t.Fatalf("repository query = %#v", repository.query)
	}
	if got.Total != 80 || got.PageCount != 2 || got.TodayCount != 12 || got.Freshness != formatV1Time(updated) || len(got.Items) != 1 {
		t.Fatalf("pool metadata = %#v", got)
	}
	if got.Items[0].Selected != selected || got.Items[0].SameEvent == nil || got.Items[0].SameEvent.ID != "seat" || got.Items[0].Reason != nil {
		t.Fatalf("pool seat projection = %#v", got.Items[0])
	}
}

func TestSitePoolDoesNotTreatCompanyNameAsSubstringAndCapsPaging(t *testing.T) {
	definitions := []topics.Definition{{Slug: "deepseek", Name: "DeepSeek / V4", EntityID: "deepseek", Aliases: []string{"deep seek"}}}
	if got := sitePoolEntityTag("deepseek v4", definitions); got != "" {
		t.Fatalf("compound free-text search became an entity lookup: %q", got)
	}
	if got := sitePoolEntityTag("Deep Seek", definitions); got != "entity:deepseek" {
		t.Fatalf("configured topic alias did not resolve: %q", got)
	}
	query := "  " + strings.Repeat("a", 198) + "😀tail  "
	service := NewService(&sitePoolFixtureRepository{fakeRepository: &fakeRepository{}, data: SitePoolData{Rows: []SitePoolRow{}, Total: 6000}})
	got, err := service.SitePool(context.Background(), SitePoolQuery{Channel: "all", Search: &query, Page: 900}, time.Now(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Page != 50 || got.PageCount != 50 || got.Total != sitePoolCountCap || got.Filters.Search == nil || *got.Filters.Search != strings.Repeat("a", 198)+"😀" {
		t.Fatalf("bounded site pool = %#v", got)
	}
}
