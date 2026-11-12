package editorial

import (
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestBuildMaterialKeepsSourceAndBeijingTime(t *testing.T) {
	published := time.Date(2026, 10, 8, 16, 5, 0, 0, time.UTC)
	body := "  第一段\n\n第二段  "
	input := Input{
		Title: "  标题\n示例  ", URL: "https://example.test/post", PublishedAt: &published,
		BodyText: &body, Source: Source{Name: "信源", Kind: "rss", Tier: "T1", FirstParty: true},
	}
	material := BuildMaterial(input)
	for _, expected := range []string{
		"类型：RSS；分级：T1；一手来源：是",
		"发布时间：2026-10-09 00:05（北京时间）",
		"标题：标题 示例",
		"正文：\n  第一段\n\n第二段  ",
		"原文链接：https://example.test/post",
	} {
		if !strings.Contains(material, expected) {
			t.Errorf("BuildMaterial() missing %q in:\n%s", expected, material)
		}
	}
}

func TestBuildScoreInputUsesPublishedTimeAndXQuote(t *testing.T) {
	published := time.Date(2026, 2, 3, 12, 34, 56, 789_000_000, time.UTC)
	input := Input{Title: " 标题 ", PublishedAt: &published, XPost: &XPost{
		Text: " 主帖 ", Quoted: &QuotedPost{Handle: "quoted", Text: "引用内容"},
	}}
	score := BuildScoreInput(input)
	for _, expected := range []string{
		"【发布时间（北京时间）】\n2026-02-03T20:34:56.789+08:00",
		"【标题】\n标题",
		"[引用 @quoted]：引用内容",
	} {
		if !strings.Contains(score, expected) {
			t.Errorf("BuildScoreInput() missing %q in:\n%s", expected, score)
		}
	}
	if strings.Contains(score, "【来源】") {
		t.Fatal("score input must not include source facts")
	}
}

func TestRenderContextAndPrefilterJSONPreservePromptContract(t *testing.T) {
	published := time.Date(2026, 10, 8, 9, 10, 11, 0, time.UTC)
	input := Input{Title: "新闻", URL: "https://x.com/i/article/123", PublishedAt: &published,
		BodyStatus: "pending", Source: Source{Name: "源", Kind: "x_search", Tags: []string{"AI", "政策"}},
		XPost: &XPost{AuthorName: "作者", Handle: "person", Text: "仅链接 https://x.com/i/article/123",
			Media: []Media{{Kind: "image"}}, Quoted: &QuotedPost{Handle: "other", Text: "被引用内容"}},
	}
	context := RenderContext(input, true)
	for _, expected := range []string{
		"【来源标签】AI, 政策", "【媒体】1 张图 · 含 X 长文链接（正文未抓到）",
		"【引用 @other】（以下是作者转发/引用的**他人**内容，不是作者本人的产出）",
		"【正文（作者自己的内容）】",
	} {
		if !strings.Contains(context, expected) {
			t.Errorf("RenderContext() missing %q in:\n%s", expected, context)
		}
	}
	if strings.Contains(PrefilterUser(input), `\u003c`) {
		t.Fatal("JSON.stringify-compatible output should not HTML-escape prompt text")
	}
	if MissingEvidence(input) {
		t.Fatal("an X post is evidence")
	}
}

func TestWithXArticleAndMissingEvidence(t *testing.T) {
	post := &XPost{Text: "原帖", Handle: "user"}
	joined := WithXArticle(post, &XArticle{Title: "长文标题", Text: "长文正文"})
	if joined == post || joined.Handle != post.Handle || joined.Text != "原帖\n\n【X 长文】长文标题\n\n长文正文" {
		t.Fatalf("WithXArticle() = %#v", joined)
	}
	if post.Text != "原帖" {
		t.Fatal("WithXArticle mutated its input")
	}
	if !MissingEvidence(Input{BodyText: strptr("  "), Excerpt: strptr("")}) {
		t.Fatal("whitespace only input should be missing evidence")
	}
	if MissingEvidence(Input{XPost: &XPost{Quoted: &QuotedPost{Text: "quoted"}}}) {
		t.Fatal("quoted post text should count as evidence")
	}
}

func TestBodyLimitCountsUTF16CodeUnits(t *testing.T) {
	body := strings.Repeat("a", MaxBodyChars-1) + "😀tail"
	input := Input{Title: "标题", BodyText: &body}
	material := BuildMaterial(input)
	if !strings.Contains(material, "\n【原文超过 60000 字符，后文未提供】") {
		t.Fatal("expected an explicit truncated-body marker")
	}
	clipped := capBody(body)
	if got := len(utf16.Encode([]rune(clipped))); got != MaxBodyChars-1 {
		t.Fatalf("clipped UTF-16 length = %d, want %d", got, MaxBodyChars-1)
	}
	if strings.HasSuffix(clipped, "\uFFFD") || strings.HasSuffix(clipped, "😀") {
		t.Fatalf("UTF-16 cut should not produce a broken rune: suffix %q", clipped[len(clipped)-8:])
	}
}

func TestScoreInputTimeOmitsZeroMilliseconds(t *testing.T) {
	wholeSecond := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := ScoreInputTime(wholeSecond); got != "2026-01-02T11:04:05+08:00" {
		t.Fatalf("ScoreInputTime() = %q", got)
	}
	withMillis := wholeSecond.Add(120 * time.Millisecond)
	if got := ScoreInputTime(withMillis); got != "2026-01-02T11:04:05.120+08:00" {
		t.Fatalf("ScoreInputTime() = %q", got)
	}
}

func strptr(value string) *string { return &value }
