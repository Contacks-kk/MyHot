package httpapi

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/feeds"
	"aihot/backend-go/internal/publiccontent"
)

type rssItemsRepository struct {
	rows []feeds.Item
	err  error
}

func (r rssItemsRepository) Items(context.Context, feeds.ItemQuery) ([]feeds.Item, error) {
	return r.rows, r.err
}

type rssReportsReader struct{}

func (rssReportsReader) V1FeedIssues(context.Context, string, int, time.Time, string, string, string, string) ([]publiccontent.V1FeedIssue, error) {
	return []publiccontent.V1FeedIssue{}, nil
}

func rssTestServer(repo rssItemsRepository) *Server {
	return &Server{Feeds: feeds.NewService(repo, rssReportsReader{}, feeds.Config{
		SiteURL: "https://myhot.example", SiteName: "Example", Categories: []feeds.Category{{Key: "ai-models", Label: "模型"}},
	})}
}

func TestRSSHandlerSupportsFeedRoutesETagHeadAndPreflight(t *testing.T) {
	server := rssTestServer(rssItemsRepository{rows: []feeds.Item{{ID: "article_1", Title: "模型发布", URL: "https://source.example/article"}}})
	handler := http.HandlerFunc(server.rssFeed)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/feed.xml?ignored=yes", nil))
	if first.Code != http.StatusOK || first.Header().Get("Content-Type") != "application/rss+xml; charset=utf-8" || first.Header().Get("Cache-Control") != "private, no-store" || first.Header().Get("Surrogate-Control") != "no-store" || first.Header().Get("Vary") != "Accept-Encoding" || !strings.Contains(first.Body.String(), "article_1") {
		t.Fatalf("unexpected feed response: status=%d headers=%v body=%s", first.Code, first.Header(), first.Body.String())
	}
	var document struct {
		Channel struct {
			Title string `xml:"title"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(first.Body.Bytes(), &document); err != nil || document.Channel.Title == "" {
		t.Fatalf("RSS response is not a complete XML document: title=%q err=%v", document.Channel.Title, err)
	}
	conditionalRequest := httptest.NewRequest(http.MethodGet, "/feed.xml", nil)
	conditionalRequest.Header.Set("If-None-Match", first.Header().Get("ETag"))
	conditional := httptest.NewRecorder()
	handler.ServeHTTP(conditional, conditionalRequest)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional RSS response = %d %q", conditional.Code, conditional.Body.String())
	}
	if conditional.Header().Get("Cache-Control") != "private, no-store" || conditional.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("conditional RSS response must remain private: %v", conditional.Header())
	}
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/feed/daily.xml", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD RSS response = %d %q", head.Code, head.Body.String())
	}
	options := httptest.NewRecorder()
	handler.ServeHTTP(options, httptest.NewRequest(http.MethodOptions, "/feed/category/ai-models.xml", nil))
	if options.Code != http.StatusNoContent || options.Header().Get("Allow") != "GET, HEAD, OPTIONS" || options.Body.Len() != 0 {
		t.Fatalf("OPTIONS RSS response = %d headers=%v body=%q", options.Code, options.Header(), options.Body.String())
	}
}

func TestRSSRouteRequiresVerifiedSessionButKeepsOptionsAnonymous(t *testing.T) {
	handler := rssTestServer(rssItemsRepository{}).Handler()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/feed.xml", nil))
		if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" || !strings.Contains(response.Body.String(), `"code":"unauthorized"`) {
			t.Fatalf("anonymous %s feed response = %d headers=%v body=%s", method, response.Code, response.Header(), response.Body.String())
		}
	}
	options := httptest.NewRecorder()
	handler.ServeHTTP(options, httptest.NewRequest(http.MethodOptions, "/feed.xml", nil))
	if options.Code != http.StatusNoContent || options.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
		t.Fatalf("anonymous feed OPTIONS response = %d headers=%v", options.Code, options.Header())
	}
}

func TestRSSHandlerPreservesNotFoundMethodAndServiceErrorResponses(t *testing.T) {
	server := rssTestServer(rssItemsRepository{})
	handler := http.HandlerFunc(server.rssFeed)
	notFound := httptest.NewRecorder()
	handler.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/feed/category/no-such-category.xml", nil))
	if notFound.Code != http.StatusNotFound || notFound.Body.String() != "Not found" {
		t.Fatalf("unknown category response = %d %q", notFound.Code, notFound.Body.String())
	}
	method := httptest.NewRecorder()
	handler.ServeHTTP(method, httptest.NewRequest(http.MethodPost, "/feed.xml", strings.NewReader("")))
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
		t.Fatalf("unsupported method response = %d headers=%v", method.Code, method.Header())
	}
	failedServer := rssTestServer(rssItemsRepository{err: errors.New("database password must not be returned")})
	failedHandler := http.HandlerFunc(failedServer.rssFeed)
	failed := httptest.NewRecorder()
	failedHandler.ServeHTTP(failed, httptest.NewRequest(http.MethodGet, "/feed.xml", nil))
	if failed.Code != http.StatusServiceUnavailable || failed.Header().Get("Retry-After") != "60" || failed.Header().Get("Cache-Control") != "private, no-store" || failed.Header().Get("Surrogate-Control") != "no-store" || failed.Body.String() != "Feed temporarily unavailable" {
		t.Fatalf("feed error response = %d headers=%v body=%q", failed.Code, failed.Header(), failed.Body.String())
	}
}
