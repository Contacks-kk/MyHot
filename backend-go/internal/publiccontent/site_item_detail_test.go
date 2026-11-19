package publiccontent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/topics"
)

type siteItemFixtureRepository struct {
	*fakeRepository
	row         *SiteItemDetailRow
	related     []SiteItemStoryRef
	group       *SiteItemGroup
	seatHolder  *topics.SameEvent
	definitions []topics.Definition
}

func (r *siteItemFixtureRepository) SiteItem(_ context.Context, _ string, definitions []topics.Definition) (*SiteItemDetailRow, error) {
	r.definitions = definitions
	return r.row, r.err
}

func (r *siteItemFixtureRepository) SiteItemRelatedStories(context.Context, string) ([]SiteItemStoryRef, error) {
	return r.related, r.err
}

func (r *siteItemFixtureRepository) SiteItemGroup(context.Context, int64, time.Time) (*SiteItemGroup, error) {
	return r.group, r.err
}

func (r *siteItemFixtureRepository) SiteItemSeatHolder(context.Context, int64, time.Time) (*topics.SameEvent, error) {
	return r.seatHolder, r.err
}

func TestSiteItemSummaryOnlyRemovesEditorialJudgementsAndBody(t *testing.T) {
	selected, score := true, 92.5
	repository := &siteItemFixtureRepository{fakeRepository: &fakeRepository{}, row: &SiteItemDetailRow{
		Item:          topics.PageItemRow{ID: "summary", Title: "Headline", Summary: stringPointer("Brief"), Reason: stringPointer("Editorial reason"), Category: stringPointer("ai-models"), Tags: []string{"Agent", "entity:openai"}, Score: &score, Selected: &selected, Channel: "news", BodyMode: "summary", TimelineAt: time.Now(), SourceName: "Publisher"},
		OriginalTitle: stringPointer("Original headline"), OriginalURL: "https://publisher.example/story", DiscoveredAt: time.Now(), Visibility: "summary-only", SourceMode: "editorial", Indexable: true, Author: stringPointer("Reporter"), StoryPublicID: stringPointer("12345678-1234-4234-8234-123456789abc"), StoryTitle: stringPointer("Story"),
	}}
	got, err := NewService(repository).SiteItem(context.Background(), "summary", "zh", time.Now(), "", false)
	if err != nil || !got.Found || got.Item == nil {
		t.Fatalf("summary item = %#v, %v", got, err)
	}
	item := got.Item
	if item.ReadingMode != "summary-only" || item.Selected || item.Score != nil || item.Category != nil || item.Story != nil || item.Reason != nil || item.X != nil || item.Body != nil || len(item.Tags) != 0 || item.Indexable || item.MarkdownAvailable || item.Author != nil {
		t.Fatalf("summary-only leaked editorial/body fields: %#v", item)
	}
}

func TestSiteItemBuildsReadingBodyRelationshipsAndSafeProjection(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	selected, seat, score, complete := true, false, 81.6, false
	factID := int64(42)
	repository := &siteItemFixtureRepository{fakeRepository: &fakeRepository{}, row: &SiteItemDetailRow{
		Item:          topics.PageItemRow{ID: "article_1", Title: "Translated title", Summary: stringPointer("Short summary"), Reason: stringPointer("Seat reason"), Category: stringPointer("ai-models"), Tags: []string{"Agent", "entity:openai"}, Score: &score, Selected: &selected, Channel: "news", PublishedAt: timePointer(now.Add(-time.Hour)), TimelineAt: now.Add(-time.Hour), BodyMode: "full", SourceName: "Publisher（RSS notes）"},
		OriginalTitle: stringPointer("Original title"), OriginalURL: "https://publisher.example/article", DiscoveredAt: now.Add(-time.Hour), Visibility: "public", SourceMode: "editorial", Indexable: true, Seat: seat, FactID: &factID,
		Language: stringPointer("en"), BodyHTML: stringPointer(`<h2>Original section</h2><script>unsafe</script><p>Original body</p>`), BodyText: stringPointer("Original body"),
		TranslationHTML: stringPointer(`<h2>Chinese section</h2><p>译文</p>`), TranslationText: stringPointer("译文"), TranslationDone: &complete,
		TopicSlugs: []string{"openai"}, StoryPublicID: stringPointer("12345678-1234-4234-8234-123456789abc"), StoryTitle: stringPointer("Related story"),
	}, related: []SiteItemStoryRef{{PublicID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Title: "Another story"}},
		group: &SiteItemGroup{FactID: "fact-42", ReportCount: 3, AdditionalSourceCount: 2}, seatHolder: &topics.SameEvent{ID: "seat_article", Title: "Selected report"}}
	definitions := []topics.Definition{{Slug: "openai", Name: "OpenAI"}}
	service := NewService(repository)
	service.SetTopics(definitions)
	got, err := service.SiteItem(context.Background(), "article_1", "zh", now, "", false)
	if err != nil || !got.Found || got.Item == nil {
		t.Fatalf("item detail = %#v, %v", got, err)
	}
	item := got.Item
	if item.Source.Name != "Publisher" || item.Score == nil || *item.Score != 82 || item.Selected != true || item.Reason != nil || item.SameEvent == nil || item.SameEvent.ID != "seat_article" {
		t.Fatalf("summary/seat projection = %#v", item)
	}
	if item.Group == nil || item.Group.ReportCount != 3 || !reflect.DeepEqual(item.Topics, []SiteItemTopic{{Slug: "openai", Name: "OpenAI"}}) || len(item.RelatedStories) != 1 || !item.MarkdownAvailable {
		t.Fatalf("relationships = %#v", item)
	}
	if item.BodyLanguage != "zh" || item.Body == nil || item.Body.ZH == nil || item.Body.Original != nil || item.Body.Complete || !item.HasTranslation || len(item.Outline) != 1 || item.Outline[0].Text != "Chinese section" {
		t.Fatalf("reading body = %#v outline=%#v", item.Body, item.Outline)
	}
	if jsonText, err := json.Marshal(item); err != nil || string(jsonText) == "" {
		t.Fatalf("detail DTO cannot be encoded: %s %v", jsonText, err)
	}
}

/** X post text, translations, quoted text, and Markdown obey the source's full-text license. */
func TestSiteItemXFullTextRequiresSourceLicense(t *testing.T) {
	const (
		mainText       = "licensed X post text"
		translatedText = "X post translation"
		quotedText     = "quoted post text"
		quotedZH       = "quoted post translation"
		summary        = "licensed summary"
	)
	xPost, err := json.Marshal(map[string]any{
		"authorName": "Author",
		"handle":     "author",
		"text":       mainText,
		"media": []map[string]any{
			{"kind": "image", "url": "https://images.example/first.jpg", "poster": "https://images.example/first-poster.jpg", "width": 1280, "height": 720, "alt": "first image"},
			{"kind": "video", "url": "https://images.example/video.jpg", "poster": "https://images.example/video-poster.jpg", "width": 1280, "height": 720},
			{"kind": "image", "url": "https://youtube.com/watch?v=example", "width": 1280, "height": 720},
			{"kind": "image", "url": "https://images.example/pixel.jpg", "width": 1, "height": 1},
		},
		"quoted": map[string]any{
			"authorName": "Quoted author",
			"handle":     "quoted",
			"text":       quotedText,
			"url":        "https://x.com/quoted/status/123",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name               string
		bodyMode           string
		summary            *string
		wantFullText       bool
		wantMarkdownExport bool
	}{
		{name: "source license withheld", bodyMode: "summary", summary: stringPointer(summary), wantMarkdownExport: true},
		{name: "source license granted", bodyMode: "full", summary: stringPointer(summary), wantFullText: true, wantMarkdownExport: true},
		{name: "unlicensed post without summary", bodyMode: "summary", wantMarkdownExport: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := &SiteItemDetailRow{
				Item: topics.PageItemRow{
					ID:       "x_article",
					Title:    "X title",
					Summary:  test.summary,
					Channel:  "x",
					BodyMode: test.bodyMode,
					XPost:    xPost,
					QuotedZH: stringPointer(quotedZH),
				},
				OriginalURL:     "https://x.com/author/status/123",
				Visibility:      "public",
				SourceMode:      "editorial",
				TranslationText: stringPointer(translatedText),
			}
			repository := &siteItemFixtureRepository{fakeRepository: &fakeRepository{}, row: row}
			service := NewService(repository)

			lookup, err := service.SiteItem(context.Background(), "x_article", "zh", time.Now(), "site-item-image-secret", true)
			if err != nil || !lookup.Found || lookup.Item == nil {
				t.Fatalf("X detail = %#v, %v", lookup, err)
			}
			encoded, err := json.Marshal(lookup.Item)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantFullText {
				if lookup.Item.X == nil || lookup.Item.X.Quoted == nil || lookup.Item.Body == nil {
					t.Fatalf("licensed X text was not projected: %#v", lookup.Item)
				}
				media := lookup.Item.X.Media
				if len(media) != 2 || media[0].Kind != "image" || media[1].Kind != "video" || !strings.Contains(media[0].URL, "mode=full") || media[0].Poster == nil || !strings.Contains(*media[0].Poster, "mode=thumb") || media[0].SrcSet == nil || !strings.Contains(*media[0].SrcSet, "mode=image-720") {
					t.Fatalf("licensed X media projection = %#v", media)
				}
				if strings.Contains(string(encoded), "youtube.com/watch") || strings.Contains(string(encoded), "pixel.jpg") {
					t.Fatalf("non-article media leaked into the X detail: %s", encoded)
				}
				for _, marker := range []string{translatedText, quotedText, quotedZH} {
					if !strings.Contains(string(encoded), marker) {
						t.Errorf("licensed detail is missing %q: %s", marker, encoded)
					}
				}
				original, err := service.SiteItem(context.Background(), "x_article", "original", time.Now(), "site-item-image-secret", true)
				if err != nil || original.Item == nil || original.Item.Body == nil || original.Item.Body.Original == nil || !strings.Contains(*original.Item.Body.Original, mainText) {
					t.Errorf("licensed original body = %#v, %v", original, err)
				}
			} else {
				if lookup.Item.X != nil || lookup.Item.Body != nil {
					t.Fatalf("unlicensed X body was projected: %#v", lookup.Item)
				}
				for _, marker := range []string{mainText, translatedText, quotedText, quotedZH} {
					if strings.Contains(string(encoded), marker) {
						t.Errorf("unlicensed detail contains %q: %s", marker, encoded)
					}
				}
			}
			if lookup.Item.MarkdownAvailable != test.wantMarkdownExport {
				t.Fatalf("Markdown availability = %t, want %t", lookup.Item.MarkdownAvailable, test.wantMarkdownExport)
			}

			export, err := service.SiteItemMarkdown(context.Background(), "x_article", "Example", "https://site.example", "example", true)
			if err != nil || (export != nil) != test.wantMarkdownExport {
				t.Fatalf("Markdown export = %#v, %v", export, err)
			}
			if export == nil {
				return
			}
			if test.summary != nil && !strings.Contains(export.Body, *test.summary) {
				t.Errorf("licensed summary is missing from Markdown: %s", export.Body)
			}
			for _, marker := range []string{mainText, translatedText, quotedText, quotedZH} {
				if strings.Contains(export.Body, marker) != test.wantFullText {
					t.Errorf("Markdown full-text visibility for %q = %t, want %t: %s", marker, strings.Contains(export.Body, marker), test.wantFullText, export.Body)
				}
			}
		})
	}
}

func TestSiteItemHidesWithheldAndNonEditorialItems(t *testing.T) {
	for _, row := range []*SiteItemDetailRow{
		{Visibility: "withdrawn", SourceMode: "editorial"},
		{Visibility: "public", SourceMode: "hot_signal"},
		nil,
	} {
		repository := &siteItemFixtureRepository{fakeRepository: &fakeRepository{}, row: row}
		got, err := NewService(repository).SiteItem(context.Background(), "article_1", "zh", time.Now(), "", false)
		if err != nil || got.Found || got.Item != nil {
			t.Errorf("hidden item = %#v, %v", got, err)
		}
	}
}

func TestSiteItemMarkdownKeepsExportContractAndLicensedBody(t *testing.T) {
	published := time.Date(2026, 10, 8, 7, 6, 5, 0, time.FixedZone("UTC+8", 8*60*60))
	complete, selected := true, true
	repository := &siteItemFixtureRepository{fakeRepository: &fakeRepository{}, row: &SiteItemDetailRow{
		Item: topics.PageItemRow{
			ID: "article_1", Title: "中文标题", Summary: stringPointer("摘要"), Reason: stringPointer("推荐原因"),
			Selected: &selected, Channel: "news", BodyMode: "full", SourceName: "Publisher",
			PublishedAt: &published,
		},
		OriginalTitle: stringPointer("Original title"), OriginalURL: "https://publisher.example/story",
		DiscoveredAt: published.Add(-time.Hour), Visibility: "public", SourceMode: "editorial", Seat: true,
		Language: stringPointer("en"), BodyHTML: stringPointer("<h2>Original</h2><p>Body text</p>"),
		BodyText: stringPointer("Body text"), TranslationHTML: stringPointer("<p>中文译文</p>"), TranslationDone: &complete,
	}}
	export, err := NewService(repository).SiteItemMarkdown(context.Background(), "article_1", "Example", "https://site.example/", "example-site", true)
	if err != nil || export == nil {
		t.Fatalf("markdown export = %#v, %v", export, err)
	}
	if export.Filename != "example-site-article_1.md" {
		t.Errorf("filename = %q", export.Filename)
	}
	for _, expected := range []string{
		"# 中文标题", "> 原标题：Original title", "- 来源：Publisher",
		"- 发布时间：2026-10-07T23:06:05.000Z",
		"- Example：https://site.example/items/article_1",
		"## 摘要\n\n摘要", "## 推荐理由\n\n推荐原因",
		"## 正文 · 中文译文\n\n中文译文", "## 正文 · 原文\n\n## Original\n\nBody text",
	} {
		if !strings.Contains(export.Body, expected) {
			t.Errorf("export body %q does not contain %q", export.Body, expected)
		}
	}
}

func TestSiteItemMarkdownUsesCollectionTimeOnlyWhenPublicationTimeIsUnknown(t *testing.T) {
	discovered := time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)
	repository := &siteItemFixtureRepository{fakeRepository: &fakeRepository{}, row: &SiteItemDetailRow{
		Item:        topics.PageItemRow{ID: "article_1", Title: "Fixture", Summary: stringPointer("Summary")},
		OriginalURL: "https://publisher.example/story", DiscoveredAt: discovered, Visibility: "public", SourceMode: "editorial",
	}}
	export, err := NewService(repository).SiteItemMarkdown(context.Background(), "article_1", "Example", "https://site.example", "example", true)
	if err != nil || export == nil {
		t.Fatalf("markdown export = %#v, %v", export, err)
	}
	date := "2026-10-08T03:00:00.000Z"
	dateLine := ""
	for _, line := range strings.Split(export.Body, "\n") {
		if strings.Contains(line, date) {
			dateLine = line
			break
		}
	}
	if !strings.Contains(dateLine, "\u6536\u5f55\u65f6\u95f4\uff1a") {
		t.Fatalf("unknown publication date must use the explicit collection time: %s", export.Body)
	}
}
func TestSiteItemMarkdownExcludesSummaryOnlyAndNonPublicItems(t *testing.T) {
	for _, row := range []*SiteItemDetailRow{
		{Visibility: "summary-only", SourceMode: "editorial", Item: topics.PageItemRow{Summary: stringPointer("summary")}},
		{Visibility: "withdrawn", SourceMode: "editorial", Item: topics.PageItemRow{Summary: stringPointer("summary")}},
		{Visibility: "public", SourceMode: "hot_signal", Item: topics.PageItemRow{Summary: stringPointer("summary")}},
	} {
		repository := &siteItemFixtureRepository{fakeRepository: &fakeRepository{}, row: row}
		export, err := NewService(repository).SiteItemMarkdown(context.Background(), "article_1", "Example", "https://site.example", "example", true)
		if err != nil || export != nil {
			t.Errorf("markdown export for hidden row = %#v, %v", export, err)
		}
	}
}
