package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/account"
	"aihot/backend-go/internal/auth"
	"aihot/backend-go/internal/config"
)

type stubSessionStore struct {
	session      account.Session
	renewNext    bool
	err          error
	lastTouch    bool
	resolveCalls int
	deleteCalls  int
}

/** Create records no-op creation for unit tests. */
func (s *stubSessionStore) Create(context.Context, account.SessionRecord, time.Duration) error {
	return s.err
}

/** Resolve captures touch hints and simulates throttled Redis renewal. */
func (s *stubSessionStore) Resolve(_ context.Context, _ string, _, _ time.Duration, touch bool) (account.Session, bool, error) {
	s.resolveCalls++
	s.lastTouch = touch
	if s.err != nil {
		return account.Session{}, false, s.err
	}
	return s.session, touch && s.renewNext, nil
}

/** Delete records session deletion outcomes for logout tests. */
func (s *stubSessionStore) Delete(context.Context, int64, string) error {
	s.deleteCalls++
	return s.err
}

/** RevokeUser records full-user revocation outcomes. */
func (s *stubSessionStore) RevokeUser(context.Context, int64) error {
	return s.err
}

func newSlidingTestServer(store account.SessionStore) *Server {
	server := &Server{
		Config: config.Config{
			CookieSecure: false,
			TrustProxy:   true,
		},
		limiter: newRateLimiter(),
		Accounts: account.NewService(
			nil,
			store,
			config.Config{},
			nil,
		),
	}
	return server
}

func TestWithUserRefreshesCookieOnlyWhenThrottledRenewalFires(t *testing.T) {
	token, _, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	store := &stubSessionStore{
		session: account.Session{
			ID:    42,
			Email: "user@example.invalid",
			CSRF:  "csrf-token-value",
		},
		renewNext: true,
	}
	handler := newSlidingTestServer(store).Handler()

	// Normal browser API request triggers touch=true and syncs refreshed Max-Age cookie
	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/me", nil)
	req.AddCookie(&http.Cookie{
		Name:  sessionCookie,
		Value: token,
	})
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || !store.lastTouch {
		t.Fatalf("expected 200 with touch=true, got status=%d touch=%t body=%s", resp.Code, store.lastTouch, resp.Body.String())
	}
	cookies := resp.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || cookies[0].Value != token || cookies[0].MaxAge != int(sessionLifetime.Seconds()) {
		t.Fatalf("expected refreshed session cookie, got %+v", cookies)
	}

	// Non-renewing read omits Set-Cookie
	store.renewNext = false
	reqNoRenew := httptest.NewRequest(http.MethodGet, "http://localhost/api/me", nil)
	reqNoRenew.AddCookie(&http.Cookie{
		Name:  sessionCookie,
		Value: token,
	})
	respNoRenew := httptest.NewRecorder()
	handler.ServeHTTP(respNoRenew, reqNoRenew)
	if respNoRenew.Code != http.StatusOK || len(respNoRenew.Result().Cookies()) != 0 {
		t.Fatalf("non-renewing request should not emit Set-Cookie: status=%d cookies=%+v", respNoRenew.Code, respNoRenew.Result().Cookies())
	}
}

func TestInternalSSROrWebSocketOrBadCSRFSkipsCookieRefresh(t *testing.T) {
	store := &stubSessionStore{
		session: account.Session{
			ID:    42,
			Email: "user@example.invalid",
			CSRF:  "csrf-token-value",
		},
		renewNext: true,
	}
	handler := newSlidingTestServer(store).Handler()

	// Internal SSR/OG read sets X-Session-Touch: skip -> touch=false and no Set-Cookie
	ssrReq := httptest.NewRequest(http.MethodGet, "http://localhost/api/me", nil)
	ssrReq.Header.Set("X-Session-Touch", "skip")
	ssrReq.AddCookie(&http.Cookie{
		Name:  sessionCookie,
		Value: "tok",
	})
	ssrResp := httptest.NewRecorder()
	handler.ServeHTTP(ssrResp, ssrReq)
	if ssrResp.Code != http.StatusOK || store.lastTouch || len(ssrResp.Result().Cookies()) != 0 {
		t.Fatalf("SSR request should skip sliding touch and Set-Cookie: status=%d touch=%t cookies=%+v", ssrResp.Code, store.lastTouch, ssrResp.Result().Cookies())
	}

	// WebSocket upgrade sets Upgrade: websocket -> touch=false and no Set-Cookie
	wsReq := httptest.NewRequest(http.MethodGet, "http://localhost/api/ws/markets", nil)
	wsReq.Header.Set("Upgrade", "websocket")
	wsReq.AddCookie(&http.Cookie{
		Name:  sessionCookie,
		Value: "tok",
	})
	wsResp := httptest.NewRecorder()
	handler.ServeHTTP(wsResp, wsReq)
	if store.lastTouch || len(wsResp.Result().Cookies()) != 0 {
		t.Fatalf("WebSocket upgrade should skip sliding touch and Set-Cookie: touch=%t cookies=%+v", store.lastTouch, wsResp.Result().Cookies())
	}

	// Failed CSRF on logout does not clear or refresh cookie and does not delete session
	logoutReq := httptest.NewRequest(http.MethodPost, "http://localhost/api/auth/logout", nil)
	logoutReq.Header.Set("Origin", "http://localhost")
	logoutReq.AddCookie(&http.Cookie{
		Name:  sessionCookie,
		Value: "tok",
	})
	logoutResp := httptest.NewRecorder()
	handler.ServeHTTP(logoutResp, logoutReq)
	if logoutResp.Code != http.StatusForbidden || store.deleteCalls != 0 || len(logoutResp.Result().Cookies()) != 0 {
		t.Fatalf("CSRF failure should block logout without Set-Cookie: status=%d deletes=%d cookies=%+v", logoutResp.Code, store.deleteCalls, logoutResp.Result().Cookies())
	}
}

func TestRedisOutageReturnsServiceUnavailableInsteadOfUnauthorized(t *testing.T) {
	store := &stubSessionStore{
		err: account.ErrSessionStoreUnavailable,
	}
	handler := newSlidingTestServer(store).Handler()

	meReq := httptest.NewRequest(http.MethodGet, "http://localhost/api/me", nil)
	meReq.AddCookie(&http.Cookie{
		Name:  sessionCookie,
		Value: "tok",
	})
	meResp := httptest.NewRecorder()
	handler.ServeHTTP(meResp, meReq)
	if meResp.Code != http.StatusServiceUnavailable {
		t.Fatalf("Redis outage on /api/me must return 503, got %d (%s)", meResp.Code, meResp.Body.String())
	}

	rssReq := httptest.NewRequest(http.MethodGet, "http://localhost/feed.xml", nil)
	rssReq.AddCookie(&http.Cookie{
		Name:  sessionCookie,
		Value: "tok",
	})
	rssResp := httptest.NewRecorder()
	handler.ServeHTTP(rssResp, rssReq)
	if rssResp.Code != http.StatusServiceUnavailable || !strings.Contains(rssResp.Body.String(), `"code":"temporarily_unavailable"`) {
		t.Fatalf("Redis outage on /feed.xml must return 503 temporarily_unavailable, got %d (%s)", rssResp.Code, rssResp.Body.String())
	}
}
