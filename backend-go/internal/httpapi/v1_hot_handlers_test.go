package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestV1HotTopicsRequiresVerifiedSessionAndRetainsPublicHeaders(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/hot-topics", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "*" || recorder.Header().Get("Access-Control-Allow-Methods") != "GET, HEAD, OPTIONS" {
		t.Fatalf("public API headers missing: %v", recorder.Header())
	}
}

func TestV1HotTopicsPreflightAndMethodContracts(t *testing.T) {
	server := &Server{}
	preflight := httptest.NewRecorder()
	server.Handler().ServeHTTP(preflight, httptest.NewRequest(http.MethodOptions, "/api/v1/hot-topics", nil))
	if preflight.Code != http.StatusNoContent || preflight.Header().Get("Cache-Control") != "public, max-age=86400" || preflight.Header().Get("Access-Control-Max-Age") != "86400" {
		t.Fatalf("preflight status=%d headers=%v", preflight.Code, preflight.Header())
	}

	method := httptest.NewRecorder()
	server.Handler().ServeHTTP(method, httptest.NewRequest(http.MethodPost, "/api/v1/hot-topics", nil))
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, HEAD, OPTIONS" || method.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("method response status=%d headers=%v body=%s", method.Code, method.Header(), method.Body.String())
	}
}

func TestV1HotTopicsStrictQueryIgnoresEmptySeparatorsOnly(t *testing.T) {
	if key, ok := firstRawQueryName("&&"); ok {
		t.Fatalf("empty query separators yielded key %q", key)
	}
	if key, ok := firstRawQueryName("q=1&x=2"); !ok || key != "q" {
		t.Fatalf("first query key = %q, %v", key, ok)
	}
	if key, ok := firstRawQueryName("%71=1"); !ok || key != "q" {
		t.Fatalf("decoded query key = %q, %v", key, ok)
	}
}
