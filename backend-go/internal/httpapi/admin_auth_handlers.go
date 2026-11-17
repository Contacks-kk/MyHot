package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aihot/backend-go/internal/adminauth"
)

const adminSessionLifetime = adminauth.SessionDays * 24 * time.Hour

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	query, _ := url.ParseQuery(r.URL.RawQuery)
	returnTo := query.Get("return")
	if returnTo == "" {
		returnTo = "/admin"
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, adminLoginPage(returnTo, ""), http.StatusFound)
}

func (s *Server) adminLoginOptions(w http.ResponseWriter, _ *http.Request) {
	password, feishu := s.AdminAuth.Options()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"password": password, "feishu": feishu})
}

func (s *Server) adminPasswordLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeAdminProblem(w, http.StatusForbidden, "forbidden", "Invalid request origin.")
		return
	}
	returnTo, password, ok := parseAdminPasswordRequest(w, r)
	if !ok {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "The request could not be processed.")
		return
	}
	if returnTo == "" {
		returnTo = "/admin"
	}
	ip := clientKey(r, s.Config.TrustProxy)
	clientLimited := s.limiter.limited("admin-login:ip:"+ip, 10, 15*time.Minute)
	globalLimited := s.limiter.limited("admin-login:all", 50, 15*time.Minute)
	if clientLimited || globalLimited {
		http.Redirect(w, r, adminLoginPage(returnTo, "too-many"), http.StatusSeeOther)
		return
	}
	token, target, err := s.AdminAuth.PasswordLogin(r.Context(), password, returnTo, r.UserAgent())
	if err != nil {
		loginError := "wrong"
		if strings.Contains(err.Error(), ": unset") {
			loginError = "unset"
		}
		http.Redirect(w, r, adminLoginPage(returnTo, loginError), http.StatusSeeOther)
		return
	}
	w.Header().Add("Set-Cookie", adminauth.Cookie(adminauth.SessionCookie, token, int(adminSessionLifetime.Seconds()), s.adminCookieSecure()))
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) adminFeishuLogin(w http.ResponseWriter, r *http.Request) {
	returnTo := r.URL.Query().Get("return")
	if returnTo == "" {
		returnTo = "/admin"
	}
	_, feishu := s.AdminAuth.Options()
	if !feishu {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, adminLoginPage(returnTo, ""), http.StatusFound)
		return
	}
	s.adminFeishuRedirect(w, returnTo)
}

func (s *Server) adminFeishuRedirect(w http.ResponseWriter, returnTo string) {
	location, state, err := s.AdminAuth.LoginRedirect(returnTo)
	if err != nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	w.Header().Set("Set-Cookie", adminauth.Cookie(adminauth.StateCookie, state, 600, s.adminCookieSecure()))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusFound)
}

func (s *Server) adminOAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	stateCookie := adminauth.ParseCookies(r.Header.Get("Cookie"))[adminauth.StateCookie]
	token, target, err := s.AdminAuth.CompleteLogin(r.Context(), r.URL.Query().Get("code"), r.URL.Query().Get("state"), stateCookie, r.UserAgent())
	if err != nil {
		message := "登录失败，请稍后再试"
		if errors.Is(err, adminauth.ErrLoginRejected) {
			switch {
			case strings.Contains(err.Error(), ": state"):
				message = "登录状态已失效，请重新登录"
			case strings.Contains(err.Error(), ": not allowlisted"):
				message = "这个飞书账号没有后台权限"
			}
		}
		if !errors.Is(err, adminauth.ErrLoginRejected) {
			// Provider details can include network information; don't return them to the browser.
			log.Printf("admin OAuth login failed: %T", err)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>登录失败 · %s</title><p style="font:16px system-ui;padding:40px">%s。<a href="/api/auth/login">重新登录</a></p>`, html.EscapeString(s.Config.SiteName), html.EscapeString(message))
		return
	}
	w.Header().Add("Set-Cookie", adminauth.Cookie(adminauth.SessionCookie, token, int(adminSessionLifetime.Seconds()), s.adminCookieSecure()))
	w.Header().Add("Set-Cookie", adminauth.Cookie(adminauth.StateCookie, "", 0, s.adminCookieSecure()))
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) adminAuthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.AdminAuth == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	admin, err := s.AdminAuth.SessionPrincipal(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	if admin == nil || admin.Dev {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if s.AdminAuth == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	if err := s.AdminAuth.EndSession(r.Context(), r.Header.Get("Cookie")); err != nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	w.Header().Set("Set-Cookie", adminauth.Cookie(adminauth.SessionCookie, "", 0, s.adminCookieSecure()))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) adminMe(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": admin.Name, "csrf": admin.CSRF, "dev": admin.Dev, "super": admin.Super})
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (*adminauth.AdminPrincipal, bool) {
	if w.Header().Get("Cache-Control") != "private, no-store" {
		w.Header().Set("Cache-Control", "no-store")
	}
	if s.AdminAuth == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return nil, false
	}
	admin, err := s.AdminAuth.SessionPrincipal(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		log.Printf("admin session lookup failed: %T", err)
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return nil, false
	}
	if admin == nil {
		writeAdminProblem(w, http.StatusUnauthorized, "unauthorized", "Sign in to the admin first.")
		return nil, false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		provided := []byte(r.Header.Get("X-CSRF-Token"))
		actual := []byte(admin.CSRF)
		if len(provided) != len(actual) || subtle.ConstantTimeCompare(provided, actual) != 1 {
			writeAdminProblem(w, http.StatusForbidden, "forbidden", "Missing or stale CSRF token.")
			return nil, false
		}
	}
	return admin, true
}

func (s *Server) adminCookieSecure() bool {
	return strings.HasPrefix(strings.ToLower(s.Config.SiteURL), "https://")
}

func parseAdminPasswordRequest(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if r.Body == nil || r.ContentLength == 0 {
		return "", "", true
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return "", "", false
	}
	switch mediaType {
	case "application/x-www-form-urlencoded":
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		if err := r.ParseForm(); err != nil {
			return "", "", false
		}
		return lastFormValue(r.PostForm, "return"), lastFormValue(r.PostForm, "password"), true
	case "application/json":
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		var fields map[string]any
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
			return "", "", false
		}
		return stringField(fields["return"]), stringField(fields["password"]), true
	default:
		return "", "", false
	}
}

func stringField(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func lastFormValue(values url.Values, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return items[len(items)-1]
}

func adminLoginPage(returnTo, errCode string) string {
	if adminauth.SafeReturn(returnTo) == "/super" {
		return "/super?error=" + url.QueryEscape(errCode)
	}
	location := "/admin/login?return=" + url.QueryEscape(adminauth.SafeReturn(returnTo))
	if errCode != "" {
		location += "&error=" + url.QueryEscape(errCode)
	}
	return location
}

func writeAdminProblem(w http.ResponseWriter, status int, code, detail string) {
	idBytes := make([]byte, 16)
	requestID := "00000000-0000-4000-8000-000000000000"
	if _, err := rand.Read(idBytes); err == nil {
		idBytes[6] = idBytes[6]&0x0f | 0x40
		idBytes[8] = idBytes[8]&0x3f | 0x80
		encoded := hex.EncodeToString(idBytes)
		requestID = encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
	}
	titles := map[int]string{400: "Bad request", 401: "Unauthorized", 403: "Forbidden", 404: "Not found", 409: "Conflict", 429: "Too many requests", 500: "Internal error", 503: "Temporarily unavailable"}
	title := titles[status]
	if title == "" {
		title = "Error"
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", requestID)
	if w.Header().Get("Cache-Control") != "private, no-store" {
		w.Header().Set("Cache-Control", "no-store")
	}
	problem := map[string]any{
		"type": "/problems/" + strings.ReplaceAll(code, "_", "-"), "title": title, "status": status,
		"detail": detail, "code": code, "requestId": requestID,
	}
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "30")
		problem["retryAfter"] = 30
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem)
}
