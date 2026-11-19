package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/auth"
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
	token, tokenHash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_sessions(user_id,token_hash,csrf_token,expires_at) VALUES($1,$2,'fixture-csrf',now()+interval '1 hour')`, userID, tokenHash); err != nil {
		t.Fatal(err)
	}
	server := New(config.Config{SiteURL: "http://localhost", SiteName: "Example"}, pool, orm, map[string]interests.Entity{})
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

	expiredToken, expiredHash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_sessions(user_id,token_hash,csrf_token,expires_at) VALUES($1,$2,'expired-csrf',now()-interval '1 hour')`, userID, expiredHash); err != nil {
		t.Fatal(err)
	}
	unverifiedEmail := "rss-unverified-" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "") + "@example.invalid"
	var unverifiedUserID int64
	if err := pool.QueryRow(ctx, `INSERT INTO user_accounts(email,password_hash) VALUES($1,'fixture') RETURNING id`, unverifiedEmail).Scan(&unverifiedUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM user_accounts WHERE id=$1`, unverifiedUserID) })
	unverifiedToken, unverifiedHash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_sessions(user_id,token_hash,csrf_token,expires_at) VALUES($1,$2,'unverified-csrf',now()+interval '1 hour')`, unverifiedUserID, unverifiedHash); err != nil {
		t.Fatal(err)
	}
	for _, value := range []struct {
		name  string
		token string
	}{
		{name: "forged", token: "forged-session-token"},
		{name: "expired", token: expiredToken},
		{name: "unverified", token: unverifiedToken},
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

	if _, err := pool.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1`, userID); err != nil {
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
