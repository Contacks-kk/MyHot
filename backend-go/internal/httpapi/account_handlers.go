package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"aihot/backend-go/internal/account"
	"aihot/backend-go/internal/mailer"
)

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) checkRegistrationCode(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if s.limiter.limited("code-check:"+clientKey(r, s.Config.TrustProxy), 30, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后重试")
		return
	}
	var input struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	email, err := normalizeEmail(input.Email)
	if err != nil {
		writeError(w, http.StatusBadRequest, "验证码错误")
		return
	}
	timing, err := s.Accounts.CheckVerification(email, input.Code)
	if errors.Is(err, account.ErrInvalidToken) {
		writeError(w, http.StatusBadRequest, "验证码错误")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "验证码暂时无法校验")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verified": true, "serverTime": timing.ServerTime, "expiresAt": timing.ExpiresAt})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if s.limiter.limited("register:"+clientKey(r, s.Config.TrustProxy), 8, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后重试")
		return
	}
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if !s.Config.OpenRegistration || !s.mailConfigured() {
		writeError(w, http.StatusForbidden, "当前暂未开放注册")
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	email, err := normalizeEmail(input.Email)
	if err != nil || len(input.Password) < 12 || len(input.Password) > 256 {
		writeError(w, http.StatusBadRequest, "请提供有效邮箱和至少 12 位密码")
		return
	}
	result, err := s.Accounts.Register(r.Context(), email, input.Password, input.Code)
	if errors.Is(err, account.ErrDuplicateAccount) {
		writeError(w, http.StatusConflict, "该邮箱已注册")
		return
	}
	if errors.Is(err, account.ErrInvalidToken) {
		writeError(w, http.StatusBadRequest, "验证码无效或已过期，请重新获取")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "账号暂时无法创建")
		return
	}
	s.writeUserSession(w, http.StatusCreated, result)
}

/** Authenticate user account credentials and create session. */
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	// Check rate limit
	if s.limiter.limited("login:"+clientKey(r, s.Config.TrustProxy), 12, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后重试")
		return
	}

	// Verify request origin
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}

	// Parse credentials from form or JSON body
	var input credentials
	contentType := r.Header.Get("Content-Type")
	isForm := strings.HasPrefix(contentType, "application/x-www-form-urlencoded") || strings.HasPrefix(contentType, "multipart/form-data")
	if isForm {
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "请求内容无效")
			return
		}
		input.Email = r.PostForm.Get("email")
		input.Password = r.PostForm.Get("password")
	} else {
		if !readJSON(w, r, &input) {
			return
		}
	}

	// Validate and normalize email address
	email, err := normalizeEmail(input.Email)
	if err != nil {
		if isForm {
			http.Redirect(w, r, "/login?error=invalid", http.StatusSeeOther)
			return
		}
		writeError(w, http.StatusUnauthorized, "邮箱或密码不正确")
		return
	}

	// Verify account credentials
	result, err := s.Accounts.Login(r.Context(), email, input.Password)
	if errors.Is(err, account.ErrInvalidCredentials) {
		if isForm {
			http.Redirect(w, r, "/login?error=credentials", http.StatusSeeOther)
			return
		}
		writeError(w, http.StatusUnauthorized, "邮箱或密码不正确")
		return
	}
	if errors.Is(err, account.ErrUnverifiedAccount) {
		if isForm {
			http.Redirect(w, r, "/verify-email", http.StatusSeeOther)
			return
		}
		writeError(w, http.StatusForbidden, "请先完成邮箱验证")
		return
	}
	if err != nil {
		if isForm {
			http.Redirect(w, r, "/login?error=unavailable", http.StatusSeeOther)
			return
		}
		writeError(w, http.StatusServiceUnavailable, "登录暂不可用")
		return
	}

	// Establish authenticated user session
	s.writeUserSession(w, http.StatusOK, result)
	if isForm {
		returnTo := r.URL.Query().Get("return")
		if returnTo == "" {
			returnTo = r.PostForm.Get("return")
		}
		if returnTo == "" || !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
			returnTo = "/"
		}
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
}

/** setUserSessionCookie writes or refreshes the HttpOnly browser session cookie. */
func (s *Server) setUserSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(sessionLifetime),
		MaxAge:   int(sessionLifetime.Seconds()),
		HttpOnly: true,
		Secure:   s.Config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) writeUserSession(w http.ResponseWriter, status int, result account.LoginResult) {
	s.setUserSessionCookie(w, result.Token)
	writeJSON(w, status, map[string]any{
		"user": map[string]any{
			"id":    result.UserID,
			"email": result.Email,
		},
		"csrfToken": result.CSRF,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	user, _, rawToken, err := s.resolveUserSession(r, false)
	if err != nil {
		if errors.Is(err, errNoSession) || errors.Is(err, account.ErrNoSession) {
			writeError(w, http.StatusUnauthorized, "请先登录")
		} else {
			writeError(w, http.StatusServiceUnavailable, "会话服务暂不可用")
		}
		return
	}
	if csrf := r.Header.Get("X-CSRF-Token"); csrf == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(user.CSRF)) != 1 {
		writeError(w, http.StatusForbidden, "页面已过期，请刷新后重试")
		return
	}
	if err := s.Accounts.Logout(r.Context(), user.ID, rawToken); err != nil {
		writeError(w, http.StatusServiceUnavailable, "会话服务暂不可用")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, user userContext) {
	if s.limiter.limited("delete:"+strconv.FormatInt(user.ID, 10), 5, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后重试")
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if err := s.Accounts.DeleteAccount(r.Context(), user.ID, input.Password); errors.Is(err, account.ErrPasswordMismatch) {
		writeError(w, http.StatusForbidden, "密码不正确，账号未删除")
		return
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, "账号暂时无法删除")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if s.limiter.limited("verify:"+clientKey(r, s.Config.TrustProxy), 10, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后重试")
		return
	}
	var input struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	email, err := normalizeEmail(input.Email)
	if err == nil {
		err = s.Accounts.VerifyEmail(r.Context(), email, input.Code)
	}
	if err != nil {
		if errors.Is(err, account.ErrInvalidToken) || email == "" {
			writeError(w, http.StatusBadRequest, "验证码无效或已过期，请重新获取")
		} else {
			writeError(w, http.StatusServiceUnavailable, "邮箱暂时无法验证")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "邮箱验证完成，请登录"})
}

// send verification code
func (s *Server) resendVerification(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if s.limiter.limited("resend:"+clientKey(r, s.Config.TrustProxy), 12, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后重试")
		return
	}
	if !s.mailConfigured() {
		writeError(w, http.StatusServiceUnavailable, "邮件服务尚未配置")
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	email, err := normalizeEmail(input.Email)
	if err != nil {
		writeError(w, http.StatusBadRequest, "请提供有效邮箱")
		return
	}
	timing, err := s.Accounts.ResendVerification(r.Context(), email)
	var limit *account.VerificationLimit
	if errors.As(err, &limit) {
		now := time.Now()
		seconds := int(limit.RetryAt.Sub(now).Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "请稍后重试", "serverTime": now, "retryAt": limit.RetryAt})
		return
	}
	if errors.Is(err, account.ErrDuplicateAccount) {
		writeError(w, http.StatusConflict, "该邮箱已注册并验证，请登录")
		return
	}
	if errors.Is(err, account.ErrRegistrationClosed) {
		writeError(w, http.StatusForbidden, "当前暂未开放注册")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "验证码暂时无法发送")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"message": "验证码已发送，60秒内有效", "serverTime": timing.ServerTime, "expiresAt": timing.ExpiresAt, "retryAt": timing.RetryAt})
}

// forgot password
func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if s.limiter.limited("forgot:"+clientKey(r, s.Config.TrustProxy), 5, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后重试")
		return
	}
	if !s.mailConfigured() {
		writeError(w, http.StatusServiceUnavailable, "邮件服务尚未配置")
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	email, err := normalizeEmail(input.Email)
	if err == nil {
		if err := s.Accounts.ForgotPassword(r.Context(), email); err != nil {
			writeError(w, http.StatusServiceUnavailable, "重置邮件暂时无法发送")
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "如账号存在，重置邮件会发送到该邮箱"})
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if s.limiter.limited("reset:"+clientKey(r, s.Config.TrustProxy), 10, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后重试")
		return
	}
	var input struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if len(input.Token) < 32 || len(input.Token) > 128 || len(input.Password) < 12 || len(input.Password) > 256 {
		writeError(w, http.StatusBadRequest, "链接无效或密码不符合要求")
		return
	}
	e := s.Accounts.ResetPassword(r.Context(), input.Token, input.Password)
	if errors.Is(e, account.ErrInvalidToken) {
		writeError(w, http.StatusBadRequest, "链接无效或已过期")
		return
	}
	if e != nil {
		writeError(w, http.StatusServiceUnavailable, "密码暂时无法更新")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "密码已更新，请重新登录"})
}

func (s *Server) mailConfigured() bool {
	return s.Config.SMTPHost != "" && s.Config.SMTPFrom != "" && s.Config.SMTPPort > 0 && s.Config.SMTPPort <= 65535
}
func (s *Server) mailer() mailer.Sender {
	if s.MailSender != nil {
		return s.MailSender
	}
	return mailer.SMTP{Host: s.Config.SMTPHost, Port: s.Config.SMTPPort, Username: s.Config.SMTPUsername, Password: s.Config.SMTPPassword, From: s.Config.SMTPFrom}
}

func normalizeEmail(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid email")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || !strings.Contains(value, "@") {
		return "", errors.New("invalid email")
	}
	return value, nil
}
