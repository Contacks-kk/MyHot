// Package collector collects the platform's public source pool without model calls.
package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	IdentityKey                       string
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
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, DialContext: modelconfig.DialPublic, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxIdleConns: 10},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("source redirect limit reached")
			}
			if err := validURL(req.URL); err != nil {
				return err
			}
			first := via[0]
			if (first.Method != http.MethodGet || first.Header.Get("Authorization") != "" || first.Header.Get("Cookie") != "" || first.Header.Get("X-API-Key") != "") && (!strings.EqualFold(first.URL.Host, req.URL.Host) || !strings.EqualFold(first.URL.Scheme, req.URL.Scheme)) {
				return errors.New("authenticated source redirect changed origin")
			}
			return nil
		}}
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
	return s.collectSource(ctx, id, false)
}

// CollectSourceNow bypasses the enabled and due-time gates for an administrator's
// explicit fetch. It still uses the same per-source advisory lock and run record.
func (s *Service) CollectSourceNow(ctx context.Context, id string) error {
	return s.collectSource(ctx, id, true)
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

// collectSource 抓取流程入口，设置11分钟上下文超时
func (s *Service) collectSource(ctx context.Context, id string, force bool) error {
	ctx, cancel := context.WithTimeout(ctx, 11*time.Minute)
	defer cancel()
	run, sourceFound, err := s.Repository.BeginSourceRun(ctx, id, force)
	if err != nil || !sourceFound {
		return err
	}
	defer s.Repository.ReleaseSource(run)
	if run.Reused {
		return nil
	}
	src := run.Source
	if src.Kind == "x_search" {
		return s.collectXSource(ctx, run)
	}
	var items []item
	var fetchErr error
	var cursorUpdates map[string]string
	var notModified bool
	failureStage := "source_fetch"
	failureClass := "source_fetch"
	if src.Kind == "rss" {
		items, cursorUpdates, notModified, fetchErr = s.fetchRSS(ctx, src)
		var classified *sourceFetchError
		if errors.As(fetchErr, &classified) {
			failureClass = classified.class
		}
	} else {
		items, fetchErr = s.fetch(ctx, src)
	}
	found := len(items)
	created := 0
	if fetchErr == nil && !notModified {
		fetchNow := time.Now().UTC()
		items = filterItemsWithMode(items, src.Config, src.Mode, src.First, fetchNow)
		detail := src.Config.object("detail")
		maxDetails := detail.integer("maxFetches", 3)
		if maxDetails > 3 {
			maxDetails = 3
		}
		if detail.text("summarySelector") == "" && detail.text("titleSelector") == "" && detail.text("publishedAtSelector") == "" {
			maxDetails = 0
		}

		// Allow at most 10 fresh 48h items through on an isolated source's first fetch; mark overflow as backfill.
		isolatedLiveCount := 0
		for _, candidate := range items {
			if maxDetails > 0 && candidate.Body == "" {
				maxDetails--
				detailCtx, stop := context.WithTimeout(ctx, 8*time.Second)
				if body, detailErr := s.fetchURL(detailCtx, candidate.URL, http.MethodGet, nil, nil); detailErr == nil {
					enrichDetail(&candidate, body, detail)
				}
				stop()
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
	finishCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if fetchErr != nil {
		// Do not store HTTP errors containing source credentials, query strings or response bodies.
		message := "source collection failed"
		if errors.Is(fetchErr, errUnsupported) {
			message = "source adapter is unsupported by Go collector; configure RSS, CSS/Markdown list or ordinary JSON"
		}
		if err := s.Repository.FinishFailure(finishCtx, run, found, created, message); err != nil {
			return err
		}
		log.Printf("source collection failed source_id=%q source_kind=%q stage=%s class=%s", src.ID, src.Kind, failureStage, failureClass)
		return fmt.Errorf("%w: %s", ErrFetchRecorded, message)
	}
	return s.Repository.FinishSuccess(finishCtx, run, found, created, cursorUpdates, notModified)
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

/** storeItemWithBackfill stores one collected item with explicit backfill classification. */
func (s *Service) storeItemWithBackfill(ctx context.Context, run *sourceRun, candidate item, explicitBackfill string, now time.Time) (bool, error) {
	status := candidate.BodyStatus
	if status == "" {
		status = "pending"
		if candidate.Body != "" {
			status = "ok"
		}
	}
	identity := candidate.IdentityKey
	if identity == "" {
		identity = "url:" + candidate.URL
		if match := tweetPattern.FindStringSubmatch(candidate.URL); len(match) > 1 {
			identity = "x:" + match[1]
		}
	}
	stored, err := s.Materials.Store(ctx, materials.Input{
		SourceID: run.Source.ID, URL: candidate.URL, IdentityKey: identity, Title: candidate.Title,
		Author: candidate.Author, PublishedAt: candidate.Published, DiscoveredAt: now, Backfill: explicitBackfill,
		Excerpt: candidate.Excerpt, BodyText: candidate.Body, BodyStatus: status, Via: "fetch",
		Language: candidate.Language, Media: candidate.Media, XPost: candidate.XPost, Raw: candidate.Raw,
	})
	return stored.Created, err
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
	items, err := s.fetch(ctx, source{ID: id, Kind: kind, Config: settings(config)})
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
func (s *Service) fetchURL(ctx context.Context, raw, method string, headers settings, body any) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid source URL")
	}
	if err = validURL(u); err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		encoded, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, err
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
		return nil, errors.New("source HTTP request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("source HTTP status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8*1024*1024 {
		return nil, errors.New("source response is too large")
	}
	return data, nil
}

func (s *Service) fetchRSS(ctx context.Context, src source) ([]item, map[string]string, bool, error) {
	address := src.Config.text("feedUrl")
	if address == "" {
		address = src.Config.text("url")
	}
	headers := settings{}
	if src.Cursor.text("rssURL") == address {
		if etag := src.Cursor.text("rssETag"); etag != "" {
			headers["If-None-Match"] = etag
		} else if modified := src.Cursor.text("rssLastModified"); modified != "" {
			headers["If-Modified-Since"] = modified
		}
	}
	body, responseHeaders, status, err := s.fetchRSSResponse(ctx, address, headers)
	if err != nil {
		return nil, nil, false, &sourceFetchError{class: rssResponseFailureClass(err), err: err}
	}
	updates := map[string]string{"rssETag": responseHeaders.Get("ETag"), "rssLastModified": responseHeaders.Get("Last-Modified"), "rssURL": address}
	if status == http.StatusNotModified {
		if updates["rssETag"] == "" {
			updates["rssETag"] = src.Cursor.text("rssETag")
		}
		if updates["rssLastModified"] == "" {
			updates["rssLastModified"] = src.Cursor.text("rssLastModified")
		}
		return nil, updates, true, nil
	}
	items, err := parseRSS(body, address)
	if err != nil {
		return nil, nil, false, &sourceFetchError{class: "rss_parse", err: err}
	}
	return items, updates, false, err
}

func (s *Service) fetchRSSResponse(ctx context.Context, raw string, headers settings) ([]byte, http.Header, int, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, 0, errors.New("invalid source URL")
	}
	if err = validURL(u); err != nil {
		return nil, nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, 0, err
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
		return nil, nil, 0, &sourceFetchError{class: class, err: errors.New("source HTTP request failed")}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified {
		return nil, res.Header, res.StatusCode, nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, nil, res.StatusCode, fmt.Errorf("source HTTP status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024+1))
	if err != nil {
		return nil, nil, res.StatusCode, err
	}
	if len(data) > 8*1024*1024 {
		return nil, nil, res.StatusCode, errors.New("source response is too large")
	}
	return data, res.Header, res.StatusCode, nil
}

func (s *Service) fetch(ctx context.Context, src source) ([]item, error) {
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
			return nil, errUnsupported
		}
		body = cfg["bodyJson"]
	}
	data, err := s.fetchURL(ctx, address, method, cfg.object("headers"), body)
	if err != nil {
		return nil, err
	}
	switch src.Kind {
	case "rss":
		return parseRSS(data, address)
	case "web_list":
		return parseWeb(data, cfg, address)
	case "json_list":
		return parseJSON(data, cfg, address)
	}
	return nil, errUnsupported
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
		if err != nil || validURL(u) != nil || candidate.Title == "" {
			continue
		}
		candidate.URL = normalizeURL(u.String(), cfg.boolean("preserveFragment"))
		if seen[candidate.URL] || !urlAllowed(candidate.URL, cfg) {
			continue
		}
		if candidate.Published != nil && after != nil && candidate.Published.Before(*after) {
			continue
		}
		if !categoriesAllowed(candidate.Categories, cfg) {
			continue
		}
		if first && candidate.Published != nil {
			months := cfg.object("_aihot").integer("initialBackfillMonths", 12)
			if candidate.Published.Before(now.AddDate(0, -months, 0)) {
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
	limit := 300
	if first && mode != "isolated" {
		limit = cfg.object("_aihot").integer("initialBackfillLimit", 30)
		if limit > 300 {
			limit = 300
		}
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
}
