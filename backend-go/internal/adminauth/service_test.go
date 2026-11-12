package adminauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"aihot/backend-go/internal/config"
)

type testRepository struct {
	session SessionRecord
	hash    string
	deleted []string
	created int
	err     error
}

func (r *testRepository) CreatePasswordSession(_ context.Context, hash, csrf, binding string, _ time.Time, _ *string) (int64, error) {
	r.created++
	r.hash = hash
	r.session = SessionRecord{UserID: 7, CSRF: csrf, Name: "Password Admin", AuthMethod: "password", AuthBinding: binding}
	return 7, r.err
}

func (r *testRepository) CreateFeishuSession(_ context.Context, claims FeishuClaims, hash, csrf, binding string, _ time.Time, _ *string) (int64, error) {
	r.created++
	r.hash = hash
	encoded, _ := json.Marshal(claims.persisted())
	r.session = SessionRecord{UserID: 8, CSRF: csrf, Name: "Feishu Admin", AuthMethod: "feishu", AuthBinding: binding, AuthClaims: encoded}
	return 8, r.err
}

func (r *testRepository) FindSession(_ context.Context, hash string) (SessionRecord, error) {
	if hash != r.hash || r.hash == "" {
		return SessionRecord{}, ErrNotFound
	}
	return r.session, nil
}

func (r *testRepository) DeleteSession(_ context.Context, hash string) error {
	r.deleted = append(r.deleted, hash)
	return nil
}

type testFeishu struct{ profile FeishuProfile }

func (f testFeishu) Exchange(context.Context, string, string, string, string) (FeishuProfile, error) {
	return f.profile, nil
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
	repo := &testRepository{}
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
