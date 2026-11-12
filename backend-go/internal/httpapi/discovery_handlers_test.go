package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLLMSTxtRequiresVerifiedUserSession(t *testing.T) {
	server := &Server{}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/llms.txt", nil))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("anonymous llms response = %d %#v", response.Code, response.Header())
	}
	if response.Header().Get("ETag") != "" {
		t.Fatal("llms service ran before the session was verified")
	}
}

func TestLLMSTxtResponseKeepsPrivateConditionalAndHeadBehavior(t *testing.T) {
	body := "# Example\n"
	get := httptest.NewRecorder()
	writeLLMSTxtResponse(get, httptest.NewRequest(http.MethodGet, "/llms.txt", nil), body)
	if get.Code != http.StatusOK || get.Body.String() != body || get.Header().Get("Content-Type") != "text/plain; charset=utf-8" || get.Header().Get("Cache-Control") != "private, no-store" || get.Header().Get("Surrogate-Control") != "no-store" || get.Header().Get("Vary") != "Accept-Encoding" || get.Header().Get("Content-Length") != "10" {
		t.Fatalf("llms response = %d %#v %q", get.Code, get.Header(), get.Body.String())
	}
	etag := get.Header().Get("ETag")
	if etag == "" {
		t.Fatal("llms response omitted its ETag")
	}
	conditional := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
	conditional.Header.Set("If-None-Match", etag)
	unchanged := httptest.NewRecorder()
	writeLLMSTxtResponse(unchanged, conditional, body)
	if unchanged.Code != http.StatusNotModified || unchanged.Body.Len() != 0 {
		t.Fatalf("conditional llms response = %d %q", unchanged.Code, unchanged.Body.String())
	}
	head := httptest.NewRecorder()
	writeLLMSTxtResponse(head, httptest.NewRequest(http.MethodHead, "/llms.txt", nil), body)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD llms response = %d %#v %q", head.Code, head.Header(), head.Body.String())
	}
}

func TestLLMSTxtFailureKeepsLegacyRetryContract(t *testing.T) {
	response := httptest.NewRecorder()
	writeLLMSUnavailable(response)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "30" || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("llms failure = %d %#v %s", response.Code, response.Header(), response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["detail"] != "Temporarily unavailable." || body["code"] != "temporarily_unavailable" || body["retryAfter"] != float64(30) {
		t.Fatalf("llms failure body = %#v", body)
	}
}
