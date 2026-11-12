package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/mediaurl"
)

func TestParseRSSPreservesContentDateAndCategories(t *testing.T) {
	xml := `<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>Source</title><link>https://source.example.invalid</link><description>Feed</description><item><title>OpenAI release</title><link>/article/1</link><description><![CDATA[<p>Summary</p>]]></description><content:encoded><![CDATA[<p>Full content</p>]]></content:encoded><pubDate>Sun, 04 Oct 2026 00:00:00 GMT</pubDate><category>AI</category></item></channel></rss>`
	entries, err := parseRSS([]byte(xml), "https://source.example.invalid/feed")
	if err != nil || len(entries) != 1 {
		t.Fatalf("RSS entries = %+v: %v", entries, err)
	}
	got := entries[0]
	if got.URL != "https://source.example.invalid/article/1" || got.Published == nil || got.Body != "<p>Full content</p>" || len(got.Categories) != 1 {
		t.Fatalf("RSS content = %+v", got)
	}
	if len(got.Media) != 0 {
		t.Fatalf("expected nil Media when item has no images, got %s", got.Media)
	}
}

func TestParseRSSExtractsFiltersDeduplicatesAndCapsMediaMetadata(t *testing.T) {
	xml := `<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:media="http://search.yahoo.com/mrss/">
	<channel>
		<title>Source</title>
		<link>https://source.example.invalid</link>
		<image><url>https://source.example.invalid/site-logo.png</url><title>Logo</title></image>
		<item>
			<title>Visual benchmark report</title>
			<link>https://source.example.invalid/posts/visual-1</link>
			<enclosure url="/images/hero.jpg" type="image/jpeg" />
			<media:thumbnail url="https://source.example.invalid/images/hero.jpg" width="800" height="450" />
			<media:content url="https://source.example.invalid/images/diagram.png" medium="image" width="1200" height="680" />
			<description><![CDATA[<img src="/avatar/u1.png" width="320" height="320"><img data-src="/images/inline-1.webp" alt="  Latency chart  " width="900" height="500"><img src="https://secure.adnxs.com/pixel" width="1" height="1">]]></description>
			<content:encoded><![CDATA[<figure><img src="/images/inline-2.png" width="700" height="400"><figcaption>Throughput comparison across engines</figcaption></figure><img src="/tiny-badge.png" width="48" height="48">]]></content:encoded>
		</item>
	</channel></rss>`

	entries, err := parseRSS([]byte(xml), "https://source.example.invalid/feed.xml")
	if err != nil || len(entries) != 1 {
		t.Fatalf("RSS entries = %+v: %v", entries, err)
	}

	var media []mediaurl.MediaItem
	if err := json.Unmarshal(entries[0].Media, &media); err != nil {
		t.Fatalf("unmarshal media failed (%s): %v", entries[0].Media, err)
	}

	if len(media) != 4 {
		t.Fatalf("expected 4 deduplicated valid images (no feed logo, avatar, pixel, or 48x48 badge), got %+v", media)
	}
	if media[0].URL != "https://source.example.invalid/images/hero.jpg" {
		t.Fatalf("expected enclosure hero first, got %+v", media[0])
	}
	if media[1].URL != "https://source.example.invalid/images/diagram.png" || media[1].Width == nil || *media[1].Width != 1200 {
		t.Fatalf("unexpected media:content entry: %+v", media[1])
	}
	if media[2].Alt == nil || *media[2].Alt != "Throughput comparison across engines" {
		t.Fatalf("expected figcaption fallback alt on content img, got %+v", media[2])
	}
	if media[3].URL != "https://source.example.invalid/images/inline-1.webp" || media[3].Alt == nil || *media[3].Alt != "Latency chart" {
		t.Fatalf("expected cleaned data-src + alt on description img, got %+v", media[3])
	}
}

func TestParseWebSelectorsAndJSONMappings(t *testing.T) {
	data := []byte(`<article><a href="/news/1">Fallback</a><h2>Release</h2><time datetime="2026-10-04T08:00:00+08:00"></time><p>Short summary</p></article>`)
	cfg := settings{"itemSelector": "article", "titleSelector": "h2", "publishedAtSelector": "time"}
	entries, err := parseWeb(data, cfg, "https://source.example.invalid/list")
	if err != nil || len(entries) != 1 || entries[0].Title != "Release" || entries[0].Published == nil || entries[0].Published.UTC().Hour() != 0 {
		t.Fatalf("web entries = %+v: %v", entries, err)
	}
	cfg = settings{"itemsPath": "data.items", "titlePaths": []string{"headline"}, "summaryPaths": []string{"text"}, "urlTemplate": "https://source.example.invalid/news/{id}", "publishedAtPath": "timestamp", "publishedAtUnit": "epoch_ms", "requireBoolean": map[string]any{"path": "active", "equals": true}}
	entries, err = parseJSON([]byte(`{"data":{"items":[{"id":1,"headline":"Release","text":"JSON summary","timestamp":1791072000000,"active":true},{"id":2,"headline":"Skip","active":false}]}}`), cfg, "https://source.example.invalid")
	if err != nil || len(entries) != 1 || entries[0].URL != "https://source.example.invalid/news/1" || entries[0].Published == nil || entries[0].Excerpt != "JSON summary" {
		t.Fatalf("JSON entries = %+v: %v", entries, err)
	}
}

func TestFilteringCanonicalDedupAndCategoryRules(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(-2, 0, 0)
	candidates := []item{
		{URL: "https://www.source.example.invalid/news/1?utm_source=test", Title: "<b>Release</b>", Categories: []string{"AI"}},
		{URL: "https://source.example.invalid/news/1", Title: "duplicate", Categories: []string{"AI"}},
		{URL: "https://source.example.invalid/old", Title: "Archived", Published: &old, Categories: []string{"AI"}},
		{URL: "https://source.example.invalid/deny", Title: "Denied", Categories: []string{"Spam"}},
		{URL: "https://other.example.invalid/news", Title: "Other", Categories: []string{"AI"}},
	}
	got := filterItems(candidates, settings{"allowUrlPrefixes": []string{"https://source.example.invalid/"}, "allowCategories": []string{"AI"}, "denyCategories": []string{"Spam"}}, true, now)
	if len(got) != 1 || got[0].Title != "Release" || strings.Contains(got[0].URL, "utm_") {
		t.Fatalf("filtered = %+v", got)
	}
}

func TestPreviewUsesAdmissionRulesAndDoesNotNeedRepository(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.Add(-time.Hour).Format(time.RFC3339)
	published := now.Add(-30 * time.Minute).Format(time.RFC3339)
	feedURL := "https://feed.example.invalid/rss"
	body := fmt.Sprintf(`<rss version="2.0"><channel><title>Feed</title>
		<item><title>Kept report</title><link>https://site.example.invalid/articles/kept/1</link><description>%s</description><pubDate>%s</pubDate><category>AI</category></item>
		<item><title>Denied path</title><link>https://site.example.invalid/articles/private/2</link><pubDate>%s</pubDate><category>AI</category></item>
		<item><title>Sponsored report</title><link>https://site.example.invalid/articles/kept/3</link><pubDate>%s</pubDate><category>AI</category></item>
		<item><title>Outside date window</title><link>https://site.example.invalid/articles/kept/4</link><pubDate>%s</pubDate><category>AI</category></item>
	</channel></rss>`, strings.Repeat("brief ", 50), published, published, published, now.Add(-2*time.Hour).Format(time.RFC3339))
	client := fakeDoer(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != feedURL {
			t.Fatalf("preview requested %q", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	service := NewService(nil, client, nil)
	result, err := service.Preview(context.Background(), "draft", "rss", map[string]any{
		"feedUrl":              feedURL,
		"allowUrlPrefixes":     []any{"http://site.example.invalid/articles/"},
		"denyUrlPrefixes":      []any{"https://site.example.invalid/articles/private/"},
		"itemUrlPrefixRewrite": map[string]any{"from": "https://site.example.invalid/articles/kept/", "to": "https://example.org/accepted/"},
		"ingestNoiseFilter":    map[string]any{"dropMarkers": []any{"sponsored"}},
		"allowCategories":      []any{"AI"},
		"publishedAfter":       cutoff,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || len(result.Items) != 1 || result.Items[0].Title != "Kept report" || result.Items[0].URL != "https://example.org/accepted/1" {
		t.Fatalf("preview admission changed: %+v", result)
	}
	if len([]rune(result.Items[0].Excerpt)) != 200 || result.Items[0].PublishedAt == nil || result.Milliseconds < 0 {
		t.Fatalf("preview response fields changed: %+v", result.Items[0])
	}
}

/** TestIsolatedFirstImportAdmitsTenWithinWindowAndMarksOverflowBackfill verifies isolated first-import slicing. */
func TestIsolatedFirstImportAdmitsTenWithinWindowAndMarksOverflowBackfill(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	candidates := make([]item, 0, 36)
	for i := 0; i < 35; i++ {
		pub := now.Add(-time.Duration(i+1) * time.Hour)
		if i >= 20 {
			pub = now.Add(-time.Duration(50+i) * time.Hour)
		}
		candidates = append(candidates, item{
			URL:       fmt.Sprintf("https://source.example.invalid/post/%d", i),
			Title:     fmt.Sprintf("Post %d", i),
			Published: &pub,
		})
	}
	undated := item{URL: "https://source.example.invalid/post/undated", Title: "Undated"}
	candidates = append(candidates, undated)

	filtered := filterItemsWithMode(candidates, settings{}, "isolated", true, now)
	if len(filtered) != 36 {
		t.Fatalf("isolated first fetch kept %d items, want all 36 within overflow window", len(filtered))
	}

	isolatedLiveCount := 0
	live := 0
	backfill := 0
	for _, candidate := range filtered {
		if reason := firstImportBackfillReason(source{Mode: "isolated", First: true}, candidate.Published, now, &isolatedLiveCount); reason == "" {
			live++
		} else {
			backfill++
		}
	}
	if live != 10 || backfill != 26 {
		t.Fatalf("isolated live=%d backfill=%d, want 10/26", live, backfill)
	}

	editorialFiltered := filterItemsWithMode(candidates, settings{}, "editorial", true, now)
	if len(editorialFiltered) != 30 {
		t.Fatalf("editorial first fetch limit changed: got %d, want 30", len(editorialFiltered))
	}
	editorialLive := 0
	if reason := firstImportBackfillReason(source{Mode: "editorial", First: true}, candidates[0].Published, now, &editorialLive); reason != "first-import" {
		t.Fatalf("editorial first fetch reason = %q, want first-import", reason)
	}
}
