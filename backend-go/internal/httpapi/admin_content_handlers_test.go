package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aihot/backend-go/internal/adminauth"
	"aihot/backend-go/internal/admincontent"
	"aihot/backend-go/internal/adminlabels"
	"aihot/backend-go/internal/config"
)

type memoryAdminContentRepository struct {
	query    string
	chainID  string
	chain    *admincontent.Chain
	seoInput admincontent.SEOInput
	seoActor string
}

type memoryAdminContentCommands struct {
	visibility admincontent.VisibilityInput
	step       string
	requestID  string
	actor      string
}

type memoryXEncodingRepairRepository struct {
	plan      *admincontent.XEncodingPlan
	input     admincontent.XEncodingRepairInput
	articleID string
	actor     string
	calls     int
}

type memoryPublicationDateCorrectionRepository struct {
	plan    *admincontent.PublicationDateCorrectionPlan
	input   admincontent.PublicationDateCorrectionInput
	article string
	actor   string
	preview int
	correct int
}

/** PreviewPublicationDateCorrection returns the plan used to verify the protected admin route. */
func (r *memoryPublicationDateCorrectionRepository) PreviewPublicationDateCorrection(context.Context, string, string) (*admincontent.PublicationDateCorrectionPlan, error) {
	r.preview++
	return r.plan, nil
}

/** CorrectPublicationDate records the approved correction passed by the HTTP handler. */
func (r *memoryPublicationDateCorrectionRepository) CorrectPublicationDate(_ context.Context, article string, input admincontent.PublicationDateCorrectionInput, actor string) (*admincontent.PublicationDateCorrectionResult, bool, error) {
	r.article, r.input, r.actor = article, input, actor
	r.correct++
	return &admincontent.PublicationDateCorrectionResult{
		ArticleID: article, Revision: input.Version, Status: "corrected", PublishedAt: input.PublishedAt,
	}, true, nil
}

/** PreviewXEncodingRepair returns the fixed plan used by the route contract test. */
func (r *memoryXEncodingRepairRepository) PreviewXEncodingRepair(context.Context, string) (*admincontent.XEncodingPlan, error) {
	return r.plan, nil
}

/** NormalizeXEncoding records the reviewed approval passed by the HTTP handler. */
func (r *memoryXEncodingRepairRepository) NormalizeXEncoding(_ context.Context, articleID string, input admincontent.XEncodingRepairInput, actor string) (*admincontent.XEncodingRepairResult, bool, error) {
	r.articleID, r.input, r.actor, r.calls = articleID, input, actor, r.calls+1
	return &admincontent.XEncodingRepairResult{
		ArticleID: articleID,
		Revision:  input.Version,
		Status:    "repaired",
	}, true, nil
}

func (r *memoryAdminContentCommands) SetVisibility(_ context.Context, _ string, input admincontent.VisibilityInput, actor string) (*admincontent.PublishResult, bool, error) {
	r.visibility, r.actor = input, actor
	return &admincontent.PublishResult{ArticleID: "article-1", Visibility: input.Visibility}, true, nil
}
func (*memoryAdminContentCommands) OverrideFields(context.Context, string, admincontent.OverrideInput, string) (*admincontent.PublishResult, bool, error) {
	return &admincontent.PublishResult{ArticleID: "article-1"}, true, nil
}
func (r *memoryAdminContentCommands) Rerun(_ context.Context, _, step, requestID, actor string) (*admincontent.RerunResult, bool, error) {
	r.step, r.requestID, r.actor = step, requestID, actor
	jobID := "9321"
	return &admincontent.RerunResult{JobID: &jobID}, true, nil
}
func (*memoryAdminContentCommands) Detach(context.Context, string, string, string) (map[string]int, bool, error) {
	return map[string]int{"detached": 1}, true, nil
}
func (*memoryAdminContentCommands) MergeStories(context.Context, int64, int64, string, string) (map[string]int, bool, error) {
	return map[string]int{"moved": 1}, true, nil
}

func (*memoryAdminContentRepository) ByID(context.Context, string) ([]admincontent.Row, error) {
	return nil, nil
}
func (*memoryAdminContentRepository) ByURL(context.Context, []string, []string) ([]admincontent.Row, error) {
	return nil, nil
}
func (r *memoryAdminContentRepository) ByTitle(_ context.Context, query string) ([]admincontent.Row, error) {
	r.query = query
	return []admincontent.Row{}, nil
}
func (r *memoryAdminContentRepository) Chain(_ context.Context, id string) (*admincontent.Chain, error) {
	r.chainID = id
	return r.chain, nil
}
func (r *memoryAdminContentRepository) SetSEO(_ context.Context, _ string, input admincontent.SEOInput, actor string) (*admincontent.PublishResult, error) {
	r.seoInput, r.seoActor = input, actor
	return &admincontent.PublishResult{ArticleID: "article-1", Changed: true, ChangeKind: "content", Visibility: "public"}, nil
}
func TestAdminContentSearchKeepsAdminSessionBoundary(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	contentRepo := &memoryAdminContentRepository{chain: &admincontent.Chain{Article: admincontent.ArticleDetail{ID: "article-1"}}}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminContent: admincontent.NewService(contentRepo), limiter: newRateLimiter()}

	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/content?q=fixture", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("content diagnostics were available without an admin session: %d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/content?q=%20fixture%20", nil)
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || contentRepo.query != "fixture" {
		t.Fatalf("content search binding changed: status=%d query=%q body=%s", response.Code, contentRepo.query, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/admin/content/article-1", nil)
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || contentRepo.chainID != "article-1" || !strings.Contains(response.Body.String(), `"article":{"id":"article-1"`) {
		t.Fatalf("content chain route did not preserve its shape: status=%d id=%q body=%s", response.Code, contentRepo.chainID, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/seo", strings.NewReader(`{"indexed":true,"reason":"reviewed"}`))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	withoutCSRF := httptest.NewRecorder()
	server.Handler().ServeHTTP(withoutCSRF, request)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("SEO mutation without CSRF status=%d body=%s", withoutCSRF.Code, withoutCSRF.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/seo", strings.NewReader(`{"indexed":true,"reason":"reviewed"}`))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	seoResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(seoResponse, request)
	if seoResponse.Code != http.StatusOK || contentRepo.seoInput != (admincontent.SEOInput{Indexed: true, Reason: "reviewed"}) || contentRepo.seoActor != "admin:41" || !strings.Contains(seoResponse.Body.String(), `"articleId":"article-1"`) {
		t.Fatalf("SEO mutation contract changed: status=%d input=%#v actor=%q body=%s", seoResponse.Code, contentRepo.seoInput, contentRepo.seoActor, seoResponse.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/seo", strings.NewReader(`{"indexed":true,"reason":"  "}`))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	invalid := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalid, request)
	if invalid.Code != http.StatusBadRequest || contentRepo.seoActor != "admin:41" {
		t.Fatalf("blank SEO reason status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestSharedContentCommandsRequireAdminCSRFAndPreserveIdempotency(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	content := admincontent.NewService(&memoryAdminContentRepository{})
	commands := &memoryAdminContentCommands{}
	content.SetCommands(commands, []string{"ai"})
	server := &Server{Config: cfg, AdminAuth: adminService, AdminContent: content, limiter: newRateLimiter()}

	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/visibility", strings.NewReader(`{"visibility":"withdrawn","reason":"reviewed","version":0}`))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("content command accepted without CSRF: status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/visibility", strings.NewReader(`{"visibility":"withdrawn","reason":"reviewed","version":0}`))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || commands.visibility.Visibility != "withdrawn" || commands.actor != "admin:41" {
		t.Fatalf("visibility command contract changed: status=%d input=%#v actor=%q body=%s", response.Code, commands.visibility, commands.actor, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/rerun", strings.NewReader(`{"step":"analyze"}`))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	request.Header.Set("Idempotency-Key", "admin-request-123")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || commands.step != "analyze" || commands.requestID != "admin-request-123" || !strings.Contains(response.Body.String(), `"jobId":"9321"`) {
		t.Fatalf("rerun request identity or response changed: status=%d step=%q request=%q body=%s", response.Code, commands.step, commands.requestID, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/rerun", strings.NewReader(`{"step":"typo"}`))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	request.Header.Set("Idempotency-Key", "admin-request-456")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || commands.step != "analyze" {
		t.Fatalf("invalid rerun step reached command repository: status=%d step=%q body=%s", response.Code, commands.step, response.Body.String())
	}

}

/** TestXEncodingRepairRequiresAdminCSRFAndPreservesTheApprovalRequest locks down operator access. */
func TestXEncodingRepairRequiresAdminCSRFAndPreservesTheApprovalRequest(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	plan := &admincontent.XEncodingPlan{
		ArticleID: "article-1",
		Version:   4,
		Hash:      strings.Repeat("a", 64),
		Changed:   true,
	}
	repair := &memoryXEncodingRepairRepository{plan: plan}
	content := admincontent.NewService(&memoryAdminContentRepository{})
	content.SetXEncodingRepairRepository(repair)
	server := &Server{Config: cfg, AdminAuth: adminService, AdminContent: content, limiter: newRateLimiter()}

	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/content/article-1/x-encoding", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("X encoding preview was available without an admin session: %d", unauthorized.Code)
	}
	previewRequest := httptest.NewRequest(http.MethodGet, "/api/admin/content/article-1/x-encoding", nil)
	previewRequest.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	preview := httptest.NewRecorder()
	server.Handler().ServeHTTP(preview, previewRequest)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"hash":"`+plan.Hash+`"`) || preview.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("X encoding preview contract changed: status=%d headers=%v body=%s", preview.Code, preview.Header(), preview.Body.String())
	}

	approval := `{"version":4,"hash":"` + plan.Hash + `","requestId":"repair-request-123","reason":"confirmed provider escaping"}`
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/x-encoding", strings.NewReader(approval))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	withoutCSRF := httptest.NewRecorder()
	server.Handler().ServeHTTP(withoutCSRF, request)
	if withoutCSRF.Code != http.StatusForbidden || repair.calls != 0 {
		t.Fatalf("X encoding repair accepted without CSRF: status=%d calls=%d", withoutCSRF.Code, repair.calls)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/x-encoding", strings.NewReader(approval))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || repair.articleID != "article-1" || repair.input.Version != 4 || repair.input.Hash != plan.Hash || repair.actor != "admin:41" || repair.calls != 1 {
		t.Fatalf("approved repair did not preserve its proof and actor: status=%d repair=%#v body=%s", response.Code, repair, response.Body.String())
	}
}

/** TestPublicationDateCorrectionRequiresAdminCSRFAndPreservesTheApproval locks down historical correction access. */
func TestPublicationDateCorrectionRequiresAdminCSRFAndPreservesTheApproval(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminRepo := &memoryAdminRepository{}
	adminService := adminauth.NewService(adminRepo, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	plan := &admincontent.PublicationDateCorrectionPlan{
		ArticleID: "article-1", Version: 4, Hash: strings.Repeat("a", 64), Changed: true,
	}
	correction := &memoryPublicationDateCorrectionRepository{plan: plan}
	content := admincontent.NewService(&memoryAdminContentRepository{})
	content.SetPublicationDateCorrectionRepository(correction)
	server := &Server{Config: cfg, AdminAuth: adminService, AdminContent: content, limiter: newRateLimiter()}
	previewPath := "/api/admin/content/article-1/publication-date?publishedAt=2026-11-09T12%3A00%3A00.000Z"
	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, previewPath, nil))
	if unauthorized.Code != http.StatusUnauthorized || correction.preview != 0 {
		t.Fatalf("date preview was available without an admin session: status=%d calls=%d", unauthorized.Code, correction.preview)
	}
	previewRequest := httptest.NewRequest(http.MethodGet, previewPath, nil)
	previewRequest.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	preview := httptest.NewRecorder()
	server.Handler().ServeHTTP(preview, previewRequest)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"hash":"`+plan.Hash+`"`) || preview.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("publication date preview contract changed: status=%d headers=%v body=%s", preview.Code, preview.Header(), preview.Body.String())
	}
	approval := `{"version":4,"hash":"` + plan.Hash + `","publishedAt":"2026-11-09T12:00:00.000Z","requestId":"date-request-123","reason":"verified source timestamp"}`
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/publication-date", strings.NewReader(approval))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	withoutCSRF := httptest.NewRecorder()
	server.Handler().ServeHTTP(withoutCSRF, request)
	if withoutCSRF.Code != http.StatusForbidden || correction.correct != 0 {
		t.Fatalf("publication date correction accepted without CSRF: status=%d calls=%d", withoutCSRF.Code, correction.correct)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/article-1/publication-date", strings.NewReader(approval))
	request.Header.Set("Cookie", adminauth.SessionCookie+"="+token)
	request.Header.Set("X-CSRF-Token", adminRepo.session.CSRF)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || correction.article != "article-1" || correction.input.Version != 4 || correction.input.Hash != plan.Hash || correction.actor != "admin:41" || correction.correct != 1 {
		t.Fatalf("approved date correction was not forwarded intact: status=%d correction=%#v body=%s", response.Code, correction, response.Body.String())
	}
}

var _ admincontent.Repository = (*memoryAdminContentRepository)(nil)
var _ admincontent.CommandRepository = (*memoryAdminContentCommands)(nil)
var _ admincontent.XEncodingRepairRepository = (*memoryXEncodingRepairRepository)(nil)
var _ admincontent.PublicationDateCorrectionRepository = (*memoryPublicationDateCorrectionRepository)(nil)

type memoryAdminLabelsRepository struct {
	overviewCalled bool
	hotspotsCalled bool
	lastFilter     adminlabels.HotspotsFilter
}

/** Overview returns the label catalog used by the admin route contract. */
func (r *memoryAdminLabelsRepository) Overview(context.Context) (adminlabels.OverviewResult, error) {
	r.overviewCalled = true
	return adminlabels.OverviewResult{
		StableLabels:    []adminlabels.StableLabel{{Name: "Agent", Type: adminlabels.LabelTypeTopic, ArticleCount: 10}},
		CandidateLabels: []adminlabels.CandidateLabel{{Name: "视频生成", MappedTo: "视频", Status: "synonym", ArticleCount: 5}},
		Totals:          adminlabels.Totals{StableCount: 1, CandidateCount: 1, TotalTagged: 15},
	}, nil
}

/** Hotspots records the requested filters and returns a representative row. */
func (r *memoryAdminLabelsRepository) Hotspots(_ context.Context, filter adminlabels.HotspotsFilter) (adminlabels.HotspotsResult, error) {
	r.hotspotsCalled = true
	r.lastFilter = filter
	return adminlabels.HotspotsResult{
		Label: filter.Label,
		Page:  filter.Page,
		Total: 1,
		Rows:  []adminlabels.LabelHotspot{{ArticleID: "art-1", Title: "Agent Architecture", Tags: []string{"Agent"}}},
	}, nil
}

/** TestAdminLabelsRoutesRequireAdminAndReturnData preserves access and response behavior. */
func TestAdminLabelsRoutesRequireAdminAndReturnData(t *testing.T) {
	cfg := config.Config{SessionSecret: "admin-session-secret", AdminPassword: "correct-admin-password", SiteURL: "http://localhost:3000"}
	adminService := adminauth.NewService(&memoryAdminRepository{}, cfg, nil)
	token, _, err := adminService.PasswordLogin(context.Background(), cfg.AdminPassword, "/admin", "agent")
	if err != nil {
		t.Fatal(err)
	}
	repo := &memoryAdminLabelsRepository{}
	server := &Server{Config: cfg, AdminAuth: adminService, AdminLabels: adminlabels.NewService(repo), limiter: newRateLimiter()}

	unauthorizedOverview := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorizedOverview, httptest.NewRequest(http.MethodGet, "/api/admin/labels", nil))
	if unauthorizedOverview.Code != http.StatusUnauthorized || repo.overviewCalled {
		t.Fatalf("unauthorized overview status=%d called=%t", unauthorizedOverview.Code, repo.overviewCalled)
	}
	unauthorizedHotspots := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorizedHotspots, httptest.NewRequest(http.MethodGet, "/api/admin/labels/hotspots", nil))
	if unauthorizedHotspots.Code != http.StatusUnauthorized || repo.hotspotsCalled {
		t.Fatalf("unauthorized hotspots status=%d called=%t", unauthorizedHotspots.Code, repo.hotspotsCalled)
	}

	authorizedOverview := httptest.NewRequest(http.MethodGet, "/api/admin/labels", nil)
	authorizedOverview.AddCookie(&http.Cookie{Name: adminauth.SessionCookie, Value: token})
	overviewResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(overviewResponse, authorizedOverview)
	if overviewResponse.Code != http.StatusOK || !repo.overviewCalled || !strings.Contains(overviewResponse.Body.String(), `"name":"Agent"`) || !strings.Contains(overviewResponse.Body.String(), `"name":"视频生成"`) {
		t.Fatalf("authorized overview status=%d body=%s", overviewResponse.Code, overviewResponse.Body.String())
	}

	authorizedHotspots := httptest.NewRequest(http.MethodGet, "/api/admin/labels/hotspots?label=Agent&sort=score&q=Architecture", nil)
	authorizedHotspots.AddCookie(&http.Cookie{Name: adminauth.SessionCookie, Value: token})
	hotspotsResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(hotspotsResponse, authorizedHotspots)
	if hotspotsResponse.Code != http.StatusOK || !repo.hotspotsCalled || !strings.Contains(hotspotsResponse.Body.String(), `"article_id":"art-1"`) {
		t.Fatalf("authorized hotspots status=%d body=%s", hotspotsResponse.Code, hotspotsResponse.Body.String())
	}
	if repo.lastFilter.Label != "Agent" || repo.lastFilter.SortBy != "score" || repo.lastFilter.Query != "Architecture" {
		t.Fatalf("filter parameters not passed correctly: %#v", repo.lastFilter)
	}
}
