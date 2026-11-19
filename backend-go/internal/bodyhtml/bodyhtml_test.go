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
	body, outline, err := Sanitize(`<h3> </h3><img src="https://secure.adnxs.com/pixel"><img src="javascript:alert(1)"><img src="https://example.org/pixel" width="1" height="1"><img src="https://example.org/chart.png" width="800" height="400"><p><strong>kept</strong></p>`, "https://news.example/", "test-image-secret", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "secure.adnxs.com") || strings.Contains(body, "javascript:") || strings.Contains(body, "example.org/pixel") || strings.Count(body, "<img") != 1 || !strings.Contains(body, "u=https%3A%2F%2Fexample.org%2Fchart.png") || len(outline) != 0 || !strings.Contains(body, "<p><strong>kept</strong></p>") {
		t.Fatalf("sanitized body=%q outline=%#v", body, outline)
	}
}

/** TestSanitizeAddsResponsiveCandidatesOnlyForKnownLargeImages preserves body image sizing behavior. */
func TestSanitizeAddsResponsiveCandidatesOnlyForKnownLargeImages(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	body, _, err := Sanitize(`<img src="https://example.org/chart.png?a=1&amp;b=2" width="800" height="400"><img src="https://example.org/small.png" width="160" height="80"><img src="https://example.org/unknown.png">`, "https://news.example/article", "test-image-secret", now)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(body, "srcset="); got != 1 {
		t.Fatalf("responsive source set count = %d, want only the known large image: %s", got, body)
	}
	if !strings.Contains(body, "mode=image-720") || !strings.Contains(body, "mode=image-1200") || !strings.Contains(body, "mode=image-1600") {
		t.Fatalf("large image candidates are incomplete: %s", body)
	}
	if !strings.Contains(body, `sizes="(min-width: 1536px) 760px, (min-width: 1024px) calc(100vw - 524px), (min-width: 640px) 608px, calc(100vw - 32px)"`) || strings.Contains(body, `sizes="auto`) {
		t.Fatalf("responsive sizes changed or used auto containment: %s", body)
	}
	if !strings.Contains(body, `width="800" height="400"`) || strings.Count(body, "<img") != 3 {
		t.Fatalf("responsive candidates changed intrinsic dimensions or omitted images: %s", body)
	}
}

/** TestSanitizeDropsVideoPageImagesAndKeepsThumbnailsAndLinks preserves supported media links. */
func TestSanitizeDropsVideoPageImagesAndKeepsThumbnailsAndLinks(t *testing.T) {
	videoPages := []string{
		"https://www.youtube.com/watch?v=example&t=1",
		"https://youtube.com/shorts/example",
		"https://m.youtube.com/watch?v=example",
		"https://www.youtube.com/shorts/example",
	}
	pictures := []string{
		"https://www.youtube.com/s/desktop/example/favicon_32x32.png",
		"https://i.ytimg.com/vi/example/hqdefault.jpg",
		"https://example.org/watch.png",
		"https://youtube.com.example.org/watch?v=example",
	}
	var images strings.Builder
	for _, source := range append(videoPages, pictures...) {
		images.WriteString(`<img src="`)
		images.WriteString(strings.ReplaceAll(source, "&", "&amp;"))
		images.WriteString(`" width="800" height="400">`)
	}
	page := videoPages[0]
	video := `<a href="` + strings.ReplaceAll(page, "&", "&amp;") + `">Watch video</a><video src="https://example.org/video.mp4" poster="` + videoPages[1] + `"></video>`
	body, _, err := Sanitize(video+images.String(), "https://news.example/article", "test-image-secret", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(body, "<img"); got != len(pictures) {
		t.Fatalf("image count = %d, want only real thumbnails and images: %s", got, body)
	}
	if strings.Contains(body, "u=https%3A%2F%2Fwww.youtube.com%2Fwatch") || strings.Contains(body, "u=https%3A%2F%2Fyoutube.com%2Fshorts") {
		t.Fatalf("video page was signed as an image: %s", body)
	}
	if !strings.Contains(body, `<a href="https://www.youtube.com/watch?v=example&amp;t=1">Watch video</a>`) || !strings.Contains(body, `<video src="https://example.org/video.mp4"></video>`) {
		t.Fatalf("video link or element was removed: %s", body)
	}
	if !strings.Contains(body, "i.ytimg.com%2Fvi%2Fexample%2Fhqdefault.jpg") || !strings.Contains(body, "youtube.com.example.org%2Fwatch") {
		t.Fatalf("real thumbnail or unrelated hostname was removed: %s", body)
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
