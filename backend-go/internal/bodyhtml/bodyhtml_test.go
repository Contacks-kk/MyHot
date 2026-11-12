package bodyhtml

import (
	"strings"
	"testing"
	"time"
)

func TestSanitizeWhitelistsHTMLAndSignsArticleImages(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	body, outline, err := Sanitize(`<h2 onclick="alert(1)">A safe heading</h2><div><script>secret()</script><a href="javascript:alert(1)" onclick="bad()">unsafe link</a><img data-src="/images/chart.png" onerror="bad()" width="900"></div><p class="msr-promo">promotion</p><iframe>frame secret</iframe>`, "https://news.example/articles/one", "test-image-secret", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"onclick", "onerror", "javascript:", "secret()", "promotion", "frame secret", "iframe"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("sanitized body contains %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `<h2 id="sec-1">A safe heading</h2>`) || !strings.Contains(body, `loading="lazy"`) || !strings.Contains(body, `u=https%3A%2F%2Fnews.example%2Fimages%2Fchart.png`) {
		t.Fatalf("sanitized body is missing normalized heading or signed image: %s", body)
	}
	if len(outline) != 1 || outline[0] != (OutlineEntry{ID: "sec-1", Text: "A safe heading", Level: 2}) {
		t.Fatalf("outline = %#v", outline)
	}
}

func TestSanitizeDropsUnsafeAndTrackingImages(t *testing.T) {
	body, outline, err := Sanitize(`<h3> </h3><img src="https://secure.adnxs.com/pixel"><img src="javascript:alert(1)"><p><strong>kept</strong></p>`, "https://news.example/", "test-image-secret", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "img") || len(outline) != 0 || !strings.Contains(body, "<p><strong>kept</strong></p>") {
		t.Fatalf("sanitized body=%q outline=%#v", body, outline)
	}
}

func TestMarkdownPreservesStructureAndInlineSpacing(t *testing.T) {
	body, err := Markdown(`<h2>Updates</h2><p>Read <strong>the report</strong> now: <a href="/report">source</a>.</p><ul><li>First</li><li>Second</li></ul><pre><code class="language-go">fmt.Println(1)</code></pre><script>bad()</script>`, "https://news.example/articles/one")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"## Updates",
		"Read **the report** now: [source](https://news.example/report).",
		"- First\n- Second",
		"```go\nfmt.Println(1)\n```",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("Markdown output %q does not contain %q", body, expected)
		}
	}
	if strings.Contains(body, "bad()") {
		t.Fatalf("unsafe script content appears in Markdown: %s", body)
	}
}

func TestMarkdownKeepsTurndownHTMLStructures(t *testing.T) {
	body, err := Markdown(`<p><u>underline</u> <sup>1</sup> <sub>2</sub> <mark>marked</mark></p><dl><dt>Term</dt><dd>Definition</dd></dl><video src="movie.mp4" poster="/poster.jpg" controls></video>`, "https://news.example/articles/one")
	if err != nil {
		t.Fatal(err)
	}
	want := `<u>underline</u> <sup>1</sup> <sub>2</sub> <mark>marked</mark>

<dl><dt>Term</dt><dd>Definition</dd></dl>

<video src="movie.mp4" poster="https://news.example/poster.jpg"></video>`
	if body != want {
		t.Fatalf("Markdown output differs from the existing Turndown contract:\n got: %q\nwant: %q", body, want)
	}
}
