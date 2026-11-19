package articlebody

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/receipts"
)

type memoryRepository struct {
	candidate Candidate
	body      *Extracted
	failed    int
	terminal  bool
	saved     bool
}

func (r *memoryRepository) Pending(context.Context, int) ([]Candidate, error) {
	return []Candidate{r.candidate}, nil
}
func (r *memoryRepository) Find(context.Context, string) (Candidate, error) { return r.candidate, nil }
func (r *memoryRepository) SaveBody(_ context.Context, _ Candidate, body *Extracted) (bool, error) {
	r.saved = true
	r.body = body
	return body != nil, nil
}
func (r *memoryRepository) RecordFailure(context.Context, string, int, string) (int, bool, error) {
	r.failed++
	return r.failed, r.terminal || r.failed >= 3, nil
}

type fakeFetcher struct {
	finalURL string
	content  string
	body     []byte
	err      error
	calls    int
}

func (f *fakeFetcher) Fetch(context.Context, string) (string, string, []byte, error) {
	f.calls++
	return f.finalURL, f.content, f.body, f.err
}

type fakePaidReader struct {
	markdown string
	err      error
	read     int
	done     int64
}

type fakeXArticleReader struct {
	article   json.RawMessage
	err       error
	tweetID   string
	articleID string
}

func (r *fakeXArticleReader) ReadArticle(_ context.Context, tweetID, articleID string) (json.RawMessage, int64, error) {
	r.tweetID, r.articleID = tweetID, articleID
	return r.article, 29, r.err
}

func (r *fakePaidReader) Read(context.Context, string, string) (string, int64, error) {
	r.read++
	return r.markdown, 17, r.err
}
func (r *fakePaidReader) Complete(_ context.Context, id int64) error { r.done = id; return nil }

func TestExtractUsesJinaFallbackAndCompletesReceipt(t *testing.T) {
	repository := testRepository()
	fetcher := &fakeFetcher{finalURL: repository.candidate.URL, content: "text/html", body: []byte("<html><body><main>not long enough</main></body></html>")}
	paragraph := strings.Repeat("The source explains what changed, why it matters, and how customers will be affected. ", 5)
	reader := &fakePaidReader{markdown: paragraph}
	service := NewService(repository, fetcher, reader)

	outcome, err := service.Extract(context.Background(), repository.candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != "ok" || !repository.saved || repository.body == nil || reader.read != 1 || reader.done != 17 {
		t.Fatalf("unexpected extraction outcome: %#v repo=%#v reader=%#v", outcome, repository, reader)
	}
}

/** TestExtractDoesNotUseJinaAfterRedirectToVideo keeps player pages out of article extraction. */
func TestExtractDoesNotUseJinaAfterRedirectToVideo(t *testing.T) {
	repository := testRepository()
	player := strings.Repeat("Video unavailable. Watch later. Sign in. Recommended videos. ", 12)
	fetcher := &fakeFetcher{
		finalURL: "https://www.youtube.com/watch?v=video123",
		content:  "text/html",
		body:     []byte("<html><body><article><p>" + player + "</p></article></body></html>"),
	}
	reader := &fakePaidReader{markdown: strings.Repeat("This player page must not be read as article text. ", 8)}
	service := NewService(repository, fetcher, reader)

	outcome, err := service.Extract(context.Background(), repository.candidate.ID)
	if err != nil || outcome.State != "unconfirmed" || fetcher.calls != 1 || reader.read != 0 || !repository.saved || repository.body != nil {
		t.Fatalf("outcome=%#v err=%v fetches=%d reader=%#v saved=%t body=%#v", outcome, err, fetcher.calls, reader, repository.saved, repository.body)
	}
}

/** TestExtractSkipsDirectVideoBeforeNetworkOrPaidFallback keeps video URLs out of extraction. */
func TestExtractSkipsDirectVideoBeforeNetworkOrPaidFallback(t *testing.T) {
	repository := testRepository()
	repository.candidate.URL = "https://www.youtube.com/watch?v=video123"
	fetcher := &fakeFetcher{}
	reader := &fakePaidReader{markdown: strings.Repeat("Video player controls are not an article transcript. ", 8)}
	service := NewService(repository, fetcher, reader)

	outcome, err := service.Extract(context.Background(), repository.candidate.ID)
	if err != nil || outcome.State != "skipped" || fetcher.calls != 0 || reader.read != 0 || repository.saved {
		t.Fatalf("outcome=%#v err=%v fetches=%d paid reads=%d saved=%t", outcome, err, fetcher.calls, reader.read, repository.saved)
	}
}

func TestExtractRetriesTransientFailureAndStopsAfterThreeAttempts(t *testing.T) {
	repository := testRepository()
	fetcher := &fakeFetcher{err: errors.New("private request detail")}
	service := NewService(repository, fetcher, nil)

	first, err := service.Extract(context.Background(), repository.candidate.ID)
	if err != nil || first.State != "retrying" || first.RetryAfter != 10*time.Minute || repository.failed != 1 {
		t.Fatalf("first result = %#v, %v; failures=%d", first, err, repository.failed)
	}
	repository.failed = 2
	third, err := service.Extract(context.Background(), repository.candidate.ID)
	if err != nil || third.State != "unconfirmed" || third.RetryAfter != 0 || repository.failed != 3 {
		t.Fatalf("third result = %#v, %v; failures=%d", third, err, repository.failed)
	}
}

func TestExtractTreatsJinaBudgetLimitAsUnconfirmed(t *testing.T) {
	repository := testRepository()
	fetcher := &fakeFetcher{finalURL: repository.candidate.URL, content: "text/html", body: []byte("<html></html>")}
	reader := &fakePaidReader{err: &receipts.BudgetExceededError{Service: "jina", Window: "day", RetryAfterSecond: 3600}}
	service := NewService(repository, fetcher, reader)

	outcome, err := service.Extract(context.Background(), repository.candidate.ID)
	if err != nil || outcome.State != "unconfirmed" || !repository.saved || repository.body != nil || repository.failed != 0 {
		t.Fatalf("outcome = %#v, %v; repository=%#v", outcome, err, repository)
	}
}

func TestExtractXArticleUsesPostIDAndPreservesArticleFormatting(t *testing.T) {
	repository := testRepository()
	repository.candidate.SourceKind = "x_search"
	repository.candidate.SourceConfig = []byte(`{}`)
	repository.candidate.XPostPresent = true
	repository.candidate.XPost = json.RawMessage(`{"tweetId":"12345","text":"https://x.com/i/article/777"}`)
	repository.candidate.BodyText = stringPointer("https://x.com/i/article/777")
	reader := &fakeXArticleReader{article: json.RawMessage(`{"title":"Long form","content_state":{"blocks":[
		{"type":"header-two","text":"Context"},{"type":"blockquote","text":"A quoted point"},
		{"type":"unordered-list-item","text":"First item"},{"type":"atomic","text":"ignored media"}]}}`)}
	fetcher := &fakeFetcher{}
	service := NewService(repository, fetcher, nil, reader)

	outcome, err := service.Extract(context.Background(), repository.candidate.ID)
	if err != nil || outcome.State != "ok" || fetcher.calls != 0 || reader.tweetID != "12345" || reader.articleID != repository.candidate.ID {
		t.Fatalf("outcome=%#v err=%v fetches=%d reader=%#v", outcome, err, fetcher.calls, reader)
	}
	want := "https://x.com/i/article/777\n\n# Long form\n\n## Context\n\n> A quoted point\n\n- First item"
	if repository.body == nil || repository.body.Text != want || repository.body.Title != "Long form" || len(repository.body.XArticle) == 0 {
		t.Fatalf("saved X Article body=%#v, want text %q", repository.body, want)
	}
}

func TestExtractXArticleRemovesPreviousCopyBeforeAppendingRevision(t *testing.T) {
	oldArticle := json.RawMessage(`{"title":"Old","content_state":{"blocks":[{"type":"unstyled","text":"Old body"}]}}`)
	repository := testRepository()
	repository.candidate.SourceKind = "x_search"
	repository.candidate.XPostPresent = true
	repository.candidate.XPost = json.RawMessage(`{"tweetId":"tweet-1","text":"Post introduction that exceeds the title replacement threshold."}`)
	repository.candidate.XArticle = oldArticle
	repository.candidate.BodyText = stringPointer("Post introduction that exceeds the title replacement threshold.\n\n# Old\n\nOld body")
	reader := &fakeXArticleReader{article: json.RawMessage(`{"title":"New","content_state":{"blocks":[{"type":"unstyled","text":"New body"}]}}`)}
	service := NewService(repository, &fakeFetcher{}, nil, reader)

	outcome, err := service.Extract(context.Background(), repository.candidate.ID)
	want := "Post introduction that exceeds the title replacement threshold.\n\n# New\n\nNew body"
	if err != nil || outcome.State != "ok" || repository.body == nil || repository.body.Text != want || repository.body.Title != repository.candidate.Title {
		t.Fatalf("outcome=%#v err=%v saved=%#v, want text %q", outcome, err, repository.body, want)
	}
}

func TestExtractXArticleWithoutReadableBlocksIsUnconfirmed(t *testing.T) {
	repository := testRepository()
	repository.candidate.SourceKind = "x_search"
	repository.candidate.XPostPresent = true
	repository.candidate.XPost = json.RawMessage(`{"tweetId":"tweet-2","text":"https://x.com/i/article/9"}`)
	reader := &fakeXArticleReader{article: json.RawMessage(`{"title":"Empty","content_state":{"blocks":[{"type":"atomic","text":"media"}]}}`)}
	service := NewService(repository, &fakeFetcher{}, nil, reader)

	outcome, err := service.Extract(context.Background(), repository.candidate.ID)
	if err != nil || outcome.State != "unconfirmed" || !repository.saved || repository.body != nil {
		t.Fatalf("outcome=%#v err=%v saved=%t body=%#v", outcome, err, repository.saved, repository.body)
	}
}

func TestExtractXArticleWithoutReaderMarksBodyUnconfirmed(t *testing.T) {
	repository := testRepository()
	repository.candidate.SourceKind = "x_search"
	repository.candidate.XPostPresent = true
	repository.candidate.XPost = json.RawMessage(`{"tweetId":"tweet-3","text":"https://x.com/i/article/9"}`)
	service := NewService(repository, &fakeFetcher{}, nil)

	outcome, err := service.Extract(context.Background(), repository.candidate.ID)
	if err != nil || outcome.State != "unconfirmed" || !repository.saved {
		t.Fatalf("outcome=%#v err=%v saved=%t", outcome, err, repository.saved)
	}
}

func TestHotSignalXArticleBodyEligibilityHonorsHistoricalBoundary(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	candidate := Candidate{
		SourceKind: "x_search", ParticipationMode: "hot_signal", BodyStatus: "pending", XPostPresent: true,
		XPost: json.RawMessage(`{"tweetId":"tweet-4"}`), AllowHotSignal: true, Backfill: true,
		DiscoveredAt: now, PublishedAt: timePointer(now.Add(-47 * time.Hour)),
	}
	if !ShouldExtract(candidate) {
		t.Fatal("recent hot-signal X Article was not eligible")
	}
	candidate.PublishedAt = timePointer(now.Add(-49 * time.Hour))
	if ShouldExtract(candidate) {
		t.Fatal("historical hot-signal X Article was eligible")
	}
}

func TestExtractSkipsPublishedAndNonEditorialCandidates(t *testing.T) {
	for _, mutate := range []func(*Candidate){
		func(candidate *Candidate) { candidate.HasPublication = true },
		func(candidate *Candidate) { candidate.ParticipationMode = "isolated" },
		func(candidate *Candidate) { candidate.SourceConfig = []byte(`{}`); candidate.Bare = false },
	} {
		repository := testRepository()
		mutate(&repository.candidate)
		fetcher := &fakeFetcher{}
		service := NewService(repository, fetcher, nil)
		outcome, err := service.Extract(context.Background(), repository.candidate.ID)
		if err != nil || outcome.State != "skipped" || fetcher.calls != 0 || repository.saved {
			t.Fatalf("outcome=%#v err=%v fetches=%d saved=%t", outcome, err, fetcher.calls, repository.saved)
		}
	}
}

func TestPageFetcherUsesBoundedResponseAndAcceptsRedirectURL(t *testing.T) {
	requestURL, _ := url.Parse("https://example.com/final")
	client := httpDoerFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader("<html>ok</html>")), Request: &http.Request{URL: requestURL}}, nil
	})
	fetcher := NewPageFetcher(client)
	finalURL, contentType, body, err := fetcher.Fetch(context.Background(), "https://example.com/start")
	if err != nil || finalURL != requestURL.String() || contentType != "text/html; charset=utf-8" || string(body) != "<html>ok</html>" {
		t.Fatalf("Fetch() = %q, %q, %q, %v", finalURL, contentType, body, err)
	}
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (f httpDoerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func testRepository() *memoryRepository {
	return &memoryRepository{candidate: Candidate{
		ID: "article-1", URL: "https://example.com/article", BodyStatus: "pending", SourceKind: "rss",
		ParticipationMode: "editorial", SourceConfig: []byte(`{"fetchPublicContent":true}`),
	}}
}

func stringPointer(value string) *string { return &value }

func timePointer(value time.Time) *time.Time { return &value }
