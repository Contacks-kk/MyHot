package publication

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/model"
)

func TestStableJSONSortsNestedObjectKeysLikeLegacyPublisher(t *testing.T) {
	got, err := stableJSON(map[string]any{"z": map[string]any{"b": 2, "a": "<value>"}, "a": []any{true, nil}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":[true,null],"z":{"a":"<value>","b":2}}` {
		t.Fatalf("stable JSON = %s", got)
	}
}

func TestStableJSONPreservesLiteralBackslashEscapes(t *testing.T) {
	separator := "line\u2028separator"
	literal := `text\\u2028`
	got, err := stableJSON(map[string]string{"separator": separator, "literal": literal})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"literal\":\"text\\\\\\\\u2028\",\"separator\":\"line\u2028separator\"}"
	if string(got) != want {
		t.Fatalf("stable JSON = %q, want %q", got, want)
	}
}

func TestStableJSONLeavesJavaScriptLineSeparatorsUnescaped(t *testing.T) {
	value := "line" + string(rune(0x2028)) + "separator"
	got, err := stableJSON(map[string]string{"value": value})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"value\":\"" + value + "\"}"
	if string(got) != want {
		t.Fatalf("stable JSON = %q, want %q", got, want)
	}
}

func TestLedgerPayloadKeepsExternalShapeAndMillisecondDates(t *testing.T) {
	settings := interests.PublicationSettings{PublicCategories: interests.PublicCategories{Merge: map[string]string{"ai-model": "technology"}}}
	repository := NewRepository(nil, nil, settings, "Example", "https://example.test")
	articleID := "article-1"
	category := "ai-model"
	original := "Original"
	summary := "Summary"
	reason := "Reason"
	score := 8.5
	published := time.Date(2026, 10, 9, 1, 2, 3, 987654321, time.FixedZone("offset", 8*60*60))
	discovered := time.Date(2026, 10, 9, 2, 3, 4, 123456789, time.UTC)
	payload := repository.ledgerPayload(model.Publication{
		ArticleID: articleID, Title: "Title", OriginalTitle: &original, Summary: &summary, URL: "https://publisher.test/item",
		PublishedAt: &published, DiscoveredAt: discovered, Category: &category, Score: &score, Selected: true, Reason: &reason,
	}, "OpenAI（官网）")
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{`"source":{"name":"OpenAI"}`, `"category":"technology"`, `"score":9`,
		`"publishedAt":"2026-10-08T17:02:03.987Z"`, `"discoveredAt":"2026-10-09T02:03:04.123Z"`,
		`"aihot":"https://example.test/items/article-1"`, `"attribution":{"name":"Example","url":"https://example.test/items/article-1"}`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("payload missing %s: %s", expected, text)
		}
	}
}

func TestTruncateJSUsesUTF16CodeUnits(t *testing.T) {
	value := strings.Repeat("a", 11999) + "😀" + "tail"
	got := truncateJS(value, 12000)
	if len([]rune(got)) != 12000 || !strings.HasSuffix(got, "�") {
		t.Fatalf("JS slice compatibility: rune count=%d suffix=%q", len([]rune(got)), got[len(got)-4:])
	}
}
