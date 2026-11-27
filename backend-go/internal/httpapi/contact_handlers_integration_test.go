package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aihot/backend-go/internal/adminsettings"
	"aihot/backend-go/internal/auth"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/interests"
)

func TestContactImageServesUploadedFilePrivatelyAndRechecksSession(t *testing.T) {
	pool, orm := authTestPools(t)
	ctx := context.Background()
	email := fmt.Sprintf("contact-image-%d@example.invalid", time.Now().UnixNano())
	var userID int64
	if err := pool.QueryRow(ctx, "INSERT INTO user_accounts(email,password_hash,verified_at) VALUES($1,'fixture',now()) RETURNING id", email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM user_accounts WHERE id=$1", userID) })
	dataDir := t.TempDir()
	uploads := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "qr-wechat-1234abcd.png"
	image := []byte("contact image fixture")
	if err := os.WriteFile(filepath.Join(uploads, name), image, 0o644); err != nil {
		t.Fatal(err)
	}
	server := New(config.Config{DatabaseURL: "integration-test", DataDir: dataDir, SiteContactDir: filepath.Join(t.TempDir(), "contact")}, pool, orm, map[string]interests.Entity{})
	redisStore := attachTestRedis(t, server)
	token := seedTestUserSession(t, redisStore, userID, "csrf", time.Hour)
	currentQR := "/contact/qr-wechat-1234abcd.png"
	server.AdminSettings = adminsettings.NewService(&memoryAdminSettingsRepository{contact: adminsettings.Contact{WechatQR: &currentQR}}, adminsettings.Config{})
	handler := server.Handler()
	path := "/contact/" + name
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("anonymous contact read status=%d headers=%v", unauthorized.Code, unauthorized.Header())
	}
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != string(image) || response.Header().Get("Content-Type") != "image/png" || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("contact image response status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	aliasPath := "/api/site/contact-alias/wechat"
	aliasAnonymous := httptest.NewRecorder()
	handler.ServeHTTP(aliasAnonymous, httptest.NewRequest(http.MethodGet, aliasPath, nil))
	if aliasAnonymous.Code != http.StatusUnauthorized || aliasAnonymous.Header().Get("Cache-Control") != "private, no-store" || aliasAnonymous.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("anonymous contact alias status=%d headers=%v", aliasAnonymous.Code, aliasAnonymous.Header())
	}
	aliasRequest := httptest.NewRequest(http.MethodGet, aliasPath+"?source=outside", nil)
	aliasRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	aliasResponse := httptest.NewRecorder()
	handler.ServeHTTP(aliasResponse, aliasRequest)
	if aliasResponse.Code != http.StatusFound || aliasResponse.Header().Get("Location") != currentQR || aliasResponse.Header().Get("Cache-Control") != "private, no-store" || aliasResponse.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("contact alias response status=%d headers=%v", aliasResponse.Code, aliasResponse.Header())
	}
	aliasHead := httptest.NewRecorder()
	aliasHeadRequest := httptest.NewRequest(http.MethodHead, aliasPath, nil)
	aliasHeadRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	handler.ServeHTTP(aliasHead, aliasHeadRequest)
	if aliasHead.Code != http.StatusFound || aliasHead.Body.Len() != 0 || aliasHead.Header().Get("Location") != currentQR {
		t.Fatalf("contact alias HEAD response status=%d headers=%v body=%q", aliasHead.Code, aliasHead.Header(), aliasHead.Body.String())
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("contact image response has no ETag")
	}
	conditional := httptest.NewRequest(http.MethodGet, path, nil)
	conditional.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	conditional.Header.Set("If-None-Match", etag)
	unchanged := httptest.NewRecorder()
	handler.ServeHTTP(unchanged, conditional)
	if unchanged.Code != http.StatusNotModified || unchanged.Body.Len() != 0 || unchanged.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("conditional contact response status=%d headers=%v body=%q", unchanged.Code, unchanged.Header(), unchanged.Body.String())
	}
	invalid := httptest.NewRequest(http.MethodGet, "/contact/qr-wechat.txt", nil)
	invalid.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusNotFound || invalidResponse.Body.Len() != 0 || invalidResponse.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("invalid contact image response status=%d headers=%v body=%q", invalidResponse.Code, invalidResponse.Header(), invalidResponse.Body.String())
	}
	missing := httptest.NewRequest(http.MethodGet, "/contact/missing.png", nil)
	missing.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusNotFound || missingResponse.Body.String() != "Not found" || missingResponse.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("missing contact image response status=%d headers=%v body=%q", missingResponse.Code, missingResponse.Header(), missingResponse.Body.String())
	}
	if err := redisStore.Delete(ctx, userID, auth.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	revoked := httptest.NewRequest(http.MethodGet, path, nil)
	revoked.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	revoked.Header.Set("If-None-Match", etag)
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, revoked)
	if denied.Code != http.StatusUnauthorized || denied.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("revoked session used an image validator: status=%d headers=%v", denied.Code, denied.Header())
	}
	revokedAlias := httptest.NewRecorder()
	deniedAlias := httptest.NewRequest(http.MethodGet, aliasPath, nil)
	deniedAlias.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	handler.ServeHTTP(revokedAlias, deniedAlias)
	if revokedAlias.Code != http.StatusUnauthorized || revokedAlias.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("revoked session used contact alias: status=%d headers=%v", revokedAlias.Code, revokedAlias.Header())
	}
}
