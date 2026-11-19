package topics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
)

type summaryRepository struct {
	rows []SummaryRow
}

func (r summaryRepository) Summaries(context.Context, []Definition, time.Time) ([]SummaryRow, error) {
	return r.rows, nil
}

type pageRepository struct {
	summaryRepository
	data PageData
	page int
}

func (r *pageRepository) Page(_ context.Context, _ Definition, page int, _ time.Time) (PageData, error) {
	r.page = page
	return r.data, nil
}

func TestLoadCatalogBuildsCompanyPatternsAndPreservesTopicOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "topics.json")
	data := `{"groups":[{"key":"company","name":"Companies","blurb":"Tracked companies"}],"topics":[{"slug":"example","name":"Example / EX","group":"company","entityId":"example","aliases":["ignored-for-title-pattern"],"definition":"Definition"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDUSTRY_TOPICS_FILE", path)
	groups, definitions, err := LoadCatalog(map[string]interests.Entity{"example": {Name: "Example Corp", Aliases: []string{"Example+Corp"}, OtherNames: []string{"Ex Labs"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(definitions) != 1 || definitions[0].Position != 0 || !reflect.DeepEqual(definitions[0].Tags, []string{"entity:example"}) {
		t.Fatalf("catalog order or company tags changed: groups=%#v topics=%#v", groups, definitions)
	}
	pattern := *definitions[0].Pattern
	for _, term := range []string{`Example`, `EX`, `Example Corp`, `Example\+Corp`, `Ex Labs`} {
		if !strings.Contains(pattern, term) {
			t.Fatalf("company pattern %q does not include %q", pattern, term)
		}
	}
	if strings.Contains(pattern, "ignored-for-title-pattern") || !strings.Contains(pattern, `(?<![A-Za-z])`) || !strings.Contains(pattern, `(?![A-Za-z])`) {
		t.Fatalf("company title pattern differs from the site rule: %q", pattern)
	}
}

/** TestLoadCatalogPreservesBundledPath verifies configuration loads from the relocated catalog. */
func TestLoadCatalogPreservesBundledPath(t *testing.T) {
	t.Setenv("INDUSTRY_TOPICS_FILE", "")
	expectedGroups, expectedTopics, err := LoadCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDUSTRY_TOPICS_FILE", "../../config/topics.json")
	groups, topics, err := LoadCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Regex alternatives are assembled from a map and can have equivalent orders.
	for i := range topics {
		topics[i].Pattern = nil
	}
	for i := range expectedTopics {
		expectedTopics[i].Pattern = nil
	}
	if len(topics) == 0 || !reflect.DeepEqual(groups, expectedGroups) || !reflect.DeepEqual(topics, expectedTopics) {
		t.Fatal("bundled path must load the same topic catalog")
	}
}

func TestListBuildsSummaryCountsIndexabilityAndLatest(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)
	old := now.Add(-31 * 24 * time.Hour)
	firstTitle, secondTitle := "newest", "older"
	id1, id2 := "1", "2"
	rows := []SummaryRow{
		{Slug: "field", ID: &id1, Title: &firstTitle, At: &recent},
		{Slug: "field", ID: &id2, Title: &secondTitle, At: &old},
		{Slug: "company", ID: &id1, Title: &firstTitle, At: &recent},
		{Slug: "empty"},
	}
	for i := 0; i < 18; i++ {
		id := "old"
		rows = append(rows, SummaryRow{Slug: "field", ID: &id, Title: &secondTitle, At: &old})
	}
	service := NewService(summaryRepository{rows: rows}, []Group{{Key: "company", Name: "Company", Blurb: "Tracked"}}, []Definition{
		{Slug: "company", Name: "Company One", Group: "company", Definition: "about one"},
		{Slug: "field", Name: "A field", Group: "field", Definition: "about field"},
		{Slug: "empty", Name: "Empty", Group: "field", Definition: "about empty"},
	})
	result, err := service.List(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 1 || len(result.Topics) != 3 || result.Topics[0].Slug != "company" || result.Topics[0].Brand == nil || result.Topics[0].Brand.Monogram != "C" {
		t.Fatalf("topic order or company monogram changed: %#v", result)
	}
	if result.Topics[0].Total != 1 || result.Topics[0].Recent != 1 || result.Topics[0].Indexable || result.Topics[0].Latest == nil || result.Topics[0].Latest.Title != firstTitle || result.Topics[0].Latest.At != recent.Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("company summary changed: %#v", result.Topics[0])
	}
	if result.Topics[1].Total != 20 || result.Topics[1].Recent != 1 || !result.Topics[1].Indexable || result.Topics[1].Brand != nil {
		t.Fatalf("field summary changed: %#v", result.Topics[1])
	}
	if result.Topics[2].Total != 0 || result.Topics[2].Recent != 0 || result.Topics[2].Indexable || result.Topics[2].Latest != nil {
		t.Fatalf("empty topic summary changed: %#v", result.Topics[2])
	}
}

func TestPageBuildsTopicSummaryAndFeedRowsAndValidatesPage(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	published := now.Add(-time.Hour)
	score := 91.6
	summary, reason, category, quotedZH := "summary", "why", "ai-models", "引用翻译"
	repo := &pageRepository{data: PageData{
		Total: 21, Recent: 1, PoolTotal: 42,
		Latest: &Latest{Title: "Newest", At: now.Add(-time.Minute).Format("2006-01-02T15:04:05.000Z")},
		Items: []PageItemRow{{ID: "article-1", Title: "Article", Summary: &summary, Reason: &reason, Category: &category,
			Tags: []string{"ai-models", "entity:openai"}, Score: &score, Channel: "news", PublishedAt: &published,
			TimelineAt: now, SourceName: "OpenAI（官网 RSS）", QuotedZH: &quotedZH}},
	}}
	service := NewService(repo, []Group{{Key: "company", Name: "Companies"}}, []Definition{{Slug: "openai", Name: "OpenAI", Group: "company", Definition: "Company topic"}})
	result, err := service.Page(context.Background(), "openai", 1, now)
	if err != nil || result == nil {
		t.Fatalf("topic page = %#v, %v", result, err)
	}
	if repo.page != 1 || result.Topic.Total != 21 || result.Topic.Recent != 1 || !result.Topic.Indexable || result.Topic.Latest == nil || result.Topic.Latest.Title != "Newest" || result.Topic.PoolTotal != 42 || result.Topic.GroupName != "Companies" {
		t.Fatalf("topic summary changed: %#v page=%d", result.Topic, repo.page)
	}
	if result.PageCount != 2 || result.PageSize != 20 || result.Modules == nil || len(result.Items) != 1 {
		t.Fatalf("pagination or module response changed: %#v", result)
	}
	item := result.Items[0]
	if item.ID != "article-1" || item.Source.Name != "OpenAI" || !reflect.DeepEqual(item.Tags, []string{"ai-models"}) || item.Score == nil || *item.Score != 92 || item.X != nil || item.Reason != &reason || item.PublishedAt == nil || *item.PublishedAt != published.Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("feed item projection changed: %#v", item)
	}
	if missing, err := service.Page(context.Background(), "missing", 1, now); err != nil || missing != nil {
		t.Fatalf("unknown topic page = %#v, %v", missing, err)
	}
	if invalid, err := service.Page(context.Background(), "openai", 1.5, now); err != nil || invalid != nil {
		t.Fatalf("fractional topic page = %#v, %v", invalid, err)
	}
	if outside, err := service.Page(context.Background(), "openai", 3, now); err != nil || outside != nil {
		t.Fatalf("out-of-range topic page = %#v, %v", outside, err)
	}
}

func TestFeedXViewKeepsSignedMediaAndFullTextPolicy(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	quotedZH := "引用翻译"
	row := PageItemRow{ID: "x-1", Title: "Post", Channel: "x", BodyMode: "summary", QuotedZH: &quotedZH, XPost: json.RawMessage(`{
		"authorName":"Author","handle":"author","avatarUrl":"https://images.example/avatar.png",
		"quoted":{"authorName":"Quoted","handle":"quoted","text":"Original quote","url":"https://x.com/quoted/status/1"},
		"media":[{"kind":"image","url":"https://images.example/post.png","poster":"https://images.example/poster.png","width":640,"height":480,"alt":"post"},
		{"kind":"video","url":"https://images.example/video.mp4","width":640,"height":480}]
	}`)}
	item, err := toFeedItem(row, now, RuntimeConfig{ImageProxySecret: "signing-secret", XPostIsFullText: false})
	if err != nil || item.X == nil {
		t.Fatalf("licensed X card = %#v, %v", item.X, err)
	}
	if item.X.AuthorName != "Author" || item.X.AvatarURL == nil || !strings.HasPrefix(*item.X.AvatarURL, "/api/img-proxy?u=") || item.X.AvatarSrcSet == nil || len(item.X.Media) != 2 {
		t.Fatalf("X author/avatar/media shape changed: %#v", item.X)
	}
	if item.X.Quoted == nil || item.X.Quoted.Translation == nil || *item.X.Quoted.Translation != quotedZH || item.X.Media[0].FullURL == nil || item.X.Media[0].SrcSet == nil || item.X.Media[0].Poster == nil {
		t.Fatalf("quoted translation or responsive media missing: %#v", item.X)
	}
	if hidden, err := toFeedItem(row, now, RuntimeConfig{ImageProxySecret: "signing-secret", XPostIsFullText: true}); err != nil || hidden.X != nil {
		t.Fatalf("summary-only X post bypassed full-text policy: x=%#v err=%v", hidden.X, err)
	}
	row.BodyMode = "full"
	if _, err := toFeedItem(row, now, RuntimeConfig{XPostIsFullText: true}); err == nil || !strings.Contains(err.Error(), "IMG_PROXY_SIGN_SECRET") {
		t.Fatalf("missing image signing secret did not fail safely: %v", err)
	}
}
