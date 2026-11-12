package agentmd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/publiccontent"
)

type v1ItemsFixture struct {
	responses []publiccontent.V1ItemsResponse
	queries   []publiccontent.V1ItemsQuery
	err       error
}

func (f *v1ItemsFixture) HotTopics(context.Context, time.Time, string, string) (publiccontent.HotTopicsResponse, error) {
	return publiccontent.HotTopicsResponse{}, nil
}

func (f *v1ItemsFixture) V1Items(_ context.Context, query publiccontent.V1ItemsQuery, _ time.Time, _ string) (publiccontent.V1ItemsResponse, error) {
	f.queries = append(f.queries, query)
	if f.err != nil {
		return publiccontent.V1ItemsResponse{}, f.err
	}
	if len(f.responses) == 0 {
		return publiccontent.V1ItemsResponse{Items: []publiccontent.V1Item{}}, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func TestLatestRendersV1ItemsWithConfiguredCategoryAndWindow(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	published := "2026-10-07T02:00:00.000Z"
	category := "ai-models"
	reason := "发布了新模型"
	summary := "摘要内容"
	fixture := &v1ItemsFixture{responses: []publiccontent.V1ItemsResponse{{
		Items: []publiccontent.V1Item{{
			Title: "Model [release]", Summary: &summary, Source: publiccontent.V1ItemSource{Name: "X：Researcher (@researcher)"},
			Links:       publiccontent.V1ItemLinks{AIHot: "https://site.invalid/items/a1", Original: "https://source.invalid/a1"},
			PublishedAt: &published, DiscoveredAt: "2026-10-07T03:00:00.000Z", Category: &category, Selected: true, Reason: &reason,
		}}, Page: publiccontent.V1ItemsPage{HasMore: true},
	}}}
	service := NewService(fixture, "Example", "https://site.invalid", map[string]string{"ai-models": "模型"})
	answer, err := service.Latest(context.Background(), LatestQuery{Window: "24h", Mode: "selected", Category: &category, Limit: 10}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Example 精选 · 模型 · 过去 24 小时",
		"1 条，从新到旧，时间为北京时间。后面还有，调大 limit（最多 30）可以多看。",
		"[Model \\[release\\]](https://site.invalid/items/a1)",
		"Researcher · 发布于 10-07 10:00 · 模型",
		"摘要：摘要内容", "推荐理由：发布了新模型", "原文：https://source.invalid/a1",
	} {
		if !strings.Contains(answer, want) {
			t.Errorf("answer missing %q:\n%s", want, answer)
		}
	}
	if len(fixture.queries) != 1 || fixture.queries[0].Mode != "selected" || fixture.queries[0].Window != "24h" || fixture.queries[0].By != "timeline" || fixture.queries[0].Limit != 10 || fixture.queries[0].Search != nil || fixture.queries[0].Category == nil || *fixture.queries[0].Category != category {
		t.Fatalf("queries = %#v", fixture.queries)
	}
}

func TestLatestEmptyResponseKeepsRequestedScope(t *testing.T) {
	answer := LatestAnswer(publiccontent.V1ItemsResponse{Items: []publiccontent.V1Item{}}, LatestQuery{Window: "7d", Mode: "all", Limit: 30}, "Example", nil, time.Now())
	if !strings.Contains(answer, "Example 全部公开动态 · 最近 7 天") || !strings.Contains(answer, "最近 7 天没有符合条件的全部公开动态") || strings.Contains(answer, "不可信外部资料开始") {
		t.Fatalf("empty answer = %s", answer)
	}
}

func TestSearchExpandsToAllOnlyWhenSelectedHasNoResults(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	category := "paper"
	item := publiccontent.V1Item{Title: "Paper", Source: publiccontent.V1ItemSource{Name: "Journal"}, Links: publiccontent.V1ItemLinks{AIHot: "https://site.invalid/items/p1", Original: "https://source.invalid/p1"}, DiscoveredAt: "2026-10-07T02:00:00.000Z"}
	fixture := &v1ItemsFixture{responses: []publiccontent.V1ItemsResponse{
		{Items: []publiccontent.V1Item{}},
		{Items: []publiccontent.V1Item{item}},
	}}
	service := NewService(fixture, "Example", "https://site.invalid", map[string]string{"paper": "论文"})
	answer, err := service.Search(context.Background(), SearchQuery{Window: "7d", Category: &category, Limit: 12, Text: "  OpenAI  "}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.queries) != 2 || fixture.queries[0].Mode != "selected" || fixture.queries[1].Mode != "all" {
		t.Fatalf("query modes = %#v", fixture.queries)
	}
	for _, query := range fixture.queries {
		if query.Window != "7d" || query.By != "timeline" || query.Limit != 12 || query.Category == nil || *query.Category != category || query.Search == nil || *query.Search != "  OpenAI  " {
			t.Errorf("query = %#v", query)
		}
	}
	for _, want := range []string{"精选里没有，以下来自全部公开动态", "[Paper](https://site.invalid/items/p1)", "告诉用户这些没有进入 Example 精选"} {
		if !strings.Contains(answer, want) {
			t.Errorf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestSearchDoesNotExpandWhenSelectedHasMatchesAndMapsErrors(t *testing.T) {
	item := publiccontent.V1Item{Title: "Selected", Links: publiccontent.V1ItemLinks{AIHot: "https://site.invalid/items/a1"}}
	fixture := &v1ItemsFixture{responses: []publiccontent.V1ItemsResponse{{Items: []publiccontent.V1Item{item}}}}
	service := NewService(fixture, "Example", "https://site.invalid")
	answer, err := service.Search(context.Background(), SearchQuery{Window: "24h", Limit: 10, Text: "ai"}, time.Now())
	if err != nil || len(fixture.queries) != 1 || strings.Contains(answer, "精选里没有") {
		t.Fatalf("answer=%s calls=%d error=%v", answer, len(fixture.queries), err)
	}

	wantErr := errors.New("repository unavailable")
	fixture.err = wantErr
	_, err = service.Search(context.Background(), SearchQuery{Window: "24h", Limit: 10, Text: "ai"}, time.Now())
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestLatestAnswerHasSameHintsAndItemLineOrdering(t *testing.T) {
	items := []publiccontent.V1Item{
		{Title: "One", Source: publiccontent.V1ItemSource{Name: "Source"}, Links: publiccontent.V1ItemLinks{AIHot: "https://site.invalid/1", Original: "https://source.invalid/1"}, DiscoveredAt: "2026-10-07T02:00:00.000Z"},
		{Title: "Two", Source: publiccontent.V1ItemSource{Name: "Source"}, Links: publiccontent.V1ItemLinks{AIHot: "https://site.invalid/2", Original: "https://source.invalid/2"}, DiscoveredAt: "2026-10-07T01:00:00.000Z"},
	}
	answer := LatestAnswer(publiccontent.V1ItemsResponse{Items: items}, LatestQuery{Window: "24h", Mode: "all", Limit: 30}, "Example", nil, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	if strings.Index(answer, "[One]") > strings.Index(answer, "[Two]") || !strings.Contains(answer, "用户要全部就全列") || !strings.Contains(answer, "标题链接到 Example") {
		t.Fatalf("answer = %s", answer)
	}
	if got := v1ItemLines(items, "Example", nil, time.Now()); len(got) == 0 || !reflect.DeepEqual(got[len(got)-1], "") {
		t.Fatalf("item lines should retain trailing blank line, got %#v", got)
	}
}

type storyFixture struct {
	lookups []publiccontent.V1StoryLookup
	ids     []string
	story   publiccontent.V1StoryResponse
	found   bool
}

func (f *storyFixture) HotTopics(context.Context, time.Time, string, string) (publiccontent.HotTopicsResponse, error) {
	return publiccontent.HotTopicsResponse{}, nil
}

func (f *storyFixture) ResolveV1Story(_ context.Context, publicID string) (publiccontent.V1StoryLookup, error) {
	f.ids = append(f.ids, publicID)
	if len(f.lookups) == 0 {
		return publiccontent.V1StoryLookup{Kind: "not_found"}, nil
	}
	lookup := f.lookups[0]
	f.lookups = f.lookups[1:]
	return lookup, nil
}

func (f *storyFixture) V1Story(_ context.Context, storyID int64, _ time.Time, _ string) (publiccontent.V1StoryResponse, bool, error) {
	if storyID != 42 {
		return publiccontent.V1StoryResponse{}, false, nil
	}
	return f.story, f.found, nil
}

func TestStoryFollowsMergeAndRendersCappedTimeline(t *testing.T) {
	latest := "2026-10-07T02:00:00.000Z"
	first := "2026-10-06T02:00:00.000Z"
	var story publiccontent.V1StoryBody
	story.Title = "Model launch"
	story.Status = "active"
	story.SourceCount = 3
	story.ReportCount = 3
	story.FirstReportAt = first
	story.LatestAt = latest
	story.Latest = "New release"
	digest := "Several developments"
	story.Digest = &digest
	story.Links.AIHot = "https://site.invalid/story/new-id"
	story.Reports = []publiccontent.V1StoryReport{
		{Title: "Newest [report]", Source: publiccontent.V1StorySource{Name: "X：Researcher (@researcher)", FirstParty: true}, PublishedAt: latest, Links: publiccontent.V1StoryItemLinks{AIHot: "https://site.invalid/items/a1"}},
		{Title: "Older report", Source: publiccontent.V1StorySource{Name: "Media"}, PublishedAt: first, Links: publiccontent.V1StoryItemLinks{AIHot: "https://site.invalid/items/a2"}},
	}
	story.Storyline = []publiccontent.V1RelatedStory{{PublicID: "related-id", Title: "Related event"}}
	fixture := &storyFixture{lookups: []publiccontent.V1StoryLookup{{Kind: "merged", Target: "new-id"}, {Kind: "found", ID: 42}}, story: publiccontent.V1StoryResponse{Story: story}, found: true}
	service := NewService(fixture, "Example", "https://site.invalid")
	answer, found, err := service.Story(context.Background(), "old-id", 1, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	if err != nil || !found || !reflect.DeepEqual(fixture.ids, []string{"old-id", "new-id"}) {
		t.Fatalf("answer=%s found=%v ids=%#v error=%v", answer, found, fixture.ids, err)
	}
	for _, want := range []string{
		"# Example 事件：Model launch", "持续更新 · 3 篇报道 · 3 个信源", "事件页：https://site.invalid/story/new-id",
		"事件综述：Several developments", "1. 10-07 10:00 · Researcher（一手） · [Newest \\[report\\]](https://site.invalid/items/a1)",
		"相关事件：", "Related event：https://site.invalid/api/v1/agent/stories/related-id", "时间线只列了最新 1 篇，共 3 篇；要看更多加 limit（最多 50）。",
	} {
		if !strings.Contains(answer, want) {
			t.Errorf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestStoryAnswerWithoutDataAndLookupNotFound(t *testing.T) {
	fixture := &storyFixture{lookups: []publiccontent.V1StoryLookup{{Kind: "not_found"}}}
	service := NewService(fixture, "Example", "https://site.invalid")
	_, found, err := service.Story(context.Background(), "unknown", 20, time.Now())
	if err != nil || found {
		t.Fatalf("found=%v error=%v", found, err)
	}
	answer := StoryAnswer(publiccontent.V1StoryBody{}, 20, "Example", "https://site.invalid", time.Now())
	if !strings.Contains(answer, "报道时间线（从新到旧）") || !strings.Contains(answer, "不可信外部资料开始") {
		t.Fatalf("empty story answer differs from the existing renderer: %s", answer)
	}
}
