package collector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/receipts"
)

type xSearchCall struct{ query, searchType, cursor, subject, window string }
type scriptedXProvider struct {
	search func(xSearchCall) (XPage, error)
	calls  []xSearchCall
}

func (p *scriptedXProvider) Search(_ context.Context, query, searchType, cursor, subject, window string) (XPage, error) {
	call := xSearchCall{query: query, searchType: searchType, cursor: cursor, subject: subject, window: window}
	p.calls = append(p.calls, call)
	return p.search(call)
}

func TestReadXSearchPaginatesDeduplicatesAndCarriesReceiptIDs(t *testing.T) {
	provider := &scriptedXProvider{search: func(call xSearchCall) (XPage, error) {
		if call.query != "from:demo since_id:100" || call.searchType != "Latest" || call.subject != "source:demo" || call.window == "" {
			t.Fatalf("search call = %#v", call)
		}
		switch call.cursor {
		case "":
			return XPage{ReceiptID: 11, NextCursor: "page-2", Tweets: []tweet{{ID: "103"}, {ID: "102"}}}, nil
		case "page-2":
			return XPage{ReceiptID: 12, Tweets: []tweet{{ID: "102"}, {ID: "101", Retweeted: &tweet{ID: "99"}}}}, nil
		default:
			t.Fatalf("unexpected cursor %q", call.cursor)
			return XPage{}, nil
		}
	}}
	service := &Service{XProvider: provider}
	result, err := service.readXSearch(context.Background(), "from:demo", "100", nil, "source:demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tweets) != 2 || result.Tweets[0].ID != "103" || result.Tweets[1].ID != "102" {
		t.Fatalf("tweets = %#v", result.Tweets)
	}
	if result.LastID != "103" || result.Pages != 2 || !reflect.DeepEqual(result.ReceiptIDs, []int64{11, 12}) {
		t.Fatalf("read result = %#v", result)
	}
}

func TestPreviewXUsesSavedCursorAndAdmissionWithoutPersistingCandidates(t *testing.T) {
	provider := &scriptedXProvider{search: func(call xSearchCall) (XPage, error) {
		if call.query != "from:demo since_id:100" || call.searchType != "Top" || call.cursor != "" || call.subject != "source:demo" || call.window == "" {
			t.Fatalf("preview search call = %#v", call)
		}
		return XPage{Tweets: []tweet{{ID: "101", CreatedAt: "2026-10-08T02:03:04Z", FullText: "Sponsored research launch", User: xUser{Handle: "writer"}}}}, nil
	}}
	service := &Service{XProvider: provider}
	result, err := service.PreviewX(context.Background(), "demo", map[string]any{
		"query": "from:demo", "searchType": "Top",
		"itemUrlPrefixRewrite": map[string]any{"from": "https://x.com/", "to": "https://archive.example/x/"},
		"ingestNoiseFilter":    map[string]any{"dropMarkersTitleOnly": []any{"sponsored"}, "keepIfMatches": []any{"launch"}},
	}, map[string]any{"lastTweetId": "100"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || len(result.Items) != 1 || result.Items[0].URL != "https://archive.example/x/writer/status/101" || result.Items[0].Title != "Sponsored research launch" || result.Items[0].Excerpt != "Sponsored research launch" || result.Items[0].PublishedAt == nil {
		t.Fatalf("preview result = %#v", result)
	}
}

func TestReadXSearchKeepsLaterPageFailureAsBacklog(t *testing.T) {
	pageCalls := 0
	provider := &scriptedXProvider{search: func(call xSearchCall) (XPage, error) {
		if call.cursor == "" {
			return XPage{ReceiptID: 21, NextCursor: "next", Tweets: []tweet{{ID: "201"}}}, nil
		}
		pageCalls++
		return XPage{}, errors.New("temporarily unavailable")
	}}
	result, err := (&Service{XProvider: provider}).readXSearch(context.Background(), "from:demo", "200", nil, "source:demo", "Latest")
	if err != nil {
		t.Fatal(err)
	}
	if pageCalls != 2 || !result.Truncated || len(result.Backlog) != 1 || result.Backlog[0] != (XBacklog{Query: "from:demo since_id:200", Next: "next"}) {
		t.Fatalf("read result = %#v; repeated failed page calls=%d", result, pageCalls)
	}
	if !reflect.DeepEqual(result.ReceiptIDs, []int64{21}) || result.LastID != "201" {
		t.Fatalf("received page was not retained: %#v", result)
	}
}

func TestReadXSearchDropsProviderRejectedExpiredBacklog(t *testing.T) {
	provider := &scriptedXProvider{search: func(call xSearchCall) (XPage, error) {
		if call.cursor != "expired" {
			return XPage{ReceiptID: 31, Tweets: []tweet{{ID: "301"}}}, nil
		}
		return XPage{}, xProviderError{status: 400, message: "socialdata HTTP 400"}
	}}
	result, err := (&Service{XProvider: provider}).readXSearch(context.Background(), "from:demo", "300", []XBacklog{{Query: "from:demo since_id:100", Next: "expired"}}, "source:demo", "Latest")
	if err != nil || result.Dropped != 1 || len(result.Backlog) != 0 || result.LastID != "301" {
		t.Fatalf("read result = %#v, error=%v", result, err)
	}
}

func TestXWindowKeyMatchesTypeScriptReceiptBucket(t *testing.T) {
	input := time.Date(2026, 10, 8, 12, 29, 59, 999_000_000, time.FixedZone("offset", 8*60*60))
	if got := xWindowKey(input); got != "2026-10-08T04:00:00.000Z" {
		t.Fatalf("receipt bucket = %q", got)
	}
}

func TestXAdmissionRewritesUrlsAndAppliesNoiseKeepRules(t *testing.T) {
	items := []item{
		{URL: "https://x.com/writer/status/1", IdentityKey: "x:1", Title: "Sponsored giveaway"},
		{URL: "https://x.com/writer/status/2", IdentityKey: "x:2", Title: "Sponsored product launch"},
	}
	config := settings{
		"itemUrlPrefixRewrite": map[string]any{"from": "https://x.com/", "to": "https://archive.example/x/"},
		"ingestNoiseFilter":    map[string]any{"dropMarkersTitleOnly": []any{"sponsored"}, "keepIfMatches": []any{"launch"}},
	}
	admitted := admitXItems(items, config)
	if len(admitted) != 1 || admitted[0].IdentityKey != "x:2" || admitted[0].URL != "https://archive.example/x/writer/status/2" {
		t.Fatalf("admitted X posts = %#v", admitted)
	}
}

func TestUnsupportedXConfigFindsUnknownAndNestedKeys(t *testing.T) {
	config := settings{
		"query":             "from:writer -filter:replies",
		"adapter":           "unknown",
		"ingestNoiseFilter": map[string]any{"dropMarkers": []any{"promo"}, "misspelled": true},
	}
	got := unsupportedXConfig(config)
	if len(got) != 2 || !containsString(got, "adapter") || !containsString(got, "ingestNoiseFilter.misspelled") {
		t.Fatalf("unsupported X config = %#v", got)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestTweetCandidatePreservesXPostMediaAndArticleBodyStatus(t *testing.T) {
	var post tweet
	input := `{"id_str":"123","tweet_created_at":"2026-10-01T02:03:04Z","full_text":"Breaking &amp; useful https://t.co/a https://x.com/i/article/99","lang":"en","user":{"name":"Author","screen_name":"writer","profile_image_url_https":"https://img/x_normal.png"},"entities":{"urls":[{"url":"https://t.co/a","expanded_url":"https://example.com/story"}],"media":[{"type":"photo","media_url_https":"https://img/p.jpg","original_info":{"width":640,"height":480}}]},"favorite_count":4,"retweet_count":2,"views_count":9}`
	if err := json.Unmarshal([]byte(input), &post); err != nil {
		t.Fatal(err)
	}
	candidate := tweetCandidate(post)
	if candidate.URL != "https://x.com/writer/status/123" || candidate.Language != "en" || candidate.BodyStatus != "pending" || !strings.Contains(candidate.Body, "https://example.com/story") {
		t.Fatalf("candidate = %#v", candidate)
	}
	var xPost map[string]any
	if err := json.Unmarshal(candidate.XPost, &xPost); err != nil {
		t.Fatal(err)
	}
	if xPost["avatarUrl"] != "https://img/x_bigger.png" || xPost["lang"] != "en" {
		t.Fatalf("X post metadata = %#v", xPost)
	}
	media := xPost["media"].([]any)[0].(map[string]any)
	if media["kind"] != "image" || media["poster"] != nil || media["width"] != float64(640) {
		t.Fatalf("media = %#v", media)
	}
	var raw map[string]any
	_ = json.Unmarshal(candidate.Raw, &raw)
	if raw["favorite"] != float64(4) || raw["retweet"] != float64(2) || raw["views"] != float64(9) {
		t.Fatalf("metrics = %#v", raw)
	}
}

func TestXShardPlanningIsStableBoundedAndSeparatedByMode(t *testing.T) {
	rows := []xShardRow{}
	for i := 0; i < 60; i++ {
		mode := "editorial"
		if i%2 == 1 {
			mode = "hot_signal"
		}
		config, _ := json.Marshal(map[string]any{"query": "from:handle_" + strings.Repeat("x", 8) + " -filter:replies", "publisherRole": "organization"})
		cursor, _ := json.Marshal(map[string]string{"lastTweetId": "123"})
		rows = append(rows, xShardRow{ID: strings.Repeat("s", 1) + strings.Repeat("0", 2) + string(rune('a'+i%26)) + string(rune('A'+i/26)), Kind: "x_search", ParticipationMode: mode, Config: config, Cursor: cursor})
	}
	first := planXShards(rows)
	reversed := append([]xShardRow(nil), rows...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	second := planXShards(reversed)
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("shards = %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Key != second[i].Key || len(first[i].SourceIDs) > 24 {
			t.Fatalf("shards are unstable or overfull: %#v vs %#v", first[i], second[i])
		}
		for _, id := range first[i].SourceIDs {
			if id == "" {
				t.Fatal("empty source id in shard")
			}
		}
	}
}

type receiptMemory struct {
	request receipts.Request
	outcome receipts.Outcome
}

func (r *receiptMemory) Claim(_ context.Context, request receipts.Request, _ string) (receipts.Claim, error) {
	r.request = request
	return receipts.Claim{ReceiptID: 44, AttemptID: 55}, nil
}
func (r *receiptMemory) RecordFailure(context.Context, receipts.Claim, string, string, time.Duration) error {
	return nil
}
func (r *receiptMemory) RecordReceived(_ context.Context, _ receipts.Claim, outcome receipts.Outcome, _ time.Duration) error {
	r.outcome = outcome
	return nil
}
func (r *receiptMemory) Complete(context.Context, int64) error       { return nil }
func (r *receiptMemory) Reject(context.Context, int64, string) error { return nil }

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func TestSocialDataSearchUsesPaidReceiptAndCapturesUsage(t *testing.T) {
	repository := &receiptMemory{}
	var captured *http.Request
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		captured = request
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tweets":[{"id_str":"1"}],"next_cursor":"page-2"}`))}, nil
	})
	provider := NewSocialData("secret", "https://socialdata.example", receipts.NewService(repository), client)
	page, err := provider.Search(context.Background(), "from:demo since_id:1", "Latest", "", "source:demo", "2026-10-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	query, _ := url.ParseQuery(captured.URL.RawQuery)
	if captured.Header.Get("Authorization") != "Bearer secret" || query.Get("query") != "from:demo since_id:1" || query.Get("type") != "Latest" || page.ReceiptID != 44 || page.NextCursor != "page-2" || len(page.Tweets) != 1 {
		t.Fatalf("request=%#v query=%v page=%#v", captured, query, page)
	}
	if repository.request.Service != "socialdata" || repository.request.Purpose != "source_fetch" || repository.request.Subject != "source:demo" {
		t.Fatalf("receipt identity = %#v", repository.request)
	}
	var usage map[string]int
	if err := json.Unmarshal(repository.outcome.Usage, &usage); err != nil || usage["tweets"] != 1 || repository.outcome.Cost == nil || *repository.outcome.Cost != 0.0002 || repository.outcome.Currency != "USD" || repository.outcome.CostBasis != "estimated" {
		t.Fatalf("receipt usage = %#v (%s), error=%v", repository.outcome, repository.outcome.Usage, err)
	}
}

func TestSocialDataArticleLookupUsesPostIdentityAndRecordsEstimatedCost(t *testing.T) {
	repository := &receiptMemory{}
	var captured *http.Request
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		captured = request
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"article":{"title":"A story","content_state":{"blocks":[]}}}`))}, nil
	})
	provider := NewSocialData("secret", "https://socialdata.example", receipts.NewService(repository), client)
	article, receiptID, err := provider.ReadArticle(context.Background(), "post-123", "article-456")
	if err != nil || receiptID != 44 || string(article) != `{"title":"A story","content_state":{"blocks":[]}}` {
		t.Fatalf("article=%s receipt=%d err=%v", article, receiptID, err)
	}
	if captured.URL.String() != "https://socialdata.example/twitter/article/post-123" || captured.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("request = %#v", captured)
	}
	if repository.request.Service != "socialdata" || repository.request.Purpose != "x_article" || repository.request.Subject != "article:article-456" {
		t.Fatalf("receipt identity = %#v", repository.request)
	}
	var identity map[string]string
	if err := json.Unmarshal(mustJSONValue(repository.request.Identity), &identity); err != nil || identity["article"] != "post-123" {
		t.Fatalf("receipt identity payload = %#v, %v", repository.request.Identity, err)
	}
	var usage map[string]int
	if err := json.Unmarshal(repository.outcome.Usage, &usage); err != nil || usage["articles"] != 1 || repository.outcome.Cost == nil || *repository.outcome.Cost != 0.0002 || repository.outcome.Currency != "USD" || repository.outcome.CostBasis != "estimated" {
		t.Fatalf("receipt outcome = %#v, error=%v", repository.outcome, err)
	}
}

func TestSocialDataArticleLookupTreatsNotFoundAsFreeEmptyArticle(t *testing.T) {
	repository := &receiptMemory{}
	client := doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found"))}, nil
	})
	provider := NewSocialData("secret", "https://socialdata.example", receipts.NewService(repository), client)
	article, receiptID, err := provider.ReadArticle(context.Background(), "post-404", "article-456")
	var usage map[string]int
	if err != nil || receiptID != 44 || string(article) != "null" || json.Unmarshal(repository.outcome.Usage, &usage) != nil || usage["articles"] != 0 || repository.outcome.Cost != nil {
		t.Fatalf("article=%s receipt=%d outcome=%#v err=%v", article, receiptID, repository.outcome, err)
	}
}

func mustJSONValue(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
