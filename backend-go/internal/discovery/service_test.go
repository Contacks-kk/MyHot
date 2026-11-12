package discovery

import (
	"context"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/publiccontent"
	"aihot/backend-go/internal/topics"
)

type discoveryRepository struct {
	reports publiccontent.DiscoveryReports
}

func (r discoveryRepository) DiscoveryReports(context.Context) (publiccontent.DiscoveryReports, error) {
	return r.reports, nil
}

type discoveryTopics struct{ response topics.Response }

func (t discoveryTopics) List(context.Context, time.Time) (topics.Response, error) {
	return t.response, nil
}

func TestLLMSTxtListsOnlyAvailableReportsAndIndexableTopics(t *testing.T) {
	config := Config{
		SiteName: "Example", SiteURL: "https://example.test", Description: "Example description", Version: "4.0.0",
		Subject: "AI", DailyTime: "08:00", WeeklyTime: "10:00", MonthlyTime: "10:30", ReportNoun: "大事", MCPPrefix: "example",
		CategoryKeys: []string{"news", "paper", "release"},
		TopicGroups:  []topics.Group{{Key: "company", Name: "公司"}, {Key: "field", Name: "方向"}},
		Feeds: map[string]Feed{
			"selected":      {Path: "/feed.xml", Description: "selected"},
			"selected-full": {Path: "/feed/full.xml", Description: "selected full"},
			"all":           {Path: "/feed/all.xml", Description: "all"},
			"daily":         {Path: "/feed/daily.xml", Description: "daily"},
			"weekly":        {Path: "/feed/weekly.xml", Description: "weekly"},
			"monthly":       {Path: "/feed/monthly.xml", Description: "monthly"},
		},
	}
	service := NewService(discoveryRepository{reports: publiccontent.DiscoveryReports{Daily: true, Weekly: false, Monthly: true}}, discoveryTopics{topics.Response{
		Groups: config.TopicGroups,
		Topics: []topics.Summary{
			{Slug: "indexed", Name: "Indexed", Definition: "Current", Indexable: true},
			{Slug: "small", Name: "Small", Definition: "Not indexed", Indexable: false},
		},
	}}, config)

	result, err := service.LLMSTxt(context.Background(), time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Example",
		"[日报 RSS](https://example.test/feed/daily.xml): daily",
		"[月报 RSS](https://example.test/feed/monthly.xml): 每月 1 日 10:30",
		"slug 支持 news / paper / release",
		"[Indexed](https://example.test/topics/indexed): Current",
		"需要已验证的用户会话 Cookie",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("llms.txt missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"周报 RSS", "[公开 API · 周报列表]", "Small", "匿名只读", "MCP 同样匿名只读",
	} {
		if strings.Contains(result, unwanted) {
			t.Errorf("llms.txt unexpectedly contains %q", unwanted)
		}
	}
	if !strings.HasSuffix(result, "\n") {
		t.Fatal("llms.txt must end with a newline")
	}
}

func TestSubjectPhrasingMatchesSiteCopy(t *testing.T) {
	for _, test := range []struct{ subject, with, after string }{
		{"AI", "AI 热点榜", "每日 AI 精选动态"},
		{"人工智能", "人工智能热点榜", "每日人工智能精选动态"},
	} {
		if got := withSubject("热点榜", test.subject); got != test.with {
			t.Errorf("withSubject(%q) = %q, want %q", test.subject, got, test.with)
		}
		if got := subjectAfter("每日", "精选动态", test.subject); got != test.after {
			t.Errorf("subjectAfter(%q) = %q, want %q", test.subject, got, test.after)
		}
	}
}
