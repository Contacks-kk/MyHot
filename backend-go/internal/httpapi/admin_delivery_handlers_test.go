package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/adminauth"
	"aihot/backend-go/internal/admindeliveries"
	"aihot/backend-go/internal/config"
)

type memoryAdminDeliveryRepository struct {
	id      int64
	outcome string
	note    string
	actor   string
	err     error
}

func (r *memoryAdminDeliveryRepository) Resolve(_ context.Context, id int64, outcome, note, actor string) (*admindeliveries.Result, bool, error) {
	r.id, r.outcome, r.note, r.actor = id, outcome, note, actor
	if r.err != nil {
		return nil, true, r.err
	}
	status := "failed"
	if outcome == "sent" {
		status = "sent"
	}
	return &admindeliveries.Result{ID: id, Status: status}, true, nil
}

func (*memoryAdminDeliveryRepository) MarkStale(context.Context, time.Time) (admindeliveries.StaleCounts, error) {
	return admindeliveries.StaleCounts{}, nil
}

type memoryAdminResendRepository struct {
	memoryAdminDeliveryRepository
	record       *admindeliveries.ResendRecord
	claimVersion string
	claimed      bool
	claimPayload json.RawMessage
	finished     []string
}

func (r *memoryAdminResendRepository) LoadResend(context.Context, int64) (*admindeliveries.ResendRecord, bool, error) {
	return r.record, r.record != nil, nil
}

func (*memoryAdminResendRepository) LoadSelectedContent(context.Context, string) (*admindeliveries.SelectedContent, error) {
	return nil, nil
}

func (r *memoryAdminResendRepository) ClaimResend(_ context.Context, _ int64, _ string, payload json.RawMessage) (string, bool, error) {
	r.claimPayload = append(json.RawMessage(nil), payload...)
	return r.claimVersion, r.claimed, nil
}

func (r *memoryAdminResendRepository) FinishResend(_ context.Context, _ int64, claimVersion, beforeStatus, status, response, actor, note string) error {
	r.finished = []string{claimVersion, beforeStatus, status, response, actor, note}
	return nil
}

type memoryAdminWebhookSender struct{ url string }

func (s *memoryAdminWebhookSender) Send(_ context.Context, url string, _ any) (admindeliveries.WebhookResult, error) {
	s.url = url
	return admindeliveries.WebhookResult{Status: "sent", Body: `{"code":0}`}, nil
}

func TestAdminDeliveryResolveKeepsSessionCSRFAndOutcomeContract(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	deliveryRepo := &memoryAdminDeliveryRepository{}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminDeliveries: admindeliveries.NewService(deliveryRepo, admindeliveries.Config{}, nil), limiter: newRateLimiter()}
	body := `{"outcome":"sent","note":" confirmed "}`

	unauthorized := httptest.NewRecorder()
	unauthorizedRequest := httptest.NewRequest(http.MethodPost, "/api/admin/deliveries/17/resolve", strings.NewReader(body))
	unauthorizedRequest.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(unauthorized, unauthorizedRequest)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("delivery resolution was available without an admin session: %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/admin/deliveries/17/resolve", strings.NewReader(body))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	forbidden := httptest.NewRecorder()
	server.Handler().ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden || deliveryRepo.id != 0 {
		t.Fatalf("delivery resolution without CSRF was accepted: status=%d repository=%#v", forbidden.Code, deliveryRepo)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/deliveries/17/resolve", strings.NewReader(body))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || deliveryRepo.id != 17 || deliveryRepo.outcome != "sent" || deliveryRepo.note != "confirmed" || deliveryRepo.actor != "admin:41" || !strings.Contains(response.Body.String(), `"status":"sent"`) {
		t.Fatalf("delivery resolution contract changed: status=%d repository=%#v body=%s", response.Code, deliveryRepo, response.Body.String())
	}
}

func TestAdminDeliveryResolveMapsStateConflict(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	deliveryRepo := &memoryAdminDeliveryRepository{err: admindeliveries.ErrNotResolvable}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminDeliveries: admindeliveries.NewService(deliveryRepo, admindeliveries.Config{}, nil), limiter: newRateLimiter()}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/deliveries/17/resolve", strings.NewReader(`{"outcome":"drop","note":"reviewed"}`))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("resolved delivery conflict status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminDeliveryResendKeepsAdminCSRFAndUsesGoSender(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "https://site.example", SiteName: "Example", FeishuContentPushEnabled: true}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	repository := &memoryAdminResendRepository{
		record:       &admindeliveries.ResendRecord{Status: "failed", Version: "v1", Payload: []byte(`{"card":"stored"}`), ConfigRef: "push", Kind: "feishu_webhook", Enabled: true, SubjectKind: "other", SubjectID: "other-1"},
		claimVersion: "v2", claimed: true,
	}
	sender := &memoryAdminWebhookSender{}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminDeliveries: admindeliveries.NewService(repository, admindeliveries.Config{ContentPushEnabled: true, WebhookURLs: map[string]string{"push": "https://hooks.example.invalid"}}, sender), limiter: newRateLimiter()}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/deliveries/17/resolve", strings.NewReader(`{"outcome":"resend","note":" checked "}`))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || sender.url != "https://hooks.example.invalid" || len(repository.finished) != 6 || repository.finished[0] != "v2" || repository.finished[1] != "failed" || repository.finished[2] != "sent" || repository.finished[4] != "admin:41" || repository.finished[5] != "checked" {
		t.Fatalf("resend contract changed: status=%d body=%s sender=%#v repository=%#v", response.Code, response.Body.String(), sender, repository)
	}
}

func TestAdminDeliveryResolveValidatesOutcomesAndNotesBeforeRepositoryWrites(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	deliveryRepo := &memoryAdminDeliveryRepository{}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminDeliveries: admindeliveries.NewService(deliveryRepo, admindeliveries.Config{}, nil), limiter: newRateLimiter()}
	cases := []struct {
		name, contentType, body, detail string
		status                          int
	}{
		{name: "missing fields", contentType: "application/json", body: `{}`, detail: "outcome: Invalid option", status: http.StatusBadRequest},
		{name: "unknown outcome", contentType: "application/json", body: `{"outcome":"resent","note":"checked"}`, detail: "expected one of", status: http.StatusBadRequest},
		{name: "blank note", contentType: "application/json", body: `{"outcome":"drop","note":"　"}`, detail: "note: Too small", status: http.StatusBadRequest},
		{name: "plain text is an object validation error", contentType: "text/plain", body: `{"outcome":"drop","note":"checked"}`, detail: "expected object, received string", status: http.StatusBadRequest},
		{name: "unsupported content type", contentType: "application/xml", body: `<resolution/>`, detail: "could not be processed", status: http.StatusUnsupportedMediaType},
		{name: "invalid JSON is rejected by the framework parser", contentType: "application/json", body: `{"outcome":`, detail: "could not be processed", status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/admin/deliveries/17/resolve", strings.NewReader(tc.body))
			request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
			request.Header.Set("Content-Type", tc.contentType)
			request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.detail) || deliveryRepo.id != 0 {
				t.Fatalf("invalid delivery resolution was not rejected before repository access: status=%d repo=%#v body=%s", response.Code, deliveryRepo, response.Body.String())
			}
		})
	}
}
