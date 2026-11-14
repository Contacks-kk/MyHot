package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"aihot/backend-go/internal/adminauth"
)

/** adminAccountLogin signs in an authorized user with their account credentials. */
func (s *Server) adminAccountLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeAdminProblem(w, http.StatusForbidden, "forbidden", "Invalid request origin.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if err := r.ParseForm(); err != nil {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "The request could not be processed.")
		return
	}
	returnTo := r.PostForm.Get("return")
	ip := clientKey(r, s.Config.TrustProxy)
	if s.limiter.limited("admin-login:ip:"+ip, 10, 15*time.Minute) || s.limiter.limited("admin-login:all", 50, 15*time.Minute) {
		http.Redirect(w, r, adminLoginPage(returnTo, "too-many"), http.StatusSeeOther)
		return
	}
	token, target, err := s.AdminAuth.AccountLogin(r.Context(), r.PostForm.Get("email"), r.PostForm.Get("password"), returnTo, r.UserAgent())
	if err != nil {
		if !errors.Is(err, adminauth.ErrLoginRejected) {
			writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
			return
		}
		http.Redirect(w, r, adminLoginPage(returnTo, "wrong"), http.StatusSeeOther)
		return
	}
	w.Header().Add("Set-Cookie", adminauth.Cookie(adminauth.SessionCookie, token, int(adminSessionLifetime.Seconds()), s.adminCookieSecure()))
	http.Redirect(w, r, target, http.StatusSeeOther)
}

/** requireSuper restricts user privilege management to a key-authenticated session. */
func (s *Server) requireSuper(w http.ResponseWriter, r *http.Request) (*adminauth.AdminPrincipal, bool) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return nil, false
	}
	if !admin.Super {
		writeAdminProblem(w, http.StatusForbidden, "forbidden", "Super administrator access is required.")
		return nil, false
	}
	return admin, true
}

/** superUsers lists accounts for the super administrator. */
func (s *Server) superUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSuper(w, r); !ok {
		return
	}
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil && r.URL.Query().Get("after") != "" || after < 0 {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "Invalid cursor.")
		return
	}
	accounts, err := s.AdminAuth.ListAccounts(r.Context(), after)
	if err != nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": accounts})
}

/** superSetAdmin grants or revokes an account's administrator privileges. */
func (s *Server) superSetAdmin(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireSuper(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var body struct {
		IsAdmin *bool `json:"isAdmin"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err != nil || id <= 0 || json.NewDecoder(r.Body).Decode(&body) != nil || body.IsAdmin == nil {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "Invalid account or grant.")
		return
	}
	if err := s.AdminAuth.SetAccountAdmin(r.Context(), admin.UserID, id, *body.IsAdmin); err != nil {
		if errors.Is(err, adminauth.ErrNotFound) {
			writeAdminProblem(w, http.StatusNotFound, "not_found", "Account not found.")
			return
		}
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"isAdmin": *body.IsAdmin})
}
