package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/media"
	"aihot/backend-go/internal/personal"
)

func TestGoServerOwnsSignedImageProxyRoute(t *testing.T) {
	service := media.NewService(media.Config{DataDir: t.TempDir(), SignSecret: "route-test-secret"}, nil, nil)
	server := &Server{Config: config.Config{ImageProxySignSecret: "route-test-secret"}, MediaProxy: media.NewHandler(service, nil)}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/img-proxy", nil))
	if response.Code != http.StatusBadRequest || response.Header().Get("X-Img-Proxy-Sig") != "invalid" {
		t.Fatalf("image proxy route status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestContentResponsesProjectSignedMediaViewsWithCapsAndFallback(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	mediaRaw := json.RawMessage(`[
		{"kind":"image","url":"https://img.example.invalid/1.jpg","alt":"First"},
		{"kind":"image","url":"https://img.example.invalid/2.jpg"},
		{"kind":"image","url":"https://img.example.invalid/3.jpg"},
		{"kind":"image","url":"https://img.example.invalid/4.jpg"},
		{"kind":"image","url":"https://img.example.invalid/5.jpg"}
	]`)
	cards := []personal.Card{{ArticleID: "a1", Title: "T", Summary: "S", MediaJSON: mediaRaw}}

	list := contentResponses(cards, now, "route-test-secret", "image-336", 4)
	if len(list) != 1 || len(list[0].Media) != 4 || !strings.Contains(list[0].Media[0].URL, "mode=image-336") {
		t.Fatalf("unexpected list card media projection: %+v", list)
	}

	detail := contentResponses(cards, now, "route-test-secret", "image-720", 9)
	if len(detail[0].Media) != 5 || !strings.Contains(detail[0].Media[0].URL, "mode=image-720") {
		t.Fatalf("unexpected detail media projection: %+v", detail)
	}

	noSecret := contentResponses(cards, now, "", "image-336", 4)
	if len(noSecret[0].Media) != 0 {
		t.Fatalf("expected empty media without sign secret, got %+v", noSecret[0].Media)
	}
}
