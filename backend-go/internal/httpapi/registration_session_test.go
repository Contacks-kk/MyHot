package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aihot/backend-go/internal/account"
	"aihot/backend-go/internal/config"
)

func TestRegistrationResponseEstablishesBrowserSession(t *testing.T) {
	for _, secure := range []bool{false, true} {
		server := &Server{Config: config.Config{CookieSecure: secure}}
		response := httptest.NewRecorder()
		server.writeUserSession(response, http.StatusCreated, account.LoginResult{UserID: 7, Email: "fixture@example.invalid", Token: "fixture-session-token", CSRF: "fixture-csrf"})
		cookies := response.Result().Cookies()
		if response.Code != 201 || len(cookies) != 1 {
			t.Fatalf("registration must issue one session: %d %v", response.Code, cookies)
		}
		cookie := cookies[0]
		if cookie.Name != sessionCookie || cookie.Value != "fixture-session-token" || !cookie.HttpOnly || cookie.Secure != secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != int(sessionLifetime.Seconds()) {
			t.Fatalf("unsafe registration cookie: %+v", cookie)
		}
		request := httptest.NewRequest(http.MethodGet, "http://localhost/api/me", nil)
		request.AddCookie(cookie)
		if value, err := request.Cookie(sessionCookie); err != nil || value.Value != "fixture-session-token" {
			t.Fatal("cookie cannot accompany the session confirmation request")
		}
		var body struct {
			CSRF string `json:"csrfToken"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.CSRF != "fixture-csrf" {
			t.Fatal("missing registration CSRF")
		}
		if strings.Contains(response.Body.String(), "fixture-session-token") {
			t.Fatal("session token leaked into JSON")
		}
	}
}
