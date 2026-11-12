package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/adminauth"
	"aihot/backend-go/internal/adminmodels"
	"aihot/backend-go/internal/config"
	feedbackdomain "aihot/backend-go/internal/feedback"
	"aihot/backend-go/internal/receiptrecovery"
)

type memoryAdminRepository struct {
	hash    string
	session adminauth.SessionRecord
}

func (r *memoryAdminRepository) CreatePasswordSession(_ context.Context, hash, csrf, binding string, _ time.Time, _ *string) (int64, error) {
	r.hash = hash
	r.session = adminauth.SessionRecord{UserID: 41, CSRF: csrf, Name: "Operator", AuthMethod: "password", AuthBinding: binding}
	return 41, nil
}

func (r *memoryAdminRepository) CreateFeishuSession(_ context.Context, _ adminauth.FeishuClaims, hash, csrf, binding string, _ time.Time, _ *string) (int64, error) {
	r.hash = hash
	r.session = adminauth.SessionRecord{UserID: 42, CSRF: csrf, Name: "Operator", AuthMethod: "feishu", AuthBinding: binding}
	return 42, nil
}

func (r *memoryAdminRepository) FindSession(_ context.Context, hash string) (adminauth.SessionRecord, error) {
	if r.hash == "" || hash != r.hash {
		return adminauth.SessionRecord{}, adminauth.ErrNotFound
	}
	return r.session, nil
}

func (r *memoryAdminRepository) DeleteSession(_ context.Context, hash string) error {
	if hash == r.hash {
		r.hash = ""
	}
	return nil
}

func TestAdminPasswordRoutesKeepFormRedirectAndSessionContract(t *testing.T) {
	repo := &memoryAdminRepository{}
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "https://admin.example", SiteName: "Example"}
	server := &Server{Config: cfg, AdminAuth: adminauth.NewService(repo, cfg, nil), limiter: newRateLimiter()}
	handler := server.Handler()

	options := httptest.NewRecorder()
	handler.ServeHTTP(options, httptest.NewRequest(http.MethodGet, "/api/auth/options", nil))
	if options.Code != http.StatusOK || options.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("options contract changed: status=%d headers=%v", options.Code, options.Header())
	}
	var choices map[string]bool
	if err := json.Unmarshal(options.Body.Bytes(), &choices); err != nil || !choices["password"] || choices["feishu"] {
		t.Fatalf("unexpected login options: %s (%v)", options.Body.String(), err)
	}

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/api/auth/login?return=%2Fadmin%2Fsources%3Fsort%3Drecent", nil))
	if login.Code != http.StatusFound || login.Header().Get("Location") != "/admin/login?return=%2Fadmin%2Fsources%3Fsort%3Drecent" {
		t.Fatalf("login redirect contract changed: status=%d location=%q", login.Code, login.Header().Get("Location"))
	}

	form := url.Values{"return": {"/admin/sources?sort=recent"}, "password": {cfg.AdminPassword}}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/password", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	password := httptest.NewRecorder()
	handler.ServeHTTP(password, request)
	if password.Code != http.StatusSeeOther || password.Header().Get("Location") != "/admin/sources?sort=recent" {
		t.Fatalf("password login contract changed: status=%d location=%q body=%s", password.Code, password.Header().Get("Location"), password.Body.String())
	}
	setCookies := password.Result().Cookies()
	if len(setCookies) != 1 || setCookies[0].Name != adminauth.SessionCookie || !setCookies[0].HttpOnly || !setCookies[0].Secure || setCookies[0].SameSite != http.SameSiteLaxMode || setCookies[0].MaxAge != int(adminSessionLifetime.Seconds()) {
		t.Fatalf("admin session cookie contract changed: %#v", setCookies)
	}
	adminCookie := adminauth.SessionCookie + "=" + url.QueryEscape(setCookies[0].Value)

	check := httptest.NewRecorder()
	handler.ServeHTTP(check, httptest.NewRequest(http.MethodGet, "/api/auth/check", nil).WithContext(context.Background()))
	if check.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin check status = %d", check.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/auth/check", nil)
	request.Header.Set("Cookie", adminCookie)
	check = httptest.NewRecorder()
	handler.ServeHTTP(check, request)
	if check.Code != http.StatusNoContent || check.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authenticated admin check changed: status=%d headers=%v", check.Code, check.Header())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/admin/me", nil)
	request.Header.Set("Cookie", adminCookie)
	me := httptest.NewRecorder()
	handler.ServeHTTP(me, request)
	var identity map[string]any
	if me.Code != http.StatusOK || json.Unmarshal(me.Body.Bytes(), &identity) != nil || identity["name"] != "Operator" || identity["dev"] != false {
		t.Fatalf("admin me response changed: status=%d body=%s", me.Code, me.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/auth/logout", nil)
	request.Header.Set("Cookie", adminCookie)
	logout := httptest.NewRecorder()
	handler.ServeHTTP(logout, request)
	if logout.Code != http.StatusSeeOther || logout.Header().Get("Location") != "/" || repo.hash != "" {
		t.Fatalf("admin logout contract changed: status=%d location=%q session=%q", logout.Code, logout.Header().Get("Location"), repo.hash)
	}
}

func TestAdminAuthUnavailableAndInvalidSessionUseProblemResponses(t *testing.T) {
	badRepo := &errorAdminRepository{}
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	server := &Server{Config: cfg, AdminAuth: adminauth.NewService(badRepo, cfg, nil), limiter: newRateLimiter()}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/admin/me", nil)
	request.Header.Set("Cookie", "aihot_admin=present")
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Type") != "application/problem+json" || response.Header().Get("X-Request-Id") == "" {
		t.Fatalf("database failure response changed: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestFeedbackRoutesRequireAdminSessionAndCSRF(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	feedbackRepo := &memoryFeedbackHTTPRepository{}
	server := &Server{Config: cfg, AdminAuth: adminService, Feedback: feedbackdomain.NewService(feedbackRepo, feedbackdomain.Config{}), limiter: newRateLimiter()}
	handler := server.Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/feedback", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("feedback list did not require an admin session: status=%d headers=%v", unauthorized.Code, unauthorized.Header())
	}
	cookie := adminauth.SessionCookie + "=" + token

	request := httptest.NewRequest(http.MethodPost, "/api/admin/feedback-bans", strings.NewReader(`{"sourceHash":"source","reason":"policy"}`))
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Content-Type", "application/json")
	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden || feedbackRepo.banned {
		t.Fatalf("feedback write without CSRF was accepted: status=%d", forbidden.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/feedback-bans", strings.NewReader(`{"sourceHash":"source","reason":"policy"}`))
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, request)
	if allowed.Code != http.StatusNoContent || !feedbackRepo.banned || feedbackRepo.actor != "admin:41" {
		t.Fatalf("feedback write failed with the session CSRF token: status=%d actor=%q", allowed.Code, feedbackRepo.actor)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/admin/feedback?q=%20urgent%20&page=2", nil)
	request.Header.Set("Cookie", cookie)
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, request)
	if listed.Code != http.StatusOK || feedbackRepo.listQuery != "urgent" || feedbackRepo.listPage != 2 {
		t.Fatalf("feedback filters were not passed through: status=%d query=%q page=%d", listed.Code, feedbackRepo.listQuery, feedbackRepo.listPage)
	}
}

type memoryAdminModelsRepository struct {
	overrides map[string]string
	key       string
	model     *string
	actor     string
	reason    string
}

func (*memoryAdminModelsRepository) Overview(context.Context, time.Time) (adminmodels.OverviewRows, error) {
	return adminmodels.OverviewRows{}, nil
}
func (r *memoryAdminModelsRepository) Overrides(context.Context) (map[string]string, error) {
	return r.overrides, nil
}
func (r *memoryAdminModelsRepository) Switch(_ context.Context, key string, selected *string, actor, reason string, _, _ adminmodels.ModelSource) error {
	r.key, r.model, r.actor, r.reason = key, selected, actor, reason
	return nil
}

func TestAdminModelRoutesKeepSessionCSRFAndJSONContract(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	modelRepo := &memoryAdminModelsRepository{overrides: map[string]string{}}
	catalog := adminmodels.Catalog{
		Presets: map[string]adminmodels.Preset{"named": {Service: "vendor", Model: "gpt-4"}}, PresetOrder: []string{"named"},
		Capabilities: []adminmodels.Capability{{Key: "score", Label: "Score", Env: "SCORE_MODEL", Default: "default", Purposes: []string{"score_article"}}},
	}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminModels: adminmodels.NewService(modelRepo, catalog, map[string]string{}), limiter: newRateLimiter()}
	handler := server.Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/models", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("model usage was available without an admin session: status=%d headers=%v", unauthorized.Code, unauthorized.Header())
	}
	cookie := adminauth.SessionCookie + "=" + token
	request := httptest.NewRequest(http.MethodGet, "/api/admin/models?days=0", nil)
	request.Header.Set("Cookie", cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var overview adminmodels.Overview
	if err := json.Unmarshal(response.Body.Bytes(), &overview); err != nil || response.Code != http.StatusOK || overview.Days != 7 || len(overview.Capabilities) != 1 || overview.Benches == nil {
		t.Fatalf("model overview binding or shape changed: status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/models/score", strings.NewReader(`{"model":"named","reason":"switch"}`))
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Content-Type", "application/json")
	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden || modelRepo.key != "" {
		t.Fatalf("model switch without CSRF was accepted: status=%d", forbidden.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/models/score", strings.NewReader(`{"model":"named","reason":" switch "}`))
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, request)
	if updated.Code != http.StatusOK || modelRepo.key != "score" || modelRepo.actor != "admin:41" || modelRepo.reason != " switch " || modelRepo.model == nil || *modelRepo.model != "named" {
		t.Fatalf("model switch failed with valid session and CSRF: status=%d repository=%#v body=%s", updated.Code, modelRepo, updated.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/models/score", strings.NewReader(`{"model":"missing","reason":"switch"}`))
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, request)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("unknown model status = %d body=%s", invalid.Code, invalid.Body.String())
	}
}

type errorAdminRepository struct{}

func (errorAdminRepository) CreatePasswordSession(context.Context, string, string, string, time.Time, *string) (int64, error) {
	return 0, errors.New("unavailable")
}
func (errorAdminRepository) CreateFeishuSession(context.Context, adminauth.FeishuClaims, string, string, string, time.Time, *string) (int64, error) {
	return 0, errors.New("unavailable")
}
func (errorAdminRepository) FindSession(context.Context, string) (adminauth.SessionRecord, error) {
	return adminauth.SessionRecord{}, errors.New("database unavailable")
}
func (errorAdminRepository) DeleteSession(context.Context, string) error { return nil }

type memoryFeedbackHTTPRepository struct {
	banned    bool
	actor     string
	listQuery string
	listPage  int
}

func (r *memoryFeedbackHTTPRepository) List(_ context.Context, _, query string, page int) (feedbackdomain.ListResult, error) {
	r.listQuery, r.listPage = query, page
	return feedbackdomain.ListResult{Rows: []feedbackdomain.FeedbackRow{}, Counts: map[string]int{}, Bans: []feedbackdomain.BanRow{}}, nil
}
func (*memoryFeedbackHTTPRepository) ScreenshotKey(context.Context, int64) (*string, error) {
	return nil, nil
}
func (*memoryFeedbackHTTPRepository) Update(_ context.Context, _ int64, patch feedbackdomain.Patch, actor string) (map[string]any, bool, error) {
	return map[string]any{"status": patch.Status, "version": patch.Version}, true, nil
}
func (*memoryFeedbackHTTPRepository) Erase(context.Context, int64, string, string) (bool, error) {
	return true, nil
}
func (r *memoryFeedbackHTTPRepository) Ban(_ context.Context, _, _, actor string) error {
	r.banned, r.actor = true, actor
	return nil
}
func (*memoryFeedbackHTTPRepository) Unban(context.Context, string, string) error { return nil }

type memoryReceiptRecoveryRepository struct {
	status                 string
	found                  bool
	result                 *receiptrecovery.Result
	actor, note, errorText string
	billed                 *bool
}

func (r *memoryReceiptRecoveryRepository) Status(context.Context, int64) (string, bool, error) {
	return r.status, r.found, nil
}
func (r *memoryReceiptRecoveryRepository) Release(_ context.Context, id int64, errorText, actor, note string, billed *bool) (*receiptrecovery.Result, bool, error) {
	r.actor, r.note, r.errorText, r.billed = actor, note, errorText, billed
	if r.result == nil {
		return nil, false, nil
	}
	return r.result, true, nil
}
func (*memoryReceiptRecoveryRepository) MarkStalePending(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (*memoryReceiptRecoveryRepository) DueUnknown(context.Context, time.Time, int) ([]int64, error) {
	return nil, nil
}

func TestAdminReceiptReleaseKeepsSessionCSRFAndJSONContract(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	recoveryRepo := &memoryReceiptRecoveryRepository{status: "unknown", found: true, result: &receiptrecovery.Result{ID: 27, Status: "failed", Purpose: "score_article", Requeued: true}}
	server := &Server{Config: cfg, AdminAuth: adminService, ReceiptRecovery: receiptrecovery.NewService(recoveryRepo), limiter: newRateLimiter()}
	path := "/api/admin/receipts/27/release"
	body := `{"billed":false,"note":" reviewed "}`

	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("release without admin session status=%d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	forbidden := httptest.NewRecorder()
	server.Handler().ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden || recoveryRepo.actor != "" {
		t.Fatalf("release without CSRF status=%d repository=%#v", forbidden.Code, recoveryRepo)
	}

	request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || recoveryRepo.actor != "admin:41" || recoveryRepo.note != "reviewed" || recoveryRepo.errorText != "人工核对：供应商未计费。reviewed" || recoveryRepo.billed == nil || *recoveryRepo.billed || !strings.Contains(response.Body.String(), `"requeued":true`) {
		t.Fatalf("receipt release contract changed: status=%d repository=%#v body=%s", response.Code, recoveryRepo, response.Body.String())
	}
}
