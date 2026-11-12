package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aihot/backend-go/internal/adminauth"
	"aihot/backend-go/internal/adminsettings"
	"aihot/backend-go/internal/config"
)

type memoryAdminSettingsRepository struct {
	budget  adminsettings.BudgetInput
	actor   string
	contact adminsettings.Contact
}

func (*memoryAdminSettingsRepository) Overview(context.Context) (adminsettings.Overview, error) {
	return adminsettings.Overview{Targets: []adminsettings.Target{}, Budgets: []adminsettings.Budget{}}, nil
}
func (r *memoryAdminSettingsRepository) Contact(context.Context) (adminsettings.Contact, error) {
	return r.contact, nil
}
func (*memoryAdminSettingsRepository) SetTargetEnabled(_ context.Context, key string, enabled bool, _ string, _ string) (map[string]any, bool, error) {
	return map[string]any{"key": key, "enabled": enabled}, true, nil
}
func (r *memoryAdminSettingsRepository) UpdateBudget(_ context.Context, service string, input adminsettings.BudgetInput, actor string) (map[string]any, error) {
	r.budget, r.actor = input, actor
	return map[string]any{"service": service, "per_minute": input.PerMinute, "per_hour": input.PerHour, "per_day": input.PerDay}, nil
}
func (r *memoryAdminSettingsRepository) ReplaceContactQR(_ context.Context, slot, imageURL, actor string) (adminsettings.Contact, error) {
	r.actor = actor
	if slot == "wechatQr" {
		r.contact.WechatQR = &imageURL
	} else {
		r.contact.FeishuQR = &imageURL
	}
	return r.contact, nil
}

func TestAdminSettingsRoutesRequireSessionAndCSRFAndBindCamelCaseBudget(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	settingsRepo := &memoryAdminSettingsRepository{}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminSettings: adminsettings.NewService(settingsRepo, adminsettings.Config{}), limiter: newRateLimiter()}
	handler := server.Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("settings were available without an admin session: %d", unauthorized.Code)
	}

	body := `{"perMinute":3,"perHour":12,"perDay":40,"reason":"operational limit"}`
	request := httptest.NewRequest(http.MethodPut, "/api/admin/budgets/openai", strings.NewReader(body))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("budget update without CSRF was accepted: %d", forbidden.Code)
	}

	request = httptest.NewRequest(http.MethodPut, "/api/admin/budgets/openai", strings.NewReader(body))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, request)
	if updated.Code != http.StatusOK || settingsRepo.budget != (adminsettings.BudgetInput{PerMinute: 3, PerHour: 12, PerDay: 40, Reason: "operational limit"}) || settingsRepo.actor != "admin:41" {
		t.Fatalf("budget body was not bound with the existing camelCase contract: status=%d budget=%#v actor=%q response=%s", updated.Code, settingsRepo.budget, settingsRepo.actor, updated.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	get.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, get)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("settings overview contract changed: status=%d headers=%v", response.Code, response.Header())
	}
}

func TestAdminContactQRRouteRequiresSessionCSRFAndValidImage(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	settingsRepo := &memoryAdminSettingsRepository{}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminSettings: adminsettings.NewService(settingsRepo, adminsettings.Config{DataDir: t.TempDir()}), limiter: newRateLimiter()}
	handler := server.Handler()
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 120, 120))); err != nil {
		t.Fatal(err)
	}
	body := `{"slot":"wechatQr","image":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(imageBytes.Bytes()) + `"}`
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/admin/settings/contact-qr", strings.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("contact QR update was available without an admin session: %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/admin/settings/contact-qr", strings.NewReader(body))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("contact QR update without CSRF was accepted: %d", forbidden.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/settings/contact-qr", strings.NewReader(body))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, request)
	if updated.Code != http.StatusOK || settingsRepo.contact.WechatQR == nil || !strings.HasPrefix(*settingsRepo.contact.WechatQR, "/contact/qr-wechat-") || settingsRepo.actor != "admin:41" {
		t.Fatalf("contact QR upload contract changed: status=%d contact=%#v actor=%q response=%s", updated.Code, settingsRepo.contact, settingsRepo.actor, updated.Body.String())
	}

	badImage := httptest.NewRecorder()
	badRequest := httptest.NewRequest(http.MethodPost, "/api/admin/settings/contact-qr", strings.NewReader(`{"slot":"wechatQr","image":"data:image/png;base64,%%%"}`))
	badRequest.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	badRequest.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	badRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(badImage, badRequest)
	if badImage.Code != http.StatusBadRequest {
		t.Fatalf("malformed image data URL status = %d body=%s", badImage.Code, badImage.Body.String())
	}
}

// Keep this compile-time assertion close to the HTTP fake so interface drift is caught locally.
var _ adminsettings.Repository = (*memoryAdminSettingsRepository)(nil)
