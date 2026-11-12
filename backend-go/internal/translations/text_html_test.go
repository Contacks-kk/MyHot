package translations

import (
	"strings"
	"testing"
)

func TestTextToHTMLEscapesMarkupAndPreservesLinksListsAndBreaks(t *testing.T) {
	got := textToHTML("<script>alert(1)</script>\nline two\n\n- first\n- second\n\nSee https://example.com/docs?a=1&b=2。")
	for _, want := range []string{
		"&lt;script&gt;alert(1)&lt;/script&gt;<br>line two",
		"<ul><li>first</li><li>second</li></ul>",
		`<a href="https://example.com/docs?a=1&amp;b=2" target="_blank" rel="noopener noreferrer nofollow">https://example.com/docs?a=1&amp;b=2</a>。`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML %q does not contain %q", got, want)
		}
	}
}

func TestTextBlocksSupportQuoteHeadingAndCode(t *testing.T) {
	if got := textBlock("> quoted\n> line"); got != "<blockquote><p>quoted<br>line</p></blockquote>" {
		t.Fatalf("quote HTML = %q", got)
	}
	if got := textBlock("## Heading"); got != "<h3>Heading</h3>" {
		t.Fatalf("heading HTML = %q", got)
	}
	if got := textBlock("```go\nfmt.Println(1)\n```"); got != "<pre><code>fmt.Println(1)</code></pre>" {
		t.Fatalf("code HTML = %q", got)
	}
}
