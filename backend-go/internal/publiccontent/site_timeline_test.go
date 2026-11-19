package publiccontent

import (
	"context"
	"encoding/json"
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
	itemIDs []string
}

/** TestSiteTimelineGroupsDatesByBeijingMidnight keeps leap-day and year boundaries stable. */
func TestSiteTimelineGroupsDatesByBeijingMidnight(t *testing.T) {
	for _, test := range []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "before year boundary", at: time.Date(2023, time.December, 31, 15, 59, 59, 999_000_000, time.UTC), want: "2023-12-31"},
		{name: "year boundary", at: time.Date(2023, time.December, 31, 16, 0, 0, 0, time.UTC), want: "2024-01-01"},
		{name: "leap day starts", at: time.Date(2024, time.February, 28, 16, 0, 0, 0, time.UTC), want: "2024-02-29"},
		{name: "before leap day ends", at: time.Date(2024, time.February, 29, 15, 59, 59, 999_000_000, time.UTC), want: "2024-02-29"},
		{name: "after leap day", at: time.Date(2024, time.February, 29, 16, 0, 0, 0, time.UTC), want: "2024-03-01"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := beijingDate(test.at); got != test.want {
				t.Fatalf("beijingDate(%s) = %q, want %q", test.at.Format(time.RFC3339Nano), got, test.want)
			}
		})
	}
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

/** SiteTimelineItems records the service's hydration scope and returns matching fixtures. */
func (r *siteTimelineFixtureRepository) SiteTimelineItems(_ context.Context, _ SiteTimelineQuery, _ time.Time, ids []string) ([]topics.PageItemRow, error) {
	r.itemIDs = append([]string(nil), ids...)
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

/** TestSiteTimelineBuildsCursorAndKeepsFilteredCardContract protects the compact card wire shape. */
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
	if !reflect.DeepEqual(repository.itemIDs, []string{"article-1"}) {
		t.Fatalf("hydrated item IDs = %v, want only the representative on this page", repository.itemIDs)
	}
	if got.NextCursor == nil || got.Filters.Channel != "news" || got.Filters.Category == nil || *got.Filters.Category != category || got.Filters.Tag == nil || *got.Filters.Tag != tag || !reflect.DeepEqual(got.DayCounts, repository.days) {
		t.Fatalf("timeline metadata = %#v", got)
	}
	if got.Cards[0].Item.SameEvent != nil {
		t.Fatalf("timeline added a field the TypeScript timeline does not return: %#v", got.Cards[0].Item.SameEvent)
	}
	encoded, err := json.Marshal(got.Cards[0])
	if err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	if err := json.Unmarshal(encoded, &card); err != nil {
		t.Fatal(err)
	}
	item, ok := card["item"].(map[string]any)
	if !ok {
		t.Fatalf("timeline card item = %T", card["item"])
	}
	for _, field := range []string{"links", "originalTitle", "revision", "discoveredAt", "story", "body"} {
		if _, exists := item[field]; exists {
			t.Errorf("timeline card unexpectedly serialized detail field %q: %s", field, encoded)
		}
	}
	decoded, err := decodeSiteTimelineCursor(*got.NextCursor)
	if err != nil || decoded.Anchor != first.UnixMilli() || decoded.GroupKey != "aarticle-1" || decoded.Binding != siteTimelineBindingHash(query) {
		t.Fatalf("next cursor = %#v, %v", decoded, err)
	}
}

/** TestSiteTimelineKeepsRepresentativeAndFactGroupCounts hydrates only selected group representatives. */
func TestSiteTimelineKeepsRepresentativeAndFactGroupCounts(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	firstAnchor := now.Add(-time.Minute)
	secondAnchor := now.Add(-10 * 24 * time.Hour)
	organization, person := "organization", "person"
	owner := "openai"
	reports := []struct {
		factID    int64
		articleID string
		sourceID  string
		role      string
		at        time.Time
	}{
		{factID: 42, articleID: "person-report-42", sourceID: "source-person-42", role: person, at: firstAnchor},
		{factID: 42, articleID: "official-report-42", sourceID: "source-official-42", role: organization, at: firstAnchor},
		{factID: 42, articleID: "other-report-42-1", sourceID: "source-other-42-1", role: person, at: firstAnchor},
		{factID: 42, articleID: "other-report-42-2", sourceID: "source-other-42-2", role: person, at: firstAnchor},
		{factID: 42, articleID: "other-report-42-3", sourceID: "source-other-42-3", role: person, at: firstAnchor},
		{factID: 42, articleID: "other-report-42-4", sourceID: "source-other-42-4", role: person, at: firstAnchor},
		{factID: 43, articleID: "person-report-43", sourceID: "source-person-43", role: person, at: secondAnchor},
		{factID: 43, articleID: "official-report-43", sourceID: "source-official-43", role: organization, at: secondAnchor},
		{factID: 43, articleID: "other-report-43-1", sourceID: "source-other-43-1", role: person, at: secondAnchor},
		{factID: 43, articleID: "other-report-43-2", sourceID: "source-other-43-2", role: person, at: secondAnchor},
		{factID: 43, articleID: "other-report-43-3", sourceID: "source-other-43-3", role: person, at: secondAnchor},
		{factID: 43, articleID: "other-report-43-4", sourceID: "source-other-43-4", role: person, at: secondAnchor},
	}
	members := make([]SiteTimelineMember, 0, len(reports))
	pool := make([]SiteTimelinePoolReport, 0, len(reports))
	items := make([]topics.PageItemRow, 0, len(reports))
	for _, report := range reports {
		role := report.role
		bodyMode := "full"
		if report.role == organization {
			bodyMode = "summary"
		}
		members = append(members, SiteTimelineMember{
			FactID: report.factID, ArticleID: report.articleID, SourceID: report.sourceID,
			SourceTier: "T2", PublisherRole: &role, OwnerEntityID: &owner,
			FactSubject: stringPointer("OpenAI"),
			Item:        topics.PageItemRow{ID: report.articleID, Title: report.articleID, BodyMode: bodyMode, TimelineAt: report.at},
		})
		pool = append(pool, SiteTimelinePoolReport{FactID: report.factID, ArticleID: report.articleID, SourceID: report.sourceID})
		items = append(items, topics.PageItemRow{
			ID: report.articleID, Title: report.articleID, Channel: "news", BodyMode: bodyMode, TimelineAt: report.at,
		})
	}
	repository := &siteTimelineFixtureRepository{
		fakeRepository: &fakeRepository{},
		anchors:        []SiteTimelineAnchor{{Key: "f42", AnchorAt: firstAnchor}, {Key: "f43", AnchorAt: secondAnchor}},
		facts: SiteTimelineFactData{
			Members: members,
			Pool:    pool,
			Facts:   []SiteTimelineFact{{ID: 42, PublicID: "public-fact-42"}, {ID: 43, PublicID: "public-fact-43"}},
		},
		items: items,
	}
	entities := map[string]interests.Entity{"openai": {Name: "OpenAI", Aliases: []string{"Open AI"}}}
	got, err := NewService(repository, entities).SiteTimeline(context.Background(), SiteTimelineQuery{Channel: "news", Limit: 20}, now, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cards) != 2 || got.Cards[0].Item.ID != "official-report-42" || got.Cards[1].Item.ID != "official-report-43" {
		t.Fatalf("representative card = %#v", got.Cards)
	}
	if !reflect.DeepEqual(repository.itemIDs, []string{"official-report-42", "official-report-43"}) {
		t.Fatalf("hydrated item IDs = %v, want only the two representatives", repository.itemIDs)
	}
	for index, factID := range []string{"public-fact-42", "public-fact-43"} {
		want := &SiteTimelineGroup{FactID: factID, ReportCount: 6, AdditionalSourceCount: 5}
		if !reflect.DeepEqual(got.Cards[index].Group, want) {
			t.Fatalf("fact group %d = %#v, want %#v", index, got.Cards[index].Group, want)
		}
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
