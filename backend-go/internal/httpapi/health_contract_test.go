package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

type healthCheckFunc func(context.Context) error

func (f healthCheckFunc) Check(ctx context.Context) error { return f(ctx) }

func TestHealthKeepsLegacySuccessContract(t *testing.T) {
	t.Setenv("AIHOT_RELEASE", "contract-test")
	server := &Server{Health: healthCheckFunc(func(context.Context) error { return nil })}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true || body["db"] != "ok" || body["release"] != "contract-test" {
		t.Fatalf("legacy response fields changed: %#v", body)
	}
	if _, ok := body["ms"].(float64); !ok {
		t.Fatalf("ms field missing or not numeric: %#v", body)
	}
	if len(body) != 4 {
		t.Fatalf("unexpected response fields: %#v", body)
	}
}

func TestHealthKeepsLegacyProblemContract(t *testing.T) {
	t.Setenv("AIHOT_RELEASE", "")
	server := &Server{Health: healthCheckFunc(func(context.Context) error { return context.DeadlineExceeded })}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if response.Header().Get("Retry-After") != "30" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected retry/cache headers: %#v", response.Header())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	requestID, ok := body["requestId"].(string)
	if !ok || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(requestID) {
		t.Fatalf("invalid problem request id: %#v", body["requestId"])
	}
	if response.Header().Get("X-Request-Id") != requestID || body["code"] != "temporarily_unavailable" || body["retryAfter"] != float64(30) {
		t.Fatalf("legacy problem response changed: %#v", body)
	}
}
