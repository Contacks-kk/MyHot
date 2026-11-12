package httpapi

import (
	"aihot/backend-go/internal/config"
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func closedGORM(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	orm, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := orm.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	return orm
}

func TestSameOriginUsesForwardedHostWhenBehindWebProxy(t *testing.T) {
	req := httptest.NewRequest("POST", "http://go-api:3002/api/me/model", nil)
	req.Header.Set("Origin", "https://myhot.example")
	req.Header.Set("X-Forwarded-Host", "myhot.example")
	if !sameOrigin(req, true) {
		t.Fatal("same-origin request behind the web proxy should be accepted")
	}
	req.Header.Set("X-Forwarded-Host", "attacker.example")
	if sameOrigin(req, true) {
		t.Fatal("mismatched forwarded host should be rejected")
	}
}

/** TestManualHotScanRoutesAreRetired checks removed scan endpoints return 404. */
func TestManualHotScanRoutesAreRetired(t *testing.T) {
	handler := (&Server{}).Handler()
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/me/hot/scan"},
		{http.MethodGet, "/api/me/hot/scan/1"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, "http://localhost"+test.path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", test.method, test.path, response.Code)
		}
	}
}

func TestIndexNowKeyFileContract(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef"
	server := (&Server{Config: config.Config{IndexNowKey: key}}).Handler()
	request := httptest.NewRequest(http.MethodGet, "http://localhost/"+key+".txt", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != key {
		t.Fatalf("IndexNow file = status %d body %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/plain; charset=UTF-8" || response.Header().Get("Cache-Control") != "public, max-age=3600" {
		t.Fatalf("IndexNow headers = %#v", response.Header())
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("IndexNow file did not include its ETag")
	}
	conditional := httptest.NewRequest(http.MethodGet, "http://localhost/"+key+".txt", nil)
	conditional.Header.Set("If-None-Match", etag)
	conditionalResponse := httptest.NewRecorder()
	server.ServeHTTP(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
		t.Fatalf("conditional IndexNow file = status %d body %q", conditionalResponse.Code, conditionalResponse.Body.String())
	}
	head := httptest.NewRequest(http.MethodHead, "http://localhost/"+key+".txt", nil)
	headResponse := httptest.NewRecorder()
	server.ServeHTTP(headResponse, head)
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 || headResponse.Header().Get("Content-Length") != "32" {
		t.Fatalf("HEAD IndexNow file = status %d length %q body %q", headResponse.Code, headResponse.Header().Get("Content-Length"), headResponse.Body.String())
	}
}

func TestAdminSourceWriteRoutesAreRetired(t *testing.T) {
	handler := (&Server{}).Handler()
	for _, target := range []struct{ method, path string }{
		{http.MethodPost, "/api/admin/sources"},
		{http.MethodPatch, "/api/admin/sources/demo"},
		{http.MethodPost, "/api/admin/sources/preview"},
		{http.MethodPost, "/api/admin/sources/demo/preview"},
		{http.MethodPost, "/api/admin/sources/demo/fetch"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(target.method, target.path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want route removed (404)", target.method, target.path, response.Code)
		}
	}
}

func TestSameOriginRejectsOpaqueOriginAndAllowsNonBrowserClient(t *testing.T) {
	req := httptest.NewRequest("POST", "http://localhost:3002/api/auth/login", nil)
	if !sameOrigin(req) {
		t.Fatal("requests without Origin should remain available to non-browser clients")
	}
	req.Header.Set("Origin", "null")
	if sameOrigin(req) {
		t.Fatal("opaque origins must be rejected")
	}
}

func TestRateLimiterSeparatesKeysAndAppliesLimit(t *testing.T) {
	limiter := newRateLimiter()
	window := time.Minute
	for i := 0; i < 2; i++ {
		if limiter.limited("user-a", 2, window) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	if !limiter.limited("user-a", 2, window) {
		t.Fatal("attempt over the configured limit should be rejected")
	}
	if limiter.limited("user-b", 2, window) {
		t.Fatal("a separate user should have a separate limit")
	}
}

func TestForwardingHeadersRequireExplicitTrustedProxy(t *testing.T) {
	req := httptest.NewRequest("POST", "http://localhost:3002/api/auth/login", nil)
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("X-Forwarded-Host", "attacker.example")
	req.Header.Set("X-Real-IP", "8.8.8.8")
	if sameOrigin(req) {
		t.Fatal("untrusted forwarded host bypassed origin validation")
	}
	if clientKey(req) != "127.0.0.1" {
		t.Fatal("untrusted forwarded IP bypassed limiter")
	}
	if clientKey(req, true) != "8.8.8.8" {
		t.Fatal("trusted proxy IP should be used")
	}
}

func TestUnavailableDatabaseDoesNotInvalidateIdentity(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://postgres@127.0.0.1:55432/nonexistent_test")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	server := New(config.Config{}, pool, closedGORM(t, "postgres://postgres@127.0.0.1:55432/nonexistent_test"), nil)
	me := httptest.NewRequest("GET", "http://localhost/api/me", nil)
	me.AddCookie(&http.Cookie{Name: sessionCookie, Value: "fixture-session"})
	for _, request := range []*http.Request{me, httptest.NewRequest("POST", "http://localhost/api/auth/login", strings.NewReader("{\"email\":\"fixture@example.invalid\",\"password\":\"fixture-password\"}"))} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != 503 {
			t.Fatalf("database outage became invalid credentials:%d", response.Code)
		}
	}
}
