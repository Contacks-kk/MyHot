package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"aihot/backend-go/internal/adminsettings"
)

func TestContactAliasRedirectUsesCurrentQRAndPrivateHeaders(t *testing.T) {
	location := "/contact/qr-wechat-1234abcd.png"
	contact := adminsettings.Contact{WechatQR: &location}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request := httptest.NewRequest(method, "/api/site/contact-alias/wechat", nil)
		response := httptest.NewRecorder()
		writeContactAliasRedirect(response, request, "wechat", contact)
		if response.Code != http.StatusFound || response.Header().Get("Location") != location {
			t.Fatalf("%s alias redirect = %d %q", method, response.Code, response.Header().Get("Location"))
		}
		if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
			t.Fatalf("%s alias cache headers = %v", method, response.Header())
		}
	}
}

func TestContactAliasRequiresVerifiedSessionAndHandlesUnknownSlot(t *testing.T) {
	server := &Server{AdminSettings: adminsettings.NewService(&memoryAdminSettingsRepository{}, adminsettings.Config{})}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/site/contact-alias/wechat", nil))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("anonymous alias status/headers = %d %v", response.Code, response.Header())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/site/contact-alias/unknown", nil)
	response = httptest.NewRecorder()
	writeContactAliasRedirect(response, request, "unknown", adminsettings.Contact{})
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown contact slot status = %d", response.Code)
	}
}
