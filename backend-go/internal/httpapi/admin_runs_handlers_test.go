package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aihot/backend-go/internal/adminauth"
	"aihot/backend-go/internal/adminruns"
	"aihot/backend-go/internal/config"
)

type memoryAdminRunsRepository struct{ called bool }

func (r *memoryAdminRunsRepository) Load(context.Context) (adminruns.Data, error) {
	r.called = true
	return adminruns.Data{}, nil
}

type memoryAdminRequeueRepository struct {
	group  *string
	reason string
	actor  string
	called bool
}

func (*memoryAdminRequeueRepository) Load(context.Context) (adminruns.Data, error) {
	return adminruns.Data{}, nil
}

func (r *memoryAdminRequeueRepository) RequeueFailed(_ context.Context, group *string, reason, actor, _ string) (adminruns.RequeueResult, error) {
	r.group, r.reason, r.actor, r.called = group, reason, actor, true
	return adminruns.RequeueResult{Requeued: 7}, nil
}

func TestAdminRunsRouteRequiresAdminAndReturnsOverview(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminService := adminauth.NewService(&memoryAdminRepository{}, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	repository := &memoryAdminRunsRepository{}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminRuns: adminruns.NewService(repository), limiter: newRateLimiter()}

	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/runs", nil))
	if unauthorized.Code != http.StatusUnauthorized || repository.called {
		t.Fatalf("runs were available without admin authentication: status=%d called=%v", unauthorized.Code, repository.called)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/runs", nil)
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !repository.called {
		t.Fatalf("runs overview did not use the service: status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("admin runs must not be cached: %q", got)
	}
}

func TestAdminProcessingRequeueKeepsSessionCSRFAndJSONContract(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	repository := &memoryAdminRequeueRepository{}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminRuns: adminruns.NewService(repository), limiter: newRateLimiter()}
	path := "/api/admin/processing/requeue"
	body := `{"group":"provider timeout","reason":" operator review "}`

	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized || repository.called {
		t.Fatalf("requeue was available without admin authentication: status=%d called=%v", unauthorized.Code, repository.called)
	}

	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	forbidden := httptest.NewRecorder()
	server.Handler().ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden || repository.called {
		t.Fatalf("requeue without CSRF was accepted: status=%d called=%v", forbidden.Code, repository.called)
	}

	request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"group":null,"reason":" \t "}`))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	invalid := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalid, request)
	if invalid.Code != http.StatusBadRequest || repository.called {
		t.Fatalf("blank requeue reason was accepted: status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !repository.called || repository.group == nil || *repository.group != "provider timeout" || repository.reason != " operator review " || repository.actor != "admin:41" || !strings.Contains(response.Body.String(), `"requeued":7`) {
		t.Fatalf("requeue contract changed: status=%d repository=%#v body=%s", response.Code, repository, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("admin requeue must not be cached: %q", response.Header().Get("Cache-Control"))
	}
}
