package sourceicons

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"aihot/backend-go/internal/collector"
	"aihot/backend-go/internal/model"
	xhtml "golang.org/x/net/html"
)

const (
	BatchSize   = 200
	RetryDays   = 30
	MPRetryDays = 3
	MPPause     = 3 * time.Second
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Repository interface {
	RefreshXAvatars(context.Context) (int, error)
	Missing(context.Context, time.Time, int) ([]Source, error)
	Save(context.Context, string, *string, time.Time) error
}

type Sleeper func(context.Context, time.Duration) error

type Source struct {
	ID     string
	Kind   string
	Config json.RawMessage
	URLs   model.TextArray `gorm:"type:text[]"`
}

type Result struct {
	XAvatars int `json:"xAvatars"`
	Checked  int `json:"checked"`
	Found    int `json:"found"`
}

type Service struct {
	repository Repository
	client     HTTPDoer
	userAgent  string
	sleep      Sleeper
	now        func() time.Time
}

func NewService(repository Repository, client HTTPDoer, userAgent string) *Service {
	if client == nil {
		client = collector.NewHTTPClient()
	}
	return &Service{repository: repository, client: client, userAgent: userAgent, sleep: sleepContext, now: time.Now}
}

func (s *Service) SetSleeper(sleeper Sleeper) {
	if sleeper != nil {
		s.sleep = sleeper
	}
}

func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) Run(ctx context.Context) (Result, error) {
	x, err := s.repository.RefreshXAvatars(ctx)
	if err != nil {
		return Result{}, err
	}
	due, err := s.repository.Missing(ctx, s.now().UTC(), BatchSize)
	if err != nil {
		return Result{}, err
	}
	result := Result{XAvatars: x, Checked: len(due)}
	checkedAt := s.now().UTC()
	for _, source := range due {
		var icon *string
		if value := s.findIcon(ctx, source); value != "" {
			icon = &value
			result.Found++
		}
		if err := s.repository.Save(ctx, source.ID, icon, checkedAt); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Service) findIcon(ctx context.Context, source Source) string {
	var config map[string]any
	if json.Unmarshal(source.Config, &config) != nil || config == nil {
		return ""
	}
	if source.Kind == "mp_account" {
		for index, article := range source.URLs {
			if index > 0 {
				if err := s.sleep(ctx, MPPause); err != nil {
					return ""
				}
			}
			body, finalURL, ok := s.page(ctx, article, 10_000_000)
			if !ok {
				continue
			}
			if avatar := mpAvatar(body); avatar != "" {
				if candidate, err := resolveURL(avatar, finalURL); err == nil {
					candidate = strings.Replace(candidate, "http://", "https://", 1)
					if s.usableImage(ctx, candidate) {
						return candidate
					}
				}
			}
		}
		return ""
	}
	if ownerIcon := githubReleaseOwnerIcon(config); ownerIcon != "" && s.usableImage(ctx, ownerIcon) {
		return ownerIcon
	}
	home := homeOf(source.URLs, config)
	if home == "" {
		return ""
	}
	body, finalURL, ok := s.page(ctx, home, 4_000_000)
	var candidates []string
	if ok {
		candidates = iconCandidates(body, finalURL)
	} else {
		candidates = []string{strings.TrimRight(home, "/") + "/favicon.ico"}
	}
	for _, candidate := range candidates[:min(len(candidates), 5)] {
		if s.usableImage(ctx, candidate) {
			return candidate
		}
	}
	return ""
}

/** githubReleaseOwnerIcon uses the project owner's avatar to distinguish GitHub release feeds. */
func githubReleaseOwnerIcon(config map[string]any) string {
	raw, _ := config["feedUrl"].(string)
	if raw == "" {
		raw, _ = config["url"].(string)
	}
	u, err := publicURL(raw)
	if err != nil || !strings.EqualFold(u.Host, "github.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || parts[2] != "releases.atom" || parts[0] == "" || strings.Trim(parts[0], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" {
		return ""
	}
	return "https://github.com/" + parts[0] + ".png"
}

func (s *Service) page(ctx context.Context, raw string, maxBytes int64) ([]byte, string, bool) {
	requestURL, err := publicURL(raw)
	if err != nil {
		return nil, "", false
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, "", false
	}
	request.Header.Set("User-Agent", s.userAgent)
	request.Header.Set("Accept", "text/html,*/*;q=0.8")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, "", false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Request == nil || response.Request.URL == nil {
		return nil, "", false
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, "", false
	}
	return data, response.Request.URL.String(), true
}

func (s *Service) usableImage(ctx context.Context, raw string) bool {
	requestURL, err := publicURL(raw)
	if err != nil {
		return false
	}
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return false
	}
	request.Header.Set("User-Agent", s.userAgent)
	request.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")
	response, err := s.client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 15*1024*1024+1))
	if err != nil || len(data) == 0 || len(data) > 15*1024*1024 {
		return false
	}
	return imageBytes(response.Header.Get("Content-Type"), data)
}

func publicURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("source URL must be public HTTP or HTTPS without credentials")
	}
	return u, nil
}

func resolveURL(raw, base string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	value, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	resolved := baseURL.ResolveReference(value)
	if _, err := publicURL(resolved.String()); err != nil {
		return "", err
	}
	return resolved.String(), nil
}

func homeOf(articleURLs []string, config map[string]any) string {
	counts := make(map[string]int)
	order := make([]string, 0, len(articleURLs))
	valid := 0
	for _, raw := range articleURLs {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			continue
		}
		origin := urlOrigin(u)
		order = append(order, origin)
		counts[origin]++
		valid++
	}
	if valid > 0 {
		best := order[0]
		for _, origin := range order[1:] {
			if counts[origin] > counts[best] {
				best = origin
			}
		}
		if float64(counts[best]) >= max(1, float64(valid)*0.7) {
			return best
		}
	}
	configured, _ := config["url"].(string)
	if configured == "" {
		configured, _ = config["feedUrl"].(string)
	}
	configured = strings.TrimPrefix(configured, "http://r.jina.ai/")
	configured = strings.TrimPrefix(configured, "https://r.jina.ai/")
	u, err := url.Parse(configured)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return urlOrigin(u)
}

func urlOrigin(input *url.URL) string {
	u := *input
	u.User = nil
	u.Path, u.RawPath, u.RawQuery, u.Fragment, u.RawFragment = "", "", "", "", ""
	u.ForceQuery = false
	u.Scheme = strings.ToLower(u.Scheme)
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		host := u.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		u.Host = host
	}
	return u.Scheme + "://" + u.Host
}

func iconCandidates(data []byte, base string) []string {
	type candidate struct {
		url   string
		score int
	}
	var found []candidate
	tokenizer := xhtml.NewTokenizer(bytes.NewReader(data))
	for {
		tokenType := tokenizer.Next()
		if tokenType == xhtml.ErrorToken {
			break
		}
		if tokenType != xhtml.StartTagToken && tokenType != xhtml.SelfClosingTagToken {
			continue
		}
		tag, hasAttr := tokenizer.TagName()
		if !bytes.EqualFold(tag, []byte("link")) {
			continue
		}
		attrs := make(map[string]string)
		for hasAttr {
			key, value, more := tokenizer.TagAttr()
			attrs[strings.ToLower(string(key))] = string(value)
			hasAttr = more
		}
		rel, href := strings.ToLower(attrs["rel"]), attrs["href"]
		if !strings.Contains(rel, "icon") || href == "" || strings.HasPrefix(strings.ToLower(href), "data:") {
			continue
		}
		resolved, err := resolveURL(href, base)
		if err != nil {
			continue
		}
		size := 0
		for _, part := range strings.Fields(attrs["sizes"]) {
			pieces := strings.Split(strings.ToLower(part), "x")
			if len(pieces) != 2 {
				continue
			}
			var width int
			if _, err := fmt.Sscanf(pieces[0], "%d", &width); err == nil {
				size = max(size, width)
			}
		}
		if size > 512 {
			size = 512
		}
		score := size
		if strings.Contains(rel, "apple-touch-icon") {
			score += 1000
		}
		if strings.HasSuffix(strings.ToLower(strings.SplitN(href, "?", 2)[0]), ".svg") {
			score += 200
		}
		if strings.HasSuffix(strings.ToLower(strings.SplitN(href, "?", 2)[0]), ".ico") {
			score -= 300
		}
		found = append(found, candidate{url: resolved, score: score})
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].score > found[j].score })
	seen := make(map[string]bool)
	result := make([]string, 0, len(found)+1)
	for _, entry := range found {
		if !seen[entry.url] {
			seen[entry.url] = true
			result = append(result, entry.url)
		}
	}
	if fallback, err := resolveURL("/favicon.ico", base); err == nil && !seen[fallback] {
		result = append(result, fallback)
	}
	return result
}

var mpAvatarPattern = regexp.MustCompile(`(?is)round_head_img\s*[:=]\s*["']([^"']+)["']`)

func mpAvatar(data []byte) string {
	match := mpAvatarPattern.FindSubmatch(data)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(string(match[1]))
}

func imageBytes(contentType string, data []byte) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if strings.HasPrefix(contentType, "image/svg+xml") {
		decoder := xml.NewDecoder(strings.NewReader(string(data)))
		for {
			token, err := decoder.Token()
			if err != nil {
				return false
			}
			if start, ok := token.(xml.StartElement); ok {
				return strings.EqualFold(start.Name.Local, "svg")
			}
		}
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		return true
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return contentType == "image/webp" || contentType == "application/octet-stream"
	}
	if len(data) >= 6 && data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] == 0 {
		entries := int(data[4]) | int(data[5])<<8
		return entries > 0 && len(data) >= 6+entries*16
	}
	return false
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
