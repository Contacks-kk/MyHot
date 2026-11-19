package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type failingHealth struct {
	err error
}

type failingStreamWriter struct {
	*httptest.ResponseRecorder
}

/** Check returns the configured health error for request logging tests. */
func (h failingHealth) Check(context.Context) error {
	return h.err
}

/** Write simulates a transport failure after a streaming response has started. */
func (w failingStreamWriter) Write([]byte) (int, error) {
	return 0, errors.New("private stream failure")
}

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

/** TestServerFailureLogsKeepRequestContextAndDropSecrets verifies safe API diagnostics. */
func TestServerFailureLogsKeepRequestContextAndDropSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	server := (&Server{
		Health: failingHealth{err: errors.New("database failure private-marker")},
		Logger: logger,
	}).Handler()
	missing := httptest.NewRecorder()
	server.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "http://localhost/api/missing?token=private-query", nil))
	if missing.Code != http.StatusNotFound || output.Len() != 0 {
		t.Fatalf("ordinary 404 response = %d, logs = %s", missing.Code, output.String())
	}

	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/health?token=private-query", nil)
	request.Header.Set("Authorization", "Bearer private-auth")
	request.Header.Set("Cookie", "session=private-cookie")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("health response = status %d headers %#v", response.Code, response.Header())
	}
	if !strings.Contains(response.Body.String(), `"code":"temporarily_unavailable"`) {
		t.Fatalf("health response contract changed: %s", response.Body.String())
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("error log is not JSON: %v: %s", err, output.String())
	}
	if record["msg"] != "http request failed" || record["method"] != http.MethodGet || record["path"] != "/api/health" || record["status"] != float64(http.StatusServiceUnavailable) {
		t.Fatalf("unexpected error log: %#v", record)
	}
	if record["request_id"] != response.Header().Get("X-Request-Id") {
		t.Fatalf("request id differs between response and log: %#v / %q", record["request_id"], response.Header().Get("X-Request-Id"))
	}
	if stack, ok := record["stack"].(string); !ok || !strings.Contains(stack, "health") {
		t.Fatalf("error log omitted handler stack: %#v", record["stack"])
	}
	for _, secret := range []string{"private-query", "private-auth", "private-cookie", "private-marker"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("error log contains request secret %q: %s", secret, output.String())
		}
	}
}

/** TestServerFailureLogsCaptureStreamingWriteErrorsWithoutTheirMessages protects stream diagnostics. */
func TestServerFailureLogsCaptureStreamingWriteErrorsWithoutTheirMessages(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := logServerFailures(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("stream body"))
	}))
	recorder := failingStreamWriter{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://localhost/api/stream?token=private-query", nil))

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("stream error log is not JSON: %v: %s", err, output.String())
	}
	if record["msg"] != "http request failed" || record["path"] != "/api/stream" || record["status"] != float64(http.StatusOK) || record["write_error_type"] != "*errors.errorString" {
		t.Fatalf("unexpected stream error record: %#v", record)
	}
	if _, ok := record["stack"].(string); !ok || strings.Contains(output.String(), "private stream failure") || strings.Contains(output.String(), "private-query") {
		t.Fatalf("stream log omitted stack or exposed private values: %s", output.String())
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
