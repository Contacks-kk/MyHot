package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"aihot/backend-go/internal/account"
	"aihot/backend-go/internal/adminauth"
	"aihot/backend-go/internal/admincontent"
	"aihot/backend-go/internal/admindeliveries"
	"aihot/backend-go/internal/adminlabels"
	"aihot/backend-go/internal/adminmodels"
	"aihot/backend-go/internal/adminops"
	"aihot/backend-go/internal/adminruns"
	"aihot/backend-go/internal/adminselectbench"
	"aihot/backend-go/internal/adminsettings"
	"aihot/backend-go/internal/adminsources"
	"aihot/backend-go/internal/agentmd"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/discovery"
	"aihot/backend-go/internal/feedback"
	"aihot/backend-go/internal/feeddiscovery"
	"aihot/backend-go/internal/feeds"
	"aihot/backend-go/internal/ingest"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/mailer"
	"aihot/backend-go/internal/mcpapi"
	"aihot/backend-go/internal/personal"
	"aihot/backend-go/internal/publiccontent"
	"aihot/backend-go/internal/receiptrecovery"
	"aihot/backend-go/internal/topics"
	"aihot/backend-go/internal/usersources"
)

var errNoSession = errors.New("no session")

const sessionCookie = "myhot_session"
const sessionLifetime = 30 * 24 * time.Hour

type Server struct {
	Config                config.Config
	Logger                *slog.Logger
	ContactDir            string
	Entities              map[string]interests.Entity
	CategoryKeys          map[string]struct{}
	CategoryOrder         []string
	CategoryLabels        map[string]string
	PublicCategoryOrder   []string
	PublicCategoryLabels  map[string]string
	PublicCategoryMembers map[string][]string
	Catalog               *interests.CatalogService
	TopicDefinitions      []topics.Definition
	TopicGroups           []topics.Group
	ReportSubject         string
	limiter               *rateLimiter
	MailSender            mailer.Sender
	Accounts              *account.Service
	Agent                 *agentmd.Service
	AdminAuth             *adminauth.Service
	AdminContent          *admincontent.Service
	AdminDeliveries       *admindeliveries.Service
	ReceiptRecovery       *receiptrecovery.Service
	AdminOps              *adminops.Service
	AdminRuns             *adminruns.Service
	AdminSources          *adminsources.Service
	AdminLabels           *adminlabels.Service
	UserSources           *usersources.Service
	FeedDiscovery         *feeddiscovery.Service
	AdminSelectBench      *adminselectbench.Service
	AdminSettings         *adminsettings.Service
	AdminModels           *adminmodels.Service
	Feedback              *feedback.Service
	Ingest                *ingest.Service
	Feeds                 *feeds.Service
	Discovery             *discovery.Service
	Personal              *personal.Service
	PublicContent         *publiccontent.Service
	MCPService            *mcpapi.Service
	MCPHandler            http.Handler
	MediaProxy            http.Handler
	Topics                interface {
		List(context.Context, time.Time) (topics.Response, error)
		Page(context.Context, string, float64, time.Time) (*topics.TopicPage, error)
	}
	SiteInfo interface {
		Meta() ([]byte, string, error)
		Changelog() ([]byte, string, error)
		OpenAPIV1() ([]byte, string, error)
	}
	Sitemap interface {
		Load(context.Context) (publiccontent.SitemapDocument, error)
	}
	Health                interface{ Check(context.Context) error }
	EnqueueConnectionTest personal.ConnectionTestEnqueuer
	EnqueueManualDaily    func(context.Context, int64) (int64, error)
	ConnectionTestKind    string
}

/** ShutdownMCP closes active MCP streams before the HTTP server drains requests. */
func (s *Server) ShutdownMCP() {
	if s.MCPService != nil {
		s.MCPService.Close()
	}
}

/** resolvePublicCategory validates a public category and returns the stored categories it covers. */
func (s *Server) resolvePublicCategory(category string) ([]string, bool) {
	if members, exists := s.PublicCategoryMembers[category]; exists && len(members) > 0 {
		return append([]string(nil), members...), true
	}
	if len(s.PublicCategoryMembers) == 0 {
		if _, exists := s.CategoryKeys[category]; exists {
			return []string{category}, true
		}
	}
	return nil, false
}

/** publicCategoryOptions returns the stable categories accepted by public API filters. */
func (s *Server) publicCategoryOptions() []string {
	if len(s.PublicCategoryMembers) > 0 || len(s.PublicCategoryOrder) > 0 {
		return s.PublicCategoryOrder
	}
	return s.CategoryOrder
}

/** Handler builds the API routing and middleware. */
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if s.Config.IndexNowKey != "" {
		mux.HandleFunc("GET /"+s.Config.IndexNowKey+".txt", s.indexNowKey)
	}
	mux.HandleFunc("GET /api/health", s.health)
	if s.MediaProxy != nil {
		mux.Handle("/api/img-proxy", s.MediaProxy)
	}
	mux.HandleFunc("POST /api/ingest/items", s.ingestItems)
	mux.HandleFunc("/feed.xml", s.rssRoute)
	mux.HandleFunc("/feed/", s.rssRoute)
	mux.HandleFunc("GET /api/site/meta", s.siteMeta)
	mux.HandleFunc("GET /api/site/changelog", s.siteChangelog)
	mux.HandleFunc("GET /llms.txt", s.llmsTxt)
	mux.HandleFunc("/llms.txt", s.llmsMethodNotAllowed)
	mux.HandleFunc("GET /api/site/contact-alias/{slot}", s.contactAlias)
	mux.HandleFunc("GET /api/site/hot", s.siteHot)
	mux.HandleFunc("GET /api/site/timeline", s.siteTimeline)
	mux.HandleFunc("GET /api/site/pool", s.sitePool)
	mux.HandleFunc("GET /api/site/items/{id}/original", s.siteItemDetail)
	mux.HandleFunc("GET /api/site/items/{id}", s.siteItemDetail)
	mux.HandleFunc("GET /items/{id}/markdown", s.siteItemMarkdown)
	mux.HandleFunc("GET /api/site/stories/{publicId}", s.siteStoryDetail)
	mux.HandleFunc("GET /sitemap.xml", s.sitemap)
	mux.HandleFunc("GET /openapi-v1.json", s.openAPIV1)
	mux.HandleFunc("OPTIONS /openapi-v1.json", v1SelectedPreflight)
	mux.HandleFunc("/openapi-v1.json", s.openAPIMethodNotAllowed)
	mux.Handle("/api/mcp", s.mcpEndpoint())
	mux.HandleFunc("POST /api/site/feedback", s.submitFeedback)
	mux.HandleFunc("GET /api/site/topics", s.siteTopics)
	mux.HandleFunc("GET /api/site/topics/{slug}", s.siteTopicPage)
	mux.HandleFunc("GET /api/site/reports/{kind}", s.siteReportIndex)
	mux.HandleFunc("GET /api/site/reports/{kind}/latest-page", s.siteReportLatestPage)
	mux.HandleFunc("GET /api/site/reports/{kind}/{key}", s.siteReportDetail)
	mux.HandleFunc("GET /api/site/reports/{kind}/navigation/{key}", s.siteReportNavigation)
	mux.HandleFunc("GET /api/site/reports/daily/months/{month}", s.siteDailyReportMonth)
	mux.HandleFunc("GET /api/site/items/availability", s.itemAvailability)
	mux.HandleFunc("GET /api/site/groups/{factId}/reports", s.groupReports)
	mux.HandleFunc("GET /api/site/stories/{publicId}/followups", s.storyFollowups)
	mux.HandleFunc("GET /api/v1/items", s.v1Items)
	mux.HandleFunc("OPTIONS /api/v1/items", v1SelectedPreflight)
	mux.HandleFunc("/api/v1/items", v1SelectedMethodNotAllowed)
	mux.HandleFunc("GET /api/v1/stories/{publicId}", s.v1Story)
	mux.HandleFunc("OPTIONS /api/v1/stories/{publicId}", v1SelectedPreflight)
	mux.HandleFunc("/api/v1/stories/{publicId}", v1SelectedMethodNotAllowed)
	mux.HandleFunc("GET /api/v1/hot-topics", s.v1HotTopics)
	mux.HandleFunc("OPTIONS /api/v1/hot-topics", v1HotTopicsPreflight)
	mux.HandleFunc("/api/v1/hot-topics", v1HotTopicsMethodNotAllowed)
	mux.HandleFunc("GET /api/v1/agent/hot", s.v1AgentHot)
	mux.HandleFunc("GET /api/v1/agent", s.v1AgentGuide)
	mux.HandleFunc("GET /api/v1/agent/latest", s.v1AgentLatest)
	mux.HandleFunc("GET /api/v1/agent/search", s.v1AgentSearch)
	mux.HandleFunc("GET /api/v1/agent/stories/{publicId}", s.v1AgentStory)
	mux.HandleFunc("GET /api/v1/agent/daily", s.v1AgentDailyLatest)
	mux.HandleFunc("GET /api/v1/agent/daily/{date}", s.v1AgentDailyByDate)
	mux.HandleFunc("GET /api/v1/agent/weekly", s.v1AgentWeeklyLatest)
	mux.HandleFunc("GET /api/v1/agent/weekly/{week}", s.v1AgentWeeklyByWeek)
	mux.HandleFunc("GET /api/v1/agent/monthly", s.v1AgentMonthlyLatest)
	mux.HandleFunc("GET /api/v1/agent/monthly/{month}", s.v1AgentMonthlyByMonth)
	mux.HandleFunc("GET /api/v1/selected/snapshot", s.v1SelectedSnapshot)
	mux.HandleFunc("OPTIONS /api/v1/selected/snapshot", v1SelectedPreflight)
	mux.HandleFunc("/api/v1/selected/snapshot", v1SelectedMethodNotAllowed)
	mux.HandleFunc("GET /api/v1/selected/changes", s.v1SelectedChanges)
	mux.HandleFunc("OPTIONS /api/v1/selected/changes", v1SelectedPreflight)
	mux.HandleFunc("/api/v1/selected/changes", v1SelectedMethodNotAllowed)
	mux.HandleFunc("/api/v1", s.v1UnknownFallback)
	mux.HandleFunc("/api/v1/{path...}", s.v1UnknownFallback)
	registerV1ReportRoutes(mux, s)
	mux.HandleFunc("GET /api/auth/login", s.adminLogin)
	mux.HandleFunc("GET /api/auth/options", s.adminLoginOptions)
	mux.HandleFunc("POST /api/auth/password", s.adminPasswordLogin)
	mux.HandleFunc("POST /api/auth/admin-account", s.adminAccountLogin)
	mux.HandleFunc("GET /api/super/users", s.superUsers)
	mux.HandleFunc("PUT /api/super/users/{id}", s.superSetAdmin)
	mux.HandleFunc("GET /api/auth/feishu", s.adminFeishuLogin)
	mux.HandleFunc("GET /api/auth/callback", s.adminOAuthCallback)
	mux.HandleFunc("GET /api/auth/check", s.adminAuthCheck)
	mux.HandleFunc("POST /api/admin/auth/logout", s.adminLogout)
	mux.HandleFunc("GET /api/admin/me", s.adminMe)
	mux.HandleFunc("GET /api/admin/catalog", s.adminCatalog)
	mux.HandleFunc("GET /api/admin/feedback", s.adminFeedbackList)
	mux.HandleFunc("PATCH /api/admin/feedback/{id}", s.adminFeedbackUpdate)
	mux.HandleFunc("POST /api/admin/feedback/{id}/erase", s.adminFeedbackErase)
	mux.HandleFunc("GET /api/admin/feedback/{id}/screenshot", s.adminFeedbackScreenshot)
	mux.HandleFunc("POST /api/admin/feedback-bans", s.adminFeedbackBan)
	mux.HandleFunc("DELETE /api/admin/feedback-bans/{hash}", s.adminFeedbackUnban)
	mux.HandleFunc("GET /api/admin/nav-counts", s.adminNavCounts)
	mux.HandleFunc("GET /api/admin/audit", s.adminAudit)
	mux.HandleFunc("GET /api/admin/runs", s.adminRunsOverview)
	mux.HandleFunc("POST /api/admin/processing/requeue", s.adminProcessingRequeue)
	mux.HandleFunc("GET /api/admin/content", s.adminContentSearch)
	mux.HandleFunc("GET /api/admin/content/{id}", s.adminContentDetail)
	mux.HandleFunc("GET /api/admin/content/{id}/x-encoding", s.adminContentXEncodingPreview)
	mux.HandleFunc("POST /api/admin/content/{id}/x-encoding", s.adminContentXEncodingNormalize)
	mux.HandleFunc("GET /api/admin/content/{id}/publication-date", s.adminContentPublicationDatePreview)
	mux.HandleFunc("POST /api/admin/content/{id}/publication-date", s.adminContentPublicationDateCorrect)
	mux.HandleFunc("POST /api/admin/content/{id}/seo", s.adminContentSEO)
	mux.HandleFunc("POST /api/admin/content/{id}/visibility", s.adminContentVisibility)
	mux.HandleFunc("POST /api/admin/content/{id}/override", s.adminContentOverride)
	mux.HandleFunc("POST /api/admin/content/{id}/rerun", s.adminContentRerun)
	mux.HandleFunc("POST /api/admin/content/{id}/detach", s.adminContentDetach)
	mux.HandleFunc("POST /api/admin/stories/merge", s.adminStoriesMerge)
	mux.HandleFunc("POST /api/admin/deliveries/{id}/resolve", s.adminDeliveryResolve)
	mux.HandleFunc("POST /api/admin/receipts/{id}/release", s.adminReceiptRelease)
	mux.HandleFunc("GET /api/admin/sources", s.adminSourcesList)
	mux.HandleFunc("GET /api/admin/sources/{id}", s.adminSourceDetail)
	mux.HandleFunc("GET /api/admin/labels", s.adminLabelsOverview)
	mux.HandleFunc("GET /api/admin/labels/hotspots", s.adminLabelHotspots)
	mux.HandleFunc("GET /api/admin/selectbench", s.adminSelectBenchList)
	mux.HandleFunc("GET /api/admin/selectbench/{id}", s.adminSelectBenchRun)
	mux.HandleFunc("POST /api/admin/selectbench/import", s.adminSelectBenchImport)
	mux.HandleFunc("GET /api/admin/settings", s.adminSettingsOverview)
	mux.HandleFunc("GET /api/admin/models", s.adminModelsOverview)
	mux.HandleFunc("POST /api/admin/models/{capability}", s.adminSwitchModel)
	mux.HandleFunc("POST /api/admin/notify-targets/{key}", s.adminSetNotifyTarget)
	mux.HandleFunc("POST /api/admin/settings/contact-qr", s.adminReplaceContactQR)
	mux.HandleFunc("/contact/{file...}", s.contactImage)
	mux.HandleFunc("PUT /api/admin/budgets/{service}", s.adminUpdateBudget)
	mux.HandleFunc("GET /api/auth/registration-status", s.registrationStatus)
	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("POST /api/auth/register/code/check", s.checkRegistrationCode)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("POST /api/auth/verify", s.verifyEmail)
	mux.HandleFunc("POST /api/auth/verify/resend", s.resendVerification)
	mux.HandleFunc("POST /api/auth/password/forgot", s.forgotPassword)
	mux.HandleFunc("POST /api/auth/password/reset", s.resetPassword)
	mux.HandleFunc("GET /api/me", s.withUser(s.me))
	mux.HandleFunc("DELETE /api/me", s.withUser(s.deleteAccount))
	mux.HandleFunc("GET /api/me/model", s.withUser(s.getModel))
	mux.HandleFunc("PUT /api/me/model", s.withUser(s.putModel))
	mux.HandleFunc("POST /api/me/model/test", s.withUser(s.testModel))
	mux.HandleFunc("GET /api/me/model/test/{id}", s.withUser(s.getModelTest))
	mux.HandleFunc("GET /api/me/interests", s.withUser(s.getInterests))
	mux.HandleFunc("PUT /api/me/interests", s.withUser(s.putInterests))
	mux.HandleFunc("GET /api/me/entities", s.withUser(s.getEntities))
	mux.HandleFunc("GET /api/me/catalog", s.userCatalog)
	mux.HandleFunc("GET /api/me/feed", s.withUser(s.feed))
	mux.HandleFunc("GET /api/me/feed/funnel", s.withUser(s.funnel))
	mux.HandleFunc("GET /api/me/hot", s.withUser(s.hot))
	mux.HandleFunc("GET /api/me/articles/{id}", s.withUser(s.personalArticle))
	mux.HandleFunc("GET /api/me/daily", s.withUser(s.daily))
	mux.HandleFunc("POST /api/me/daily/generate", s.withUser(s.generateDaily))
	mux.HandleFunc("GET /api/me/daily/generate/{jobId}", s.withUser(s.manualDailyStatus))
	mux.HandleFunc("GET /api/me/history", s.withUser(s.history))
	mux.HandleFunc("GET /api/me/sources", s.withUser(s.userSourcesList))
	mux.HandleFunc("GET /api/me/sources/explore", s.withUser(s.userSourcesExplore))
	mux.HandleFunc("POST /api/me/sources", s.withUser(s.userSourcesCreate))
	mux.HandleFunc("POST /api/me/sources/subscribe", s.withUser(s.userSourcesSubscribeExisting))
	mux.HandleFunc("POST /api/me/sources/discover", s.withUser(s.userSourcesDiscover))
	mux.HandleFunc("POST /api/me/ingest", s.withUser(s.userIngestArticle))
	mux.HandleFunc("GET /api/me/sources/{id}", s.withUser(s.userSourceGet))
	mux.HandleFunc("GET /api/me/sources/{id}/preview", s.withUser(s.userSourcePreview))
	mux.HandleFunc("PUT /api/me/sources/{id}", s.withUser(s.userSourceUpdate))
	mux.HandleFunc("PATCH /api/me/sources/{id}", s.withUser(s.userSourceUpdate))
	mux.HandleFunc("DELETE /api/me/sources/{id}", s.withUser(s.userSourceDelete))
	mux.HandleFunc("POST /api/me/sources/{id}/fetch", s.withUser(s.userSourceFetch))
	mux.HandleFunc("GET /api/me/sources/{id}/fetch/{jobId}", s.withUser(s.userSourceFetchStatus))
	mux.HandleFunc("/api/", s.apiFallback)
	return logServerFailures(s.Logger, securityHeaders(mux))
}

func (s *Server) apiFallback(w http.ResponseWriter, r *http.Request) {
	writeAdminProblem(w, http.StatusNotFound, "not_found", "No such endpoint.")
}

type userContext struct {
	ID    int64
	Email string
	CSRF  string
}
type userHandler func(http.ResponseWriter, *http.Request, userContext)

func (s *Server) withUser(next userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setPrivateSiteHeaders(w)
		user, err := s.userFromRequest(r)
		if err != nil {
			if errors.Is(err, errNoSession) || errors.Is(err, account.ErrNoSession) {
				writeError(w, http.StatusUnauthorized, "请先登录")
			} else {
				writeError(w, http.StatusServiceUnavailable, "会话服务暂不可用")
			}
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !sameOrigin(r, s.Config.TrustProxy) {
				writeError(w, http.StatusForbidden, "请求来源无效")
				return
			}
			csrf := r.Header.Get("X-CSRF-Token")
			if csrf == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(user.CSRF)) != 1 {
				writeError(w, http.StatusForbidden, "页面已过期，请刷新后重试")
				return
			}
		}
		next(w, r, user)
	}
}

func (s *Server) userFromRequest(r *http.Request) (userContext, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return userContext{}, errNoSession
	}
	var user userContext
	value, err := s.Accounts.ResolveSession(r.Context(), cookie.Value)
	user.ID, user.Email, user.CSRF = value.ID, value.Email, value.CSRF
	return user, err
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	started := time.Now()
	if err := s.Health.Check(ctx); err != nil {
		writeHealthProblem(w)
		return
	}
	ms := int64((time.Since(started) + time.Millisecond/2) / time.Millisecond)
	release := strings.TrimSpace(os.Getenv("AIHOT_RELEASE"))
	if release == "" {
		release = "dev"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "db": "ok", "ms": ms, "release": release})
}

// The health route keeps the legacy public endpoint's Problem Details failure contract.
func writeHealthProblem(w http.ResponseWriter) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "db": "unavailable"})
		return
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	requestID := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("Retry-After", "30")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "/problems/temporarily-unavailable", "title": "Temporarily unavailable", "status": http.StatusServiceUnavailable,
		"detail": "Temporarily unavailable.", "code": "temporarily_unavailable", "requestId": requestID, "retryAfter": 30,
	})
}

func (s *Server) registrationStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.Config.OpenRegistration && s.mailConfigured(),
	})
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, user userContext) {
	writeJSON(w, http.StatusOK, map[string]any{"id": user.ID, "email": user.Email, "csrfToken": user.CSRF})
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "请求内容无效")
		return false
	}
	if dec.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "请求内容无效")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func sameOrigin(r *http.Request, trustProxy ...bool) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := parsed.Host
	requestHost := r.Host
	if len(trustProxy) > 0 && trustProxy[0] {
		if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
			requestHost = forwarded
		}
	}
	return (parsed.Scheme == "https" || parsed.Scheme == "http") && strings.EqualFold(host, requestHost)
}

func clientKey(r *http.Request, trustProxy ...bool) string {
	if len(trustProxy) > 0 && trustProxy[0] {
		if proxyIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(proxyIP) != nil {
			return proxyIP
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{attempts: map[string][]time.Time{}}
}

func (l *rateLimiter) limited(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	recent := l.attempts[key][:0]
	for _, at := range l.attempts[key] {
		if now.Sub(at) < window {
			recent = append(recent, at)
		}
	}
	if len(recent) >= limit {
		l.attempts[key] = recent
		return true
	}
	recent = append(recent, now)
	l.attempts[key] = recent
	if len(l.attempts) > 5000 {
		l.attempts = map[string][]time.Time{key: recent}
	}
	return len(recent) > limit
}
