package articlebody

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"aihot/backend-go/internal/bodyhtml"
	"aihot/backend-go/internal/collector"
	"aihot/backend-go/internal/receipts"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

const maxPageBytes = 6 * 1024 * 1024

type repository interface {
	Pending(context.Context, int) ([]Candidate, error)
	Find(context.Context, string) (Candidate, error)
	SaveBody(context.Context, Candidate, *Extracted) (bool, error)
	RecordFailure(context.Context, string, int, string) (int, bool, error)
}

type PageFetcher interface {
	Fetch(context.Context, string) (finalURL, contentType string, body []byte, err error)
}

type PaidReader interface {
	Read(context.Context, string, string) (markdown string, receiptID int64, err error)
	Complete(context.Context, int64) error
}

type XArticleReader interface {
	ReadArticle(context.Context, string, string) (json.RawMessage, int64, error)
}

type Outcome struct {
	State      string
	RetryAfter time.Duration
}

type Service struct {
	repository repository
	fetcher    PageFetcher
	reader     PaidReader
	xReader    XArticleReader
}

func NewService(repository repository, fetcher PageFetcher, reader PaidReader, xReader ...XArticleReader) *Service {
	service := &Service{repository: repository, fetcher: fetcher, reader: reader}
	if len(xReader) > 0 {
		service.xReader = xReader[0]
	}
	return service
}

func (s *Service) Scan(ctx context.Context, limit int) ([]Candidate, error) {
	return s.repository.Pending(ctx, limit)
}

func (s *Service) Extract(ctx context.Context, articleID string) (Outcome, error) {
	candidate, err := s.repository.Find(ctx, articleID)
	if errors.Is(err, ErrNotFound) {
		return Outcome{State: "missing"}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if candidate.XPostPresent && s.xReader == nil {
		if !eligible(candidate) {
			return Outcome{State: "skipped"}, nil
		}
		return s.saveUnconfirmed(ctx, candidate)
	}
	if !eligible(candidate) {
		return Outcome{State: "skipped"}, nil
	}
	if candidate.XPostPresent {
		return s.extractXArticle(ctx, candidate)
	}

	var body *Extracted
	finalURL, contentType, raw, fetchErr := s.fetcher.Fetch(ctx, candidate.URL)
	if fetchErr == nil && strings.Contains(strings.ToLower(contentType), "html") && pageFetchable(finalURL, candidate.SourceKind) {
		body, _ = ParseHTML(string(raw), finalURL, sourceUTCOffset(candidate.SourceConfig))
	}
	var receiptID int64
	if body == nil {
		if s.reader == nil {
			if fetchErr != nil {
				return s.retry(ctx, candidate, "article page fetch failed")
			}
			return s.retry(ctx, candidate, "article page had no readable body and Jina is not configured")
		}
		markdown, id, readErr := s.reader.Read(ctx, candidate.URL, candidate.ID)
		if readErr != nil {
			var budget *receipts.BudgetExceededError
			if errors.As(readErr, &budget) {
				return s.saveUnconfirmed(ctx, candidate)
			}
			return s.retry(ctx, candidate, "article body fallback failed")
		}
		receiptID = id
		rendered, renderErr := markdownToBody(markdown, candidate.URL)
		if renderErr != nil {
			return Outcome{}, renderErr
		}
		body = rendered
	}

	if _, err := s.repository.SaveBody(ctx, candidate, body); err != nil {
		return Outcome{}, err
	}
	if receiptID != 0 {
		if err := s.reader.Complete(ctx, receiptID); err != nil {
			return Outcome{}, err
		}
	}
	if body == nil {
		return Outcome{State: "unconfirmed"}, nil
	}
	return Outcome{State: "ok"}, nil
}

func (s *Service) saveUnconfirmed(ctx context.Context, candidate Candidate) (Outcome, error) {
	if _, err := s.repository.SaveBody(ctx, candidate, nil); err != nil {
		return Outcome{}, err
	}
	return Outcome{State: "unconfirmed"}, nil
}

func (s *Service) retry(ctx context.Context, candidate Candidate, safeMessage string) (Outcome, error) {
	_, terminal, err := s.repository.RecordFailure(ctx, candidate.ID, candidate.Revision, safeMessage)
	if err != nil {
		return Outcome{}, err
	}
	if terminal {
		return Outcome{State: "unconfirmed"}, nil
	}
	return Outcome{State: "retrying", RetryAfter: 10 * time.Minute}, nil
}

func eligible(candidate Candidate) bool {
	if candidate.BodyStatus != "pending" || candidate.HasPublication && !candidate.AllowHotSignal {
		return false
	}
	if candidate.XPostPresent {
		if candidate.SourceKind != "x_search" || (candidate.ParticipationMode != "editorial" &&
			!(candidate.AllowHotSignal && candidate.ParticipationMode == "hot_signal" && !isHistorical(candidate))) {
			return false
		}
		var post struct {
			TweetID string `json:"tweetId"`
		}
		return json.Unmarshal(candidate.XPost, &post) == nil && strings.TrimSpace(post.TweetID) != ""
	}
	if candidate.ParticipationMode != "editorial" {
		return false
	}
	var config struct {
		FetchPublicContent bool            `json:"fetchPublicContent"`
		Detail             json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(candidate.SourceConfig, &config) != nil {
		return false
	}
	wantsBody := config.FetchPublicContent || len(config.Detail) > 0 && string(config.Detail) != "null" || candidate.SourceKind == "web_list"
	if !wantsBody && !candidate.Bare {
		return false
	}
	return pageFetchable(candidate.URL, candidate.SourceKind)
}

// ShouldExtract reports whether the candidate meets the same durable eligibility rules used by
// Scan and Extract. It lets the shared processing pipeline leave body work to this service before
// analysis is scheduled.
func ShouldExtract(candidate Candidate) bool { return eligible(candidate) }

func isHistorical(candidate Candidate) bool {
	return candidate.Backfill && (candidate.PublishedAt == nil || candidate.DiscoveredAt.Sub(*candidate.PublishedAt) > 48*time.Hour)
}

func (s *Service) extractXArticle(ctx context.Context, candidate Candidate) (Outcome, error) {
	if s.xReader == nil {
		return s.retry(ctx, candidate, "SocialData X Article reader is not configured")
	}
	var post struct {
		TweetID string `json:"tweetId"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(candidate.XPost, &post); err != nil || strings.TrimSpace(post.TweetID) == "" {
		return s.saveUnconfirmed(ctx, candidate)
	}
	rawArticle, _, err := s.xReader.ReadArticle(ctx, post.TweetID, candidate.ID)
	if err != nil {
		return s.retry(ctx, candidate, "X Article lookup failed")
	}
	article, title, text := parseXArticle(rawArticle)
	if text == "" {
		return s.saveUnconfirmed(ctx, candidate)
	}
	previous := xArticleBlock(candidate.XArticle)
	base := stringValue(candidate.BodyText)
	if previous != "" && strings.HasSuffix(base, previous) {
		base = strings.TrimSpace(strings.TrimSuffix(base, previous))
	}
	bodyText := strings.TrimSpace(strings.Join(nonEmpty(base, xArticleBlock(article)), "\n\n"))
	if title == "" || !onlyXArticleLink(post.Text) {
		title = candidate.Title
	}
	if _, err := s.repository.SaveBody(ctx, candidate, &Extracted{Text: bodyText, Title: title, XArticle: article}); err != nil {
		return Outcome{}, err
	}
	return Outcome{State: "ok"}, nil
}

type xArticle struct {
	Title        string `json:"title"`
	ContentState struct {
		Blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"blocks"`
	} `json:"content_state"`
}

var xArticleLink = regexp.MustCompile(`(?i)https?://(?:www\.)?(?:x|twitter)\.com/i/article/\d+`)

func parseXArticle(raw json.RawMessage) (json.RawMessage, string, string) {
	var article xArticle
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &article) != nil {
		return nil, "", ""
	}
	lines := make([]string, 0, len(article.ContentState.Blocks))
	for _, block := range article.ContentState.Blocks {
		text := strings.TrimSpace(block.Text)
		if text == "" || block.Type == "atomic" {
			continue
		}
		mark := map[string]string{
			"header-one": "# ", "header-two": "## ", "header-three": "### ",
			"blockquote": "> ", "unordered-list-item": "- ", "ordered-list-item": "1. ",
		}[block.Type]
		lines = append(lines, mark+text)
	}
	return raw, strings.TrimSpace(article.Title), strings.TrimSpace(strings.Join(lines, "\n\n"))
}

func xArticleBlock(raw json.RawMessage) string {
	articleRaw, _, text := parseXArticle(raw)
	if len(articleRaw) == 0 || text == "" {
		return ""
	}
	var article xArticle
	_ = json.Unmarshal(articleRaw, &article)
	if title := strings.TrimSpace(article.Title); title != "" {
		return "# " + title + "\n\n" + text
	}
	return text
}

func onlyXArticleLink(text string) bool {
	return xArticleLink.MatchString(text) && utf16Length(strings.TrimSpace(xArticleLink.ReplaceAllString(text, ""))) < 30
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func pageFetchable(rawURL, sourceKind string) bool {
	if sourceKind == "x_search" || sourceKind == "mp_account" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "x.com" || host == "twitter.com" || host == "mp.weixin.qq.com" {
		return false
	}
	return !isVideoPage(parsed, host)
}

// PageFetchable shares the supported article-page predicate with the editorial pipeline.
func PageFetchable(rawURL, sourceKind string) bool { return pageFetchable(rawURL, sourceKind) }

type pageFetcher struct{ client collector.HTTPDoer }

func NewPageFetcher(client collector.HTTPDoer) PageFetcher { return &pageFetcher{client: client} }

func (f *pageFetcher) Fetch(ctx context.Context, target string) (string, string, []byte, error) {
	if !pageFetchable(target, "") {
		return "", "", nil, errors.New("article page URL is not fetchable")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", "", nil, errors.New("article page request could not be created")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MyHOT article reader)")
	response, err := f.client.Do(req)
	if err != nil {
		return "", "", nil, errors.New("article page request failed")
	}
	defer response.Body.Close()
	finalURL := target
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	if response.StatusCode != http.StatusOK {
		return finalURL, response.Header.Get("Content-Type"), nil, fmt.Errorf("article page returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes+1))
	if err != nil {
		return finalURL, response.Header.Get("Content-Type"), nil, errors.New("article page response could not be read")
	}
	if len(body) > maxPageBytes {
		return finalURL, response.Header.Get("Content-Type"), nil, errors.New("article page exceeded the body size limit")
	}
	return finalURL, response.Header.Get("Content-Type"), body, nil
}

func markdownToBody(markdown, targetURL string) (*Extracted, error) {
	if strings.TrimSpace(markdown) == "" {
		return nil, nil
	}
	var converted bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.GFM)).Convert([]byte(markdown), &converted); err != nil {
		return nil, err
	}
	rendered, _, err := bodyhtml.Sanitize(converted.String(), targetURL, "", time.Now())
	if err != nil {
		return nil, err
	}
	rendered = trimTrailingChrome(rendered)
	text, err := visibleText(rendered)
	if err != nil {
		return nil, err
	}
	if utf16Length(text) < minBodyChars {
		return nil, nil
	}
	images, err := extractImages(rendered)
	if err != nil {
		return nil, err
	}
	return &Extracted{HTML: rendered, Text: text, Images: images}, nil
}

type sourceBodyConfig struct {
	PublishedAtUTCOffset string `json:"publishedAtUtcOffset"`
	Detail               struct {
		PublishedAtUTCOffset string `json:"publishedAtUtcOffset"`
	} `json:"detail"`
}

func sourceUTCOffset(raw json.RawMessage) string {
	var config sourceBodyConfig
	if json.Unmarshal(raw, &config) != nil {
		return "+08:00"
	}
	if config.Detail.PublishedAtUTCOffset != "" {
		return config.Detail.PublishedAtUTCOffset
	}
	if config.PublishedAtUTCOffset != "" {
		return config.PublishedAtUTCOffset
	}
	return "+08:00"
}

var (
	youtubeVideo = regexp.MustCompile(`^/(?:shorts|embed|v)/[^/]+/?$`)
	vimeoVideo   = regexp.MustCompile(`^/\d+(?:/[a-zA-Z0-9]+)?/?$`)
	playerVideo  = regexp.MustCompile(`^/video/\d+/?$`)
)

func isVideoPage(parsed *url.URL, host string) bool {
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		return strings.TrimSuffix(parsed.Path, "/") == "/watch" && parsed.Query().Get("v") != "" || youtubeVideo.MatchString(parsed.Path)
	case "youtu.be":
		return regexp.MustCompile(`^/[^/]+/?$`).MatchString(parsed.Path)
	case "vimeo.com", "www.vimeo.com":
		return vimeoVideo.MatchString(parsed.Path)
	case "player.vimeo.com":
		return playerVideo.MatchString(parsed.Path)
	default:
		return false
	}
}
