package jobs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDailyInputRetainsEveryArticleWhenSummariesAreShortened(t *testing.T) {
	entries := make([]dailyEntry, 240)
	for i := range entries {
		entries[i] = dailyEntry{ArticleID: fmt.Sprint(i), Score: 90, Title: "文章标题", Source: "公共信源", Summary: strings.Repeat("摘要", 500)}
	}
	body, err := dailyInput(entries)
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != len(entries) || len(body) > 60000 {
		t.Fatalf("input count = %d, bytes = %d", len(items), len(body))
	}
	for i, item := range items {
		if item["articleId"] != entries[i].ArticleID || item["title"] != entries[i].Title || item["source"] != entries[i].Source {
			t.Fatalf("missing original metadata at %d", i)
		}
	}
	if entries[0].Summary != strings.Repeat("摘要", 500) {
		t.Fatal("saved report entries were mutated")
	}
}

func TestDailyInputFailsInsteadOfDroppingArticles(t *testing.T) {
	entries := make([]dailyEntry, 1000)
	for i := range entries {
		entries[i] = dailyEntry{ArticleID: fmt.Sprint(i), Title: strings.Repeat("标题", 80), Source: "源"}
	}
	if _, err := dailyInput(entries); err == nil {
		t.Fatal("oversized metadata should fail explicitly")
	}
}
