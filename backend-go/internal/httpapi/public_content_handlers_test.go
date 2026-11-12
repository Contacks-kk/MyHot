package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestItemAvailabilityRequiresVerifiedUserSession(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/site/items/availability?ids=a1", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d cache-control=%q body=%s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
}

func TestStoryFollowupsRequiresVerifiedUserSession(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/site/stories/12345678-1234-4234-8234-123456789abc/followups", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d cache-control=%q body=%s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
}

func TestGroupReportsRequiresVerifiedUserSession(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/site/groups/fact-1/reports", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d cache-control=%q body=%s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
}

func TestSiteTopicsRequiresVerifiedUserSession(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/site/topics", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestSiteHotRequiresVerifiedUserSession(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/site/hot", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestSiteTimelineRequiresVerifiedSessionBeforeQueryValidation(t *testing.T) {
	server := &Server{}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/site/timeline?channel=invalid&category=invalid", nil))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("unauthenticated timeline status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestSitePoolRequiresVerifiedSessionBeforeQueryValidation(t *testing.T) {
	server := &Server{}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/site/pool?channel=invalid&category=invalid", nil))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("unauthenticated pool status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestSitePoolPageMatchesBoundedNumberSemantics(t *testing.T) {
	for raw, want := range map[string]int{"": 1, "0": 1, "abc": 1, "1.8": 1, "50": 50, "900": 50, "Infinity": 50, "-2": 1} {
		if got := sitePoolPage(raw); got != want {
			t.Errorf("sitePoolPage(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestSiteItemRoutesRequireVerifiedUserSession(t *testing.T) {
	server := &Server{}
	for _, path := range []string{"/api/site/items/article_1", "/api/site/items/article_1/original", "/api/site/items/availability?ids=article_1", "/items/article_1/markdown"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
			t.Errorf("%s: status=%d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestSiteStoryRequiresVerifiedSessionBeforePublicIDValidation(t *testing.T) {
	server := &Server{}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/site/stories/not-a-uuid", nil))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("unauthenticated story status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestSiteTimelineFilterBindingMatchesSiteContract(t *testing.T) {
	server := &Server{CategoryKeys: map[string]struct{}{"ai-models": {}, "paper": {}}}
	for raw, fixture := range map[string]struct {
		values map[string][]string
		want   string
	}{
		"channel=invalid":  {values: map[string][]string{"channel": {"invalid"}}, want: "invalid channel"},
		"category=unknown": {values: map[string][]string{"category": {"unknown"}}, want: "invalid category"},
		"category=":        {values: map[string][]string{"category": {""}}, want: "invalid category"},
	} {
		filters, err := server.parseSiteTimelineFilters(fixture.values)
		if err == nil || err.Error() != fixture.want {
			t.Errorf("filters %q = %#v, %v; want %q", raw, filters, err, fixture.want)
		}
	}
	filters, err := server.parseSiteTimelineFilters(map[string][]string{"channel": {"firstParty"}, "category": {"paper"}, "tag": {"\uFEFF Agent "}})
	if err != nil || filters.Channel != "firstParty" || filters.Category != "paper" || filters.Tag != "Agent" {
		t.Fatalf("valid filters = %#v, %v", filters, err)
	}
	if got := siteTimelineLimit("1.8"); got != 1 {
		t.Fatalf("fractional limit = %d, want JS slice-compatible truncation", got)
	}
	for _, raw := range []string{"", "0", "abc"} {
		if got := siteTimelineLimit(raw); got != 20 {
			t.Errorf("limit %q = %d, want default 20", raw, got)
		}
	}
	if got := siteTimelineLimit("99"); got != 40 {
		t.Fatalf("clamped limit = %d, want 40", got)
	}
}

func TestSiteTopicPageRequiresVerifiedUserSession(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/site/topics/openai?page=2", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestSiteReportIndexRequiresVerifiedUserSession(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/site/reports/daily", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestSiteReportIndexPathRequiresSessionBeforeKindValidation(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodGet, "/api/site/reports/quarterly", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("report kind path did not enter the authenticated site handler: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestReportNavigationRoutesRequireVerifiedSession(t *testing.T) {
	server := &Server{}
	for _, path := range []string{
		"/api/site/reports/daily/navigation/2026-10-07",
		"/api/site/reports/daily/months/2026-10",
		"/api/site/reports/daily/latest-page",
		"/api/site/reports/daily/2026-10-07",
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s status=%d headers=%v body=%s", path, recorder.Code, recorder.Header(), recorder.Body.String())
		}
	}
}

func TestAgentHotRequiresVerifiedSessionAndKeepsPublicV1Headers(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/agent/hot", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAgentLatestAndSearchRequireVerifiedSessionBeforeQueryValidation(t *testing.T) {
	server := &Server{}
	for _, path := range []string{
		"/api/v1/agent?unknown=x",
		"/api/v1/agent/latest?unknown=x",
		"/api/v1/agent/search?q=x",
		"/api/v1/agent/stories/not-a-uuid?unknown=x",
		"/api/v1/agent/daily?unknown=x",
		"/api/v1/agent/weekly?unknown=x",
		"/api/v1/agent/monthly?unknown=x",
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s status=%d headers=%v body=%s", path, recorder.Code, recorder.Header(), recorder.Body.String())
		}
	}
}

func TestContactImagesRequireVerifiedUserSession(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/contact/qr-wechat-1234abcd.png", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestContactOptionsKeepsLegacyUnauthenticatedPreflightBehavior(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodOptions, "/contact/qr-wechat-1234abcd.png", nil))
	if recorder.Code != http.StatusNotFound || recorder.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("contact OPTIONS status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestGroupReportsFilterValidationMatchesSiteContract(t *testing.T) {
	server := &Server{CategoryKeys: map[string]struct{}{"ai-models": {}, "paper": {}}}
	for raw, want := range map[string]string{
		"channel=invalid":  "invalid channel",
		"category=unknown": "invalid category",
		"category=":        "invalid category",
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/site/groups/fact-1/reports?"+raw, nil)
		filters, err := server.parseGroupReportsFilters(request.URL.Query())
		if err == nil || err.Error() != want {
			t.Errorf("filters %q = %#v, %v; want %q", raw, filters, err, want)
		}
	}
	filters, err := server.parseGroupReportsFilters(url.Values{"tag": {"  Agent  "}, "category": {"paper"}, "channel": {"firstParty"}})
	if err != nil || filters.Channel != "firstParty" || filters.Category != "paper" || filters.Tag != "Agent" {
		t.Fatalf("valid filters = %#v, %v", filters, err)
	}
	filters, err = server.parseGroupReportsFilters(url.Values{"tag": {"\uFEFF" + strings.Repeat("x", 61) + " "}})
	if err != nil || filters.Tag != strings.Repeat("x", 60) {
		t.Fatalf("trimmed and bounded tag = %#v, %v", filters, err)
	}
}

func TestPrivateSiteNotFoundUsesProblemResponseAndPrivateHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePrivateSiteNotFound(recorder, "story not found")
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound || recorder.Header().Get("Content-Type") != "application/problem+json" || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if body["title"] != "Not found" || body["code"] != "not_found" || body["detail"] != "story not found" || body["requestId"] != recorder.Header().Get("X-Request-Id") {
		t.Fatalf("problem body = %#v", body)
	}
}

func TestPrivateSiteJSONKeepsPrivateCacheHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePrivateSiteJSON(recorder, http.StatusOK, map[string]string{"ok": "yes"})
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Surrogate-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}
