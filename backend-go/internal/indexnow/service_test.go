package indexnow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"aihot/backend-go/internal/topics"
)

type repositoryFixture struct {
	watermark time.Time
	found     bool
	changes   Changes
	items     []Item
	itemLimit int
	saved     []time.Time
	err       error
}

func (r *repositoryFixture) Watermark(context.Context) (time.Time, bool, error) {
	return r.watermark, r.found, r.err
}

func (r *repositoryFixture) Changes(_ context.Context, _, _ time.Time, _ []topics.Definition, storyLimit int) (Changes, error) {
	if r.err != nil {
		return Changes{}, r.err
	}
	if len(r.changes.Stories) > storyLimit {
		r.changes.Stories = r.changes.Stories[:storyLimit]
	}
	return r.changes, nil
}

func (r *repositoryFixture) Items(_ context.Context, _, _ time.Time, limit int) ([]Item, error) {
	r.itemLimit = limit
	if r.err != nil {
		return nil, r.err
	}
	if len(r.items) > limit {
		return r.items[:limit], nil
	}
	return r.items, nil
}

func (r *repositoryFixture) SaveWatermark(_ context.Context, watermark time.Time) error {
	if r.err != nil {
		return r.err
	}
	r.saved = append(r.saved, watermark)
	return nil
}

type submitterFixture struct {
	status int
	err    error
	got    Submission
	calls  int
}

func (s *submitterFixture) Submit(_ context.Context, payload Submission) (int, error) {
	s.calls++
	s.got = payload
	return s.status, s.err
}

func TestDisabledSubmissionRecordsWatermarkWithoutExternalCall(t *testing.T) {
	now := time.Date(2026, 10, 8, 1, 0, 0, 123_000_000, time.UTC)
	repo := &repositoryFixture{changes: Changes{Reports: []Report{{Kind: "daily", Key: "2026-10-08"}}}}
	submitter := &submitterFixture{}
	result, err := NewService(repo, submitter, Config{SiteURL: "https://example.invalid", Key: "0123456789abcdef0123456789abcdef"}, nil).Run(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "disabled" || result.URLs != 1 || result.HTTPStatus != nil {
		t.Fatalf("result = %#v", result)
	}
	if submitter.calls != 0 {
		t.Fatalf("disabled safety valve made %d external calls", submitter.calls)
	}
	if !reflect.DeepEqual(repo.saved, []time.Time{now}) {
		t.Fatalf("saved watermark = %#v", repo.saved)
	}
}

func TestEmptyRunRecordsWatermark(t *testing.T) {
	now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	repo := &repositoryFixture{}
	result, err := NewService(repo, nil, Config{SiteURL: "https://example.invalid"}, nil).Run(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "empty" || result.URLs != 0 || len(repo.saved) != 1 || !repo.saved[0].Equal(now) {
		t.Fatalf("result = %#v; saved = %#v", result, repo.saved)
	}
}

func TestSuccessfulSubmissionSendsCompatiblePayloadAndRecordsWatermark(t *testing.T) {
	now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	repo := &repositoryFixture{
		changes: Changes{Reports: []Report{{Kind: "daily", Key: "2026-10-08"}}},
		items:   []Item{{ID: "article-1", UpdatedAt: now.Add(-time.Minute)}},
	}
	submitter := &submitterFixture{status: http.StatusAccepted}
	result, err := NewService(repo, submitter, Config{SiteURL: "https://example.invalid/", Key: "0123456789abcdef0123456789abcdef", Enabled: true}, nil).Run(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	wantURLs := []string{"https://example.invalid/items/article-1", "https://example.invalid/daily/2026-10-08"}
	if result.Status != "sent" || result.HTTPStatus == nil || *result.HTTPStatus != http.StatusAccepted || !reflect.DeepEqual(submitter.got.URLs, wantURLs) {
		t.Fatalf("result = %#v; payload = %#v", result, submitter.got)
	}
	if submitter.got.Host != "example.invalid" || submitter.got.KeyLocation != "https://example.invalid/0123456789abcdef0123456789abcdef.txt" {
		t.Fatalf("payload = %#v", submitter.got)
	}
	if len(repo.saved) != 1 || !repo.saved[0].Equal(now) {
		t.Fatalf("saved watermark = %#v", repo.saved)
	}
}

func TestNonSuccessAndTransportFailureDoNotAdvanceWatermark(t *testing.T) {
	now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		status   int
		err      error
		wantStat string
	}{
		{name: "http rejection", status: http.StatusTooManyRequests, wantStat: "failed"},
		{name: "transport failure", err: errors.New("network unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &repositoryFixture{changes: Changes{Reports: []Report{{Kind: "weekly", Key: "2026-W41"}}}}
			submitter := &submitterFixture{status: test.status, err: test.err}
			result, err := NewService(repo, submitter, Config{SiteURL: "https://example.invalid", Key: "0123456789abcdef0123456789abcdef", Enabled: true}, nil).Run(context.Background(), now)
			if test.err != nil {
				if !errors.Is(err, test.err) || len(repo.saved) != 0 {
					t.Fatalf("err = %v, saved = %#v", err, repo.saved)
				}
				return
			}
			if err != nil || result.Status != test.wantStat || len(repo.saved) != 0 {
				t.Fatalf("result = %#v; err = %v; saved = %#v", result, err, repo.saved)
			}
		})
	}
}

func TestBatchLimitRetainsMillisecondWatermarkForNextRun(t *testing.T) {
	now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	repo := &repositoryFixture{items: make([]Item, maxURLs+1)}
	for i := range repo.items {
		repo.items[i] = Item{ID: "article", UpdatedAt: now.Add(-time.Duration(maxURLs+1-i) * time.Millisecond)}
	}
	result, err := NewService(repo, nil, Config{SiteURL: "https://example.invalid"}, nil).Run(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.URLs != maxURLs || !result.More || repo.itemLimit != maxURLs+1 {
		t.Fatalf("result=%#v item limit=%d", result, repo.itemLimit)
	}
	want := repo.items[maxURLs-1].UpdatedAt.Truncate(time.Millisecond).Add(-time.Millisecond)
	if len(repo.saved) != 1 || !repo.saved[0].Equal(want) {
		t.Fatalf("saved watermark = %#v; want %s", repo.saved, want)
	}
}

func TestHTTPSubmitterSendsIndexNowRequest(t *testing.T) {
	var got Submission
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("request = %s %s, content-type=%q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	submitter := &HTTPSubmitter{client: server.Client(), endpoint: server.URL + "/indexnow"}
	status, err := submitter.Submit(context.Background(), Submission{Host: "example.invalid", Key: "key", KeyLocation: "https://example.invalid/key.txt", URLs: []string{"https://example.invalid/"}})
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if got.Host != "example.invalid" || got.Key != "key" || !reflect.DeepEqual(got.URLs, []string{"https://example.invalid/"}) {
		t.Fatalf("payload = %#v", got)
	}
}
