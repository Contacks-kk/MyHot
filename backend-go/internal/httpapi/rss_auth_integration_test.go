package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/account"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/interests"
)

func TestRSSRequiresVerifiedSessionAndKeepsResponsesPrivate(t *testing.T) {
	pool, orm := authTestPools(t)
	ctx := context.Background()
	var userID int64
	email := "rss-auth-" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "") + "@example.invalid"
	if err := pool.QueryRow(ctx, `INSERT INTO user_accounts(email,password_hash,verified_at) VALUES($1,'fixture',now()) RETURNING id`, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM user_accounts WHERE id=$1`, userID) })

	server := New(config.Config{SiteURL: "http://localhost", SiteName: "Example"}, pool, orm, map[string]interests.Entity{})
	redisStore := attachTestRedis(t, server)
	token := seedTestUserSession(t, redisStore, userID, "fixture-csrf", time.Hour)
	expiredToken := seedTestUserSession(t, redisStore, userID, "expired-csrf", time.Second)
	handler := server.Handler()
	private := func(t *testing.T, response *httptest.ResponseRecorder, status int) {
		t.Helper()
		if response.Code != status || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost/feed.xml", nil)
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, request)
	private(t, unauthorized, http.StatusUnauthorized)

	request = httptest.NewRequest(http.MethodGet, "http://localhost/feed.xml", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request)
	private(t, first, http.StatusOK)
	if first.Header().Get("ETag") == "" || first.Header().Get("Content-Type") != "application/rss+xml; charset=utf-8" {
		t.Fatalf("authenticated RSS response headers = %v", first.Header())
	}

	request = httptest.NewRequest(http.MethodGet, "http://localhost/feed.xml", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	conditional := httptest.NewRecorder()
	handler.ServeHTTP(conditional, request)
	private(t, conditional, http.StatusNotModified)

	unverifiedEmail := "rss-unverified-" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "") + "@example.invalid"
	var unverifiedUserID int64
	if err := pool.QueryRow(ctx, `INSERT INTO user_accounts(email,password_hash) VALUES($1,'$argon2id$v=19$m=65536,t=3,p=2$dGVzdHNhbHR0ZXN0c2FsdA$dGVzdGtleXRlc3RrZXl0ZXN0a2V5dGVzdGtleXRlc3Q') RETURNING id`, unverifiedEmail).Scan(&unverifiedUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM user_accounts WHERE id=$1`, unverifiedUserID) })
	if _, err := server.Accounts.Login(ctx, unverifiedEmail, "unused-password"); err == nil || (!errors.Is(err, account.ErrUnverifiedAccount) && !errors.Is(err, account.ErrInvalidCredentials)) {
		t.Fatalf("unverified account must not obtain a session: %v", err)
	}

	// Wait past the 1s Redis TTL so expiredToken triggers real Redis key expiration
	time.Sleep(1100 * time.Millisecond)
	for _, value := range []struct {
		name  string
		token string
	}{
		{name: "forged", token: "forged-session-token"},
		{name: "expired", token: expiredToken},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			request := httptest.NewRequest(method, "http://localhost/feed.xml", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookie, Value: value.token})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			private(t, response, http.StatusUnauthorized)
			if method == http.MethodGet && !strings.Contains(response.Body.String(), `"code":"unauthorized"`) {
				t.Fatalf("%s session admitted or changed response: %s", value.name, response.Body.String())
			}
		}
	}

	if err := redisStore.RevokeUser(ctx, userID); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "http://localhost/feed.xml", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	revoked := httptest.NewRecorder()
	handler.ServeHTTP(revoked, request)
	private(t, revoked, http.StatusUnauthorized)
	if !strings.Contains(revoked.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("revoked RSS session response = %s", revoked.Body.String())
	}
}
