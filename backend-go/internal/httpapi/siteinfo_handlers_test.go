package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aihot/backend-go/internal/adminauth"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/publiccontent"
	"aihot/backend-go/internal/siteinfo"
)

/** TestCatalogRoutesProtectDisplayData verifies the two session boundaries. */
func TestCatalogRoutesProtectDisplayData(t *testing.T) {
	adminConfig := config.Config{
		SessionSecret: "admin-session-secret",
		AdminPassword: "correct-admin-password",
		SiteURL:       "http://localhost",
	}
	adminService := adminauth.NewService(&memoryAdminRepository{}, adminConfig, nil)
	token, _, err := adminService.PasswordLogin(t.Context(), adminConfig.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		AdminAuth: adminService,
		Catalog: interests.NewCatalogService(
			[]string{"research"},
			map[string]string{"research": "Research"},
			nil,
			interests.PublicationSettings{Subject: "Technology"},
			nil,
			nil,
		),
	}
	handler := server.Handler()
	for _, path := range []string{"/api/me/catalog", "/api/admin/catalog"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("anonymous %s: status=%d headers=%v", path, response.Code, response.Header())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/catalog", nil)
	request.AddCookie(&http.Cookie{Name: adminauth.SessionCookie, Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" || response.Body.String() != "{\"subject\":\"Technology\",\"categories\":[{\"key\":\"research\",\"label\":\"Research\",\"publicKey\":\"research\",\"feedLabel\":\"Research\"}],\"topicGroups\":[],\"topics\":[]}\n" {
		t.Fatalf("admin catalog: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestSiteMetadataKeepsCacheAndConditionalRequestContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changelog.json")
	if err := os.WriteFile(path, []byte(`{"latestVersion":"release-1","releases":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{SiteInfo: siteinfo.NewService(path)}

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/site/meta", nil))
	if response.Code != http.StatusOK || response.Body.String() != `{"changelogVersion":"release-1"}` {
		t.Fatalf("unexpected metadata response: status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "public, max-age=60, s-maxage=60" || response.Header().Get("Vary") != "Accept-Encoding" || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("metadata cache contract changed: %#v", response.Header())
	}
	etag := response.Header().Get("ETag")
	conditional := httptest.NewRequest(http.MethodGet, "/api/site/meta", nil)
	conditional.Header.Set("If-None-Match", `"`+etag[3:len(etag)-1]+`"`)
	unchanged := httptest.NewRecorder()
	server.Handler().ServeHTTP(unchanged, conditional)
	if unchanged.Code != http.StatusNotModified || unchanged.Body.Len() != 0 || unchanged.Header().Get("ETag") != etag {
		t.Fatalf("conditional response changed: status=%d headers=%#v body=%q", unchanged.Code, unchanged.Header(), unchanged.Body.String())
	}
}

func TestChangelogKeepsCacheAndUnavailableProblemContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changelog.json")
	if err := os.WriteFile(path, []byte(`{"latestVersion":"release-1","releases":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{SiteInfo: siteinfo.NewService(path)}
	response := httptest.NewRecorder()
	server.writeSiteInfoResponse(response, httptest.NewRequest(http.MethodGet, "/api/site/changelog", nil), "changelog")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("changelog response changed: status=%d headers=%#v", response.Code, response.Header())
	}

	server.SiteInfo = siteinfo.NewService(filepath.Join(t.TempDir(), "missing.json"))
	unavailable := httptest.NewRecorder()
	server.writeSiteInfoResponse(unavailable, httptest.NewRequest(http.MethodGet, "/api/site/changelog", nil), "changelog")
	if unavailable.Code != http.StatusServiceUnavailable || unavailable.Header().Get("Retry-After") != "10" || unavailable.Header().Get("Cache-Control") != "private, no-store" || unavailable.Header().Get("Surrogate-Control") != "no-store" || unavailable.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unavailable response changed: status=%d headers=%#v body=%s", unavailable.Code, unavailable.Header(), unavailable.Body.String())
	}
}

func TestChangelogRequiresVerifiedUserSession(t *testing.T) {
	server := &Server{SiteInfo: &fakeSiteInfo{}}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/site/changelog", nil))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Content-Type") != "application/problem+json" || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("unauthenticated changelog response changed: status=%d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestOpenAPIV1RequiresVerifiedUserSession(t *testing.T) {
	server := &Server{SiteInfo: &fakeSiteInfo{}}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/openapi-v1.json", nil))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Content-Type") != "application/problem+json" || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unauthenticated OpenAPI response changed: status=%d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestSitemapKeepsPrivateCacheConditionalAndHeadBehavior(t *testing.T) {
	document := publiccontent.SitemapDocument{
		XML: []byte("<urlset />"), ETag: `W/"sitemap-fixture"`, ExpiresAt: time.Now().Add(4 * time.Minute),
	}
	get := httptest.NewRecorder()
	writeSitemapDocument(get, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil), document)
	if get.Code != http.StatusOK || get.Body.String() != "<urlset />" || get.Header().Get("Content-Type") != "application/xml" || get.Header().Get("Vary") != "Accept-Encoding" || get.Header().Get("ETag") != `W/"sitemap-fixture"` {
		t.Fatalf("sitemap response changed: status=%d headers=%#v body=%q", get.Code, get.Header(), get.Body.String())
	}
	if get.Header().Get("Cache-Control") != "private, no-store" || get.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("sitemap cache control changed: %#v", get.Header())
	}

	conditional := httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil)
	conditional.Header.Set("If-None-Match", `"sitemap-fixture"`)
	unchanged := httptest.NewRecorder()
	writeSitemapDocument(unchanged, conditional, document)
	if unchanged.Code != http.StatusNotModified || unchanged.Body.Len() != 0 || unchanged.Header().Get("ETag") != `W/"sitemap-fixture"` {
		t.Fatalf("conditional sitemap response changed: status=%d headers=%#v body=%q", unchanged.Code, unchanged.Header(), unchanged.Body.String())
	}

	head := httptest.NewRecorder()
	writeSitemapDocument(head, httptest.NewRequest(http.MethodHead, "/sitemap.xml", nil), document)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != `W/"sitemap-fixture"` {
		t.Fatalf("HEAD sitemap response changed: status=%d headers=%#v body=%q", head.Code, head.Header(), head.Body.String())
	}
}

func TestSitemapRequiresVerifiedUserSession(t *testing.T) {
	server := &Server{}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("anonymous sitemap response changed: status=%d headers=%#v body=%q", response.Code, response.Header(), response.Body.String())
	}
	if response.Header().Get("ETag") != "" {
		t.Fatal("sitemap service ran before the session was verified")
	}
}

func TestSitemapFailureKeepsRetryResponseContract(t *testing.T) {
	response := httptest.NewRecorder()
	writeSitemapUnavailable(response)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "Sitemap temporarily unavailable" || response.Header().Get("Retry-After") != "300" || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("sitemap failure changed: status=%d headers=%#v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

type fakeSiteInfo struct{}

func (fakeSiteInfo) Meta() ([]byte, string, error) { return []byte(`{}`), `W/"meta-test"`, nil }
func (fakeSiteInfo) Changelog() ([]byte, string, error) {
	return []byte(`{}`), `W/"changelog-test"`, nil
}
func (fakeSiteInfo) OpenAPIV1() ([]byte, string, error) { return []byte(`{}`), `W/"openapi-test"`, nil }
