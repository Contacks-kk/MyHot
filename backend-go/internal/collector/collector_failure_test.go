package collector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"aihot/backend-go/internal/modelconfig"
)

func TestRSSResponseFailureClass(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "request", err: errors.New("source HTTP request failed"), want: "rss_http_request"},
		{name: "status", err: errors.New("source HTTP status 503"), want: "rss_http_status_503"},
		{name: "blocked URL", err: errors.New("source URL must be public HTTP or HTTPS without credentials"), want: "rss_url_rejected"},
		{name: "body limit", err: errors.New("source response is too large"), want: "rss_response_too_large"},
		{name: "unclassified detail stays hidden", err: errors.New("parse failed at https://example.test/feed?token=secret"), want: "rss_response_read"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := rssResponseFailureClass(test.err); got != test.want {
				t.Fatalf("rssResponseFailureClass() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRSSRequestFailureRetainsNonPublicDNSClassWithoutRequestDetails(t *testing.T) {
	_, nonPublicErr := modelconfig.DialPublic(context.Background(), "tcp", "127.0.0.1:443")
	if nonPublicErr == nil {
		t.Fatal("non-public address should be rejected before connecting")
	}
	for _, test := range []struct {
		name  string
		cause error
		class string
	}{
		{name: "non-public DNS", cause: nonPublicErr, class: "rss_dns_non_public"},
		{name: "other request failure", cause: errors.New("request failed with secret detail"), class: "rss_http_request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(nil, fakeDoer(func(req *http.Request) (*http.Response, error) {
				return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: fmt.Errorf("secret detail: %w", test.cause)}
			}), nil)
			_, _, _, err := service.fetchRSS(context.Background(), source{Kind: "rss", Config: settings{"feedUrl": "https://example.invalid/feed?token=secret"}})
			var classified *sourceFetchError
			if !errors.As(err, &classified) || classified.class != test.class {
				t.Fatalf("failure = %#v, want class %q", classified, test.class)
			}
			if err.Error() != "source HTTP request failed" {
				t.Fatalf("request details escaped sanitization: %q", err.Error())
			}
		})
	}
}
