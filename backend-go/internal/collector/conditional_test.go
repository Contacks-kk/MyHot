package collector

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRSSConditionalRequestPrefersETagAndTreats304AsSuccess(t *testing.T) {
	called := false
	service := NewService(nil, fakeDoer(func(req *http.Request) (*http.Response, error) {
		called = true
		if got := req.Header.Get("If-None-Match"); got != `"v1"` {
			t.Fatalf("If-None-Match=%q", got)
		}
		if got := req.Header.Get("If-Modified-Since"); got != "" {
			t.Fatalf("sent Last-Modified alongside ETag: %q", got)
		}
		return &http.Response{StatusCode: http.StatusNotModified, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}), nil)
	items, validators, unchanged, err := service.fetchRSS(context.Background(), source{Kind: "rss", Config: settings{"feedUrl": "https://example.invalid/feed"}, Cursor: settings{"rssURL": "https://example.invalid/feed", "rssETag": `"v1"`, "rssLastModified": "Wed, 01 Oct 2025 00:00:00 GMT"}})
	if err != nil || !called || !unchanged || len(items) != 0 || validators["rssETag"] != `"v1"` {
		t.Fatalf("304 result items=%#v validators=%#v unchanged=%t err=%v", items, validators, unchanged, err)
	}
}

func TestRSSConditionalRequestFallsBackToLastModifiedAndURLChangesInvalidateIt(t *testing.T) {
	feed := `<rss version="2.0"><channel><title>Feed</title><link>https://example.invalid</link><description>Feed</description></channel></rss>`
	for _, test := range []struct{ name, url, cursorURL, expectedHeader, expectedValue string }{
		{name: "last modified fallback", url: "https://example.invalid/feed", cursorURL: "https://example.invalid/feed", expectedHeader: "If-Modified-Since", expectedValue: "Wed, 01 Oct 2025 00:00:00 GMT"},
		{name: "changed URL", url: "https://example.invalid/new-feed", cursorURL: "https://example.invalid/feed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(nil, fakeDoer(func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get("If-None-Match"); got != "" {
					t.Fatalf("unexpected If-None-Match=%q", got)
				}
				if got := req.Header.Get(test.expectedHeader); got != test.expectedValue {
					t.Fatalf("%s=%q, want %q", test.expectedHeader, got, test.expectedValue)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Last-Modified": []string{test.expectedValue}}, Body: io.NopCloser(strings.NewReader(feed))}, nil
			}), nil)
			items, _, unchanged, err := service.fetchRSS(context.Background(), source{Kind: "rss", Config: settings{"feedUrl": test.url}, Cursor: settings{"rssURL": test.cursorURL, "rssLastModified": "Wed, 01 Oct 2025 00:00:00 GMT"}})
			if err != nil || unchanged || len(items) != 0 {
				t.Fatalf("RSS result %#v unchanged=%t err=%v", items, unchanged, err)
			}
		})
	}
}
