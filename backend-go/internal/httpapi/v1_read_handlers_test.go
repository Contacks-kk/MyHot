package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSelectedSyncRequiresVerifiedSessionAndKeepsV1Headers(t *testing.T) {
	for _, path := range []string{
		"/api/v1/items", "/api/v1/stories/12345678-1234-4234-8234-123456789abc", "/api/v1/selected/snapshot", "/api/v1/selected/changes",
		"/api/v1/dailies", "/api/v1/dailies/latest", "/api/v1/dailies/2026-10-07", "/api/v1/weeklies", "/api/v1/weeklies/latest", "/api/v1/weeklies/2026-W41",
		"/api/v1/monthlies", "/api/v1/monthlies/latest", "/api/v1/monthlies/2026-10",
	} {
		recorder := httptest.NewRecorder()
		(&Server{}).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" {
			t.Fatalf("%s status=%d headers=%v body=%s", path, recorder.Code, recorder.Header(), recorder.Body.String())
		}
		if recorder.Header().Get("Access-Control-Allow-Origin") != "*" || recorder.Header().Get("Access-Control-Allow-Methods") != "GET, HEAD, OPTIONS" {
			t.Fatalf("%s missing V1 CORS headers: %v", path, recorder.Header())
		}
	}
}

func TestSelectedSyncPreflightAndMethodContracts(t *testing.T) {
	for _, path := range []string{
		"/api/v1/items", "/api/v1/stories/12345678-1234-4234-8234-123456789abc", "/api/v1/selected/snapshot", "/api/v1/selected/changes",
		"/api/v1/dailies", "/api/v1/dailies/2026-10-07", "/api/v1/weeklies", "/api/v1/weeklies/2026-W41", "/api/v1/monthlies", "/api/v1/monthlies/2026-10",
	} {
		preflight := httptest.NewRecorder()
		(&Server{}).Handler().ServeHTTP(preflight, httptest.NewRequest(http.MethodOptions, path, nil))
		if preflight.Code != http.StatusNoContent || preflight.Header().Get("Cache-Control") != "public, max-age=86400" || preflight.Header().Get("Access-Control-Max-Age") != "86400" {
			t.Fatalf("%s preflight status=%d headers=%v", path, preflight.Code, preflight.Header())
		}

		method := httptest.NewRecorder()
		(&Server{}).Handler().ServeHTTP(method, httptest.NewRequest(http.MethodPost, path, nil))
		if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, HEAD, OPTIONS" || method.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s method status=%d headers=%v body=%s", path, method.Code, method.Header(), method.Body.String())
		}
	}
}

func TestSelectedSyncStrictQueryAndIntegerBinding(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"fields=minimal&limit=5", ""},
		{"unknown=1&fields=minimal", "Unknown query parameter: unknown."},
		{"limit=1&limit=2", "Query parameter must not be repeated: limit."},
	}
	for _, test := range cases {
		got, err := strictSelectedQuery(httptest.NewRequest(http.MethodGet, "/?"+test.query, nil), "fields", "limit")
		if test.want == "" {
			if err != nil || got["fields"] != "minimal" || got["limit"] != "5" {
				t.Fatalf("strict query %q = %#v, %v", test.query, got, err)
			}
			continue
		}
		if err == nil || err.Error() != test.want {
			t.Fatalf("strict query %q error = %v, want %q", test.query, err, test.want)
		}
	}
	for _, test := range []struct {
		value string
		min   int
		max   int
		ok    bool
	}{{"1", 1, 1000, true}, {"1000", 1, 1000, true}, {"0", 1, 1000, false}, {"1001", 1, 1000, false}, {"1.0", 1, 1000, false}, {"+1", 1, 1000, false}, {"", 1, 1000, false}} {
		_, err := parsePublicV1Integer(test.value, test.min, test.max, "limit")
		if (err == nil) != test.ok {
			t.Fatalf("integer %q error=%v, want valid=%v", test.value, err, test.ok)
		}
	}
}

func TestSelectedSyncETagHandlesNotModifiedAndHead(t *testing.T) {
	body := struct {
		SchemaVersion int    `json:"schemaVersion"`
		AsOf          string `json:"asOf"`
	}{1, "2026-10-07T00:00:00.000Z"}
	etagBody := struct {
		SchemaVersion int `json:"schemaVersion"`
	}{1}
	first := httptest.NewRecorder()
	setPrivateSiteHeaders(first)
	writeV1JSONWithETag(first, httptest.NewRequest(http.MethodGet, "/", nil), "v1-snapshot", body, etagBody)
	if first.Code != http.StatusOK || first.Header().Get("ETag") == "" || first.Header().Get("Vary") != "Accept-Encoding" || first.Body.Len() == 0 {
		t.Fatalf("first response status=%d headers=%v body=%s", first.Code, first.Header(), first.Body.String())
	}

	conditional := httptest.NewRequest(http.MethodGet, "/", nil)
	conditional.Header.Set("If-None-Match", first.Header().Get("ETag"))
	notModified := httptest.NewRecorder()
	setPrivateSiteHeaders(notModified)
	writeV1JSONWithETag(notModified, conditional, "v1-snapshot", body, etagBody)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 || notModified.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("304 status=%d headers=%v body=%s", notModified.Code, notModified.Header(), notModified.Body.String())
	}

	head := httptest.NewRecorder()
	setPrivateSiteHeaders(head)
	writeV1JSONWithETag(head, httptest.NewRequest(http.MethodHead, "/", nil), "v1-snapshot", body, etagBody)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Fatalf("HEAD status=%d headers=%v body=%s", head.Code, head.Header(), head.Body.String())
	}
}
