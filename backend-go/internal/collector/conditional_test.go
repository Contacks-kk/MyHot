package collector

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRSSConditionalRequestPrefersETagAndTreats304AsSuccess(t *testing.T) {
	called := false
	feedURL := "https://example.invalid/feed"
	config := settings{"feedUrl": feedURL}
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
	items, validators, unchanged, err := service.fetchRSS(context.Background(), source{Kind: "rss", Config: config, Cursor: settings{
		"rssURL":          feedURL,
		"rssResponseURL":  feedURL,
		"rssConfigHash":   rssConfigHash(config),
		"rssETag":         `"v1"`,
		"rssLastModified": "Wed, 01 Oct 2025 00:00:00 GMT",
	}}, false)
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
			config := settings{"feedUrl": test.url}
			cursorConfig := settings{"feedUrl": test.cursorURL}
			service := NewService(nil, fakeDoer(func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get("If-None-Match"); got != "" {
					t.Fatalf("unexpected If-None-Match=%q", got)
				}
				if got := req.Header.Get(test.expectedHeader); got != test.expectedValue {
					t.Fatalf("%s=%q, want %q", test.expectedHeader, got, test.expectedValue)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Last-Modified": []string{test.expectedValue}}, Body: io.NopCloser(strings.NewReader(feed))}, nil
			}), nil)
			items, metadata, unchanged, err := service.fetchRSS(context.Background(), source{Kind: "rss", Config: config, Cursor: settings{
				"rssURL":          test.cursorURL,
				"rssResponseURL":  test.cursorURL,
				"rssConfigHash":   rssConfigHash(cursorConfig),
				"rssLastModified": "Wed, 01 Oct 2025 00:00:00 GMT",
			}}, false)
			if err != nil || unchanged || len(items) != 0 || metadata["rssTitle"] != "Feed" {
				t.Fatalf("RSS result %#v metadata=%#v unchanged=%t err=%v", items, metadata, unchanged, err)
			}
		})
	}
}

/** TestRSSRelativeLinksUseTheFinalRedirectURL preserves the fetched document as Atom's relative base. */
func TestRSSRelativeLinksUseTheFinalRedirectURL(t *testing.T) {
	finalRequest, err := http.NewRequest(http.MethodGet, "https://publisher.example/news/feed.xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	feed := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>redirected</id><title>Research update</title><link href="article-1"/></entry></feed>`
	service := NewService(nil, fakeDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Request:    finalRequest,
			Body:       io.NopCloser(strings.NewReader(feed)),
		}, nil
	}), nil)
	items, _, unchanged, err := service.fetchRSS(context.Background(), source{
		Kind:   "rss",
		Config: settings{"feedUrl": "https://publisher.example/old/feed.xml"},
	}, false)
	if err != nil || unchanged || len(items) != 1 {
		t.Fatalf("redirected feed result = %#v unchanged=%t err=%v", items, unchanged, err)
	}
	if items[0].URL != "https://publisher.example/news/article-1" {
		t.Fatalf("relative Atom link = %q, want final document URL resolution", items[0].URL)
	}
}

/** TestRSSDetailRecoveryBypassesStoredValidators ensures incomplete detail work rereads the listing. */
func TestRSSDetailRecoveryBypassesStoredValidators(t *testing.T) {
	for _, test := range []struct {
		name   string
		cursor settings
	}{
		{name: "pending detail", cursor: settings{"detailRules": "current", "detailPending": "true"}},
		{name: "changed rules", cursor: settings{"detailRules": "old", "detailPending": "false"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			feed := `<rss version="2.0"><channel><title>Feed</title><link>https://example.invalid</link><description>Feed</description></channel></rss>`
			config := settings{"feedUrl": "https://example.invalid/feed", "detail": map[string]any{"titleSelector": "h1"}}
			cursor := settings{
				"rssURL":         config.text("feedUrl"),
				"rssResponseURL": config.text("feedUrl"),
				"rssConfigHash":  rssConfigHash(config),
				"rssETag":        `"v1"`,
			}
			for key, value := range test.cursor {
				cursor[key] = value
			}
			service := NewService(nil, fakeDoer(func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get("If-None-Match"); got != "" {
					t.Fatalf("recovery request sent If-None-Match=%q", got)
				}
				if got := req.Header.Get("If-Modified-Since"); got != "" {
					t.Fatalf("recovery request sent If-Modified-Since=%q", got)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(feed))}, nil
			}), nil)
			_, _, unchanged, err := service.fetchRSS(context.Background(), source{
				Kind: "rss", Config: config, Cursor: cursor,
			}, false)
			if err != nil || unchanged {
				t.Fatalf("recovery fetch unchanged=%t err=%v", unchanged, err)
			}
		})
	}
}

/** TestRSSConfigChangesInvalidateValidators ensures parsing edits reprocess unchanged feed bytes. */
func TestRSSConfigChangesInvalidateValidators(t *testing.T) {
	feedURL := "https://example.invalid/feed"
	oldConfig := settings{"feedUrl": feedURL, "summaryIsBody": false}
	newConfig := settings{"feedUrl": feedURL, "summaryIsBody": true}
	feed := `<rss version="2.0"><channel><title>Feed</title><link>https://example.invalid</link><description>Feed</description><item><title>Research update</title><link>https://example.invalid/article</link><description>Full feed summary.</description></item></channel></rss>`
	service := NewService(nil, fakeDoer(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "" {
			t.Fatal("source config change reused an upstream validator")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(feed))}, nil
	}), nil)
	items, updates, unchanged, err := service.fetchRSS(context.Background(), source{
		Kind:   "rss",
		Config: newConfig,
		Cursor: settings{
			"rssURL":          feedURL,
			"rssResponseURL":  feedURL,
			"rssConfigHash":   rssConfigHash(oldConfig),
			"rssETag":         `"v1"`,
			"rssLastModified": "Wed, 01 Oct 2025 00:00:00 GMT",
		},
	}, false)
	if err != nil || unchanged || len(items) != 1 || items[0].BodyHTML == "" || updates["rssConfigHash"] != rssConfigHash(newConfig) {
		t.Fatalf("changed feed config result = %#v updates=%#v unchanged=%t err=%v", items, updates, unchanged, err)
	}
}

/** TestRSSForcedFetchBypassesStoredValidators preserves manual collection semantics. */
func TestRSSForcedFetchBypassesStoredValidators(t *testing.T) {
	feedURL := "https://example.invalid/feed"
	config := settings{"feedUrl": feedURL}
	feed := `<rss version="2.0"><channel><title>Feed</title><link>https://example.invalid</link><description>Feed</description></channel></rss>`
	service := NewService(nil, fakeDoer(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "" {
			t.Fatal("forced collection sent a stored validator")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(feed))}, nil
	}), nil)
	_, _, unchanged, err := service.fetchRSS(context.Background(), source{
		Kind:   "rss",
		Config: config,
		Cursor: settings{
			"rssURL":         feedURL,
			"rssResponseURL": feedURL,
			"rssConfigHash":  rssConfigHash(config),
			"rssETag":        `"v1"`,
		},
	}, true)
	if err != nil || unchanged {
		t.Fatalf("forced collection unchanged=%t err=%v", unchanged, err)
	}
	unconditional304 := NewService(nil, fakeDoer(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "" {
			t.Fatal("forced collection sent a stored validator")
		}
		return &http.Response{
			StatusCode: http.StatusNotModified,
			Header:     make(http.Header),
			Request:    &http.Request{URL: mustURL(t, feedURL)},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}), nil)
	if _, _, unchanged, err := unconditional304.fetchRSS(context.Background(), source{
		Kind:   "rss",
		Config: config,
		Cursor: settings{
			"rssURL":         feedURL,
			"rssResponseURL": feedURL,
			"rssConfigHash":  rssConfigHash(config),
			"rssETag":        `"v1"`,
		},
	}, true); err == nil || unchanged {
		t.Fatalf("forced collection accepted an unconditional 304: unchanged=%t err=%v", unchanged, err)
	}
}

/** TestRSSChangedRedirectTargetRetriesWithoutValidators prevents unrelated 304 responses. */
func TestRSSChangedRedirectTargetRetriesWithoutValidators(t *testing.T) {
	feedURL := "https://example.invalid/redirect"
	oldTarget := "https://old.example.invalid/feed"
	newTarget := "https://new.example.invalid/feed"
	config := settings{"feedUrl": feedURL}
	var headers []string
	service := NewService(nil, fakeDoer(func(req *http.Request) (*http.Response, error) {
		headers = append(headers, req.Header.Get("If-None-Match"))
		if len(headers) == 1 {
			return &http.Response{
				StatusCode: http.StatusNotModified,
				Header:     make(http.Header),
				Request:    &http.Request{URL: mustURL(t, newTarget)},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Request:    &http.Request{URL: mustURL(t, newTarget)},
			Body:       io.NopCloser(strings.NewReader(`<rss version="2.0"><channel><title>New feed</title><link>https://new.example.invalid</link><description>Feed</description><item><title>New item</title><link>https://new.example.invalid/article</link></item></channel></rss>`)),
		}, nil
	}), nil)
	items, updates, unchanged, err := service.fetchRSS(context.Background(), source{
		Kind:   "rss",
		Config: config,
		Cursor: settings{
			"rssURL":         feedURL,
			"rssResponseURL": oldTarget,
			"rssConfigHash":  rssConfigHash(config),
			"rssETag":        `"old"`,
		},
	}, false)
	if err != nil || unchanged || len(items) != 1 || updates["rssResponseURL"] != newTarget || len(headers) != 2 || headers[0] != `"old"` || headers[1] != "" {
		t.Fatalf("redirect retest result = %#v updates=%#v unchanged=%t headers=%#v err=%v", items, updates, unchanged, headers, err)
	}
}

/** TestRSSRedirectRetryRejectsUnconditional304 prevents a retry from reusing stale cursor state. */
func TestRSSRedirectRetryRejectsUnconditional304(t *testing.T) {
	feedURL := "https://example.invalid/redirect"
	oldTarget := "https://old.example.invalid/feed"
	newTarget := "https://new.example.invalid/feed"
	config := settings{"feedUrl": feedURL}
	requests := 0
	service := NewService(nil, fakeDoer(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 && req.Header.Get("If-None-Match") != `"old"` {
			t.Fatalf("initial request validator=%q", req.Header.Get("If-None-Match"))
		}
		if requests == 2 && req.Header.Get("If-None-Match") != "" {
			t.Fatalf("retry reused validator=%q", req.Header.Get("If-None-Match"))
		}
		return &http.Response{
			StatusCode: http.StatusNotModified,
			Header:     make(http.Header),
			Request:    &http.Request{URL: mustURL(t, newTarget)},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}), nil)
	_, _, unchanged, err := service.fetchRSS(context.Background(), source{
		Kind:   "rss",
		Config: config,
		Cursor: settings{
			"rssURL":         feedURL,
			"rssResponseURL": oldTarget,
			"rssConfigHash":  rssConfigHash(config),
			"rssETag":        `"old"`,
		},
	}, false)
	if err == nil || unchanged || requests != 2 {
		t.Fatalf("redirect retry accepted an unconditional 304: unchanged=%t requests=%d err=%v", unchanged, requests, err)
	}
}

/** mustURL parses an HTTP test URL or fails the test immediately. */
func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
