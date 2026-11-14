package adminauth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"aihot/backend-go/internal/config"
)

const (
	SessionCookie = "aihot_admin"
	StateCookie   = "aihot_oauth_state"
	SessionDays   = 30
)

var ErrLoginRejected = errors.New("admin login rejected")

type FeishuClaims struct {
	AppID   string  `json:"appId"`
	UnionID *string `json:"unionId"`
	Email   *string `json:"email"`
	Name    *string `json:"-"`
}

func (c FeishuClaims) persisted() FeishuClaims {
	return FeishuClaims{AppID: c.AppID, UnionID: c.UnionID, Email: c.Email}
}

type AdminPrincipal struct {
	UserID int64  `json:"-"`
	Name   string `json:"name"`
	CSRF   string `json:"csrf"`
	Dev    bool   `json:"dev"`
	Super  bool   `json:"super"`
}

type SessionRecord struct {
	UserID      int64
	CSRF        string
	Name        string
	AuthMethod  string
	AuthBinding string
	AuthClaims  json.RawMessage
}

type FeishuProfile struct {
	UnionID         string `json:"union_id"`
	Email           string `json:"email"`
	EnterpriseEmail string `json:"enterprise_email"`
	Name            string `json:"name"`
}

type FeishuExchanger interface {
	Exchange(context.Context, string, string, string, string) (FeishuProfile, error)
}

type Service struct {
	repo   Repository
	config config.Config
	feishu FeishuExchanger
	now    func() time.Time
	random func([]byte) (int, error)
}

func NewService(repo Repository, cfg config.Config, exchanger FeishuExchanger) *Service {
	if exchanger == nil {
		exchanger = HTTPFeishuExchanger{Client: &http.Client{Timeout: 15 * time.Second}}
	}
	return &Service{repo: repo, config: cfg, feishu: exchanger, now: time.Now, random: rand.Read}
}

func (s *Service) Options() (password, feishu bool) {
	return s.config.AdminPassword != "", s.feishuConfigured()
}

func (s *Service) feishuConfigured() bool {
	return s.config.FeishuLoginAppID != "" && s.config.FeishuLoginAppSecret != ""
}

func (s *Service) SessionPrincipal(ctx context.Context, cookieHeader string) (*AdminPrincipal, error) {
	token := ParseCookies(cookieHeader)[SessionCookie]
	if token != "" {
		hash := hashToken(token)
		record, err := s.repo.FindSession(ctx, hash)
		if err == nil {
			authorized, super, authErr := s.authorizeSession(ctx, record)
			if authErr != nil {
				return nil, authErr
			}
			if authorized {
				return &AdminPrincipal{UserID: record.UserID, Name: record.Name, CSRF: record.CSRF, Super: super}, nil
			}
			if err := s.repo.DeleteSession(ctx, hash); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	if s.config.DevAdmin && s.config.EnvironmentName != "production" {
		name := s.config.DevAdminDisplayName
		if name == "" {
			name = "Dev Admin"
		}
		return &AdminPrincipal{Name: name, CSRF: "dev", Dev: true}, nil
	}
	return nil, nil
}

func (s *Service) PasswordLogin(ctx context.Context, password, returnTo, userAgent string) (string, string, error) {
	expected := s.config.AdminPassword
	if len(expected) < 12 {
		return "", "", fmt.Errorf("%w: unset", ErrLoginRejected)
	}
	given := hmacBytes([]byte("admin-password"), []byte(password))
	wanted := hmacBytes([]byte("admin-password"), []byte(expected))
	if subtle.ConstantTimeCompare(given, wanted) != 1 {
		return "", "", fmt.Errorf("%w: wrong", ErrLoginRejected)
	}
	key, err := s.secret()
	if err != nil {
		return "", "", err
	}
	binding := sessionBinding("password", expected, key)
	token, csrf, err := s.newSessionValues()
	if err != nil {
		return "", "", err
	}
	if _, err := s.repo.CreatePasswordSession(ctx, hashToken(token), csrf, binding, s.now().Add(SessionDays*24*time.Hour), truncatePointer(userAgent, 300)); err != nil {
		return "", "", err
	}
	return token, SafeReturn(returnTo), nil
}

func (s *Service) LoginRedirect(returnTo string) (string, string, error) {
	if s.config.FeishuLoginAppID == "" {
		return "", "", errors.New("FEISHU_LOGIN_APP_ID is not configured")
	}
	key, err := s.secret()
	if err != nil {
		return "", "", err
	}
	stateBytes := make([]byte, 16)
	if _, err := s.random(stateBytes); err != nil {
		return "", "", err
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes) + "|" + SafeReturn(returnTo)
	signed, err := s.sign(state, key)
	if err != nil {
		return "", "", err
	}
	values := url.Values{}
	values.Set("client_id", s.config.FeishuLoginAppID)
	values.Set("redirect_uri", s.callbackURL())
	values.Set("response_type", "code")
	values.Set("state", signed)
	return "https://passport.feishu.cn/suite/passport/oauth/authorize?" + values.Encode(), signed, nil
}

func (s *Service) CompleteLogin(ctx context.Context, code, state, stateCookie, userAgent string) (string, string, error) {
	key, err := s.secret()
	if err != nil {
		return "", "", err
	}
	expected, errExpected := s.unsign(stateCookie, key)
	given, errGiven := s.unsign(state, key)
	if errExpected != nil || errGiven != nil || expected == "" || expected != given {
		return "", "", fmt.Errorf("%w: state", ErrLoginRejected)
	}
	parts := strings.SplitN(given, "|", 2)
	returnTo := "/admin"
	if len(parts) == 2 {
		returnTo = SafeReturn(parts[1])
	}
	if !s.feishuConfigured() {
		return "", "", fmt.Errorf("%w: provider disabled", ErrLoginRejected)
	}
	profile, err := s.feishu.Exchange(ctx, code, s.config.FeishuLoginAppID, s.config.FeishuLoginAppSecret, s.callbackURL())
	if err != nil {
		return "", "", err
	}
	claims := FeishuClaims{AppID: s.config.FeishuLoginAppID}
	unionID := profile.UnionID
	if unionID != "" {
		claims.UnionID = &unionID
	}
	profileEmail := profile.EnterpriseEmail
	if profileEmail == "" {
		profileEmail = profile.Email
	}
	if email := strings.ToLower(profileEmail); email != "" {
		claims.Email = &email
	}
	if profile.Name != "" {
		claims.Name = &profile.Name
	}
	if !s.allowed(claims) {
		return "", "", fmt.Errorf("%w: not allowlisted", ErrLoginRejected)
	}
	accounts, ok := s.repo.(accountRepository)
	if !ok || claims.Email == nil {
		return "", "", fmt.Errorf("%w: not allowlisted", ErrLoginRejected)
	}
	account, err := accounts.FindAccount(ctx, 0, *claims.Email)
	if errors.Is(err, ErrNotFound) || err == nil && (!account.IsAdmin || account.VerifiedAt == nil) {
		return "", "", fmt.Errorf("%w: not allowlisted", ErrLoginRejected)
	}
	if err != nil {
		return "", "", err
	}
	identity := struct {
		AppID   string  `json:"appId"`
		UnionID *string `json:"unionId"`
		Email   *string `json:"email"`
	}{claims.AppID, claims.UnionID, claims.Email}
	binding := sessionBinding("feishu", identity, key)
	token, csrf, err := s.newSessionValues()
	if err != nil {
		return "", "", err
	}
	if _, err := s.repo.CreateFeishuSession(ctx, claims, hashToken(token), csrf, binding, s.now().Add(SessionDays*24*time.Hour), truncatePointer(userAgent, 300)); err != nil {
		return "", "", err
	}
	return token, returnTo, nil
}

func (s *Service) EndSession(ctx context.Context, cookieHeader string) error {
	token := ParseCookies(cookieHeader)[SessionCookie]
	if token == "" {
		return nil
	}
	return s.repo.DeleteSession(ctx, hashToken(token))
}

func (s *Service) allowed(claims FeishuClaims) bool {
	for _, allowed := range s.config.AdminFeishuUnionIDs {
		if claims.UnionID != nil && *claims.UnionID == allowed {
			return true
		}
	}
	for _, allowed := range s.config.AdminEmails {
		if claims.Email != nil && *claims.Email == allowed {
			return true
		}
	}
	return false
}

func (s *Service) sessionAuthorized(record SessionRecord) bool {
	key, err := s.secret()
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(record.AuthBinding) {
		return false
	}
	var binding string
	switch record.AuthMethod {
	case "password":
		if len(s.config.AdminPassword) < 12 || len(record.AuthClaims) != 0 && string(record.AuthClaims) != "null" {
			return false
		}
		binding = sessionBinding("password", s.config.AdminPassword, key)
	case "feishu":
		claims, ok := decodeClaims(record.AuthClaims)
		if !ok || !s.feishuConfigured() || claims.AppID != s.config.FeishuLoginAppID || !s.allowed(claims) {
			return false
		}
		identity := struct {
			AppID   string  `json:"appId"`
			UnionID *string `json:"unionId"`
			Email   *string `json:"email"`
		}{claims.AppID, claims.UnionID, claims.Email}
		binding = sessionBinding("feishu", identity, key)
	default:
		return false
	}
	want, err := hex.DecodeString(binding)
	if err != nil {
		return false
	}
	got, err := hex.DecodeString(record.AuthBinding)
	return err == nil && len(got) == len(want) && subtle.ConstantTimeCompare(want, got) == 1
}

func decodeClaims(raw json.RawMessage) (FeishuClaims, bool) {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		return FeishuClaims{}, false
	}
	for _, key := range []string{"appId", "unionId", "email"} {
		if _, ok := fields[key]; !ok {
			return FeishuClaims{}, false
		}
	}
	var claims FeishuClaims
	if json.Unmarshal(raw, &claims) != nil || claims.AppID == "" {
		return FeishuClaims{}, false
	}
	for _, value := range []*string{claims.UnionID, claims.Email} {
		if value != nil && *value == "" {
			return FeishuClaims{}, false
		}
	}
	if claims.Email != nil && *claims.Email != strings.ToLower(*claims.Email) {
		return FeishuClaims{}, false
	}
	return claims, true
}

func (s *Service) secret() ([]byte, error) {
	if s.config.SessionSecret == "" {
		return nil, errors.New("SESSION_SECRET is not configured")
	}
	return []byte(s.config.SessionSecret), nil
}

func (s *Service) callbackURL() string {
	return strings.TrimRight(s.config.SiteURL, "/") + "/api/auth/callback"
}

func (s *Service) newSessionValues() (string, string, error) {
	tokenBytes, csrfBytes := make([]byte, 32), make([]byte, 18)
	if _, err := s.random(tokenBytes); err != nil {
		return "", "", err
	}
	if _, err := s.random(csrfBytes); err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(tokenBytes), base64.RawURLEncoding.EncodeToString(csrfBytes), nil
}

func sessionBinding(method string, identity any, key []byte) string {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode([]any{"admin-session-v1", method, identity})
	value := bytes.TrimSuffix(output.Bytes(), []byte("\n"))
	return hex.EncodeToString(hmacBytes(key, value))
}

func (s *Service) sign(value string, key []byte) (string, error) {
	return value + "." + base64.RawURLEncoding.EncodeToString(hmacBytes(key, []byte(value))), nil
}

func (s *Service) unsign(value string, key []byte) (string, error) {
	i := strings.LastIndexByte(value, '.')
	if i <= 0 {
		return "", errors.New("invalid signature")
	}
	got, err := base64.RawURLEncoding.DecodeString(value[i+1:])
	if err != nil {
		return "", err
	}
	want := hmacBytes(key, []byte(value[:i]))
	if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
		return "", errors.New("invalid signature")
	}
	return value[:i], nil
}

func hmacBytes(key, value []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}

func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func ParseCookies(header string) map[string]string {
	out := make(map[string]string)
	for _, part := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(part, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" {
			continue
		}
		if decoded, err := url.PathUnescape(value); err == nil {
			value = decoded
		}
		out[name] = value
	}
	return out
}

func Cookie(name, value string, maxAge int, secure bool) string {
	encoded := url.PathEscape(value)
	if value == "" {
		encoded = ""
	}
	result := fmt.Sprintf("%s=%s; Path=/; HttpOnly; SameSite=Lax; Max-Age=%d", name, encoded, maxAge)
	if secure {
		result += "; Secure"
	}
	return result
}

func SafeReturn(target string) string {
	path := target
	if strings.HasPrefix(strings.ToLower(path), "http://") || strings.HasPrefix(strings.ToLower(path), "https://") {
		parsed, err := url.Parse(path)
		if err != nil {
			return "/admin"
		}
		path = parsed.EscapedPath()
		if parsed.RawQuery != "" {
			path += "?" + parsed.RawQuery
		}
	}
	if strings.HasPrefix(path, "//") || !(path == "/admin" || strings.HasPrefix(path, "/admin/") || strings.HasPrefix(path, "/admin?") || path == "/super" || strings.HasPrefix(path, "/super?")) {
		return "/admin"
	}
	return path
}

func truncatePointer(value string, max int) *string {
	if value == "" {
		return nil
	}
	if len(value) > max {
		value = value[:max]
		for !utf8.ValidString(value) && len(value) > 0 {
			value = value[:len(value)-1]
		}
	}
	return &value
}

type HTTPFeishuExchanger struct{ Client *http.Client }

func (e HTTPFeishuExchanger) Exchange(ctx context.Context, code, appID, appSecret, callback string) (FeishuProfile, error) {
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {appID}, "client_secret": {appSecret}, "code": {code}, "redirect_uri": {callback}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://passport.feishu.cn/suite/passport/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return FeishuProfile{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenResponse, err := client.Do(request)
	if err != nil {
		return FeishuProfile{}, err
	}
	token, err := decodeFeishuToken(tokenResponse)
	if err != nil {
		return FeishuProfile{}, err
	}
	if token.AccessToken == "" {
		return FeishuProfile{}, fmt.Errorf("Feishu token exchange failed (HTTP %d)", token.Status)
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://passport.feishu.cn/suite/passport/oauth/userinfo", nil)
	if err != nil {
		return FeishuProfile{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err := client.Do(request)
	if err != nil {
		return FeishuProfile{}, err
	}
	defer response.Body.Close()
	var profile FeishuProfile
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&profile); err != nil {
		return FeishuProfile{}, fmt.Errorf("Feishu profile response is not valid JSON (HTTP %d)", response.StatusCode)
	}
	return profile, nil
}

type feishuToken struct {
	AccessToken string
	Status      int
}

func decodeFeishuToken(response *http.Response) (feishuToken, error) {
	defer response.Body.Close()
	var value struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value); err != nil {
		return feishuToken{}, fmt.Errorf("Feishu token response is not valid JSON (HTTP %d)", response.StatusCode)
	}
	return feishuToken{AccessToken: value.AccessToken, Status: response.StatusCode}, nil
}
