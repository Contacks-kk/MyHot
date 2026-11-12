package agentmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/publiccontent"
)

type hotTopicsFixture struct {
	response publiccontent.HotTopicsResponse
}

func (f hotTopicsFixture) HotTopics(context.Context, time.Time, string, string) (publiccontent.HotTopicsResponse, error) {
	return f.response, nil
}

func TestServiceLoadsGoHotTopicsAndBuildsAnswer(t *testing.T) {
	service := NewService(hotTopicsFixture{response: publiccontent.HotTopicsResponse{
		Items: []publiccontent.HotTopicItem{{Rank: 1, Title: "Example", Links: publiccontent.HotLinks{AIHOT: "https://site.invalid/items/id", Story: "https://site.invalid/story/story-id"}}},
	}}, "Example", "https://site.invalid/")
	answer, err := service.Hot(context.Background(), 1, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	if err != nil || !strings.Contains(answer, "https://site.invalid/api/v1/agent/stories/story-id") {
		t.Fatalf("answer=%s error=%v", answer, err)
	}
}

func TestHotAnswerRendersPublicSourcesAndFollowupAddress(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	response := publiccontent.HotTopicsResponse{Items: []publiccontent.HotTopicItem{
		{
			Rank: 2, Title: "AI [launch]", SourceCount: 7, LatestAt: "2026-10-07T02:00:00.000Z",
			SourceNames: []string{"OpenAI（官网 RSS）", "X：Researcher (@researcher)", "OpenAI（官网 RSS）", "媒体 A", "媒体 B", "媒体 C", "媒体 D", "媒体 E"},
			Links:       publiccontent.HotLinks{AIHOT: "https://example.invalid/items/a1", Story: "https://example.invalid/story/public-123"},
		},
	}}
	answer := HotAnswer(response, 10, "Example", "https://example.invalid/", now)
	for _, want := range []string{
		"# Example 当前热点 Top 1",
		"第 2 名：[AI \\[launch\\]](https://example.invalid/items/a1)",
		"OpenAI、Researcher、媒体 A、媒体 B、媒体 C、媒体 D 等（7 个）",
		"最新进展 10-07 10:00",
		"https://example.invalid/api/v1/agent/stories/public-123",
		"［Example 不可信外部资料开始］",
		"［Example 不可信外部资料结束］",
	} {
		if !strings.Contains(answer, want) {
			t.Errorf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestHotAnswerEmptyResultDoesNotInventItems(t *testing.T) {
	answer := HotAnswer(publiccontent.HotTopicsResponse{Items: []publiccontent.HotTopicItem{}}, 10, "Example", "https://example.invalid", time.Now())
	if !strings.Contains(answer, "热点榜暂时是空的") || strings.Contains(answer, "不可信外部资料开始") {
		t.Fatalf("empty answer = %s", answer)
	}
}
