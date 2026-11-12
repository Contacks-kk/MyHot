package publiccontent

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

type v1ItemsRepo struct {
	rows       []V1ItemRecord
	err        error
	query      V1ItemsQuery
	now        time.Time
	after      *time.Time
	afterID    string
	searchTerm []string
}

func (r *v1ItemsRepo) Availability(context.Context, []string) ([]Record, error) { return nil, nil }
func (r *v1ItemsRepo) StoryFollowups(context.Context, string, time.Time) ([]FollowupMember, error) {
	return nil, nil
}
func (r *v1ItemsRepo) GroupReports(context.Context, GroupReportsQuery, time.Time) ([]GroupReportRow, error) {
	return nil, nil
}
func (r *v1ItemsRepo) V1Items(_ context.Context, query V1ItemsQuery, now time.Time, after *time.Time, afterID string, terms []string) ([]V1ItemRecord, error) {
	r.query, r.now, r.after, r.afterID, r.searchTerm = query, now, after, afterID, terms
	return r.rows, r.err
}

func TestV1ItemsBuildsCompatibleResponseAndBoundCursor(t *testing.T) {
	now := time.Date(2026, 10, 7, 3, 4, 5, 678_901_000, time.FixedZone("local", 8*60*60))
	sortAt := time.Date(2026, 10, 7, 3, 4, 5, 678_000_000, time.UTC)
	score := 82.6
	falseSelected := false
	trueSelected := true
	reason := "selected reason"
	repository := &v1ItemsRepo{rows: []V1ItemRecord{
		{ID: "article_1", Title: "Article one", SourceName: "Raw source（note）", URL: "https://source.example/1", DiscoveredAt: sortAt, Score: &score, Selected: trueSelected, Reason: &reason, SortAt: sortAt},
		{ID: "article_2", Title: "Article two", SourceName: "Source two", URL: "https://source.example/2", DiscoveredAt: sortAt.Add(-time.Minute), Selected: falseSelected, Reason: &reason, SortAt: sortAt.Add(-time.Minute)},
	}}
	query := V1ItemsQuery{Mode: "selected", Window: "7d", By: "timeline", Limit: 1}
	response, err := NewService(repository).V1Items(context.Background(), query, now, "https://myhot.example/")
	if err != nil {
		t.Fatal(err)
	}
	if response.SchemaVersion != 1 || response.Query.Mode != "selected" || response.Query.Category != nil || response.Query.Search != nil || response.Query.Ordering != "timelineDesc" || response.Page.Count != 1 || !response.Page.HasMore || response.Page.NextCursor == nil {
		t.Fatalf("response = %#v", response)
	}
	item := response.Items[0]
	if item.ID != "article_1" || item.Source.Name != "Raw source（note）" || item.Links.AIHot != "https://myhot.example/items/article_1" || item.Selected != true || item.Score == nil || *item.Score != 83 || item.Reason == nil || *item.Reason != reason {
		t.Fatalf("first item = %#v", item)
	}
	if got, want := v1ItemsBinding(query), "92a7ccca0f39"; got != want {
		t.Fatalf("query binding = %s, want TypeScript stableJson binding %s", got, want)
	}
	cursor, err := decodeItemCursor(*response.Page.NextCursor)
	if err != nil || cursor.ID != "article_1" || cursor.Binding != "92a7ccca0f39" || cursor.At != sortAt.UnixMilli() {
		t.Fatalf("next cursor = %#v, err=%v", cursor, err)
	}
	repository.rows = repository.rows[1:]
	continued, err := NewService(repository).V1Items(context.Background(), V1ItemsQuery{Mode: "selected", Window: "7d", By: "timeline", Limit: 1, Cursor: response.Page.NextCursor}, now, "https://myhot.example")
	if err != nil {
		t.Fatal(err)
	}
	if continued.Items[0].Reason != nil || continued.Query.Ordering != "timelineDesc" || repository.after == nil || !repository.after.Equal(sortAt) || repository.afterID != "article_1" {
		t.Fatalf("continuation = %#v, repo after=%v/%s", continued, repository.after, repository.afterID)
	}

	query.Search = stringPtr("  OpenAI  ")
	if got, want := itemSearchTerms(*query.Search), []string{"openai"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("search terms = %#v, want %#v", got, want)
	}
	if got := itemSearchTerms(`A% \ B_C`); !reflect.DeepEqual(got, []string{"a\\%", "\\\\", "b\\_c"}) {
		t.Fatalf("escaped search terms = %#v", got)
	}
	if got := itemSearchTerms("one two three four five six seven"); len(got) != 6 {
		t.Fatalf("term count = %d, want six", len(got))
	}
}

func TestV1ItemsRejectsMalformedAndOutOfWindowCursor(t *testing.T) {
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	query := V1ItemsQuery{Mode: "all", Window: "24h", By: "published", Limit: 10}
	service := NewService(&v1ItemsRepo{})
	for _, cursor := range []*string{
		stringPtr("bad"),
		stringPtr(encodeItemCursor(itemCursor{At: now.UnixMilli(), ID: "article_1", Binding: "wrong"})),
		stringPtr(encodeItemCursor(itemCursor{At: now.Add(-25 * time.Hour).UnixMilli(), ID: "article_1", Binding: v1ItemsBinding(query)})),
		stringPtr(encodeItemCursor(itemCursor{At: math.MaxInt64, ID: "article_1", Binding: v1ItemsBinding(query)})),
	} {
		query.Cursor = cursor
		if _, err := service.V1Items(context.Background(), query, now, "https://example.test"); err == nil {
			t.Fatalf("cursor %q should be invalid", *cursor)
		} else {
			var invalid InvalidCursorError
			if !errors.As(err, &invalid) {
				t.Fatalf("cursor error = %T %v, want InvalidCursorError", err, err)
			}
		}
	}
}

func TestV1ItemsRepositoryErrorsAndSearchCapacityAreMapped(t *testing.T) {
	repository := &v1ItemsRepo{err: errors.New("database unavailable")}
	if _, err := NewService(repository).V1Items(context.Background(), V1ItemsQuery{Mode: "all", Window: "7d", By: "timeline", Limit: 50}, time.Now(), "https://example.test"); err == nil {
		t.Fatal("expected repository error")
	}
	search := "openai news"
	query := V1ItemsQuery{Mode: "all", Window: "7d", By: "timeline", Search: &search, Limit: 50}
	for range 4 {
		publicSearchSlots <- struct{}{}
	}
	publicSearchWaiters.Store(8)
	_, err := NewService(&v1ItemsRepo{}).V1Items(context.Background(), query, time.Now(), "https://example.test")
	publicSearchWaiters.Store(0)
	for range 4 {
		<-publicSearchSlots
	}
	var busy SearchBusyError
	if !errors.As(err, &busy) || busy.RetryAfter != 5 {
		t.Fatalf("search capacity error = %T %v", err, err)
	}
}
