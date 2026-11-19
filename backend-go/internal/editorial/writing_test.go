package editorial

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

/** TestWritingPromptBuildersUseBackendPrompts verifies prompts load from the Go runtime configuration. */
func TestWritingPromptBuildersUseBackendPrompts(t *testing.T) {
	vocabulary := loadCurrentVocabulary(t)
	_, source, _, _ := runtime.Caller(0)
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	pack, err := LoadPromptsFrom(filepath.Join(repositoryRoot, "backend-go", "config", "prompts"), "MyHOT")
	if err != nil {
		t.Fatal(err)
	}
	published := time.Date(2026, 9, 8, 16, 0, 0, 0, time.UTC)
	article, err := vocabulary.BuildArticlePrompt(pack, WritingInput{
		Title: "OpenAI 发布 GPT-5.5", Text: "<p>GPT-5.5 is available.</p>", SourceKind: "rss", SourceName: "OpenAI",
		DocumentURL: "https://openai.com/blog/gpt-5-5", PublishedAt: &published,
	}, time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"原文发布日期：2026-09-09", "今天：2026-09-09", "GPT-5.5 is available.", "文档发布域主体=OpenAI"} {
		if !strings.Contains(article, expected) {
			t.Errorf("article prompt is missing %q", expected)
		}
	}
	if strings.Contains(article, "{{") {
		t.Fatal("article prompt contains an unexpanded template token")
	}
	tweet, err := vocabulary.BuildShortTweetPrompt(pack, WritingInput{
		Title: "短帖", Text: "Go build!", MainText: "Go build!", SourceName: "OpenAI",
		DocumentURL: "https://x.com/openai/status/1", QuotedText: "The release is available.", QuotedAuthor: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tweet, "@openai（仅供理解上下文") || !strings.Contains(tweet, "Go build!") || !strings.Contains(tweet, "The release is available.") {
		t.Fatalf("short tweet prompt missing expected input: %s", tweet)
	}
}

func TestWritingTextCleanupAndShortTweetRules(t *testing.T) {
	got := cleanArticleTextForLLM("<p>第一段&nbsp;文字</p>\n<script>alert('x')</script><p>第二段 &amp; &quot;引文&quot;</p>")
	if !strings.Contains(got, "第一段 文字") || !strings.Contains(got, `第二段 & "引文"`) || strings.Contains(got, "alert") {
		t.Fatalf("cleanArticleTextForLLM() = %q", got)
	}
	if got := clampText("甲乙丙丁", 3); got != "甲乙…" {
		t.Fatalf("clampText() = %q", got)
	}
	if !isShortTweet(strings.Repeat("中", 99)) || isShortTweet(strings.Repeat("中", 100)) {
		t.Fatal("Chinese short-post length boundary changed")
	}
	if !needsShortTweetTranslation("OpenAI launches GPT-5.5") || needsShortTweetTranslation("今天正式开放测试") {
		t.Fatal("short-post translation decision changed")
	}
}

func TestFinalizeCopyCompactsSummaryAndAppliesIdentityGuard(t *testing.T) {
	vocabulary := loadCurrentVocabulary(t)
	body := strings.Repeat("GPT-5.5 提供新的推理能力。", 40)
	input := WritingInput{Title: "OpenAI 发布 GPT-5.5", Text: body, SourceKind: "rss", SourceName: "OpenAI", DocumentURL: "https://openai.com"}
	summary := strings.Repeat("GPT-5.5 提供新的推理能力。", 20)
	copy := finalizeCopy(vocabulary, input, input.Title, summary)
	if utf16Length(copy.SummaryZh) > 190 || copy.IdentityGuard.Outcome != "pass" {
		t.Fatalf("final copy = %#v", copy)
	}
	unsupported := finalizeCopy(vocabulary, input, "Anthropic 发布 Claude 5", "OpenAI 更新了 GPT-5.5。")
	if unsupported.TitleZh != input.Title || unsupported.SummaryZh != "OpenAI 更新了 GPT-5.5。" || unsupported.IdentityGuard.Outcome != "fallback" {
		t.Fatalf("identity fallback = %#v", unsupported)
	}
}
