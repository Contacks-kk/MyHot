// Package collector collects the platform's public source pool without model calls.
package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/modelconfig"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}
type Service struct {
	Client     HTTPDoer
	Repository *Repository
	Materials  *materials.Service
	XProvider  XProvider
	Jina       JinaReader
}

// ErrFetchRecorded marks an upstream collection failure that has already been
// persisted to fetch_runs and sources. River should not retry this result;
// the source's next_fetch_at contains its retry schedule.
var ErrFetchRecorded = errors.New("source fetch failure recorded")

type sourceFetchError struct {
	class string
	err   error
}

func (e *sourceFetchError) Error() string { return e.err.Error() }
func (e *sourceFetchError) Unwrap() error { return e.err }

func rssResponseFailureClass(err error) string {
	var classified *sourceFetchError
	if errors.As(err, &classified) {
		return classified.class
	}
	switch {
	case err == nil:
		return "rss_response"
	case err.Error() == "source HTTP request failed":
		return "rss_http_request"
	case strings.HasPrefix(err.Error(), "source HTTP status "):
		status, parseErr := strconv.Atoi(strings.TrimPrefix(err.Error(), "source HTTP status "))
		if parseErr == nil && status >= 100 && status <= 599 {
			return fmt.Sprintf("rss_http_status_%d", status)
		}
		return "rss_http_status"
	case err.Error() == "invalid source URL":
		return "rss_invalid_url"
	case err.Error() == "source URL must be public HTTP or HTTPS without credentials":
		return "rss_url_rejected"
	case err.Error() == "source response is too large":
		return "rss_response_too_large"
	default:
		return "rss_response_read"
	}
}

type source struct {
	ID, Kind, Mode     string
	Config             settings
	Cursor             settings
	Interval, Failures int
	LastOK             *time.Time
	First              bool
}
type item struct {
	URL, Title, Excerpt, Body, Author string
	BodyHTML                          string
	OpaqueGUID                        string
	IdentityKey                       string
	DetailRules                       string
	BodyStatus                        string
	Language                          string
	Media, XPost, Raw                 json.RawMessage
	Published                         *time.Time
	Categories                        []string
}

type PreviewItem struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	PublishedAt *string `json:"publishedAt"`
	Excerpt     string  `json:"excerpt"`
}

type PreviewResult struct {
	Milliseconds int           `json:"ms"`
	Count        int           `json:"count"`
	Items        []PreviewItem `json:"items"`
}

func publicClient() *http.Client {
	return publicClientWithTimeout(25 * time.Second)
}

func publicClientWithTimeout(timeout time.Duration) *http.Client {
	return publicClientWithDialer(timeout, modelconfig.DialPublic)
}

/** NewHTTPClientWithDialer uses the supplied guarded dialer for every request hop. */
func NewHTTPClientWithDialer(timeout time.Duration, dialContext func(context.Context, string, string) (net.Conn, error)) HTTPDoer {
	if dialContext == nil {
		dialContext = modelconfig.DialPublic
	}
	return publicClientWithDialer(timeout, dialContext)
}

/** publicClientWithDialer builds an HTTP client with shared redirect and response limits. */
func publicClientWithDialer(timeout time.Duration, dialContext func(context.Context, string, string) (net.Conn, error)) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, DialContext: dialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxIdleConns: 10},
		CheckRedirect: checkPublicRedirect}
}

/** checkPublicRedirect validates redirect targets and keeps protected source requests on their original origin. */
func checkPublicRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("source redirect limit reached")
	}
	if err := validURL(req.URL); err != nil {
		return err
	}
	if len(via) == 0 {
		return nil
	}
	first := via[0]
	if first.URL == nil {
		return errors.New("source redirect has no original URL")
	}
	for _, previous := range via {
		if previous.URL != nil && strings.EqualFold(previous.URL.Scheme, "https") && strings.EqualFold(req.URL.Scheme, "http") {
			return errors.New("source redirect cannot downgrade HTTPS")
		}
	}
	if strings.EqualFold(first.URL.Scheme, req.URL.Scheme) && strings.EqualFold(first.URL.Host, req.URL.Host) {
		return applyFetchRedirectSemantics(req, via)
	}
	for _, previous := range via {
		if previous.Method != http.MethodGet || (previous.Body != nil && previous.Body != http.NoBody) || hasCredentialHeader(previous.Header) || hasCredentialQuery(previous.URL) {
			return errors.New("protected source redirect changed origin")
		}
	}
	return applyFetchRedirectSemantics(req, via)
}

/** applyFetchRedirectSemantics restores Fetch method and body rules after net/http prepares a hop. */
func applyFetchRedirectSemantics(req *http.Request, via []*http.Request) error {
	if req.Response == nil || len(via) == 0 {
		return nil
	}
	previous := via[len(via)-1]
	droppedBody := redirectDropsBody(req.Response.StatusCode, previous.Method) || hasDroppedRedirectBody(via)
	if redirectDropsBody(req.Response.StatusCode, previous.Method) {
		req.Method = http.MethodGet
	} else {
		req.Method = previous.Method
	}
	if droppedBody {
		req.Body = nil
		req.GetBody = nil
		req.ContentLength = 0
		req.TransferEncoding = nil
		clearRedirectBodyHeaders(req.Header)
		return nil
	}
	return restoreRedirectBody(req, previous)
}

/** redirectDropsBody matches Fetch's body-dropping rules for 301, 302 and 303. */
func redirectDropsBody(status int, method string) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound:
		return method == http.MethodPost
	case http.StatusSeeOther:
		return method != http.MethodGet && method != http.MethodHead
	default:
		return false
	}
}

/** hasDroppedRedirectBody keeps later hops bodyless after an earlier redirect discarded it. */
func hasDroppedRedirectBody(via []*http.Request) bool {
	for index := 1; index < len(via); index++ {
		previous := via[index-1]
		current := via[index]
		if current.Response != nil && redirectDropsBody(current.Response.StatusCode, previous.Method) {
			return true
		}
	}
	return false
}

/** restoreRedirectBody replays a body that Fetch semantics preserve across a redirect. */
func restoreRedirectBody(req, previous *http.Request) error {
	req.ContentLength = previous.ContentLength
	req.TransferEncoding = append([]string(nil), previous.TransferEncoding...)
	if previous.Body == nil || previous.Body == http.NoBody {
		return nil
	}
	if previous.GetBody == nil {
		return errors.New("source redirect cannot replay the request body")
	}
	body, err := previous.GetBody()
	if err != nil {
		return fmt.Errorf("source redirect cannot replay the request body: %w", err)
	}
	req.Body = body
	req.GetBody = previous.GetBody
	return nil
}

/** clearRedirectBodyHeaders removes headers invalidated when a redirect drops its body. */
func clearRedirectBodyHeaders(headers http.Header) {
	for _, name := range []string{"Content-Encoding", "Content-Language", "Content-Length", "Content-Location", "Content-Type"} {
		headers.Del(name)
	}
}

/** hasCredentialHeader reports whether a header name looks like it carries credentials. */
func hasCredentialHeader(headers http.Header) bool {
	for name := range headers {
		if isCredentialName(name) {
			return true
		}
	}
	return false
}

/** hasCredentialQuery reports whether a URL query contains credential-shaped parameter names. */
func hasCredentialQuery(target *url.URL) bool {
	if target == nil {
		return true
	}
	values, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return true
	}
	for name := range values {
		if isCredentialName(name) {
			return true
		}
	}
	return false
}

/** isCredentialName recognizes common credential markers in request header and query names. */
func isCredentialName(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range []string{"auth", "cookie", "credential", "token", "secret", "signature", "password", "passwd", "apikey"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, part := range strings.FieldsFunc(lower, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		if part == "key" {
			return true
		}
	}
	return false
}

func NewHTTPClient() HTTPDoer { return publicClient() }

// NewHTTPClientWithTimeout uses the same public-address and redirect protections with a provider-specific timeout.
func NewHTTPClientWithTimeout(timeout time.Duration) HTTPDoer {
	return publicClientWithTimeout(timeout)
}

func validURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errors.New("source URL must be public HTTP or HTTPS without credentials")
	}
	return nil
}
func NewService(repository *Repository, client HTTPDoer, materialService *materials.Service) *Service {
	return &Service{Repository: repository, Client: client, Materials: materialService}
}

func (s *Service) DueSourceIDs(ctx context.Context) ([]string, error) {
	return s.Repository.DueSourceIDs(ctx)
}

func (s *Service) CollectSource(ctx context.Context, id string) error {
	_, err := s.CollectSourceJob(ctx, id, false, 0)
	return err
}

// CollectSourceNow bypasses the enabled and due-time gates for an administrator's
// explicit fetch. It still uses the same per-source advisory lock and run record.
func (s *Service) CollectSourceNow(ctx context.Context, id string) error {
	_, err := s.CollectSourceJob(ctx, id, true, 0)
	return err
}

const (
	isolatedInitialLiveLimit  = 10
	isolatedInitialLiveWindow = 48 * time.Hour
)

/** firstImportBackfillReason decides whether a first-fetch item enters live timeline or backfill. */
func firstImportBackfillReason(src source, published *time.Time, now time.Time, isolatedLiveCount *int) string {
	if !src.First {
		return ""
	}
	if src.Mode == "isolated" && published != nil && !published.IsZero() && !published.Before(now.Add(-isolatedInitialLiveWindow)) && !published.After(now.Add(materials.FutureTolerance)) {
		if *isolatedLiveCount < isolatedInitialLiveLimit {
			*isolatedLiveCount++
			return ""
		}
	}
	return "first-import"
}

/** CollectSourceJob reports real RSS or web-list success and records who triggered it. */
func (s *Service) CollectSourceJob(ctx context.Context, id string, force bool, userID int64) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 11*time.Minute)
	defer cancel()
	run, sourceFound, err := s.Repository.BeginSourceRun(ctx, id, force)
	if err != nil || !sourceFound {
		return false, err
	}
	defer s.Repository.ReleaseSource(run)
	if run.Reused {
		return false, nil
	}
	src := run.Source
	if src.Kind == "x_search" {
		return false, s.collectXSource(ctx, run)
	}
	var items []item
	var fetchErr error
	var cursorUpdates map[string]string
	var paidReceiptIDs []int64
	var notModified bool
	failureStage := "source_fetch"
	failureClass := "source_fetch"
	if unknown := unsupportedSourceConfig(src.Kind, src.Config); len(unknown) > 0 {
		fetchErr = fmt.Errorf("unsupported config: %s", strings.Join(unknown, ", "))
	} else if src.Kind == "rss" {
		items, cursorUpdates, notModified, fetchErr = s.fetchRSS(ctx, src, force)
		var classified *sourceFetchError
		if errors.As(fetchErr, &classified) {
			failureClass = classified.class
		}
	} else {
		items, paidReceiptIDs, fetchErr = s.fetchWithReceipts(ctx, src)
	}
	found := len(items)
	created := 0
	detailAttempts := 0
	detailFailures := 0
	detailPending := false
	if fetchErr == nil && !notModified {
		fetchNow := time.Now().UTC()
		items = filterItemsWithMode(items, src.Config, src.Mode, src.First, fetchNow)
		if !src.First {
			initializedAt := sourceInitializedAt(src, fetchNow)
			cutoff := initializedAt.Add(-materials.StaleOnDiscovery)
			identities := make([]string, 0)
			for _, candidate := range items {
				if candidate.Published != nil && candidate.Published.Before(cutoff) {
					identities = append(identities, itemIdentityKey(candidate))
				}
			}
			existing, lookupErr := s.Repository.ExistingIdentityKeys(ctx, identities)
			if lookupErr != nil {
				fetchErr = lookupErr
				failureStage = "article_admission"
				failureClass = "article_admission"
			} else {
				items = admitExistingOrRecent(items, initializedAt, existing)
			}
		}
		if fetchErr == nil {
			detail := sourceDetailSettings(src.Config)
			detailRules := detailRuleHash(detail)
			storedDetails := map[string]storedDetail{}
			if len(detail) > 0 {
				identities := make([]string, 0, len(items))
				for _, candidate := range items {
					identities = append(identities, itemIdentityKey(candidate))
				}
				var lookupErr error
				storedDetails, lookupErr = s.Repository.StoredDetails(ctx, identities)
				if lookupErr != nil {
					fetchErr = lookupErr
					failureStage = "detail_lookup"
					failureClass = "detail_lookup"
				}
			}
			maxDetails := detail.integer("maxFetches", 3)
			if maxDetails > 3 {
				maxDetails = 3
			}
			if !hasDetailRules(detail) {
				maxDetails = 0
			}

			// Allow at most 10 fresh 48h items through on an isolated source's first fetch; mark overflow as backfill.
			isolatedLiveCount := 0
			if fetchErr == nil {
				for _, candidate := range items {
					stored, known := storedDetails[itemIdentityKey(candidate)]
					storedHeadline := false
					if known {
						if detailTitleConfigured(detail) && stored.Title != "" {
							storedHeadline = isCallToActionTitle(candidate.Title) && !isCallToActionTitle(stored.Title)
							if storedHeadline || stored.Rules != nil && *stored.Rules == detailRules {
								candidate.Title = stored.Title
							}
						}
						if candidate.Published == nil && stored.PublishedAt != nil {
							candidate.Published = stored.PublishedAt
						}
						if candidate.Excerpt == "" && stored.Excerpt != nil {
							candidate.Excerpt = *stored.Excerpt
						}
						if candidate.Body == "" && stored.BodyText != nil {
							candidate.Body = *stored.BodyText
						}
					}
					needsDetail := detailReadNeeds(candidate, detail, storedHeadline)
					if known && stored.Rules != nil && *stored.Rules == detailRules {
						needsDetail = detailReadNeed{}
					}
					if needsDetail.any() {
						if maxDetails > 0 {
							maxDetails--
							detailAttempts++
							detailErr, detailReceiptIDs := s.enrichCandidateDetail(ctx, src, &candidate, detail, needsDetail)
							paidReceiptIDs = append(paidReceiptIDs, detailReceiptIDs...)
							if detailErr == nil {
								candidate.DetailRules = detailRules
							} else {
								detailFailures++
								detailPending = true
							}
						} else {
							detailPending = true
						}
					}
					// Detail pages are fetched after list filtering; apply the same content bounds again.
					candidate.Title = truncate(plain(candidate.Title), 1000)
					candidate.Excerpt = truncate(plain(candidate.Excerpt), 2000)
					candidate.Body = truncate(plain(candidate.Body), 64000)
					candidate.Author = truncate(plain(candidate.Author), 200)

					storeNow := time.Now().UTC()
					explicitBackfill := firstImportBackfillReason(run.Source, candidate.Published, storeNow, &isolatedLiveCount)
					var added bool
					added, fetchErr = s.storeItemWithBackfill(ctx, run, candidate, explicitBackfill, storeNow)
					if fetchErr != nil {
						failureStage = "article_store"
						failureClass = "article_store"
						break
					}
					if added {
						created++
					}
				}
			}
			if fetchErr == nil && src.Kind == "rss" {
				if cursorUpdates == nil {
					cursorUpdates = make(map[string]string)
				}
				cursorUpdates["detailRules"] = detailRules
				cursorUpdates["detailPending"] = strconv.FormatBool(detailPending)
			}
		}
	}
	finishCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if fetchErr != nil {
		// Do not store HTTP errors containing source credentials, query strings or response bodies.
		message := "source collection failed"
		if errors.Is(fetchErr, errUnsupported) {
			message = "source adapter is unsupported by Go collector; configure RSS, CSS/Markdown list or ordinary JSON"
		}
		if err := s.Repository.FinishFailure(finishCtx, run, found, created, message); err != nil {
			return false, err
		}
		log.Printf("source collection failed source_id=%q source_kind=%q stage=%s class=%s", src.ID, src.Kind, failureStage, failureClass)
		return false, fmt.Errorf("%w: %s", ErrFetchRecorded, message)
	}
	var detailAudit json.RawMessage
	if len(src.Config.object("detail")) > 0 {
		detailAudit, _ = json.Marshal(map[string]any{"detailAttempts": detailAttempts, "detailFailures": detailFailures, "detailPending": detailPending})
	}
	err = s.Repository.FinishSuccess(finishCtx, run, found, created, cursorUpdates, notModified, userID, detailAudit, paidReceiptIDs)
	return err == nil && (src.Kind == "rss" || src.Kind == "web_list"), err
}

/** sourceDetailSettings applies the listing's date offset to configured detail rules. */
func sourceDetailSettings(config settings) settings {
	detail := config.object("detail")
	if len(detail) == 0 || detail.text("publishedAtUtcOffset") != "" || config.text("publishedAtUtcOffset") == "" {
		return detail
	}
	merged := make(settings, len(detail)+1)
	for key, value := range detail {
		merged[key] = value
	}
	merged["publishedAtUtcOffset"] = config.text("publishedAtUtcOffset")
	return merged
}

func (s *Service) storeItem(ctx context.Context, run *sourceRun, candidate item) (bool, error) {
	now := time.Now().UTC()
	// A source's first successful fetch treats every discovered item as backfill,
	// including items without a publication date. Later fetches use publication age.
	explicitBackfill := ""
	if run.Source.First {
		explicitBackfill = "first-import"
	}
	return s.storeItemWithBackfill(ctx, run, candidate, explicitBackfill, now)
}

/** enrichCandidateDetail applies local and paid rules while retaining each paid receipt for commit. */
func (s *Service) enrichCandidateDetail(ctx context.Context, src source, candidate *item, detail settings, needs detailReadNeed) (error, []int64) {
	targetURL, isJina := jinaTargetURL(src.Config.text("url"))
	readJinaTitle := isJina && needs.title && detail.text("titleRegex") != ""
	readJinaDate := isJina && needs.date && detail.text("publishedAtRegex") != ""
	var receiptIDs []int64
	if readJinaTitle || readJinaDate {
		if s.Jina == nil {
			return errors.New("Jina reader is not configured"), nil
		}
		page, err := s.Jina.Read(ctx, targetURL, src.ID, "source_detail", "markdown", nil)
		if err != nil {
			return err, nil
		}
		receiptIDs = append(receiptIDs, page.ReceiptID)
		enrichJinaDetail(candidate, page, detail, readJinaTitle, readJinaDate)
	}
	needsHTML := needs.summary || needs.title && !readJinaTitle || needs.date && !readJinaDate
	if !needsHTML {
		return nil, receiptIDs
	}
	detailCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	body, err := s.fetchURL(detailCtx, candidate.URL, http.MethodGet, nil, nil)
	if err != nil {
		return err, receiptIDs
	}
	enrichDetailFields(candidate, body, detail, detailReadNeed{
		title:   needs.title && !readJinaTitle,
		date:    needs.date && !readJinaDate,
		summary: needs.summary,
	})
	return nil, receiptIDs
}

/** storeItemWithBackfill stores one collected item with explicit backfill classification. */
func (s *Service) storeItemWithBackfill(ctx context.Context, run *sourceRun, candidate item, explicitBackfill string, now time.Time) (bool, error) {
	status := candidate.BodyStatus
	if status == "" {
		status = "pending"
		if candidate.Body != "" {
			status = "ok"
		}
	}
	identity := itemIdentityKey(candidate)
	stored, err := s.Materials.Store(ctx, materials.Input{
		SourceID: run.Source.ID, URL: candidate.URL, IdentityKey: identity, Title: candidate.Title,
		Author: candidate.Author, PublishedAt: candidate.Published, DiscoveredAt: now, Backfill: explicitBackfill,
		Excerpt: candidate.Excerpt, BodyText: candidate.Body, BodyHTML: candidate.BodyHTML, BodyStatus: status, Via: "fetch",
		Language: candidate.Language, Media: candidate.Media, XPost: candidate.XPost, Raw: candidate.Raw,
	})
	if err != nil {
		return false, err
	}
	if candidate.DetailRules != "" {
		if err := s.Repository.RecordDetailRules(ctx, stored.ArticleID, run.Source.ID, candidate.DetailRules); err != nil {
			return false, err
		}
	}
	return stored.Created, nil
}

/** detailRuleHash identifies the source detail rules saved with each enriched article. */
func detailRuleHash(detail settings) string {
	if len(detail) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(detail)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

/** rssConfigHash invalidates validators when any feed parsing setting changes. */
func rssConfigHash(config settings) string {
	encoded, err := json.Marshal(config)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

/** hasDetailRules reports whether the detail configuration extracts a listing field. */
func hasDetailRules(detail settings) bool {
	return detail.text("summarySelector") != "" || detailTitleConfigured(detail) || detailDateConfigured(detail)
}

/** needsDetailRead decides whether a candidate still lacks a configured detail field. */
func needsDetailRead(candidate item, detail settings, storedHeadline bool) bool {
	return detailReadNeeds(candidate, detail, storedHeadline).any()
}

type detailReadNeed struct {
	title   bool
	date    bool
	summary bool
}

/** detailReadNeeds identifies configured fields that still need a detail rendering. */
func detailReadNeeds(candidate item, detail settings, storedHeadline bool) detailReadNeed {
	return detailReadNeed{
		title:   detailTitleConfigured(detail) && (detail.boolean("titleAuthoritative") || !storedHeadline && needsRecoveredTitle(candidate.Title)),
		date:    detailDateConfigured(detail) && (candidate.Published == nil || detail.boolean("upgradeDatePrecision")),
		summary: detail.text("summarySelector") != "" && candidate.Excerpt == "",
	}
}

/** any reports whether a candidate has a missing configured detail field. */
func (need detailReadNeed) any() bool {
	return need.title || need.date || need.summary
}

/** detailTitleConfigured reports whether detail rules can recover a title. */
func detailTitleConfigured(detail settings) bool {
	return detail.text("titleSelector") != "" || detail.text("titleRegex") != ""
}

/** detailDateConfigured reports whether detail rules can recover a publication date. */
func detailDateConfigured(detail settings) bool {
	return detail.text("publishedAtSelector") != "" || detail.text("publishedAtRegex") != ""
}

/** needsRecoveredTitle identifies listing labels that need an article-page headline. */
func needsRecoveredTitle(title string) bool {
	title = strings.TrimSpace(title)
	if len([]rune(title)) > 100 {
		return true
	}
	return isCallToActionTitle(title)
}

/** isCallToActionTitle recognizes listing labels that do not identify an article. */
func isCallToActionTitle(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "read more", "read the blog", "learn more", "continue reading", "more", "阅读全文", "阅读更多", "查看详情", "了解更多":
		return true
	default:
		return false
	}
}

/** sourceInitializedAt returns the listing's first successful check or the best available fallback. */
func sourceInitializedAt(src source, fallback time.Time) time.Time {
	if value := src.Cursor.text("initializedAt"); value != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed.UTC()
		}
	}
	if src.LastOK != nil {
		return src.LastOK.UTC()
	}
	return fallback.UTC()
}

/** itemIdentityKey matches the identity used when the material is stored. */
func itemIdentityKey(candidate item) string {
	if candidate.IdentityKey != "" {
		return candidate.IdentityKey
	}
	if match := tweetPattern.FindStringSubmatch(candidate.URL); len(match) > 1 {
		return "x:" + match[1]
	}
	return "url:" + candidate.URL
}

/** annotateOpaqueRSSIdentities gives non-address feed GUIDs the stable source-scoped identity. */
func annotateOpaqueRSSIdentities(items []item, sourceID string) {
	for index := range items {
		if items[index].OpaqueGUID == "" {
			continue
		}
		hash := sha256.Sum256([]byte(items[index].OpaqueGUID + "\x01" + items[index].Title))
		items[index].IdentityKey = fmt.Sprintf("src:%s:%x", sourceID, hash[:16])
	}
}

/** admitExistingOrRecent excludes unseen archive entries while keeping corrections to known items. */
func admitExistingOrRecent(candidates []item, initializedAt time.Time, existing map[string]bool) []item {
	cutoff := initializedAt.Add(-materials.StaleOnDiscovery)
	admitted := make([]item, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Published == nil || !candidate.Published.Before(cutoff) || existing[itemIdentityKey(candidate)] {
			admitted = append(admitted, candidate)
		}
	}
	return admitted
}

// Preview reads a supported listing and applies the same publication admission
// rules as the TypeScript source preview. It never creates a fetch run or stores
// articles. X search remains on its provider-specific adapter.
func (s *Service) Preview(ctx context.Context, id, kind string, config map[string]any) (PreviewResult, error) {
	started := time.Now()
	if kind == "x_search" {
		return s.PreviewX(ctx, id, config, nil)
	}
	if kind != "rss" && kind != "web_list" && kind != "json_list" {
		return PreviewResult{}, fmt.Errorf("preview is not available for %s sources", kind)
	}
	if unknown := unsupportedSourceConfig(kind, settings(config)); len(unknown) > 0 {
		return PreviewResult{}, fmt.Errorf("unsupported config: %s", strings.Join(unknown, ", "))
	}
	items, receiptIDs, err := s.fetchWithReceipts(ctx, source{ID: id, Kind: kind, Config: settings(config)})
	if err != nil {
		return PreviewResult{}, err
	}
	items = previewAdmission(items, settings(config), time.Now().UTC())
	result := PreviewResult{Milliseconds: int(time.Since(started) / time.Millisecond), Count: len(items), Items: make([]PreviewItem, 0, min(len(items), 20))}
	for _, candidate := range items[:min(len(items), 20)] {
		var publishedAt *string
		if candidate.Published != nil {
			formatted := candidate.Published.UTC().Format("2006-01-02T15:04:05.000Z")
			publishedAt = &formatted
		}
		excerpt := candidate.Excerpt
		if excerpt == "" {
			excerpt = candidate.Body
		}
		result.Items = append(result.Items, PreviewItem{Title: candidate.Title, URL: candidate.URL, PublishedAt: publishedAt, Excerpt: truncateUTF16(excerpt, 200)})
	}
	for _, receiptID := range receiptIDs {
		if err := s.Jina.Complete(ctx, receiptID); err != nil {
			return PreviewResult{}, err
		}
	}
	return result, nil
}

// PreviewX searches the source's current cursor without persisting fetched posts or changing it.
func (s *Service) PreviewX(ctx context.Context, id string, config, cursor map[string]any) (PreviewResult, error) {
	started := time.Now()
	src := source{ID: id, Kind: "x_search", Config: settings(config), Cursor: settings(cursor)}
	if unknown := unsupportedXConfig(src.Config); len(unknown) > 0 {
		return PreviewResult{}, fmt.Errorf("unsupported config: %s", strings.Join(unknown, ", "))
	}
	base := src.Config.text("query")
	if handle := selfThreadHandle(src); handle != "" {
		base = "from:" + handle + " (-filter:replies OR filter:self_threads)"
	}
	if strings.TrimSpace(base) == "" {
		return PreviewResult{}, errors.New("X search query is missing")
	}
	lastID := cursorString(src.Cursor["lastTweetId"])
	read, err := s.readXSearch(ctx, base, lastID, readXBacklog(src.Cursor["xBacklog"]), "source:"+id, src.Config.text("searchType"))
	if err != nil {
		return PreviewResult{}, err
	}
	items := make([]item, 0, len(read.Tweets))
	for _, post := range read.Tweets {
		items = append(items, tweetCandidate(post))
	}
	items = admitXItems(items, src.Config)
	items = previewAdmission(items, src.Config, time.Now().UTC())
	result := PreviewResult{Milliseconds: int(time.Since(started) / time.Millisecond), Count: len(items), Items: make([]PreviewItem, 0, min(len(items), 20))}
	for _, candidate := range items[:min(len(items), 20)] {
		excerpt := candidate.Excerpt
		if excerpt == "" {
			excerpt = candidate.Body
		}
		result.Items = append(result.Items, PreviewItem{Title: candidate.Title, URL: candidate.URL, PublishedAt: previewTime(candidate.Published), Excerpt: truncateUTF16(excerpt, 200)})
	}
	return result, nil
}

func previewTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format("2006-01-02T15:04:05.000Z")
	return &formatted
}

/** fetchURL downloads one guarded source URL and discards its final redirect address. */
func (s *Service) fetchURL(ctx context.Context, raw, method string, headers settings, body any) ([]byte, error) {
	data, _, err := s.fetchURLWithFinalURL(ctx, raw, method, headers, body)
	return data, err
}

/** fetchURLWithFinalURL downloads one guarded source URL and returns the response's final address. */
func (s *Service) fetchURLWithFinalURL(ctx context.Context, raw, method string, headers settings, body any) ([]byte, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", errors.New("invalid source URL")
	}
	if err = validURL(u); err != nil {
		return nil, "", err
	}
	var reader io.Reader
	if body != nil {
		encoded, e := json.Marshal(body)
		if e != nil {
			return nil, "", e
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "MyHOT/1.0 (+public source collector)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		if text, ok := value.(string); ok {
			req.Header.Set(name, text)
		}
	}
	client := s.Client
	if client == nil {
		client = publicClient()
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, "", errors.New("source HTTP request failed")
	}
	defer res.Body.Close()
	finalURL := u.String()
	if res.Request != nil && res.Request.URL != nil {
		finalURL = res.Request.URL.String()
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, finalURL, fmt.Errorf("source HTTP status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024+1))
	if err != nil {
		return nil, finalURL, err
	}
	if len(data) > 8*1024*1024 {
		return nil, finalURL, errors.New("source response is too large")
	}
	return data, finalURL, nil
}

/** fetchRSS applies config-aware validators and parses one source listing. */
func (s *Service) fetchRSS(ctx context.Context, src source, force bool) ([]item, map[string]string, bool, error) {
	address := src.Config.text("feedUrl")
	if address == "" {
		address = src.Config.text("url")
	}
	configHash := rssConfigHash(src.Config)
	headers := settings{}
	previousResponseURL := src.Cursor.text("rssResponseURL")
	hasPrevious := !force && src.Cursor.text("rssURL") == address && src.Cursor.text("rssConfigHash") == configHash && previousResponseURL != ""
	hasValidator := hasPrevious && (src.Cursor.text("rssETag") != "" || src.Cursor.text("rssLastModified") != "")
	if hasPrevious {
		rules := detailRuleHash(src.Config.object("detail"))
		rulesChanged := rules != "" && src.Cursor.text("detailRules") != rules
		if hasValidator && !rulesChanged && src.Cursor.text("detailPending") != "true" {
			if etag := src.Cursor.text("rssETag"); etag != "" {
				headers["If-None-Match"] = etag
			} else if modified := src.Cursor.text("rssLastModified"); modified != "" {
				headers["If-Modified-Since"] = modified
			}
		}
	}
	usedValidator := len(headers) > 0
	body, responseHeaders, status, finalAddress, err := s.fetchRSSResponse(ctx, address, headers)
	if err != nil {
		return nil, nil, false, &sourceFetchError{class: rssResponseFailureClass(err), err: err}
	}
	if status == http.StatusNotModified && usedValidator && finalAddress != previousResponseURL {
		body, responseHeaders, status, finalAddress, err = s.fetchRSSResponse(ctx, address, settings{})
		if err != nil {
			return nil, nil, false, &sourceFetchError{class: rssResponseFailureClass(err), err: err}
		}
		usedValidator = false
	}
	if status == http.StatusNotModified && (!usedValidator || finalAddress != previousResponseURL) {
		return nil, nil, false, &sourceFetchError{class: "rss_http_status", err: errors.New("source returned an unrelated 304 response")}
	}
	updates := map[string]string{
		"rssETag":         responseHeaders.Get("ETag"),
		"rssLastModified": responseHeaders.Get("Last-Modified"),
		"rssURL":          address,
		"rssResponseURL":  finalAddress,
		"rssConfigHash":   configHash,
	}
	updates["detailRules"] = detailRuleHash(src.Config.object("detail"))
	updates["detailPending"] = src.Cursor.text("detailPending")
	if status == http.StatusNotModified {
		if updates["rssETag"] == "" {
			updates["rssETag"] = src.Cursor.text("rssETag")
		}
		if updates["rssLastModified"] == "" {
			updates["rssLastModified"] = src.Cursor.text("rssLastModified")
		}
		return nil, updates, true, nil
	}
	items, title, err := parseRSSWithSettings(body, finalAddress, src.Config, src.Mode)
	if err != nil {
		return nil, nil, false, &sourceFetchError{class: "rss_parse", err: err}
	}
	annotateOpaqueRSSIdentities(items, src.ID)
	updates["rssTitle"] = truncate(plain(title), 200)
	return items, updates, false, err
}

func (s *Service) fetchRSSResponse(ctx context.Context, raw string, headers settings) ([]byte, http.Header, int, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, 0, raw, errors.New("invalid source URL")
	}
	if err = validURL(u); err != nil {
		return nil, nil, 0, raw, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, 0, raw, err
	}
	req.Header.Set("User-Agent", "MyHOT/1.0 (+public source collector)")
	for name, value := range headers {
		if text, ok := value.(string); ok {
			req.Header.Set(name, text)
		}
	}
	client := s.Client
	if client == nil {
		client = publicClient()
	}
	res, err := client.Do(req)
	if err != nil {
		class := "rss_http_request"
		if errors.Is(err, modelconfig.ErrNonPublicAddress) {
			class = "rss_dns_non_public"
		}
		return nil, nil, 0, raw, &sourceFetchError{class: class, err: errors.New("source HTTP request failed")}
	}
	defer res.Body.Close()
	finalAddress := raw
	if res.Request != nil && res.Request.URL != nil {
		finalAddress = res.Request.URL.String()
	}
	if res.StatusCode == http.StatusNotModified {
		return nil, res.Header, res.StatusCode, finalAddress, nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, nil, res.StatusCode, finalAddress, fmt.Errorf("source HTTP status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024+1))
	if err != nil {
		return nil, nil, res.StatusCode, finalAddress, err
	}
	if len(data) > 8*1024*1024 {
		return nil, nil, res.StatusCode, finalAddress, errors.New("source response is too large")
	}
	return data, res.Header, res.StatusCode, finalAddress, nil
}

func (s *Service) fetch(ctx context.Context, src source) ([]item, error) {
	items, _, err := s.fetchWithReceipts(ctx, src)
	return items, err
}

/** fetchWithReceipts parses a source response and returns paid provider receipts to commit with it. */
func (s *Service) fetchWithReceipts(ctx context.Context, src source) ([]item, []int64, error) {
	cfg := src.Config
	address := cfg.text("url")
	if src.Kind == "rss" {
		address = cfg.text("feedUrl")
		if address == "" {
			address = cfg.text("url")
		}
	}
	method := http.MethodGet
	var body any
	if src.Kind == "json_list" {
		method = strings.ToUpper(cfg.text("method"))
		if method == "" {
			method = http.MethodGet
		}
		if method != http.MethodGet && method != http.MethodPost {
			return nil, nil, errUnsupported
		}
		body = cfg["bodyJson"]
	}
	if src.Kind == "web_list" {
		if cfg.text("adapter") == "mimo_home" {
			// Resolve the homepage's application chunks before parsing its client-rendered Blog records.
			listing := address
			var page []byte
			var receiptIDs []int64

			// Jina listings retain their paid receipt, while direct listings use the guarded source client.
			if targetURL, isJina := jinaTargetURL(address); isJina {
				if s.Jina == nil {
					return nil, nil, errors.New("Jina reader is not configured")
				}
				read, err := s.Jina.Read(ctx, targetURL, src.ID, "source_listing", "html", nil)
				if err != nil {
					return nil, nil, err
				}
				page = []byte(read.Content)
				receiptIDs = append(receiptIDs, read.ReceiptID)
				listing = targetURL
			} else {
				var err error
				page, listing, err = s.fetchURLWithFinalURL(ctx, listing, http.MethodGet, cfg.object("headers"), nil)
				if err != nil {
					return nil, nil, err
				}
			}
			base := cfg.text("baseUrl")
			if base == "" {
				base = listing
			}

			// Fetch only the scripts referenced by the homepage and keep every request under the public URL guard.
			items, err := parseMimoHome(page, base, cfg, func(scriptURL string) ([]byte, error) {
				return s.fetchURL(ctx, scriptURL, http.MethodGet, nil, nil)
			})
			return items, receiptIDs, err
		}
		if targetURL, isJina := jinaTargetURL(address); isJina {
			if s.Jina == nil {
				return nil, nil, errors.New("Jina reader is not configured")
			}
			format := "markdown"
			if cfg.text("parseMode") == "html" {
				format = "html"
			}
			var cacheTolerance *int
			if value, ok := cfg["cacheToleranceSeconds"]; ok {
				if parsed, valid := value.(float64); valid && parsed >= 0 && parsed == float64(int(parsed)) {
					tolerance := int(parsed)
					cacheTolerance = &tolerance
				}
				if parsed, valid := value.(int); valid && parsed >= 0 {
					cacheTolerance = &parsed
				}
			}
			page, err := s.Jina.Read(ctx, targetURL, src.ID, "source_listing", format, cacheTolerance)
			if err != nil {
				return nil, nil, err
			}
			parseConfig := make(settings, len(cfg)+1)
			for key, value := range cfg {
				parseConfig[key] = value
			}
			if parseConfig.text("parseMode") == "" {
				parseConfig["parseMode"] = format
			}
			if parseConfig.text("baseUrl") == "" {
				parseConfig["baseUrl"] = targetURL
			}
			items, err := parseWeb([]byte(page.Content), parseConfig, targetURL)
			return items, []int64{page.ReceiptID}, err
		}
	}
	data, responseURL, err := s.fetchURLWithFinalURL(ctx, address, method, cfg.object("headers"), body)
	if err != nil {
		return nil, nil, err
	}
	switch src.Kind {
	case "rss":
		items, _, err := parseRSSWithSettings(data, responseURL, cfg, src.Mode)
		annotateOpaqueRSSIdentities(items, src.ID)
		return items, nil, err
	case "web_list":
		items, err := parseWeb(data, cfg, responseURL)
		return items, nil, err
	case "json_list":
		items, err := parseJSON(data, cfg, responseURL)
		return items, nil, err
	}
	return nil, nil, errUnsupported
}
func filterItems(items []item, cfg settings, first bool, now time.Time) []item {
	return filterItemsWithMode(items, cfg, "", first, now)
}

/** filterItemsWithMode filters candidates while keeping up to 300 items on first isolated fetch to backfill overflow. */
func filterItemsWithMode(items []item, cfg settings, mode string, first bool, now time.Time) []item {
	after := parseDate(cfg.text("publishedAfter"), cfg)
	seen := map[string]bool{}
	filtered := []item{}
	for _, candidate := range items {
		candidate.Title = truncate(plain(candidate.Title), 1000)
		candidate.Excerpt = truncate(plain(candidate.Excerpt), 2000)
		candidate.Body = truncate(plain(candidate.Body), 64000)
		candidate.Author = truncate(plain(candidate.Author), 200)
		u, err := url.Parse(candidate.URL)
		opaqueGUID := candidate.OpaqueGUID != "" && err == nil && u.Scheme == "" && u.Host == ""
		if err != nil || candidate.Title == "" || !opaqueGUID && validURL(u) != nil {
			continue
		}
		if !opaqueGUID {
			candidate.URL = normalizeURL(u.String(), preserveItemFragment(cfg))
		}
		if seen[candidate.URL] || !urlAllowed(candidate.URL, cfg) {
			continue
		}
		if after != nil {
			if candidate.Published == nil || !candidate.Published.After(*after) || candidate.Published.After(now.Add(time.Hour)) {
				continue
			}
		}
		if !categoriesAllowed(candidate.Categories, cfg) {
			continue
		}
		if first && candidate.Published != nil {
			months := cfg.object("_aihot").integer("initialBackfillMonths", 12)
			cutoff := now.Add(-time.Duration(months) * 30 * 24 * time.Hour)
			if candidate.Published.Before(cutoff) {
				continue
			}
		}
		seen[candidate.URL] = true
		filtered = append(filtered, candidate)
	}
	if first || cfg.boolean("sortByPublishedAt") {
		sort.SliceStable(filtered, func(i, j int) bool {
			if filtered[i].Published == nil {
				return false
			}
			if filtered[j].Published == nil {
				return true
			}
			return filtered[i].Published.After(*filtered[j].Published)
		})
	}
	if first {
		limit := 300
		if mode != "isolated" {
			limit = cfg.object("_aihot").integer("initialBackfillLimit", 30)
			if limit > 300 {
				limit = 300
			}
		}
		if len(filtered) > limit {
			filtered = filtered[:limit]
		}
	}
	return filtered
}

/** preserveItemFragment keeps section URLs distinct for source formats that model page sections as posts. */
func preserveItemFragment(cfg settings) bool {
	return cfg.boolean("preserveUrlFragment") || cfg.text("parseMode") == "intercom_changelog" || cfg.text("parseMode") == "docusaurus_changelog"
}
