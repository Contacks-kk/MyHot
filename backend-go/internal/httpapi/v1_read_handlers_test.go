package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"aihot/backend-go/internal/publiccontent"
)

/** TestPublicCategoryResolutionHidesMergedKeysAndPreservesStableOrder verifies public category validation. */
func TestPublicCategoryResolutionHidesMergedKeysAndPreservesStableOrder(t *testing.T) {
	server := &Server{
		CategoryKeys:          map[string]struct{}{"security": {}, "research": {}, "policy": {}},
		CategoryOrder:         []string{"security", "research", "policy"},
		PublicCategoryOrder:   []string{"research", "policy"},
		PublicCategoryMembers: map[string][]string{"research": {"security", "research"}, "policy": {"policy"}},
	}
	if members, ok := server.resolvePublicCategory("research"); !ok || len(members) != 2 || members[0] != "security" || members[1] != "research" {
		t.Fatalf("research category = %#v, ok=%v", members, ok)
	}
	if _, ok := server.resolvePublicCategory("security"); ok {
		t.Fatal("merged internal category should not be accepted through public API")
	}
	if got := server.publicCategoryOptions(); len(got) != 2 || got[0] != "research" || got[1] != "policy" {
		t.Fatalf("public categories = %#v", got)
	}
}

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

/** TestSelectedSyncETagHandlesNotModifiedAndHead preserves content-change revalidation. */
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

	changedBody := struct {
		SchemaVersion int    `json:"schemaVersion"`
		AsOf          string `json:"asOf"`
	}{1, "2026-10-07T00:00:01.000Z"}
	changed := httptest.NewRecorder()
	setPrivateSiteHeaders(changed)
	writeV1JSONWithETag(changed, conditional, "v1-snapshot", changedBody, changedBody)
	wantChanged := `{"schemaVersion":1,"asOf":"2026-10-07T00:00:01.000Z"}`
	if changed.Code != http.StatusOK || changed.Body.String() != wantChanged || changed.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Fatalf("changed response status=%d headers=%v body=%s", changed.Code, changed.Header(), changed.Body.String())
	}
}

/** TestSelectedSyncErrorsKeepRetryAndSnapshotRecoveryContracts verifies sync error responses. */
func TestSelectedSyncErrorsKeepRetryAndSnapshotRecoveryContracts(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		status     int
		code       string
		retryAfter string
	}{
		{name: "fresh snapshot", err: publiccontent.SnapshotRequiredError{}, status: http.StatusConflict, code: "snapshot_required"},
		{name: "invalid cursor", err: publiccontent.InvalidCursorError{}, status: http.StatusBadRequest, code: "invalid_cursor"},
		{name: "database outage", err: errors.New("database unavailable"), status: http.StatusServiceUnavailable, code: "temporarily_unavailable", retryAfter: "30"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeSelectedError(response, test.err)

			if response.Code != test.status || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" || response.Header().Get("Retry-After") != test.retryAfter {
				t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != test.code {
				t.Fatalf("error code = %v, want %q", body["code"], test.code)
			}
		})
	}
}
