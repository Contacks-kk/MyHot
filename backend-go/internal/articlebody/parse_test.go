package articlebody

import (
	"strings"
	"testing"
	"time"
)

func TestParseHTMLSanitizesBodyResolvesImagesAndReadsPublishedTime(t *testing.T) {
	paragraph := "The reporting describes a substantial change in the company's product strategy, explains the customer impact, and includes detailed evidence from the people responsible for the work. "
	html := `<html><head><meta property="article:published_time" content="2026-10-08T10:20:00+08:00"></head><body><article><h1>Release notes</h1><p>` + paragraph + `</p><p>` + paragraph + `</p><p><img src="/assets/chart.png" width="640" height="480" alt="chart"></p><div class="promo-box"><p>` + paragraph + `</p></div><script>unsafe()</script></article><p>Related articles</p></body></html>`

	got, err := ParseHTML(html, "https://news.example.com/posts/1", "+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a readable article body")
	}
	if len([]rune(got.Text)) < minBodyChars {
		t.Fatalf("body is too short: %d", len([]rune(got.Text)))
	}
	if strings.Contains(got.HTML, "unsafe()") || strings.Contains(got.HTML, "promo-box") || strings.Contains(got.HTML, "Related articles") {
		t.Fatalf("HTML retained content that should be removed: %s", got.HTML)
	}
	if len(got.Images) != 1 || got.Images[0].URL != "https://news.example.com/assets/chart.png" || got.Images[0].Width == nil || *got.Images[0].Width != 640 || got.Images[0].Alt == nil || *got.Images[0].Alt != "chart" {
		t.Fatalf("unexpected image metadata: %#v", got.Images)
	}
	want := time.Date(2026, 10, 8, 2, 20, 0, 0, time.UTC)
	if got.PublishedAt == nil || !got.PublishedAt.Equal(want) {
		t.Fatalf("published time = %v, want %v", got.PublishedAt, want)
	}
}

func TestParseHTMLUsesConfiguredOffsetForUnzonedDate(t *testing.T) {
	paragraph := strings.Repeat("The article includes reporting, evidence and context. ", 12)
	got, err := ParseHTML(`<html><head><meta name="date" content="2026-10-08 10:20"></head><body><article><p>`+paragraph+`</p></article></body></html>`, "https://news.example.com/post", "+09:00")
	if err != nil || got == nil {
		t.Fatalf("ParseHTML() = (%v, %v)", got, err)
	}
	want := time.Date(2026, 10, 8, 1, 20, 0, 0, time.UTC)
	if got.PublishedAt == nil || !got.PublishedAt.Equal(want) {
		t.Fatalf("published time = %v, want %v", got.PublishedAt, want)
	}
}

func TestPageFetchableExcludesUnsupportedAndPlayerPages(t *testing.T) {
	for _, item := range []struct {
		url  string
		kind string
		want bool
	}{
		{"https://example.com/article", "rss", true},
		{"https://x.com/user/status/1", "rss", false},
		{"https://mp.weixin.qq.com/s/abc", "rss", false},
		{"https://youtube.com/watch?v=abc", "rss", false},
		{"https://youtube.com/channel/example", "rss", true},
		{"https://example.com/article", "mp_account", false},
		{"file:///tmp/article", "rss", false},
	} {
		if got := pageFetchable(item.url, item.kind); got != item.want {
			t.Errorf("pageFetchable(%q, %q) = %t, want %t", item.url, item.kind, got, item.want)
		}
	}
}
