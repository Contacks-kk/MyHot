package adminauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/auth"
	"aihot/backend-go/internal/config"
)

type testRepository struct {
	session        SessionRecord
	sessions       map[string]SessionRecord
	hash           string
	deleted        []string
	created        int
	err            error
	account        AdminAccount
	passwordUserID int64
	feishuUserID   int64
}

/** CreatePasswordSession stores a fake password session for service tests. */
func (r *testRepository) CreatePasswordSession(_ context.Context, hash, csrf, binding string, _ time.Time, _ *string) (int64, error) {
	r.created++
	r.hash = hash
	userID := r.passwordUserID
	if userID == 0 {
		userID = 7
	}
	r.session = SessionRecord{UserID: userID, CSRF: csrf, Name: "Password Admin", AuthMethod: "password", AuthBinding: binding}
	r.saveSession(hash, r.session)
	return userID, r.err
}

/** CreateFeishuSession stores a fake Feishu session for service tests. */
func (r *testRepository) CreateFeishuSession(_ context.Context, claims FeishuClaims, hash, csrf, binding string, _ time.Time, _ *string) (int64, error) {
	r.created++
	r.hash = hash
	encoded, _ := json.Marshal(claims.persisted())
	userID := r.feishuUserID
	if userID == 0 {
		userID = 8
	}
	r.session = SessionRecord{UserID: userID, CSRF: csrf, Name: "Feishu Admin", AuthMethod: "feishu", AuthBinding: binding, AuthClaims: encoded}
	r.saveSession(hash, r.session)
	return userID, r.err
}

/** saveSession stores one fake session under its token hash. */
func (r *testRepository) saveSession(hash string, session SessionRecord) {
	if r.sessions == nil {
		r.sessions = make(map[string]SessionRecord)
	}
	r.sessions[hash] = session
}

/** FindSession retrieves a fake session by its token hash. */
func (r *testRepository) FindSession(_ context.Context, hash string) (SessionRecord, error) {
	session, ok := r.sessions[hash]
	if !ok {
		return SessionRecord{}, ErrNotFound
	}
	return session, nil
}

/** DeleteSession removes one fake session and records the revocation. */
func (r *testRepository) DeleteSession(_ context.Context, hash string) error {
	r.deleted = append(r.deleted, hash)
	delete(r.sessions, hash)
	return nil
}

/** FindAccount returns the fake admin account matching an ID or email. */
func (r *testRepository) FindAccount(_ context.Context, id int64, email string) (AdminAccount, error) {
	if r.account.ID == 0 || id > 0 && r.account.ID != id || email != "" && r.account.Email != email {
		return AdminAccount{}, ErrNotFound
	}
	return r.account, nil
}

/** CreateAccountSession stores an account-password session for service tests. */
func (r *testRepository) CreateAccountSession(_ context.Context, account AdminAccount, hash, csrf, binding string, _ time.Time, _ *string) error {
	claims, _ := json.Marshal(accountClaims{AccountID: account.ID})
	r.hash = hash
	r.session = SessionRecord{UserID: 9, CSRF: csrf, Name: account.Email, AuthMethod: "password", AuthBinding: binding, AuthClaims: claims}
	r.saveSession(hash, r.session)
	return nil
}

/** ListAccounts returns the fake account collection. */
func (r *testRepository) ListAccounts(_ context.Context, _ int64, _ int) ([]AdminAccount, error) {
	return []AdminAccount{r.account}, nil
}

/** SetAccountAdmin changes the fake account grant. */
func (r *testRepository) SetAccountAdmin(_ context.Context, _ int64, _ int64, granted bool) error {
	r.account.IsAdmin = granted
	return nil
}

type testFeishu struct{ profile FeishuProfile }

func (f testFeishu) Exchange(context.Context, string, string, string, string) (FeishuProfile, error) {
	return f.profile, nil
}

type feishuRoundTripFunc func(*http.Request) (*http.Response, error)

/** RoundTrip returns a controlled Feishu response for HTTP exchanger tests. */
func (f feishuRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

/** TestHTTPFeishuExchangeErrorsDoNotExposeResponseBodies protects OAuth diagnostics from provider secrets. */
func TestHTTPFeishuExchangeErrorsDoNotExposeResponseBodies(t *testing.T) {
	const marker = "synthetic-private-provider-response"
	tests := []struct {
		name      string
		responses []struct {
			status int
			body   string
		}
		wantCode string
	}{
		{
			name: "rejected token response",
			responses: []struct {
				status int
				body   string
			}{{status: http.StatusBadRequest, body: `{"error":"` + marker + `"}`}},
			wantCode: "400",
		},
		{
			name: "malformed token response",
			responses: []struct {
				status int
				body   string
			}{{status: http.StatusBadGateway, body: marker}},
			wantCode: "502",
		},
		{
			name: "malformed profile response",
			responses: []struct {
				status int
				body   string
			}{{status: http.StatusOK, body: `{"access_token":"synthetic-access-token"}`}, {status: http.StatusBadGateway, body: marker}},
			wantCode: "502",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			responseIndex := 0
			client := &http.Client{Transport: feishuRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if responseIndex >= len(test.responses) {
					return nil, io.EOF
				}
				response := test.responses[responseIndex]
				responseIndex++
				return &http.Response{
					StatusCode: response.status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(response.body)),
					Request:    request,
				}, nil
			})}
			_, err := (HTTPFeishuExchanger{Client: client}).Exchange(context.Background(), "code", "app-id", "app-secret", "https://site.example/callback")
			if err == nil || !strings.Contains(err.Error(), test.wantCode) || strings.Contains(err.Error(), marker) {
				t.Fatalf("provider failure was not safely classified: %v", err)
			}
		})
	}
}

func TestPasswordSessionUsesBindingAndInvalidatesOnCredentialChange(t *testing.T) {
	repo := &testRepository{}
	cfg := config.Config{SessionSecret: "session-secret-for-test", AdminPassword: "a-long-enough-admin-password", SiteURL: "https://myhot.example"}
	service := NewService(repo, cfg, nil)
	token, target, err := service.PasswordLogin(context.Background(), cfg.AdminPassword, "https://outside.example/evil?x=1", "test agent")
	if err != nil {
		t.Fatal(err)
	}
	if target != "/admin" || repo.created != 1 {
		t.Fatalf("login result target=%q created=%d", target, repo.created)
	}
	principal, err := service.SessionPrincipal(context.Background(), "unrelated=%; aihot_admin="+url.QueryEscape(token))
	if err != nil || principal == nil || principal.Name != "Password Admin" || principal.CSRF == "" {
		t.Fatalf("valid session was not resolved: principal=%#v err=%v", principal, err)
	}
	cfg.AdminPassword = "a-different-long-admin-password"
	service = NewService(repo, cfg, nil)
	principal, err = service.SessionPrincipal(context.Background(), "aihot_admin="+token)
	if err != nil || principal != nil || len(repo.deleted) != 1 {
		t.Fatalf("credential change must permanently invalidate a session: principal=%#v deleted=%d err=%v", principal, len(repo.deleted), err)
	}
}

/** TestPasswordSessionsKeepCSRFAndStoreOnlyHashedTokens verifies stable sessions and logout isolation. */
func TestPasswordSessionsKeepCSRFAndStoreOnlyHashedTokens(t *testing.T) {
	repo := &testRepository{}
	cfg := config.Config{SessionSecret: "session-secret-for-test", AdminPassword: "a-long-enough-admin-password"}
	service := NewService(repo, cfg, nil)
	first, _, err := service.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "")
	if err != nil {
		t.Fatal(err)
	}

	principal, err := service.SessionPrincipal(context.Background(), "aihot_admin="+first)
	if err != nil || principal == nil || principal.CSRF == "" {
		t.Fatalf("valid session was not resolved: principal=%#v err=%v", principal, err)
	}
	repeated, err := service.SessionPrincipal(context.Background(), "aihot_admin="+first)
	if err != nil || repeated == nil || repeated.CSRF != principal.CSRF {
		t.Fatalf("session CSRF token changed: first=%#v repeated=%#v err=%v", principal, repeated, err)
	}
	if len(repo.hash) != 64 || repo.hash == first || repo.session.AuthBinding == "" {
		t.Fatalf("session must store token hash and binding, not raw token: hash=%q record=%#v", repo.hash, repo.session)
	}
	encoded, err := json.Marshal(repo.sessions[repo.hash])
	if err != nil || strings.Contains(string(encoded), first) || strings.Contains(string(encoded), cfg.AdminPassword) {
		t.Fatalf("session record leaked a secret: %s (err=%v)", encoded, err)
	}

	if err := service.EndSession(context.Background(), "aihot_admin="+first); err != nil {
		t.Fatal(err)
	}
	ended, err := service.SessionPrincipal(context.Background(), "aihot_admin="+first)
	if err != nil || ended != nil {
		t.Fatalf("ended session remained available: principal=%#v err=%v", ended, err)
	}
	remaining, err := service.SessionPrincipal(context.Background(), "aihot_admin="+second)
	if err != nil || remaining == nil {
		t.Fatalf("ending one session revoked another: principal=%#v err=%v", remaining, err)
	}
}

/** TestPasswordSessionBindingRejectsRemovedCredentialsAndCorruptRecords checks fail-closed revocation. */
func TestPasswordSessionBindingRejectsRemovedCredentialsAndCorruptRecords(t *testing.T) {
	tests := []struct {
		name      string
		configure func(config.Config) config.Config
		corrupt   func(*SessionRecord)
	}{
		{name: "removed password", configure: func(cfg config.Config) config.Config { cfg.AdminPassword = ""; return cfg }},
		{name: "short password", configure: func(cfg config.Config) config.Config { cfg.AdminPassword = "short"; return cfg }},
		{name: "rotated session secret", configure: func(cfg config.Config) config.Config { cfg.SessionSecret = "replacement-session-secret"; return cfg }},
		{name: "removed session secret", configure: func(cfg config.Config) config.Config { cfg.SessionSecret = ""; return cfg }},
		{name: "unbound legacy session", corrupt: func(record *SessionRecord) { record.AuthMethod = ""; record.AuthBinding = "" }},
		{name: "malformed binding", corrupt: func(record *SessionRecord) { record.AuthBinding = "not-a-binding" }},
		{name: "unexpected password claims", corrupt: func(record *SessionRecord) { record.AuthClaims = json.RawMessage(`{"unexpected":true}`) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &testRepository{}
			initial := config.Config{SessionSecret: "session-secret-for-test", AdminPassword: "a-long-enough-admin-password"}
			login := NewService(repo, initial, nil)
			token, _, err := login.PasswordLogin(context.Background(), initial.AdminPassword, "/admin", "")
			if err != nil {
				t.Fatal(err)
			}
			if tt.corrupt != nil {
				record := repo.sessions[repo.hash]
				tt.corrupt(&record)
				repo.sessions[repo.hash] = record
			}
			updated := initial
			if tt.configure != nil {
				updated = tt.configure(updated)
			}
			principal, err := NewService(repo, updated, nil).SessionPrincipal(context.Background(), "aihot_admin="+token)
			if err != nil || principal != nil || len(repo.deleted) != 1 {
				t.Fatalf("invalid session was not deleted: principal=%#v deleted=%d err=%v", principal, len(repo.deleted), err)
			}
		})
	}
}

/** TestFeishuSessionBindingPreservesAllowlistAndConfigurationSemantics covers verified identity changes. */
func TestFeishuSessionBindingPreservesAllowlistAndConfigurationSemantics(t *testing.T) {
	unionID, email := "union-test", "admin@example.invalid"
	verified := time.Now()
	profile := FeishuProfile{UnionID: unionID, Email: strings.ToUpper(email), Name: "Admin"}
	login := func(t *testing.T, cfg config.Config) (*testRepository, string) {
		t.Helper()
		repo := &testRepository{account: AdminAccount{ID: 1, Email: email, IsAdmin: true, VerifiedAt: &verified}}
		service := NewService(repo, cfg, testFeishu{profile: profile})
		location, stateCookie, err := service.LoginRedirect("/admin")
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(location)
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := service.CompleteLogin(context.Background(), "code", parsed.Query().Get("state"), stateCookie, "agent")
		if err != nil {
			t.Fatal(err)
		}
		return repo, token
	}
	base := config.Config{
		SessionSecret: "session-secret-for-test", FeishuLoginAppID: "test-app", FeishuLoginAppSecret: "test-secret",
		AdminFeishuUnionIDs: []string{unionID},
	}

	repo, token := login(t, base)
	emailAllowlist := base
	emailAllowlist.AdminFeishuUnionIDs = nil
	emailAllowlist.AdminEmails = []string{email}
	principal, err := NewService(repo, emailAllowlist, nil).SessionPrincipal(context.Background(), "aihot_admin="+token)
	if err != nil || principal == nil {
		t.Fatalf("email allowlist did not authorize the original verified identity: principal=%#v err=%v", principal, err)
	}
	unionAllowlist := base
	unionAllowlist.AdminEmails = nil
	principal, err = NewService(repo, unionAllowlist, nil).SessionPrincipal(context.Background(), "aihot_admin="+token)
	if err != nil || principal == nil {
		t.Fatalf("union allowlist did not authorize the original verified identity: principal=%#v err=%v", principal, err)
	}
	secretRotation := emailAllowlist
	secretRotation.FeishuLoginAppSecret = "rotated-secret"
	principal, err = NewService(repo, secretRotation, nil).SessionPrincipal(context.Background(), "aihot_admin="+token)
	if err != nil || principal == nil {
		t.Fatalf("app-secret-only rotation revoked an authorized session: principal=%#v err=%v", principal, err)
	}
	removedAllowlist := emailAllowlist
	removedAllowlist.AdminEmails = nil
	principal, err = NewService(repo, removedAllowlist, nil).SessionPrincipal(context.Background(), "aihot_admin="+token)
	if err != nil || principal != nil {
		t.Fatalf("removed identity remained authorized: principal=%#v err=%v", principal, err)
	}

	for _, change := range []struct {
		name   string
		update func(config.Config) config.Config
	}{
		{name: "app ID rotation", update: func(cfg config.Config) config.Config { cfg.FeishuLoginAppID = "replacement-app"; return cfg }},
		{name: "missing app ID", update: func(cfg config.Config) config.Config { cfg.FeishuLoginAppID = ""; return cfg }},
		{name: "missing app secret", update: func(cfg config.Config) config.Config { cfg.FeishuLoginAppSecret = ""; return cfg }},
		{name: "session secret rotation", update: func(cfg config.Config) config.Config { cfg.SessionSecret = "replacement-session-secret"; return cfg }},
		{name: "removed session secret", update: func(cfg config.Config) config.Config { cfg.SessionSecret = ""; return cfg }},
	} {
		t.Run(change.name, func(t *testing.T) {
			repo, token := login(t, base)
			updated := change.update(base)
			principal, err := NewService(repo, updated, nil).SessionPrincipal(context.Background(), "aihot_admin="+token)
			if err != nil || principal != nil || len(repo.deleted) != 1 {
				t.Fatalf("configuration change did not revoke the session: principal=%#v deleted=%d err=%v", principal, len(repo.deleted), err)
			}
		})
	}
	for _, claims := range []json.RawMessage{
		nil,
		json.RawMessage(`{}`),
		json.RawMessage(`{"appId":"test-app","unionId":42,"email":"admin@example.invalid"}`),
		json.RawMessage(`{"appId":"test-app","unionId":null,"email":[]}`),
		json.RawMessage(`{"appId":"test-app","unionId":"union-test","email":"changed@example.invalid"}`),
	} {
		repo, token := login(t, base)
		record := repo.sessions[repo.hash]
		record.AuthClaims = claims
		repo.sessions[repo.hash] = record
		principal, err := NewService(repo, base, nil).SessionPrincipal(context.Background(), "aihot_admin="+token)
		if err != nil || principal != nil || len(repo.deleted) != 1 {
			t.Fatalf("malformed or replaced claims retained access: claims=%s principal=%#v deleted=%d err=%v", claims, principal, len(repo.deleted), err)
		}
	}
}

/** TestPasswordAndFeishuSessionsKeepIndependentAuthority verifies shared-account session isolation. */
func TestPasswordAndFeishuSessionsKeepIndependentAuthority(t *testing.T) {
	verified := time.Now()
	const userID int64 = 9
	repo := &testRepository{
		passwordUserID: userID,
		feishuUserID:   userID,
		account: AdminAccount{
			ID:         userID,
			Email:      "admin@local",
			IsAdmin:    true,
			VerifiedAt: &verified,
		},
	}
	cfg := config.Config{
		SessionSecret:        "session-secret-for-test",
		AdminPassword:        "first-long-admin-password",
		AdminEmails:          []string{"admin@local"},
		FeishuLoginAppID:     "test-app",
		FeishuLoginAppSecret: "test-secret",
		SiteURL:              "https://admin.example",
	}
	service := NewService(repo, cfg, testFeishu{profile: FeishuProfile{Email: "admin@local", UnionID: "union-admin"}})
	passwordToken, _, err := service.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "")
	if err != nil {
		t.Fatal(err)
	}
	location, stateCookie, err := service.LoginRedirect("/admin")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	feishuToken, _, err := service.CompleteLogin(context.Background(), "code", parsed.Query().Get("state"), stateCookie, "")
	if err != nil {
		t.Fatal(err)
	}
	if repo.sessions[hashToken(passwordToken)].UserID != repo.sessions[hashToken(feishuToken)].UserID {
		t.Fatal("password and Feishu sessions must share the account")
	}

	cfg.AdminPassword = "replacement-long-admin-password"
	service = NewService(repo, cfg, testFeishu{profile: FeishuProfile{Email: "admin@local", UnionID: "union-admin"}})
	passwordPrincipal, err := service.SessionPrincipal(context.Background(), SessionCookie+"="+passwordToken)
	if err != nil || passwordPrincipal != nil {
		t.Fatalf("password credential rotation retained its session: principal=%#v err=%v", passwordPrincipal, err)
	}
	feishuPrincipal, err := service.SessionPrincipal(context.Background(), SessionCookie+"="+feishuToken)
	if err != nil || feishuPrincipal == nil {
		t.Fatalf("password rotation revoked the Feishu session: principal=%#v err=%v", feishuPrincipal, err)
	}

	passwordToken, _, err = service.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdminEmails = nil
	service = NewService(repo, cfg, testFeishu{profile: FeishuProfile{Email: "admin@local", UnionID: "union-admin"}})
	feishuPrincipal, err = service.SessionPrincipal(context.Background(), SessionCookie+"="+feishuToken)
	if err != nil || feishuPrincipal != nil {
		t.Fatalf("removed Feishu identity retained its session: principal=%#v err=%v", feishuPrincipal, err)
	}
	passwordPrincipal, err = service.SessionPrincipal(context.Background(), SessionCookie+"="+passwordToken)
	if err != nil || passwordPrincipal == nil {
		t.Fatalf("Feishu allowlist change revoked the password session: principal=%#v err=%v", passwordPrincipal, err)
	}
}

func TestPasswordLoginRejectsShortOrIncorrectSecrets(t *testing.T) {
	for _, password := range []string{"short", "incorrect-long-admin-password"} {
		repo := &testRepository{}
		service := NewService(repo, config.Config{SessionSecret: "secret", AdminPassword: "correct-long-admin-password"}, nil)
		if _, _, err := service.PasswordLogin(context.Background(), password, "/admin", ""); !errors.Is(err, ErrLoginRejected) {
			t.Fatalf("password %q should be rejected, got %v", password, err)
		}
		if repo.created != 0 {
			t.Fatal("rejected login created a session")
		}
	}
	repo := &testRepository{}
	service := NewService(repo, config.Config{SessionSecret: "secret", AdminPassword: "short"}, nil)
	if _, _, err := service.PasswordLogin(context.Background(), "short", "/admin", ""); !errors.Is(err, ErrLoginRejected) || err.Error() != "admin login rejected: unset" {
		t.Fatalf("short configured password should be treated as unset, got %v", err)
	}
}

func TestFeishuLoginRequiresSignedStateAndConfiguredAllowlist(t *testing.T) {
	unionID, email := "union-test", "admin@example.invalid"
	verified := time.Now()
	repo := &testRepository{account: AdminAccount{ID: 1, Email: email, IsAdmin: true, VerifiedAt: &verified}}
	cfg := config.Config{
		SessionSecret: "state-secret", SiteURL: "https://myhot.example", FeishuLoginAppID: "test-app", FeishuLoginAppSecret: "test-secret",
		AdminFeishuUnionIDs: []string{unionID},
	}
	service := NewService(repo, cfg, testFeishu{profile: FeishuProfile{UnionID: unionID, Email: "ignored@example.invalid", EnterpriseEmail: email, Name: "Admin"}})
	location, stateCookie, err := service.LoginRedirect("/admin/settings?tab=one")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.Query().Get("redirect_uri") != "https://myhot.example/api/auth/callback" || parsed.Query().Get("state") != stateCookie {
		t.Fatalf("unexpected authorize redirect %q (%v)", location, err)
	}
	token, target, err := service.CompleteLogin(context.Background(), "authorization-code", parsed.Query().Get("state"), stateCookie, "agent")
	if err != nil || token == "" || target != "/admin/settings?tab=one" {
		t.Fatalf("feishu login failed: target=%q token=%t err=%v", target, token != "", err)
	}
	principal, err := service.SessionPrincipal(context.Background(), "aihot_admin="+token)
	if err != nil || principal == nil || principal.Name != "Feishu Admin" {
		t.Fatalf("feishu session was not resolved: principal=%#v err=%v", principal, err)
	}
	if _, _, err := service.CompleteLogin(context.Background(), "code", "tampered", stateCookie, "agent"); !errors.Is(err, ErrLoginRejected) {
		t.Fatalf("tampered OAuth state must be rejected, got %v", err)
	}

	cfg.AdminFeishuUnionIDs = nil
	cfg.AdminEmails = nil
	service = NewService(repo, cfg, testFeishu{profile: FeishuProfile{UnionID: unionID}})
	if _, _, err := service.CompleteLogin(context.Background(), "code", parsed.Query().Get("state"), stateCookie, "agent"); !errors.Is(err, ErrLoginRejected) {
		t.Fatalf("unlisted identity must be rejected, got %v", err)
	}
}

func TestAccountAdminGrantAndPasswordBinding(t *testing.T) {
	hash, err := auth.HashPassword("correct account password")
	if err != nil {
		t.Fatal(err)
	}
	verified := time.Now()
	repo := &testRepository{account: AdminAccount{ID: 10, Email: "user@example.invalid", PasswordHash: hash, VerifiedAt: &verified}}
	service := NewService(repo, config.Config{SessionSecret: "test-secret"}, nil)
	if _, _, err := service.AccountLogin(context.Background(), repo.account.Email, "correct account password", "/admin", ""); !errors.Is(err, ErrLoginRejected) {
		t.Fatalf("ungranted account must be rejected: %v", err)
	}
	repo.account.IsAdmin = true
	token, _, err := service.AccountLogin(context.Background(), repo.account.Email, "correct account password", "/admin", "")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.SessionPrincipal(context.Background(), "aihot_admin="+token)
	if err != nil || principal == nil || principal.Super {
		t.Fatalf("expected ordinary admin: %#v, %v", principal, err)
	}
	repo.account.IsAdmin = false
	principal, err = service.SessionPrincipal(context.Background(), "aihot_admin="+token)
	if err != nil || principal != nil {
		t.Fatalf("revoked account retained access: %#v, %v", principal, err)
	}
	repo.account.IsAdmin = true
	repo.account.PasswordHash = "changed"
	principal, err = service.SessionPrincipal(context.Background(), "aihot_admin="+token)
	if err != nil || principal != nil {
		t.Fatalf("password change retained access: %#v, %v", principal, err)
	}
}

func TestSafeReturnAndMalformedCookieRemainContained(t *testing.T) {
	for input, want := range map[string]string{
		"/admin/sources?sort=recent":           "/admin/sources?sort=recent",
		"https://other.invalid/admin/runs?x=1": "/admin/runs?x=1",
		"//other.invalid/admin":                "/admin",
		"/login":                               "/admin",
	} {
		if got := SafeReturn(input); got != want {
			t.Errorf("SafeReturn(%q) = %q, want %q", input, got, want)
		}
	}
	cookies := ParseCookies("unrelated=%; aihot_admin=valid; invalid=%E0%A4")
	if cookies[SessionCookie] != "valid" || cookies["unrelated"] != "%" {
		t.Fatalf("malformed unrelated cookies damaged parse result: %#v", cookies)
	}
}

/** TestDevelopmentAdminModeIsExplicitAndDisabledInProduction verifies the development-only fallback. */
func TestDevelopmentAdminModeIsExplicitAndDisabledInProduction(t *testing.T) {
	dev := NewService(&testRepository{}, config.Config{DevAdmin: true, DevAdminDisplayName: "Synthetic developer", EnvironmentName: "development"}, nil)
	principal, err := dev.SessionPrincipal(context.Background(), "")
	if err != nil || principal == nil || !principal.Dev || principal.Name != "Synthetic developer" {
		t.Fatalf("development admin was not explicit: principal=%#v err=%v", principal, err)
	}
	production := NewService(&testRepository{}, config.Config{DevAdmin: true, EnvironmentName: "production"}, nil)
	principal, err = production.SessionPrincipal(context.Background(), "")
	if err != nil || principal != nil {
		t.Fatalf("development admin was enabled in production: principal=%#v err=%v", principal, err)
	}
}
