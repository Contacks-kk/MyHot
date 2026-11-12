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
	"aihot/backend-go/internal/adminselectbench"
	"aihot/backend-go/internal/config"
)

type memoryAdminSelectBenchRepository struct {
	data   adminselectbench.ImportData
	actor  string
	filter adminselectbench.Filters
}

func (*memoryAdminSelectBenchRepository) List(context.Context) ([]adminselectbench.RunListEntry, error) {
	return []adminselectbench.RunListEntry{}, nil
}
func (r *memoryAdminSelectBenchRepository) Run(_ context.Context, id string, filters adminselectbench.Filters) (*adminselectbench.RunCases, error) {
	r.filter = filters
	return &adminselectbench.RunCases{Run: adminselectbench.RunSummary{ID: id, CreatedAt: time.Now()}, Rows: []adminselectbench.CaseRow{}, Strata: []adminselectbench.Stratum{}}, nil
}
func (r *memoryAdminSelectBenchRepository) Import(_ context.Context, data adminselectbench.ImportData, actor string) error {
	r.data, r.actor = data, actor
	return nil
}

func TestSelectBenchRoutesRequireAdminAndBindFiltersAndImport(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	selectRepo := &memoryAdminSelectBenchRepository{}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminSelectBench: adminselectbench.NewService(selectRepo), limiter: newRateLimiter()}
	handler := server.Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/selectbench", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("selectbench was available without an admin session: %d", unauthorized.Code)
	}

	cookie := adminauth.SessionCookie + "=" + token
	query := httptest.NewRequest(http.MethodGet, "/api/admin/selectbench/run-1?model=model-b&outcome=fp&stratum=topic&disagree=1", nil)
	query.Header.Set("Cookie", cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, query)
	if response.Code != http.StatusOK || selectRepo.filter.Model != "model-b" || selectRepo.filter.Outcome != "fp" || selectRepo.filter.Stratum == nil || *selectRepo.filter.Stratum != "topic" || !selectRepo.filter.Disagree {
		t.Fatalf("selectbench filters changed: status=%d filters=%#v body=%s", response.Code, selectRepo.filter, response.Body.String())
	}

	body := `{"label":"fixture","report":{"models":{"model-a":{"summary":{"n":1},"cases":[]}}}}`
	request := httptest.NewRequest(http.MethodPost, "/api/admin/selectbench/import", strings.NewReader(body))
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Content-Type", "application/json")
	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("selectbench import without CSRF was accepted: %d", forbidden.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/selectbench/import", strings.NewReader(body))
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	imported := httptest.NewRecorder()
	handler.ServeHTTP(imported, request)
	var result map[string]string
	if imported.Code != http.StatusOK || json.Unmarshal(imported.Body.Bytes(), &result) != nil || result["id"] == "" || selectRepo.actor != "admin:41" || selectRepo.data.Label != "fixture" {
		t.Fatalf("selectbench import contract changed: status=%d result=%#v actor=%q data=%#v body=%s", imported.Code, result, selectRepo.actor, selectRepo.data, imported.Body.String())
	}
}

var _ adminselectbench.Repository = (*memoryAdminSelectBenchRepository)(nil)
