package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/ingest"
	"aihot/backend-go/internal/materials"
)

type ingestRepositoryFixture struct{ ensures, records int }

func (r *ingestRepositoryFixture) EnsureSource(context.Context, string, string) error {
	r.ensures++
	return nil
}
func (r *ingestRepositoryFixture) RecordSuccess(context.Context, string, int, int) error {
	r.records++
	return nil
}

type ingestMaterialsFixture struct{ stores int }

func (m *ingestMaterialsFixture) Store(context.Context, materials.Input) (materials.Result, error) {
	m.stores++
	return materials.Result{Created: true}, nil
}

func TestIngestRouteAuthRateLimitAndResponse(t *testing.T) {
	repository := &ingestRepositoryFixture{}
	materialWriter := &ingestMaterialsFixture{}
	server := &Server{
		Config:  config.Config{IngestToken: "fixture-ingest-token-123", IngestRateLimit: 1},
		limiter: newRateLimiter(),
		Ingest:  ingest.NewService(repository, materialWriter),
	}
	handler := server.Handler()
	post := func(token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/ingest/items", strings.NewReader(`{"sourceId":"fixture","items":[{"title":"story","url":"https://example.invalid/story"}]}`))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	unauthorized := post("wrong-token")
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Body.String() != "Unauthorized" || unauthorized.Header().Get("Cache-Control") != "no-store" || repository.ensures != 0 {
		t.Fatalf("unauthorized ingest changed: %d %#v %q", unauthorized.Code, unauthorized.Header(), unauthorized.Body.String())
	}
	accepted := post("fixture-ingest-token-123")
	var result ingest.Result
	if err := json.Unmarshal(accepted.Body.Bytes(), &result); err != nil || accepted.Code != http.StatusOK || !result.OK || result.Created != 1 || accepted.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("accepted ingest changed: status=%d headers=%#v body=%s err=%v", accepted.Code, accepted.Header(), accepted.Body.String(), err)
	}
	limited := post("fixture-ingest-token-123")
	var limitedBody map[string]any
	_ = json.Unmarshal(limited.Body.Bytes(), &limitedBody)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "60" || limitedBody["error"] != "rate limited" || limitedBody["ok"] != false || materialWriter.stores != 1 || repository.records != 1 {
		t.Fatalf("rate limit changed: status=%d headers=%#v body=%s", limited.Code, limited.Header(), limited.Body.String())
	}
}

/** TestIngestRouteKeepsValidItemsAcrossMalformedBatchEntries preserves partial acceptance semantics. */
func TestIngestRouteKeepsValidItemsAcrossMalformedBatchEntries(t *testing.T) {
	repository := &ingestRepositoryFixture{}
	materialWriter := &ingestMaterialsFixture{}
	server := &Server{
		Config:  config.Config{IngestToken: "fixture-ingest-token-123"},
		limiter: newRateLimiter(),
		Ingest:  ingest.NewService(repository, materialWriter),
	}
	body := `{"sourceId":"external-reporter","items":[null,false,7,"invalid",[],{},{"title":"valid","url":"https://example.com/story"},null,{"title":"duplicate","url":"https://example.com/story?utm_source=duplicate"}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/ingest/items", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer fixture-ingest-token-123")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	var result ingest.Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || !result.OK || result.Created != 1 || materialWriter.stores != 1 || repository.records != 1 {
		t.Fatalf("malformed batch changed partial acceptance: status=%d result=%#v stores=%d records=%d body=%s err=%v", response.Code, result, materialWriter.stores, repository.records, response.Body.String(), err)
	}
}

func TestIngestRouteRejectsPlaceholderTokens(t *testing.T) {
	server := &Server{Config: config.Config{IngestToken: "placeholder-token-value"}, limiter: newRateLimiter()}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/ingest/items", strings.NewReader(`{}`)))
	if response.Code != http.StatusUnauthorized || response.Body.String() != "Unauthorized" {
		t.Fatalf("placeholder token was accepted: %d %s", response.Code, response.Body.String())
	}
}
