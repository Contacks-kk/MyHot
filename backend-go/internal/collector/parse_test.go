package collector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aihot/backend-go/internal/bodyhtml"
	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/mediaurl"
)

type jinaReaderFixture struct {
	page        JinaPage
	err         error
	targetURL   string
	sourceID    string
	purpose     string
	format      string
	tolerance   *int
	completedID int64
}

/** Read captures the requested Jina identity and returns the configured fixture page. */
func (f *jinaReaderFixture) Read(_ context.Context, targetURL, sourceID, purpose, format string, tolerance *int) (JinaPage, error) {
	f.targetURL, f.sourceID, f.purpose, f.format, f.tolerance = targetURL, sourceID, purpose, format, tolerance
	return f.page, f.err
}

/** Complete records the paid receipt completed by a successful source preview. */
func (f *jinaReaderFixture) Complete(_ context.Context, receiptID int64) error {
	f.completedID = receiptID
	return nil
}

func TestParseRSSPreservesContentDateAndCategories(t *testing.T) {
	xml := `<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>Source</title><link>https://source.example.invalid</link><description>Feed</description><item><title>OpenAI release</title><link>/article/1</link><description><![CDATA[<p>Summary</p>]]></description><content:encoded><![CDATA[<p>Full content</p>]]></content:encoded><pubDate>Sun, 04 Oct 2026 00:00:00 GMT</pubDate><category>AI</category></item></channel></rss>`
	entries, err := parseRSS([]byte(xml), "https://source.example.invalid/feed")
	if err != nil || len(entries) != 1 {
		t.Fatalf("RSS entries = %+v: %v", entries, err)
	}
	got := entries[0]
	if got.URL != "https://source.example.invalid/article/1" || got.Published == nil || got.Body != "" || got.BodyStatus != "pending" || len(got.Categories) != 1 {
		t.Fatalf("RSS content = %+v", got)
	}
	if len(got.Media) != 0 {
		t.Fatalf("expected nil Media when item has no images, got %s", got.Media)
	}
}

/** TestParseRSSBodyPolicyAndPodcastIdentity preserves feed body and episode URL semantics. */
func TestParseRSSBodyPolicyAndPodcastIdentity(t *testing.T) {
	feed := `<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>Show</title>
		<item><title>Episode 1</title><guid isPermaLink="false">Buzzsprout-1</guid><enclosure url="/episodes/1.mp3" type="audio/mpeg" length="1"/></item>
		<item><title>Episode 2</title><link>https://example.org/episodes/2</link><guid isPermaLink="false">Buzzsprout-2</guid><enclosure url="/episodes/2.mp3" type="audio/mpeg" length="1"/></item>
		<item><title>Episode 3</title><guid>https://example.org/episodes/3</guid><enclosure url="/episodes/3.mp3" type="audio/mpeg" length="1"/></item>
		<item><title>Episode 4</title><guid isPermaLink="false">Buzzsprout-4</guid></item>
		<item><title>Short body</title><link>https://example.org/short</link><description>How leaders build confidence with their team.</description></item>
		<item><title>Declared body</title><link>https://example.org/declared</link><description>How leaders build confidence with their team.</description></item>
		<item><title>Teaser</title><link>https://example.org/teaser</link><description>Summary.</description><content:encoded><![CDATA[<p>AMD announced today that it is acquiring World Labs in an all-stock deal. Read the full story at The Verge.</p>]]></content:encoded></item>
	</channel></rss>`
	base := "https://example.org/podcast.xml"
	items, _, err := parseRSSWithSettings([]byte(feed), base, settings{"summaryIsBody": true}, "editorial")
	if err != nil || len(items) != 7 {
		t.Fatalf("podcast/feed items = %#v, error=%v", items, err)
	}
	wantURLs := []string{"https://example.org/episodes/1.mp3", "https://example.org/episodes/2", "https://example.org/episodes/3", "Buzzsprout-4"}
	for index, want := range wantURLs {
		if items[index].URL != want {
			t.Errorf("episode %d URL = %q, want %q", index+1, items[index].URL, want)
		}
	}
	if items[0].BodyStatus != "none" || items[0].Body != "" || items[0].OpaqueGUID != "Buzzsprout-1" || items[1].OpaqueGUID != "" || items[2].OpaqueGUID != "" || items[3].OpaqueGUID != "Buzzsprout-4" {
		t.Fatalf("episode page/media identity = %#v", items[:4])
	}
	if items[4].BodyStatus != "ok" || plain(items[4].Body) != "How leaders build confidence with their team." || items[4].Excerpt != "How leaders build confidence with their team." {
		t.Fatalf("summary body policy = %#v", items[4])
	}
	items, _, err = parseRSSWithSettings([]byte(feed), base, settings{}, "editorial")
	if err != nil || items[4].BodyStatus != "pending" || items[4].Body != "" {
		t.Fatalf("short summary without body declaration = %#v, error=%v", items[4], err)
	}
	if items[6].BodyStatus != "pending" || items[6].Body != "" || items[6].Excerpt != "Summary." {
		t.Fatalf("teaser body classification = %#v", items[6])
	}
	annotateOpaqueRSSIdentities(items, "podcast")
	expectedHash := sha256.Sum256([]byte("Buzzsprout-1\x01Episode 1"))
	if items[0].IdentityKey != fmt.Sprintf("src:podcast:%x", expectedHash[:16]) || items[3].IdentityKey == "" {
		t.Fatalf("opaque GUID identities = %q / %q", items[0].IdentityKey, items[3].IdentityKey)
	}
	filtered := filterItemsWithMode(items, settings{}, "editorial", false, time.Now())
	if len(filtered) != 7 {
		t.Fatalf("filter dropped podcast GUID items: kept %d of %d", len(filtered), len(items))
	}

	videoFeed := `<feed xmlns="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/">
		<entry><id>video:1</id><title>YouTube update</title><link href="https://www.youtube.com/watch?v=abc123"/><summary>` + strings.Repeat("A supplied video summary. ", 16) + `</summary><media:group><media:content medium="video"/><media:description>Use &lt;video&gt; and a real app. This is a video description, not a transcript.</media:description></media:group></entry>
		<entry><id>video:2</id><title>Short video</title><link href="https://www.youtube.com/shorts/abc123"/><media:description type="html">&lt;p&gt;A &lt;b&gt;short video&lt;/b&gt; description.&lt;/p&gt;</media:description></entry>
		<entry><id>video:3</id><title>Video with feed text</title><link href="https://vimeo.com/1105244173"/><content type="html"><![CDATA[<p>` + strings.Repeat("The publisher supplied actual explanatory text alongside this video. ", 5) + `</p>]]></content><summary>` + strings.Repeat("A summary. ", 32) + `</summary></entry>
		<entry><id>article:1</id><title>Article with a clip</title><link href="https://publisher.example/article"/><media:group><media:content medium="video"/><media:description>An accompanying clip.</media:description></media:group></entry>
	</feed>`
	videoItems, _, err := parseRSSWithSettings([]byte(videoFeed), "https://publisher.example/feed", settings{"summaryIsBody": true}, "editorial")
	if err != nil || len(videoItems) != 4 {
		t.Fatalf("video feed items = %#v, error=%v", videoItems, err)
	}
	if videoItems[0].Excerpt != "Use <video> and a real app. This is a video description, not a transcript." || videoItems[0].BodyStatus != "none" || videoItems[0].Body != "" {
		t.Errorf("YouTube summary was treated as transcript or media description was lost: %#v", videoItems[0])
	}
	if videoItems[1].Excerpt != "A short video description." || videoItems[1].BodyStatus != "none" || videoItems[1].Body != "" {
		t.Errorf("video HTML description = %#v", videoItems[1])
	}
	if videoItems[2].BodyStatus != "ok" || len(videoItems[2].Body) == 0 {
		t.Errorf("actual video feed content was discarded: %#v", videoItems[2])
	}
	if videoItems[3].BodyStatus != "pending" || videoItems[3].Excerpt != "" {
		t.Errorf("ordinary article with an accompanying clip = %#v", videoItems[3])
	}
}

/** TestParseAtomResolvesXMLBasesAndKeepsLinkIdentity preserves Atom URL resolution rules. */
func TestParseAtomResolvesXMLBasesAndKeepsLinkIdentity(t *testing.T) {
	base := "https://origin.example/news/feed.xml"
	feed := `<feed xmlns="http://www.w3.org/2005/Atom" xml:base="../publisher/">
		<entry xml:base="stories/"><id>feed-base</id><title>Feed base</title><link href="article-1"/></entry>
		<entry xml:base="https://publisher.example/stories/"><id>entry-base</id><title>Entry base</title><link href="article-2"/></entry>
		<entry><id>link-base</id><title>Link base</title><link xml:base="https://publisher.example/articles/" href="article-3"/></entry>
		<entry xml:base="stories/"><id>relative-base</id><title>Relative bases</title><link xml:base="../articles/" href="article-4"/></entry>
		<entry xml:base="https://publisher.example/"><id>alternate</id><title>Alternate</title><link rel="self" href="entry.xml"/><link rel="alternate" href="news#first"/></entry>
		<entry><id>fallback</id><title>Fallback</title><link rel="related" href="article-5"/></entry>
		<entry><id>fragments-a</id><title>First fragment</title><link href="notes#first"/></entry>
		<entry><id>fragments-b</id><title>Second fragment</title><link href="notes#second"/></entry>
		<entry><id>missing</id><title>Missing link</title></entry>
	</feed>`
	entries, err := parseRSS([]byte(feed), base)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://origin.example/publisher/stories/article-1",
		"https://publisher.example/stories/article-2",
		"https://publisher.example/articles/article-3",
		"https://origin.example/publisher/articles/article-4",
		"https://publisher.example/news#first",
		"https://origin.example/publisher/article-5",
		"https://origin.example/publisher/notes#first",
		"https://origin.example/publisher/notes#second",
	}
	if len(entries) != len(want) {
		t.Fatalf("Atom entries = %#v, want %d links", entries, len(want))
	}
	for index, expected := range want {
		if entries[index].URL != expected {
			t.Errorf("entry %d URL = %q, want %q", index, entries[index].URL, expected)
		}
	}
}

/** TestParseAtomXHTMLPreservesMixedContentAndSanitizesUnsafeMarkup keeps the Atom body contract. */
func TestParseAtomXHTMLPreservesMixedContentAndSanitizesUnsafeMarkup(t *testing.T) {
	feed := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>xhtml</id>
		<title type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml">New <em>research</em> result</div></title>
		<summary type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml">First <b>important</b> result, then another.</div></summary>
		<link href="https://publisher.example/article"/>
		<content type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml"><p>Before <strong>bold</strong>, between <em>italic</em>, after.</p><p>Safe article text that preserves all original publication details without truncation. Safe article text that preserves all original publication details without truncation. Safe article text that preserves all original publication details without truncation. Safe article text that preserves all original publication details without truncation.</p><script>alert(1)</script><p>&lt;img src=x onerror=alert(1)&gt;</p></div></content>
	</entry></feed>`
	entries, err := parseRSS([]byte(feed), "https://publisher.example/feed.xml")
	if err != nil || len(entries) != 1 {
		t.Fatalf("Atom XHTML entries = %#v, error=%v", entries, err)
	}
	entry := entries[0]
	if plain(entry.Title) != "New research result" || plain(entry.Excerpt) != "First important result, then another." {
		t.Fatalf("Atom XHTML title/summary = %q / %q", entry.Title, entry.Excerpt)
	}
	if !strings.Contains(entry.Body, `Before <strong>bold</strong>, between <em>italic</em>, after.`) {
		t.Fatalf("Atom XHTML mixed content order was lost: %q", entry.Body)
	}
	safe, _, err := bodyhtml.Sanitize(entry.Body, entry.URL, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(safe, "<script") || strings.Contains(safe, "<img") || !strings.Contains(safe, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatalf("Atom XHTML sanitizer output = %q", safe)
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
	visibleDate := []byte(`<article><a href="/news/date">Release</a><span title="Unix: 1789171113866">Sep 11, 2026</span></article>`)
	entries, err = parseWeb(visibleDate, settings{"itemSelector": "article", "publishedAtSelector": `span[title^='Unix:']`, "publishedAtUtcOffset": "+00:00"}, "https://source.example.invalid/list")
	if err != nil || len(entries) != 1 || entries[0].Published == nil || entries[0].Published.Format(time.RFC3339) != "2026-09-11T00:00:00Z" {
		t.Fatalf("visible listing date = %#v, error=%v", entries, err)
	}
	queryListing := settings{"url": "https://example.org/news?lang=en&kind=ai", "parseMode": "html"}
	queryHTML := []byte(`<a href="/news?kind=ai&lang=en&utm_medium=x&page=2">Next page of news</a><a href="/news?kind=ai&lang=en&id=7">A news post</a><a href="https://www.example.org/">Home on www</a>`)
	entries, err = parseWeb(queryHTML, queryListing, "https://example.org/")
	if err != nil || len(entries) != 2 || entries[0].URL != "https://example.org/news?kind=ai&lang=en&id=7" || entries[1].URL != "https://www.example.org/" {
		t.Fatalf("HTML listing navigation/query posts = %#v, error=%v", entries, err)
	}
	markdown := []byte("### [A detailed report](https://example.org/2026/09/report)\n\n" +
		"[![Image 3: Card](https://example.org/card.png)](https://example.org/2026/09/card)\n\n" +
		"[Card: A separate announcement](https://example.org/2026/09/card)\n\n" +
		"The teaser links to a [related older article](https://example.org/2026/09/related).\n\n" +
		"[Next page of news](https://example.org/news?kind=ai&lang=en&paged=2)\n\n" +
		"[First research announcement](https://example.org/?p=123)\n\n" +
		"[Second research announcement](https://example.org/?p=456&lang=en)\n\n" +
		"[About this site](https://example.org/news?kind=ai&lang=en#about)")
	entries, err = parseWeb(markdown, settings{"url": "https://r.jina.ai/https://example.org/news?lang=en&kind=ai", "parseMode": "markdown", "linksStartLine": true}, "https://example.org/news?lang=en&kind=ai")
	if err != nil || len(entries) != 4 || entries[0].Title != "A detailed report" || entries[1].Title != "Card: A separate announcement" || entries[2].URL != "https://example.org/?p=123" || entries[3].URL != "https://example.org/?p=456&lang=en" {
		t.Fatalf("Markdown headline/navigation handling = %#v, error=%v", entries, err)
	}
	finalURL, _ := url.Parse("https://example.org/landing/")
	redirectClient := fakeDoer(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`<a href="article">Redirected article</a>`)),
			Request:    &http.Request{URL: finalURL},
		}, nil
	})
	redirected, receipts, err := NewService(nil, redirectClient, nil).fetchWithReceipts(context.Background(), source{
		ID:     "redirected-listing",
		Kind:   "web_list",
		Config: settings{"url": "https://example.org/start", "parseMode": "html"},
	})
	if err != nil || len(redirected) != 1 || redirected[0].URL != "https://example.org/landing/article" || len(receipts) != 0 {
		t.Fatalf("redirected web-list base = %#v, receipts=%v, error=%v", redirected, receipts, err)
	}
	chunkURL := "https://mimo.example/static/js/async/home.hash.js"
	scripts := map[string][]byte{
		"https://mimo.example/entry.js": []byte(`var route={path:"/",load:e.e("home")};__webpack_require__.p="/";__webpack_require__.u=e=>"static/js/async/"+e+"."+({home:"hash"})[e]+".js";`),
		chunkURL:                        []byte(`sectionTitle:"Blog",blogs:[{title:"MiMo V2.6 launch",link:"/blog/mimo-v2-6",desc:"A product update."},{title:"MiMo research",link:"/blog/research",desc:"A paper update."}],sectionTitle:"Products"`),
	}
	mimoItems, err := parseMimoHome([]byte(`<script src="/entry.js"></script>`), "https://mimo.example/", settings{"url": "https://mimo.example/", "adapter": "mimo_home"}, func(address string) ([]byte, error) {
		if script, ok := scripts[address]; ok {
			return script, nil
		}
		return nil, fmt.Errorf("unexpected script URL %s", address)
	})
	if err != nil || len(mimoItems) != 2 || mimoItems[0].URL != "https://mimo.example/blog/mimo-v2-6" || mimoItems[0].Excerpt != "A product update." {
		t.Fatalf("MiMo homepage adapter = %#v, error=%v", mimoItems, err)
	}
	responses := map[string]string{
		"https://mimo.example/":         `<script src="/entry.js"></script>`,
		"https://mimo.example/entry.js": string(scripts["https://mimo.example/entry.js"]),
		chunkURL:                        string(scripts[chunkURL]),
	}
	client := fakeDoer(func(request *http.Request) (*http.Response, error) {
		response, ok := responses[request.URL.String()]
		if !ok {
			return nil, fmt.Errorf("unexpected request URL %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
	})
	service := NewService(nil, client, nil)
	fetched, receipts, err := service.fetchWithReceipts(context.Background(), source{
		ID:     "mimo-home",
		Kind:   "web_list",
		Config: settings{"url": "https://mimo.example/", "adapter": "mimo_home"},
	})
	if err != nil || len(fetched) != 2 || len(receipts) != 0 || fetched[1].Title != "MiMo research" {
		t.Fatalf("MiMo source dispatch = %#v, receipts=%v, error=%v", fetched, receipts, err)
	}

	fragmentHTML := []byte(`<article><a href="https://example.org/notes#first">First section</a></article><article><a href="https://example.org/notes#second">Second section</a></article>`)
	for _, mode := range []string{"html", "markdown"} {
		data := fragmentHTML
		if mode == "markdown" {
			data = []byte("- [First section](https://example.org/notes#first)\n- [Second section](https://example.org/notes#second)")
		}
		config := settings{"parseMode": mode, "itemSelector": "article", "preserveUrlFragment": true}
		if mode == "markdown" {
			config["linksStartLine"] = true
		}
		parsed, parseErr := parseWeb(data, config, "https://example.org/list")
		if parseErr != nil {
			t.Fatalf("%s fragment listing parse error = %v", mode, parseErr)
		}
		filtered := filterItems(parsed, config, true, time.Now().UTC())
		if len(filtered) != 2 || filtered[0].URL != "https://example.org/notes#first" || filtered[1].URL != "https://example.org/notes#second" {
			t.Fatalf("%s section identities = %#v", mode, filtered)
		}
	}

	var longListing strings.Builder
	for index := 0; index < 100; index++ {
		fmt.Fprintf(&longListing, `<article><a href="https://example.org/items/%d">Article %03d</a></article>`, index, index)
	}
	longItems, err := parseWeb([]byte(longListing.String()), settings{"itemSelector": "article"}, "https://example.org/list")
	if err != nil {
		t.Fatalf("long web list parse error = %v", err)
	}
	longItems = filterItems(longItems, settings{}, false, time.Now().UTC())
	if len(longItems) != 100 {
		t.Fatalf("long web list retained %d entries, want 100", len(longItems))
	}
	if longItems[0].Title != "Article 000" || longItems[99].Title != "Article 099" {
		t.Fatalf("long web list changed order: first=%#v last=%#v", longItems[0], longItems[99])
	}
}

/** TestParseDateUsesStrictCalendarDaysAndTheConfiguredSourceOffset preserves source date semantics. */
func TestParseDateUsesStrictCalendarDaysAndTheConfiguredSourceOffset(t *testing.T) {
	for _, raw := range []string{"Version 3", "Sonnet 5", "5", "September 2026", "2026-02-30", "2026/02/30 12:00", "2026年4月31日", "February 30, 2026"} {
		if parsed := parseDate(raw, settings{"publishedAtUtcOffset": "+00:00"}); parsed != nil {
			t.Errorf("parseDate(%q) = %s, want nil", raw, parsed.Format(time.RFC3339))
		}
	}
	for _, test := range []struct {
		value  string
		offset string
		want   string
	}{
		{value: "February 29, 2024", offset: "+00:00", want: "2024-02-29T00:00:00Z"},
		{value: "2026-09-26 10:00", offset: "+08:00", want: "2026-09-26T02:00:00Z"},
		{value: "2026-09-26T10:00:00", offset: "+08:00", want: "2026-09-26T02:00:00Z"},
		{value: "2026/09/26 10:00", offset: "+08:00", want: "2026-09-26T02:00:00Z"},
		{value: "2026年9月26日", offset: "+08:00", want: "2026-09-25T16:00:00Z"},
		{value: "Sep 26, 2026", offset: "-07:00", want: "2026-09-26T07:00:00Z"},
		{value: "2026-09-26", offset: "+08:00", want: "2026-09-26T00:00:00Z"},
		{value: "2026-09-26T10:00:00+09:00", offset: "+00:00", want: "2026-09-26T01:00:00Z"},
		{value: "Sat, 26 Sep 2026 10:00:00 +0200", offset: "-07:00", want: "2026-09-26T08:00:00Z"},
		{value: "Wed, 30 Sep 2026 17:43:58 EST", offset: "+08:00", want: "2026-09-30T22:43:58Z"},
	} {
		parsed := parseDate(test.value, settings{"publishedAtUtcOffset": test.offset})
		if parsed == nil || parsed.Format(time.RFC3339) != test.want {
			t.Errorf("parseDate(%q, %q) = %v, want %s", test.value, test.offset, parsed, test.want)
		}
	}
	if parsed := parseDate("2026-09-26 10:00", settings{"publishedAtUtcOffset": "invalid"}); parsed != nil {
		t.Errorf("invalid source offset unexpectedly produced %s", parsed)
	}
}

/** TestUnsupportedSourceConfigMatchesAdapterKeysAndDateBoundaryValidation rejects ignored inputs. */
func TestUnsupportedSourceConfigMatchesAdapterKeysAndDateBoundaryValidation(t *testing.T) {
	for _, kind := range []string{"rss", "web_list", "json_list"} {
		for _, publishedAfter := range []string{"2026-09-28T00:00:00.000Z", "2026-09-28T00:00:00Z", "2024-02-29T23:59:59.999Z"} {
			if got := unsupportedSourceConfig(kind, settings{"publishedAfter": publishedAfter}); len(got) != 0 {
				t.Errorf("%s accepted UTC boundary %q as unsupported: %v", kind, publishedAfter, got)
			}
		}
	}
	for _, value := range []any{"2026-09-28", "2026-09-28T00:00:00", "September 28, 2026", "2026-02-30T00:00:00Z", "2026-09-28T24:00:00Z", "2026-13-01T00:00:00Z", "", nil, int64(1790553600000)} {
		if got := unsupportedSourceConfig("web_list", settings{"publishedAfter": value}); !slices.Equal(got, []string{"publishedAfter"}) {
			t.Errorf("invalid publication boundary %v reported %v", value, got)
		}
	}
	for _, kind := range []string{"x_search", "mp_account", "external"} {
		if got := unsupportedSourceConfig(kind, settings{"publishedAfter": "2026-09-28T00:00:00Z"}); !slices.Equal(got, []string{"publishedAfter"}) {
			t.Errorf("%s accepted an inapplicable publication boundary: %v", kind, got)
		}
	}
	if got := unsupportedSourceConfig("web_list", settings{"parseMode": "intercom_changelog", "detail": map[string]any{"publishedAtSelector": "time", "titleFoo": "h1"}}); !slices.Equal(got, []string{"detail.titleFoo"}) {
		t.Errorf("unsupported nested configuration = %v", got)
	}
	if got := unsupportedSourceConfig("web_list", settings{"unknownConfig": true}); !slices.Equal(got, []string{"unknownConfig"}) {
		t.Errorf("unsupported top-level configuration = %v", got)
	}
}

/** TestPreviewRejectsUnsupportedConfigBeforeFetching prevents invalid source rules from being ignored. */
func TestPreviewRejectsUnsupportedConfigBeforeFetching(t *testing.T) {
	called := false
	service := NewService(nil, fakeDoer(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	}), nil)
	_, err := service.Preview(context.Background(), "draft", "web_list", map[string]any{
		"url":            "https://source.example.invalid/list",
		"publishedAfter": "2026-09-28",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported config: publishedAfter") || called {
		t.Fatalf("invalid config result err=%v, fetched=%t", err, called)
	}
}

/** TestParseJSONKeepsItemsWithInvalidEpochClaims ensures bad dates do not drop valid articles. */
func TestParseJSONKeepsItemsWithInvalidEpochClaims(t *testing.T) {
	for _, test := range []struct {
		unit string
		data string
		want string
	}{
		{unit: "epoch_s", data: `{"items":[{"id":"valid","published":1791072000},{"id":"unknown","published":"unknown"},{"id":"object","published":{"toString":null}},{"id":"array","published":[{"toString":null}]},{"id":"out-of-range","published":9000000000000}]}`, want: "2026-10-04T00:00:00Z"},
		{unit: "epoch_ms", data: `{"items":[{"id":"valid","published":1791072000000},{"id":"unknown","published":"unknown"},{"id":"object","published":{"toString":null}},{"id":"array","published":[{"toString":null}]},{"id":"out-of-range","published":9000000000000001}]}`, want: "2026-10-04T00:00:00Z"},
	} {
		items, err := parseJSON([]byte(test.data), settings{
			"itemsPath":       "items",
			"titlePaths":      []string{"id"},
			"urlTemplate":     "https://source.example.invalid/{id}",
			"publishedAtPath": "published",
			"publishedAtUnit": test.unit,
		}, "https://source.example.invalid/list")
		if err != nil || len(items) != 5 {
			t.Fatalf("%s entries = %#v, error=%v", test.unit, items, err)
		}
		if items[0].Published == nil || items[0].Published.Format(time.RFC3339) != test.want {
			t.Fatalf("%s valid timestamp = %v", test.unit, items[0].Published)
		}
		for _, item := range items[1:] {
			if item.Published != nil {
				t.Errorf("%s invalid epoch claim for %q = %v", test.unit, item.Title, item.Published)
			}
		}
	}
	days, err := parseJSON([]byte(`{"data":{"list":[{"id":"valid","day":"20260922"},{"id":"impossible","day":"20260230"}]}}`), settings{
		"itemsPath":       "data.list",
		"titlePaths":      []string{"id"},
		"urlTemplate":     "https://source.example.invalid/blog/{id}",
		"publishedAtPath": "day",
		"publishedAtUnit": "yyyymmdd",
	}, "https://source.example.invalid/list")
	if err != nil || len(days) != 2 || days[0].Published == nil || days[0].Published.Format(time.RFC3339) != "2026-09-22T00:00:00Z" || days[1].Published != nil {
		t.Fatalf("yyyymmdd publication dates = %#v, error=%v", days, err)
	}
}

/** TestParseIntercomChangelogRetainsDatedSectionIdentityBodyAndSafeLinks preserves help-center updates. */
func TestParseIntercomChangelogRetainsDatedSectionIdentityBodyAndSafeLinks(t *testing.T) {
	base := "https://help.example.invalid/help/notes"
	html := `<html><body><article>
		<div class="intercom-interblocks-subheading"><h2 id="h_month">September 2026</h2></div>
		<div class="intercom-interblocks-subheading3"><h3 id="h_newest">September 28, 2026</h3></div>
		<div class="intercom-interblocks-paragraph"><p><b>A model launch</b></p></div>
		<div class="intercom-interblocks-paragraph"><p>A new model is available. <a href="/docs/new">Read about it</a>.</p></div>
		<div class="intercom-interblocks-paragraph"><script>alert(1)</script><p>Safe article text.</p></div>
		<div class="intercom-interblocks-subheading3"><h3 id="h_empty">September 20, 2026</h3></div>
		<div><p> </p></div>
		<div class="intercom-interblocks-subheading"><h2 id="h_old_month">August 2026</h2></div>
		<div class="intercom-interblocks-subheading3"><h3 id="h_older">September 25, 2026</h3></div>
		<div class="intercom-interblocks-paragraph"><p><strong>Build plugins</strong></p></div>
		<div class="intercom-interblocks-paragraph"><p>Create a plugin and track its review.</p></div>
		<footer>Help center footer</footer></article></body></html>`
	items, err := parseWeb([]byte(html), settings{"parseMode": "intercom_changelog"}, base)
	if err != nil || len(items) != 2 {
		t.Fatalf("Intercom items = %#v, error=%v", items, err)
	}
	if items[0].URL != base+"#h_newest" || items[0].IdentityKey != "url:"+base+"#h_newest" || items[0].Title != "A model launch" || items[0].Published == nil || items[0].Published.Format(time.RFC3339) != "2026-09-28T00:00:00Z" || items[0].BodyStatus != "ok" {
		t.Fatalf("new Intercom section = %#v", items[0])
	}
	if !strings.Contains(items[0].BodyHTML, `href="https://help.example.invalid/docs/new"`) || strings.Contains(items[0].BodyHTML, "<script") || strings.Contains(items[0].Body, "Build plugins") || strings.Contains(items[0].Body, "Help center footer") {
		t.Fatalf("Intercom body = %#v", items[0])
	}
	if items[1].URL != base+"#h_older" || items[1].IdentityKey == items[0].IdentityKey || items[1].Title != "Build plugins" {
		t.Fatalf("older Intercom section identity/title = %#v", items[1])
	}
	withNewSection := strings.Replace(html,
		`<div class="intercom-interblocks-subheading3"><h3 id="h_newest">September 28, 2026</h3></div>`,
		`<div class="intercom-interblocks-subheading3"><h3 id="h_future">October 1, 2026</h3></div><div><p>A separate update.</p></div><div class="intercom-interblocks-subheading3"><h3 id="h_newest">September 28, 2026</h3></div>`, 1)
	updated, err := parseWeb([]byte(withNewSection), settings{"parseMode": "intercom_changelog"}, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range items {
		var current *item
		for index := range updated {
			if updated[index].IdentityKey == old.IdentityKey {
				current = &updated[index]
				break
			}
		}
		if current == nil || materials.ContentHash(current.Title, current.Body, current.Excerpt) != materials.ContentHash(old.Title, old.Body, old.Excerpt) {
			t.Errorf("inserting a section changed old identity or content hash for %q", old.URL)
		}
	}
}

/** TestParseDocusaurusChangelogDatesUpdatesFromHeadings verifies a heading's date scopes child entries. */
func TestParseDocusaurusChangelogDatesUpdatesFromHeadings(t *testing.T) {
	base := "https://docs.example.invalid/changelog"
	html := `<article><h2 id="date">时间：2026-09-10</h2><h3 id="first">New feature</h3><p>First release.</p><h3 id="second">Release from September 11, 2026</h3><p>Second release.</p></article>`
	items, err := parseWeb([]byte(html), settings{"parseMode": "docusaurus_changelog", "publishedAtUtcOffset": "+08:00"}, base)
	if err != nil || len(items) != 2 {
		t.Fatalf("Docusaurus items = %#v, error=%v", items, err)
	}
	if items[0].URL != base+"#first" || items[0].Published == nil || items[0].Published.Format(time.RFC3339) != "2026-09-09T16:00:00Z" || items[0].Body != "First release." {
		t.Fatalf("first Docusaurus section = %#v", items[0])
	}
	if items[1].Published == nil || items[1].Published.Format(time.RFC3339) != "2026-09-10T16:00:00Z" {
		t.Fatalf("dated Docusaurus update = %#v", items[1])
	}
}

/** TestJinaWebListUsesRenderedHTMLAndTargetBase migrates paid listing format and relative URL behavior. */
func TestJinaWebListUsesRenderedHTMLAndTargetBase(t *testing.T) {
	tolerance := 45
	reader := &jinaReaderFixture{page: JinaPage{
		Content:   `<html><body><article><a href="/news/rendered"><h2>Rendered report</h2><time datetime="2026-09-23T00:00:00Z"></time></a></article></body></html>`,
		ReceiptID: 17,
	}}
	service := &Service{Jina: reader}
	items, receiptIDs, err := service.fetchWithReceipts(context.Background(), source{
		ID:   "jina-source",
		Kind: "web_list",
		Config: settings{
			"url":                   "https://r.jina.ai/https://example.org/news/",
			"parseMode":             "html",
			"cacheToleranceSeconds": tolerance,
			"itemSelector":          "article",
			"titleSelector":         "h2",
			"publishedAtSelector":   "time",
			"allowUrlPrefixes":      []any{"https://example.org/news/"},
		},
	})
	if err != nil || len(items) != 1 || items[0].Title != "Rendered report" || items[0].URL != "https://example.org/news/rendered" || items[0].Published == nil {
		t.Fatalf("Jina rendered listing = %#v, receipts=%v, err=%v", items, receiptIDs, err)
	}
	if reader.targetURL != "https://example.org/news/" || reader.sourceID != "jina-source" || reader.purpose != "source_listing" || reader.format != "html" || reader.tolerance == nil || *reader.tolerance != tolerance || len(receiptIDs) != 1 || receiptIDs[0] != 17 {
		t.Fatalf("Jina listing request = %#v, receipts=%v", reader, receiptIDs)
	}
}

/** TestJinaWebListDefaultsToMarkdown preserves the default parser for paid Jina listings. */
func TestJinaWebListDefaultsToMarkdown(t *testing.T) {
	reader := &jinaReaderFixture{page: JinaPage{
		Content:   "# News\n\n[New research report](https://example.org/news/report-1)\n",
		ReceiptID: 18,
	}}
	service := &Service{Jina: reader}
	items, receiptIDs, err := service.fetchWithReceipts(context.Background(), source{
		ID:   "jina-markdown-source",
		Kind: "web_list",
		Config: settings{
			"url":              "https://r.jina.ai/https://example.org/news/",
			"allowUrlPrefixes": []any{"https://example.org/news/"},
		},
	})
	if err != nil || len(items) != 1 || items[0].Title != "New research report" || len(receiptIDs) != 1 || receiptIDs[0] != 18 || reader.format != "markdown" {
		t.Fatalf("Jina markdown listing = %#v, receipts=%v, reader=%#v, err=%v", items, receiptIDs, reader, err)
	}
}

/** TestJinaDetailRegexReadsProviderText migrates title and publication date recovery from Jina output. */
func TestJinaDetailRegexReadsProviderText(t *testing.T) {
	page := JinaPage{
		Raw:     "Title: sample\nURL Source: https://example.org/article\nPublished Time: 2026-11-16T10:00:00Z\n\nMarkdown Content:\n# Recovered headline\n",
		Content: "# Recovered headline\n",
	}
	candidate := item{Title: "Read the Blog"}
	enrichJinaDetail(&candidate, page, settings{
		"titleRegex":       `(?m)^#\s+(.+)$`,
		"publishedAtRegex": `Published Time:\s*(\S+)`,
	}, true, true)
	if candidate.Title != "Recovered headline" || candidate.Published == nil || candidate.Published.Format(time.RFC3339) != "2026-11-16T10:00:00Z" {
		t.Fatalf("Jina detail enrichment = %#v", candidate)
	}
}

/** TestJinaDetailUsesPaidReadAndPreservesReceipt verifies regex detail work stays on the Jina path. */
func TestJinaDetailUsesPaidReadAndPreservesReceipt(t *testing.T) {
	reader := &jinaReaderFixture{page: JinaPage{
		Raw:       "Published Time: 2026-11-16T10:00:00Z\n\nMarkdown Content:\n# Recovered headline",
		Content:   "# Recovered headline",
		ReceiptID: 23,
	}}
	service := &Service{Jina: reader, Client: fakeDoer(func(*http.Request) (*http.Response, error) {
		t.Fatal("regex-only Jina detail unexpectedly fetched the publisher page")
		return nil, nil
	})}
	candidate := item{URL: "https://example.org/article", Title: "Read the Blog"}
	detail := settings{
		"titleRegex":       `(?m)^#\s+(.+)$`,
		"publishedAtRegex": `Published Time:\s*(\S+)`,
	}
	err, receiptIDs := service.enrichCandidateDetail(context.Background(), source{
		ID:     "jina-source",
		Config: settings{"url": "https://r.jina.ai/https://example.org/news/"},
	}, &candidate, detail, detailReadNeed{title: true, date: true})
	if err != nil || candidate.Title != "Recovered headline" || candidate.Published == nil || len(receiptIDs) != 1 || receiptIDs[0] != 23 {
		t.Fatalf("Jina detail = %#v, receipts=%v, err=%v", candidate, receiptIDs, err)
	}
	if reader.targetURL != "https://example.org/news/" || reader.purpose != "source_detail" {
		t.Fatalf("Jina detail read = %#v", reader)
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

	cutoff := now.Add(-24 * time.Hour)
	before := cutoff.Add(-time.Second)
	future := now.Add(24 * time.Hour)
	current := now.Add(-time.Minute)
	window := []item{
		{URL: "https://source.example.invalid/before", Title: "Before", Published: &before},
		{URL: "https://source.example.invalid/boundary", Title: "Boundary", Published: &cutoff},
		{URL: "https://source.example.invalid/undated", Title: "Undated"},
		{URL: "https://source.example.invalid/future", Title: "Future", Published: &future},
		{URL: "https://source.example.invalid/current", Title: "Current", Published: &current},
	}
	for _, first := range []bool{false, true} {
		kept := filterItemsWithMode(window, settings{"publishedAfter": cutoff.Format(time.RFC3339)}, "editorial", first, now)
		if len(kept) != 1 || kept[0].Title != "Current" {
			t.Fatalf("fixed publication window (first=%t) retained boundary, missing, or future dates: %+v", first, kept)
		}
	}
}

func TestPreviewUsesAdmissionRulesAndDoesNotNeedRepository(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cutoff := now.Add(-time.Hour).Format(time.RFC3339)
	cutoffTime := now.Add(-time.Hour)
	published := now.Add(-30 * time.Minute).Format(time.RFC3339)
	boundary := cutoffTime
	future := now.Add(24 * time.Hour)
	current := now.Add(-30 * time.Minute)
	window := previewAdmission([]item{
		{URL: "https://example.org/boundary", Title: "Boundary", Published: &boundary},
		{URL: "https://example.org/undated", Title: "Undated"},
		{URL: "https://example.org/future", Title: "Future", Published: &future},
		{URL: "https://example.org/current", Title: "Current", Published: &current},
	}, settings{"publishedAfter": cutoff}, now)
	if len(window) != 1 || window[0].Title != "Current" {
		t.Fatalf("preview fixed publication window retained boundary, missing, or future dates: %+v", window)
	}
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

	archive := make([]item, 0, 11)
	for i := 0; i < 9; i++ {
		published := now.Add(-time.Duration(i) * time.Hour)
		archive = append(archive, item{URL: fmt.Sprintf("https://source.example.invalid/recent/%d", i), Title: "Recent", Published: &published})
	}
	tooOld := now.Add(-362 * 24 * time.Hour)
	archive = append(archive, item{URL: "https://source.example.invalid/old", Title: "Old", Published: &tooOld})
	configured := filterItemsWithMode(archive, settings{"_aihot": map[string]any{"initialBackfillMonths": 12, "initialBackfillLimit": 7}}, "editorial", true, now)
	if len(configured) != 7 {
		t.Fatalf("configured first import kept %d items, want its seven-item cap", len(configured))
	}
	for _, candidate := range configured {
		if candidate.URL == "https://source.example.invalid/old" {
			t.Fatal("first import admitted an article older than the configured 12-month window")
		}
	}
}

/** TestRegularSourceFilteringRetainsLongListingTails prevents regular fetches from dropping accepted entries. */
func TestRegularSourceFilteringRetainsLongListingTails(t *testing.T) {
	items := make([]item, 65_536)
	for index := range items {
		items[index] = item{URL: fmt.Sprintf("https://example.org/article/%d", index), Title: fmt.Sprintf("Article %d", index)}
	}
	filtered := filterItemsWithMode(items, settings{}, "editorial", false, time.Now())
	if len(filtered) != len(items) {
		t.Fatalf("regular listing retained %d of %d entries", len(filtered), len(items))
	}
	if filtered[0].Title != "Article 0" || filtered[len(filtered)-1].Title != "Article 65535" {
		t.Fatalf("regular listing changed order or lost its tail: first=%q last=%q", filtered[0].Title, filtered[len(filtered)-1].Title)
	}
}

/** TestLaterCollectionRejectsUnseenArchiveButKeepsKnownCorrections checks source-window admission. */
func TestLaterCollectionRejectsUnseenArchiveButKeepsKnownCorrections(t *testing.T) {
	initializedAt := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	itemAt := func(url string, published *time.Time) item {
		return item{URL: url, Title: url, Published: published}
	}
	date := func(delta time.Duration) *time.Time {
		value := initializedAt.Add(delta)
		return &value
	}
	items := []item{
		itemAt("https://source.example.invalid/unseen-old", date(-72*time.Hour)),
		itemAt("https://source.example.invalid/known-old", date(-72*time.Hour)),
		itemAt("https://source.example.invalid/cutoff", date(-48*time.Hour)),
		itemAt("https://source.example.invalid/recent", date(-24*time.Hour)),
		itemAt("https://source.example.invalid/undated", nil),
	}

	got := admitExistingOrRecent(items, initializedAt, map[string]bool{
		"url:https://source.example.invalid/known-old": true,
	})
	if len(got) != 4 {
		t.Fatalf("admitted %d items, want the existing correction, cutoff, recent, and undated entries: %#v", len(got), got)
	}
	for _, candidate := range got {
		if candidate.URL == "https://source.example.invalid/unseen-old" {
			t.Fatal("unseen archived item crossed the source admission window")
		}
	}
}

/** TestDetailReadCriteriaRespectKnownHeadlinesAndMissingFields. */
func TestDetailReadCriteriaRespectKnownHeadlinesAndMissingFields(t *testing.T) {
	detail := settings{"titleSelector": "h1", "publishedAtSelector": "time"}
	date := time.Now()
	candidate := item{Title: "Read more", Published: &date}
	if needsDetailRead(candidate, detail, true) {
		t.Fatal("a known recovered headline should not consume another detail request")
	}
	if !needsDetailRead(candidate, detail, false) {
		t.Fatal("a new call-to-action title should request detail enrichment")
	}
	candidate = item{Title: strings.Repeat("headline ", 13), Published: func() *time.Time { value := time.Now(); return &value }()}
	if needsDetailRead(candidate, detail, true) {
		t.Fatal("a stored headline should survive a listing's long rendering")
	}
	if !needsDetailRead(candidate, detail, false) {
		t.Fatal("an unverified long listing title should request detail enrichment")
	}
	detail["titleAuthoritative"] = true
	if !needsDetailRead(candidate, detail, true) {
		t.Fatal("an explicit authoritative title rule should still read the detail page")
	}
}

/** TestEnrichDetailExtractsConfiguredFields. */
func TestEnrichDetailExtractsConfiguredFields(t *testing.T) {
	data := []byte(`<html><body><article><h1>Recovered title</h1><time datetime="2026-11-16T10:00:00Z"></time><p class="summary">Verified summary.</p></article></body></html>`)
	detail := settings{"titleSelector": "h1", "publishedAtSelector": "time", "summarySelector": ".summary"}
	candidate := item{URL: "https://source.example.invalid/article", Title: "Read more"}
	enrichDetail(&candidate, data, detail)
	if candidate.Title != "Recovered title" || candidate.Excerpt != "Verified summary." || candidate.Published == nil {
		t.Fatalf("detail enrichment = %+v", candidate)
	}
	metaCandidate := item{}
	enrichDetailFields(&metaCandidate, []byte(`<html><head><meta name="date" content="2026-09-11T19:58:33Z"></head><body></body></html>`), settings{"publishedAtSelector": `meta[name="date"]`}, detailReadNeed{date: true})
	if metaCandidate.Published == nil || metaCandidate.Published.Format(time.RFC3339) != "2026-09-11T19:58:33Z" {
		t.Fatalf("detail meta publication date = %#v", metaCandidate.Published)
	}
	tooltipCandidate := item{}
	enrichDetailFields(&tooltipCandidate, []byte(`<span title="Unix: invalid">Sep 11, 2026</span>`), settings{"publishedAtSelector": "span", "publishedAtUtcOffset": "+00:00"}, detailReadNeed{date: true})
	if tooltipCandidate.Published == nil || tooltipCandidate.Published.Format(time.RFC3339) != "2026-09-11T00:00:00Z" {
		t.Fatalf("detail visible publication date = %#v", tooltipCandidate.Published)
	}
	jsonLDCandidate := item{}
	enrichDetailFields(&jsonLDCandidate, []byte(`<html><head><script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebSite","name":"Blog"},{"@type":"BlogPosting","datePublished":"2026-09-24"}]}</script></head><body><p>Post</p></body></html>`), settings{}, detailReadNeed{date: true})
	if jsonLDCandidate.Published == nil || jsonLDCandidate.Published.Format(time.RFC3339) != "2026-09-24T00:00:00Z" {
		t.Fatalf("detail JSON-LD publication date = %#v", jsonLDCandidate.Published)
	}
	inherited := sourceDetailSettings(settings{"publishedAtUtcOffset": "+00:00", "detail": map[string]any{"maxFetches": 1}})
	inheritedCandidate := item{}
	enrichDetailFields(&inheritedCandidate, []byte(`<meta property="article:published_time" content="2026-09-24 10:00">`), inherited, detailReadNeed{date: true})
	if inheritedCandidate.Published == nil || inheritedCandidate.Published.Format(time.RFC3339) != "2026-09-24T10:00:00Z" {
		t.Fatalf("inherited source date offset = %#v", inheritedCandidate.Published)
	}
}

/** TestEnrichDetailSupportsRegexRulesForRenderedListings. */
func TestEnrichDetailSupportsRegexRulesForRenderedListings(t *testing.T) {
	data := []byte("Published Time: 2026-11-16T10:00:00Z\n# Recovered headline\n")
	candidate := item{Title: "Read the Blog"}
	enrichDetail(&candidate, data, settings{
		"titleRegex":       `(?m)^#\s+(.+)$`,
		"publishedAtRegex": `Published Time:\s*(\S+)`,
	})
	if candidate.Title != "Recovered headline" || candidate.Published == nil || candidate.Published.Format(time.RFC3339) != "2026-11-16T10:00:00Z" {
		t.Fatalf("regex detail enrichment = %+v", candidate)
	}
}

/** TestDetailRuleHashIsStableAndTracksRuleChanges. */
func TestDetailRuleHashIsStableAndTracksRuleChanges(t *testing.T) {
	first := settings{"titleSelector": "h1", "publishedAtSelector": "time"}
	second := settings{"publishedAtSelector": "time", "titleSelector": "h1"}
	if detailRuleHash(first) != detailRuleHash(second) {
		t.Fatal("equivalent detail rules produced different hashes")
	}
	if detailRuleHash(first) == detailRuleHash(settings{"titleSelector": "main h1", "publishedAtSelector": "time"}) {
		t.Fatal("detail rule changes did not invalidate saved completion")
	}
}

/** TestCheckPublicRedirectProtectsCredentialsAndOrigin validates source redirect boundaries without network access. */
func TestCheckPublicRedirectProtectsCredentialsAndOrigin(t *testing.T) {
	newRequest := func(method, target string, header http.Header, withBody bool) *http.Request {
		parsed, err := url.Parse(target)
		if err != nil {
			t.Fatalf("parse request URL %q: %v", target, err)
		}
		request := &http.Request{Method: method, URL: parsed, Header: header}
		if withBody {
			request.Body = io.NopCloser(strings.NewReader("fixture body"))
		}
		return request
	}
	tests := []struct {
		name       string
		firstURL   string
		hopURL     string
		targetURL  string
		method     string
		headerName string
		withBody   bool
		wantError  bool
	}{
		{name: "authorization header", firstURL: "https://source.example.invalid/feed", targetURL: "https://other.example.invalid/feed", method: http.MethodGet, headerName: "Authorization", wantError: true},
		{name: "cookie header", firstURL: "https://source.example.invalid/feed", targetURL: "https://other.example.invalid/feed", method: http.MethodGet, headerName: "Cookie", wantError: true},
		{name: "proxy authorization header", firstURL: "https://source.example.invalid/feed", targetURL: "https://other.example.invalid/feed", method: http.MethodGet, headerName: "Proxy-Authorization", wantError: true},
		{name: "unknown credential header", firstURL: "https://source.example.invalid/feed", targetURL: "https://other.example.invalid/feed", method: http.MethodGet, headerName: "X-Unknown-Credential", wantError: true},
		{name: "credential query", firstURL: "https://source.example.invalid/feed?api_key=fixture-secret", targetURL: "https://other.example.invalid/feed", method: http.MethodGet, wantError: true},
		{name: "post body", firstURL: "https://source.example.invalid/feed", targetURL: "https://other.example.invalid/feed", method: http.MethodPost, withBody: true, wantError: true},
		{name: "get body", firstURL: "https://source.example.invalid/feed", targetURL: "https://other.example.invalid/feed", method: http.MethodGet, withBody: true, wantError: true},
		{name: "late cross origin hop", firstURL: "https://source.example.invalid/feed?key=fixture-secret", hopURL: "https://source.example.invalid/next", targetURL: "https://other.example.invalid/feed", method: http.MethodGet, wantError: true},
		{name: "https downgrade", firstURL: "https://source.example.invalid/feed", targetURL: "http://source.example.invalid/feed", method: http.MethodGet, wantError: true},
		{name: "late https downgrade", firstURL: "http://source.example.invalid/feed", hopURL: "https://source.example.invalid/next", targetURL: "http://source.example.invalid/final", method: http.MethodGet, wantError: true},
		{name: "ordinary public headers", firstURL: "https://source.example.invalid/feed", targetURL: "https://other.example.invalid/feed", method: http.MethodGet, wantError: false},
		{name: "same origin preserves credentials", firstURL: "https://source.example.invalid/feed?api_key=fixture-secret", targetURL: "https://source.example.invalid/next?api_key=fixture-secret", method: http.MethodGet, headerName: "Authorization", wantError: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := make(http.Header)
			if test.headerName != "" {
				header.Set(test.headerName, "fixture-secret")
			} else {
				header.Set("Accept", "application/rss+xml")
				header.Set("User-Agent", "fixture")
				header.Set("If-None-Match", `"fixture"`)
			}
			first := newRequest(test.method, test.firstURL, header, test.withBody)
			via := []*http.Request{first}
			if test.hopURL != "" {
				via = append(via, newRequest(http.MethodGet, test.hopURL, make(http.Header), false))
			}
			target := newRequest(http.MethodGet, test.targetURL, make(http.Header), false)
			err := checkPublicRedirect(target, via)
			if test.wantError && err == nil {
				t.Fatal("expected the protected redirect to be rejected")
			}
			if !test.wantError && err != nil {
				t.Fatalf("public redirect returned an error: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("redirect error exposed a credential: %v", err)
			}
		})
	}
}

/** TestPublicRedirectRetainsMethodSemantics preserves Go's HTTP redirect behavior for source requests. */
func TestPublicRedirectRetainsMethodSemantics(t *testing.T) {
	payload := `{"value":"fixture"}`
	tests := []struct {
		name        string
		status      int
		method      string
		withBody    bool
		twoHops     bool
		wantMethod  string
		wantBody    string
		wantContent bool
	}{
		{
			name:       "301 POST becomes GET",
			status:     http.StatusMovedPermanently,
			method:     http.MethodPost,
			withBody:   true,
			wantMethod: http.MethodGet,
		},
		{
			name:       "302 POST becomes GET",
			status:     http.StatusFound,
			method:     http.MethodPost,
			withBody:   true,
			wantMethod: http.MethodGet,
		},
		{
			name:        "301 PUT retains body",
			status:      http.StatusMovedPermanently,
			method:      http.MethodPut,
			withBody:    true,
			wantMethod:  http.MethodPut,
			wantBody:    payload,
			wantContent: true,
		},
		{
			name:       "303 PUT becomes GET",
			status:     http.StatusSeeOther,
			method:     http.MethodPut,
			withBody:   true,
			wantMethod: http.MethodGet,
		},
		{
			name:       "303 dropped body stays dropped on later hops",
			status:     http.StatusSeeOther,
			method:     http.MethodPost,
			withBody:   true,
			twoHops:    true,
			wantMethod: http.MethodGet,
		},
		{
			name:       "303 HEAD stays HEAD",
			status:     http.StatusSeeOther,
			method:     http.MethodHead,
			wantMethod: http.MethodHead,
		},
		{
			name:        "307 POST retains body",
			status:      http.StatusTemporaryRedirect,
			method:      http.MethodPost,
			withBody:    true,
			wantMethod:  http.MethodPost,
			wantBody:    payload,
			wantContent: true,
		},
		{
			name:        "308 PUT retains body",
			status:      http.StatusPermanentRedirect,
			method:      http.MethodPut,
			withBody:    true,
			wantMethod:  http.MethodPut,
			wantBody:    payload,
			wantContent: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var finalMethod, finalBody, finalContentType, finalAuthorization string
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/start" {
					destination := "/final"
					if test.twoHops {
						destination = "/middle"
					}
					response.Header().Set("Location", destination)
					response.WriteHeader(test.status)
					return
				}
				if request.URL.Path == "/middle" {
					response.Header().Set("Location", "/final")
					response.WriteHeader(http.StatusTemporaryRedirect)
					return
				}
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read final request body: %v", err)
				}
				finalMethod = request.Method
				finalBody = string(body)
				finalContentType = request.Header.Get("Content-Type")
				finalAuthorization = request.Header.Get("Authorization")
				_, _ = response.Write([]byte("ok"))
			}))
			defer server.Close()
			client := &http.Client{
				Transport:     &http.Transport{Proxy: nil},
				CheckRedirect: checkPublicRedirect,
			}
			defer client.CloseIdleConnections()
			service := &Service{Client: client}
			var requestBody any
			if test.withBody {
				requestBody = map[string]string{"value": "fixture"}
			}
			if _, err := service.fetchURL(context.Background(), server.URL+"/start", test.method, settings{"Authorization": "Bearer fixture"}, requestBody); err != nil {
				t.Fatal(err)
			}
			if finalMethod != test.wantMethod || finalBody != test.wantBody || (finalContentType != "") != test.wantContent || finalAuthorization != "Bearer fixture" {
				t.Fatalf("redirect request = method %q body %q content type %q authorization %q", finalMethod, finalBody, finalContentType, finalAuthorization)
			}
		})
	}
}

/** TestPublicHTTPClientUsesTheConfiguredDialerOnRedirects keeps egress selection active across hops. */
func TestPublicHTTPClientUsesTheConfiguredDialerOnRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			response.Header().Set("Location", "http://other.example.invalid/final")
			response.WriteHeader(http.StatusFound)
			return
		}
		_, _ = io.WriteString(response, request.Host)
	}))
	defer server.Close()
	var dials atomic.Int32
	client := NewHTTPClientWithDialer(time.Second, func(ctx context.Context, network, _ string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	})
	service := &Service{Client: client}
	body, err := service.fetchURL(context.Background(), "http://source.example.invalid/start", http.MethodGet, nil, nil)
	if err != nil || string(body) != "other.example.invalid" {
		t.Fatalf("redirect response = %q, error=%v", body, err)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("redirect chain used the configured dialer %d times, want one call per origin", got)
	}
}

/** TestPublicRedirectIgnoresLocationOnNonRedirectStatuses prevents accidental 3xx follow-up. */
func TestPublicRedirectIgnoresLocationOnNonRedirectStatuses(t *testing.T) {
	for _, status := range []int{http.StatusMultipleChoices, http.StatusNotModified, http.StatusUseProxy} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				requests++
				response.Header().Set("Location", "/unexpected")
				response.WriteHeader(status)
			}))
			defer server.Close()
			client := &http.Client{
				Transport:     &http.Transport{Proxy: nil},
				CheckRedirect: checkPublicRedirect,
			}
			defer client.CloseIdleConnections()
			service := &Service{Client: client}
			if _, err := service.fetchURL(context.Background(), server.URL+"/start", http.MethodGet, nil, nil); err == nil {
				t.Fatalf("HTTP %d unexpectedly succeeded", status)
			}
			if requests != 1 {
				t.Fatalf("HTTP %d followed Location with %d requests", status, requests)
			}
		})
	}
}

/** TestPublicRedirectStopsCredentialReplayBeforeDispatch preserves the cross-origin safety boundary. */
func TestPublicRedirectStopsCredentialReplayBeforeDispatch(t *testing.T) {
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		targetRequests++
		_, _ = response.Write([]byte("unexpected"))
	}))
	defer target.Close()
	sourceRequests := 0
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		sourceRequests++
		response.Header().Set("Location", target.URL+"/capture")
		response.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: checkPublicRedirect,
	}
	defer client.CloseIdleConnections()
	service := &Service{Client: client}
	_, err := service.fetchURL(context.Background(), source.URL+"/start", http.MethodPost, settings{"Authorization": "Bearer fixture"}, map[string]string{"value": "fixture"})
	if err == nil || strings.Contains(err.Error(), "Bearer fixture") {
		t.Fatalf("credential-bearing redirect error = %v", err)
	}
	if sourceRequests != 1 || targetRequests != 0 {
		t.Fatalf("redirect requests source=%d target=%d", sourceRequests, targetRequests)
	}
}

/** TestPublicRedirectLimitSanitizesURLCredentials caps cycles without exposing query values. */
func TestPublicRedirectLimitSanitizesURLCredentials(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		response.Header().Set("Location", "/loop?api_key=fixture-secret")
		response.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: checkPublicRedirect,
	}
	defer client.CloseIdleConnections()
	service := &Service{Client: client}
	_, err := service.fetchURL(context.Background(), server.URL+"/loop?api_key=fixture-secret", http.MethodGet, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "source HTTP request failed") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("redirect-cycle error exposed details: %v", err)
	}
	if requests != 3 {
		t.Fatalf("redirect cycle made %d requests, want 3", requests)
	}
}

/** TestPublicFetchCapsResponseSize rejects oversized source bodies before parsing. */
func TestPublicFetchCapsResponseSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(strings.Repeat("x", 8*1024*1024+1)))
	}))
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	service := &Service{Client: client}
	if _, err := service.fetchURL(context.Background(), server.URL+"/large", http.MethodGet, nil, nil); err == nil || err.Error() != "source response is too large" {
		t.Fatalf("oversized response error = %v", err)
	}
}

/** TestPublicFetchUsesOneDeadlineAcrossRedirects bounds the entire source request chain. */
func TestPublicFetchUsesOneDeadlineAcrossRedirects(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		time.Sleep(180 * time.Millisecond)
		if request.URL.Path == "/start" {
			response.Header().Set("Location", "/next")
			response.WriteHeader(http.StatusFound)
			return
		}
		_, _ = response.Write([]byte("too late"))
	}))
	defer server.Close()
	client := &http.Client{
		Timeout:       300 * time.Millisecond,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: checkPublicRedirect,
	}
	defer client.CloseIdleConnections()
	service := &Service{Client: client}

	// Follow one same-origin redirect while keeping the original client's total deadline.
	if _, err := service.fetchURL(context.Background(), server.URL+"/start", http.MethodGet, nil, nil); err == nil {
		t.Fatal("redirect chain unexpectedly exceeded the total request deadline")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("timed request reached %d hops, want the redirect and timed-out final hop", got)
	}
}
