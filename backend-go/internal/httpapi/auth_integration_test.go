package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/account"
	"aihot/backend-go/internal/auth"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/db"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/topics"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

type captureMail struct{ messages []string }

func (m *captureMail) Send(to, subject, text string) error {
	m.messages = append(m.messages, text)
	return nil
}
func (m *captureMail) token(t *testing.T) string {
	t.Helper()
	if len(m.messages) == 0 {
		t.Fatal("expected verification/reset mail")
	}
	for _, field := range strings.Fields(m.messages[len(m.messages)-1]) {
		if parsed, e := url.Parse(field); e == nil && parsed.Query().Get("token") != "" {
			return parsed.Query().Get("token")
		}
	}
	t.Fatal("expected token in captured test mail")
	return ""
}
func authTestPools(t *testing.T) (*pgxpool.Pool, *gorm.DB) {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("integration database must be localhost and end in _test")
	}
	pool, err := pgxpool.New(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	orm, err := db.OpenGORM(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.CloseGORM(orm) })
	return pool, orm
}

type authClient struct {
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
}

func (c *authClient) request(t *testing.T, method, path string, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(string(encoded)))
	req.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, req)
	if response.Code != want {
		t.Fatalf("%s %s: wanted %d got %d: %s", method, path, want, response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("API response must disable caching")
	}
	return response
}
func (c *authClient) login(t *testing.T, email, password string) {
	response := c.request(t, "POST", "/api/auth/login", credentials{Email: email, Password: password}, 200)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("expected one session cookie")
	}
	c.cookie = cookies[0]
	if !c.cookie.HttpOnly || c.cookie.SameSite != http.SameSiteLaxMode || c.cookie.Path != "/" {
		t.Fatal("unsafe session cookie")
	}
	var data map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	c.csrf, _ = data["csrfToken"].(string)
}
func TestAuthLifecycleAndPrivateSettingsIntegration(t *testing.T) {
	pool, orm := authTestPools(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("auth_%d", time.Now().UnixNano())
	emails := []string{prefix + "_1@example.invalid", prefix + "_2@example.invalid"}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM user_accounts WHERE email=ANY($1)", emails) })
	sender := &captureMail{}
	cfg := config.Config{PublicURL: "http://localhost", SiteURL: "https://myhot.example", UserAPIKeyEncryptionKey: []byte(strings.Repeat("e", 32)), SMTPHost: "mail.example.invalid", SMTPFrom: "noreply@example.invalid", SMTPPort: 587}
	server := New(cfg, pool, orm, map[string]interests.Entity{"openai": {Name: "OpenAI"}})
	server.Topics = topicListStub{}
	server.MailSender = sender
	clients := []authClient{{handler: server.Handler()}, {handler: server.Handler()}}
	password := "fixture-password-123"
	newPassword := "fixture-new-password-456"
	registration := func(email string) map[string]any {
		return map[string]any{"email": email, "password": password}
	}
	status := clients[0].request(t, "GET", "/api/auth/registration-status", nil, 200)
	var statusBody map[string]any
	if err := json.Unmarshal(status.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	if statusBody["enabled"] != false || len(statusBody) != 1 {
		t.Fatalf("registration status should expose only the closed state: %v", statusBody)
	}
	clients[0].request(t, "POST", "/api/auth/register", registration(emails[0]), 403)
	var count int
	pool.QueryRow(ctx, "SELECT count(*) FROM user_accounts WHERE email=$1", emails[0]).Scan(&count)
	if count != 0 {
		t.Fatal("registration closed must not create an account")
	}
	server.Config.OpenRegistration = true
	cfg.OpenRegistration = true
	server.Accounts = account.NewService(account.NewRepository(orm), cfg, accountMailer{server: server})
	status = clients[0].request(t, "GET", "/api/auth/registration-status", nil, 200)
	if err := json.Unmarshal(status.Body.Bytes(), &statusBody); err != nil || statusBody["enabled"] != true {
		t.Fatalf("explicit registration enablement with SMTP should open registration: %v err=%v", statusBody, err)
	}
	for i := range clients {
		if i == 1 {
			hash, err := auth.HashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO user_accounts(email,password_hash) VALUES($1,$2)`, emails[i], hash); err != nil {
				t.Fatal(err)
			}
			clients[i].request(t, "POST", "/api/auth/login", credentials{Email: emails[i], Password: password}, 403)
		}
		sent := clients[i].request(t, "POST", "/api/auth/verify/resend", map[string]string{"email": emails[i]}, 202)
		var timing struct {
			ServerTime time.Time `json:"serverTime"`
			ExpiresAt  time.Time `json:"expiresAt"`
			RetryAt    time.Time `json:"retryAt"`
		}
		if err := json.Unmarshal(sent.Body.Bytes(), &timing); err != nil || timing.ExpiresAt.Sub(timing.ServerTime) != time.Minute || timing.RetryAt.Sub(timing.ServerTime) != time.Minute {
			t.Fatalf("verification timing: %s %v", sent.Body.String(), err)
		}
		code := strings.TrimPrefix(strings.Split(sender.messages[len(sender.messages)-1], "\n")[0], "你的验证码是：")
		clients[i].request(t, "POST", "/api/auth/verify/resend", map[string]string{"email": emails[i]}, 429)
		if strings.Contains(sent.Body.String(), code) {
			t.Fatal("HTTP response leaked verification code")
		}
		if i == 0 {
			body := registration(emails[i])
			body["code"] = code
			registered := clients[i].request(t, "POST", "/api/auth/register", body, 201)
			cookies := registered.Result().Cookies()
			if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" {
				t.Fatal("registration did not issue a safe session cookie")
			}
			clients[i].cookie = cookies[0]
			var session struct {
				CSRF string `json:"csrfToken"`
			}
			if err := json.Unmarshal(registered.Body.Bytes(), &session); err != nil || session.CSRF == "" {
				t.Fatalf("registration CSRF: %v", err)
			}
			clients[i].csrf = session.CSRF
			clients[i].request(t, "GET", "/api/me", nil, 200)
			clients[i].request(t, "POST", "/api/auth/register", body, 400)
		} else {
			body := map[string]string{"email": emails[i], "code": code}
			clients[i].request(t, "POST", "/api/auth/verify", body, 200)
			clients[i].request(t, "POST", "/api/auth/verify", body, 400)
		}
		var tokenCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_email_tokens JOIN user_accounts ON user_accounts.id=user_id WHERE email=$1`, emails[i]).Scan(&tokenCount); err != nil || tokenCount != 0 {
			t.Fatalf("registration persisted tokens: %d %v", tokenCount, err)
		}
		if i != 0 {
			clients[i].login(t, emails[i], password)
		}
	}

	unauthenticatedHot := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthenticatedHot, httptest.NewRequest(http.MethodGet, "http://localhost/api/site/hot", nil))
	if unauthenticatedHot.Code != http.StatusUnauthorized || unauthenticatedHot.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("anonymous site hot response changed: status=%d headers=%v", unauthenticatedHot.Code, unauthenticatedHot.Header())
	}
	hot := clients[0].request(t, http.MethodGet, "/api/site/hot", nil, http.StatusOK)
	var hotBody map[string]any
	if err := json.Unmarshal(hot.Body.Bytes(), &hotBody); err != nil || hotBody["computedAt"] != nil || hotBody["windowHours"] != float64(48) || hotBody["entries"] == nil || hot.Header().Get("ETag") == "" || hot.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("authenticated empty site hot response changed: headers=%v body=%s err=%v", hot.Header(), hot.Body.String(), err)
	}
	conditionalHot := httptest.NewRequest(http.MethodGet, "http://localhost/api/site/hot", nil)
	conditionalHot.AddCookie(clients[0].cookie)
	conditionalHot.Header.Set("If-None-Match", hot.Header().Get("ETag"))
	conditionalHotResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(conditionalHotResponse, conditionalHot)
	if conditionalHotResponse.Code != http.StatusNotModified || conditionalHotResponse.Body.Len() != 0 || conditionalHotResponse.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("site hot conditional response changed: status=%d headers=%v", conditionalHotResponse.Code, conditionalHotResponse.Header())
	}
	unauthenticatedSitemap := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthenticatedSitemap, httptest.NewRequest(http.MethodGet, "http://localhost/sitemap.xml", nil))
	if unauthenticatedSitemap.Code != http.StatusUnauthorized || unauthenticatedSitemap.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("anonymous sitemap response changed: status=%d headers=%v", unauthenticatedSitemap.Code, unauthenticatedSitemap.Header())
	}
	sitemap := clients[0].request(t, "GET", "/sitemap.xml", nil, 200)
	if sitemap.Header().Get("Content-Type") != "application/xml" || sitemap.Header().Get("ETag") == "" || sitemap.Header().Get("Cache-Control") != "private, no-store" || sitemap.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("authenticated sitemap contract changed: headers=%v", sitemap.Header())
	}
	conditionalSitemap := httptest.NewRequest(http.MethodGet, "http://localhost/sitemap.xml", nil)
	conditionalSitemap.AddCookie(clients[0].cookie)
	conditionalSitemap.Header.Set("If-None-Match", sitemap.Header().Get("ETag"))
	conditionalSitemapResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(conditionalSitemapResponse, conditionalSitemap)
	if conditionalSitemapResponse.Code != http.StatusNotModified || conditionalSitemapResponse.Body.Len() != 0 || conditionalSitemapResponse.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("sitemap conditional response changed: status=%d headers=%v", conditionalSitemapResponse.Code, conditionalSitemapResponse.Header())
	}
	topicResponse := clients[0].request(t, "GET", "/api/site/topics", nil, 200)
	var topicBody topics.Response
	if err := json.Unmarshal(topicResponse.Body.Bytes(), &topicBody); err != nil || len(topicBody.Groups) != 1 || len(topicBody.Topics) != 1 || topicBody.Topics[0].Slug != "fixture" || topicResponse.Header().Get("ETag") == "" {
		t.Fatalf("topic list response changed: body=%s err=%v", topicResponse.Body.String(), err)
	}
	conditionalTopic := httptest.NewRequest(http.MethodGet, "http://localhost/api/site/topics", nil)
	conditionalTopic.AddCookie(clients[0].cookie)
	conditionalTopic.Header.Set("If-None-Match", topicResponse.Header().Get("ETag"))
	conditionalRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(conditionalRecorder, conditionalTopic)
	if conditionalRecorder.Code != http.StatusNotModified || conditionalRecorder.Body.Len() != 0 || conditionalRecorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("topic conditional response changed: status=%d headers=%v body=%s", conditionalRecorder.Code, conditionalRecorder.Header(), conditionalRecorder.Body.String())
	}
	topicPage := clients[0].request(t, "GET", "/api/site/topics/fixture?page=1", nil, 200)
	if topicPage.Header().Get("ETag") == "" || !strings.Contains(topicPage.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("topic page lost its private conditional response headers: %v", topicPage.Header())
	}
	conditionalPage := httptest.NewRequest(http.MethodGet, "http://localhost/api/site/topics/fixture?page=1", nil)
	conditionalPage.AddCookie(clients[0].cookie)
	conditionalPage.Header.Set("If-None-Match", topicPage.Header().Get("ETag"))
	conditionalPageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(conditionalPageRecorder, conditionalPage)
	if conditionalPageRecorder.Code != http.StatusNotModified || conditionalPageRecorder.Body.Len() != 0 || conditionalPageRecorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("topic page conditional response changed: status=%d headers=%v body=%s", conditionalPageRecorder.Code, conditionalPageRecorder.Header(), conditionalPageRecorder.Body.String())
	}
	model := map[string]string{"baseUrl": "https://1.1.1.1/v1", "apiKey": "fixture-private-api-key", "model": "fixture-model"}
	clients[0].request(t, "PUT", "/api/me/model", model, 200)
	modelResponse := clients[0].request(t, "GET", "/api/me/model", nil, 200)
	if strings.Contains(modelResponse.Body.String(), model["apiKey"]) {
		t.Fatal("model GET exposed secret")
	}
	otherModel := clients[1].request(t, "GET", "/api/me/model", nil, 200)
	if strings.Contains(otherModel.Body.String(), "fixture-model") {
		t.Fatal("model config crossed user boundary")
	}
	var ciphertext string
	var userID int64
	if err := pool.QueryRow(ctx, "SELECT m.api_key_ciphertext,m.user_id FROM user_model_configs m JOIN user_accounts u ON u.id=m.user_id WHERE u.email=$1", emails[0]).Scan(&ciphertext, &userID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, model["apiKey"]) {
		t.Fatal("API key must be encrypted")
	}
	clients[0].request(t, "PUT", "/api/me/interests", map[string]any{"keywords": []string{"OpenAI", "openai"}, "entityIds": []string{"openai"}}, 200)
	if r := clients[1].request(t, "GET", "/api/me/interests", nil, 200); strings.Contains(r.Body.String(), "OpenAI") {
		t.Fatal("interests crossed user boundary")
	}
	savedCSRF := clients[0].csrf
	clients[0].csrf = ""
	clients[0].request(t, "PUT", "/api/me/interests", map[string]any{"keywords": []string{"changed"}}, 403)
	clients[0].csrf = savedCSRF
	model["baseUrl"] = "http://127.0.0.1:1"
	clients[0].request(t, "PUT", "/api/me/model", model, 400)
	model["baseUrl"] = "https://127.0.0.1"
	clients[0].request(t, "PUT", "/api/me/model", model, 400)
	// The API only queues an encrypted request; it never invokes a model.
	var queueExists bool
	pool.QueryRow(ctx, "SELECT to_regclass('river_job') IS NOT NULL").Scan(&queueExists)
	if queueExists {
		result := clients[0].request(t, "POST", "/api/me/model/test", map[string]string{"baseUrl": "https://1.1.1.1/v1", "model": "fixture-model"}, 202)
		var receipt map[string]any
		json.Unmarshal(result.Body.Bytes(), &receipt)
		path := fmt.Sprintf("/api/me/model/test/%.0f", receipt["testId"])
		clients[0].request(t, "GET", path, nil, 200)
		clients[1].request(t, "GET", path, nil, 404)
	}
	oldCookie := clients[0].cookie
	oldCSRF := clients[0].csrf
	clients[0].request(t, "POST", "/api/auth/logout", nil, 204)
	clients[0].request(t, "GET", "/api/me", nil, 401)
	clients[0].cookie = nil
	clients[0].csrf = ""
	clients[0].request(t, "POST", "/api/auth/password/forgot", map[string]string{"email": emails[0]}, 202)
	resetToken := sender.token(t)
	clients[0].request(t, "POST", "/api/auth/password/reset", map[string]string{"token": resetToken, "password": newPassword}, 200)
	clients[0].request(t, "POST", "/api/auth/login", credentials{Email: emails[0], Password: password}, 401)
	clients[0].login(t, emails[0], newPassword)
	stale := authClient{handler: server.Handler(), cookie: oldCookie, csrf: oldCSRF}
	stale.request(t, "GET", "/api/me", nil, 401)
	clients[0].request(t, "GET", "/api/me", nil, 200)
	clients[0].request(t, "DELETE", "/api/me", map[string]string{"password": newPassword}, 204)
	clients[0].request(t, "GET", "/api/me", nil, 401)
	for _, table := range []string{"user_model_configs", "user_interests", "user_sessions", "user_article_results", "user_daily_reports", "user_model_call_receipts"} {
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE user_id=$1", userID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("account deletion did not clear %s: count=%d err=%v", table, count, err)
		}
	}
	if queueExists {
		pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE args->>'userId'=$1", fmt.Sprint(userID)).Scan(&count)
		if count != 0 {
			t.Fatal("encrypted personal jobs remain after account deletion")
		}
	}
	clients[1].request(t, "GET", "/api/me", nil, 200)
}

type topicListStub struct{}

func (topicListStub) List(context.Context, time.Time) (topics.Response, error) {
	return topics.Response{Groups: []topics.Group{{Key: "company", Name: "Companies", Blurb: "Fixture"}}, Topics: []topics.Summary{{Slug: "fixture", Name: "Fixture", Group: "company"}}}, nil
}

func (topicListStub) Page(context.Context, string, float64, time.Time) (*topics.TopicPage, error) {
	return &topics.TopicPage{
		Topic:     topics.PageSummary{Summary: topics.Summary{Slug: "fixture", Name: "Fixture", Group: "company", Definition: "Fixture topic"}, GroupName: "Companies"},
		Modules:   map[string]any{},
		Items:     []topics.FeedItem{},
		Page:      1,
		PageCount: 1,
		PageSize:  20,
	}, nil
}
