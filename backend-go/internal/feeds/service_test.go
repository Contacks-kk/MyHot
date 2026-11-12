package feeds

import (
	"context"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/publiccontent"
)

type feedItemsFake struct {
	query ItemQuery
	rows  []Item
	err   error
}

func (f *feedItemsFake) Items(_ context.Context, query ItemQuery) ([]Item, error) {
	f.query = query
	return f.rows, f.err
}

type feedReportsFake struct {
	kind  string
	limit int
	rows  []publiccontent.V1FeedIssue
}

func (f *feedReportsFake) V1FeedIssues(_ context.Context, kind string, limit int, _ time.Time, _, _, _, _ string) ([]publiccontent.V1FeedIssue, error) {
	f.kind, f.limit = kind, limit
	return f.rows, nil
}

func testFeedService(repo *feedItemsFake, reports *feedReportsFake) *Service {
	return NewService(repo, reports, Config{
		SiteURL: "https://myhot.example", SiteName: "Example", Subject: "AI", ImageProxySecret: "test-secret",
		Categories:    []Category{{Key: "models", Label: "模型", FeedLabel: "模型发布"}, {Key: "other", Label: "其他"}, {Key: "tip", Label: "技巧"}},
		CategoryMerge: map[string]string{"tip": "other"}, FeedLabels: map[string]string{"other": "综合"},
		AllLeavesOut: []string{"实验性内容"}, DailyTime: "08:00", WeeklyTime: "10:00", MonthlyTime: "10:30",
	})
}

func TestItemFeedPreservesSummaryAndCategoryProjection(t *testing.T) {
	now := time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)
	repo := &feedItemsFake{rows: []Item{{
		ID: "item_1", Title: "模型 <更新>", Summary: stringPointer("摘要 & 细节"), URL: "https://source.example/story?a=1&b=2",
		Category: stringPointer("tip"), PublishedAt: &now, SourceName: "X：Author (@author)",
	}}}
	service := testFeedService(repo, &feedReportsFake{})
	body, err := service.ItemFeed(context.Background(), "selected", "other", now)
	if err != nil {
		t.Fatal(err)
	}
	if repo.query.Kind != "selected" || len(repo.query.CategoryKeys) != 2 || repo.query.CategoryKeys[0] != "other" || repo.query.CategoryKeys[1] != "tip" {
		t.Fatalf("public category merge was not applied: %#v", repo.query)
	}
	for _, want := range []string{"<title>Example — 综合</title>", "/feed/category/other.xml", "<![CDATA[模型 <更新>]]>", "摘要 &amp; 细节", "<category>综合</category>", "noreply@myhot.example (Author)", "Thu, 08 Oct 2026 03:00:00 GMT"} {
		if !strings.Contains(body, want) {
			t.Errorf("RSS missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "content:encoded") {
		t.Fatal("summary feed included an inline body")
	}
}

func TestItemFeedDoesNotInventPublicationDate(t *testing.T) {
	service := testFeedService(&feedItemsFake{rows: []Item{
		{ID: "unknown_date", Title: "Unknown date", URL: "https://source.example/unknown", Summary: stringPointer("summary")},
		{ID: "known_date", Title: "Known date", URL: "https://source.example/known", Summary: stringPointer("summary"), PublishedAt: timePointer(time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC))},
	}}, &feedReportsFake{})
	body, err := service.ItemFeed(context.Background(), "all", "", time.Date(2026, 10, 8, 4, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	entries := strings.Split(body, "<item>")
	var unknown, known string
	for _, entry := range entries {
		switch {
		case strings.Contains(entry, "unknown_date"):
			unknown = entry
		case strings.Contains(entry, "known_date"):
			known = entry
		}
	}
	if unknown == "" || strings.Contains(unknown, "<pubDate>") {
		t.Fatalf("unknown publication time was fabricated: %s", unknown)
	}
	if !strings.Contains(known, "<pubDate>Thu, 08 Oct 2026 03:00:00 GMT</pubDate>") {
		t.Fatalf("trusted publication time is missing: %s", known)
	}
}

func TestFullFeedIncludesOnlyRedistributableRowsAndSignsBodyImages(t *testing.T) {
	now := time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)
	falseValue := false
	repo := &feedItemsFake{rows: []Item{
		{ID: "allowed", Title: "Allowed", URL: "https://source.example/allowed", Syndicate: true, BodyHTML: stringPointer(`<p>正文</p><img src="https://images.example/a.png">`)},
		{ID: "denied", Title: "Denied", URL: "https://source.example/denied", Syndicate: false, BodyHTML: stringPointer("<p>不能转载</p>"), TranslationComplete: &falseValue},
	}}
	body, err := testFeedService(repo, &feedReportsFake{}).ItemFeed(context.Background(), "selected-full", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "<content:encoded>") || !strings.Contains(body, "本文由 Example 聚合整理") || !strings.Contains(body, "https://myhot.example/api/img-proxy?") {
		t.Fatalf("licensed full content or signed image missing: %s", body)
	}
	if strings.Contains(body, "<content:encoded><![CDATA[<p>不能转载</p>") {
		t.Fatal("full text was included for an unlicensed row")
	}
}

func TestCDATAClosesAndReopensAroundTerminator(t *testing.T) {
	if got, want := cdata("before]]>after"), "<![CDATA[before]]]]><![CDATA[>after]]>"; got != want {
		t.Fatalf("CDATA = %q, want %q", got, want)
	}
}

func TestFullFeedRequiresImageSigningSecretOnlyWhenABodyContainsImages(t *testing.T) {
	service := NewService(&feedItemsFake{rows: []Item{{ID: "body", Title: "Body", Syndicate: true, BodyHTML: stringPointer(`<img src="https://images.example/a.png">`)}}}, nil, Config{SiteURL: "https://myhot.example", SiteName: "Example"})
	if _, err := service.ItemFeed(context.Background(), "selected-full", "", time.Now()); err == nil {
		t.Fatal("full feed returned an unsigned body image")
	}
	service = NewService(&feedItemsFake{rows: []Item{{ID: "body", Title: "Body", Syndicate: true, BodyHTML: stringPointer("<p>Plain body</p>")}}}, nil, Config{SiteURL: "https://myhot.example", SiteName: "Example"})
	if _, err := service.ItemFeed(context.Background(), "selected-full", "", time.Now()); err != nil {
		t.Fatalf("non-image licensed body should not need a proxy secret: %v", err)
	}
}

func TestReportFeedKeepsConfiguredRetentionAndIssueShape(t *testing.T) {
	reports := &feedReportsFake{rows: []publiccontent.V1FeedIssue{{
		Key: "2026-10-07", GeneratedAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC),
		Headline: stringPointer("头条"), LeadParagraph: stringPointer("导语"),
		Sections: []publiccontent.V1FeedIssueSection{{Label: "模型", Items: []publiccontent.V1FeedIssueItem{{Title: "发布", Link: "https://myhot.example/items/a"}}}},
	}}}
	body, err := testFeedService(&feedItemsFake{}, reports).ReportFeed(context.Background(), "daily", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if reports.kind != "daily" || reports.limit != 30 {
		t.Fatalf("daily retention = %s/%d", reports.kind, reports.limit)
	}
	for _, want := range []string{"<title>Example 日报</title>", "Example 日报 · 2026-10-07 — 头条", "<p>导语</p>", "<strong>模型</strong>", "daily-2026-10-07"} {
		if !strings.Contains(body, want) {
			t.Errorf("report feed missing %q\n%s", want, body)
		}
	}
}

func stringPointer(value string) *string { return &value }

func timePointer(value time.Time) *time.Time { return &value }
