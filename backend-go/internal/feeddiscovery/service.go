package feeddiscovery

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"aihot/backend-go/internal/modelconfig"
	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Candidate struct {
	Type        string `json:"type"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Recommended bool   `json:"recommended"`
}

type Result struct {
	Status      string      `json:"status"`
	OriginalURL string      `json:"originalUrl"`
	Candidates  []Candidate `json:"candidates"`
}

type Service struct {
	client HTTPDoer
}

/** NewService creates a discovery service with public-only dialer for SSRF defense. */
func NewService() *Service {
	client := &http.Client{
		Timeout: 6 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           modelconfig.DialPublic,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			MaxIdleConns:          10,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}

			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to non-http(s) scheme rejected")
			}

			return nil
		},
	}

	return &Service{
		client: client,
	}
}

/** NewServiceWithClient creates a discovery service with an injected HTTP doer for testing. */
func NewServiceWithClient(client HTTPDoer) *Service {
	return &Service{
		client: client,
	}
}

/** Discover probes a website URL to discover RSS/Atom/JSON feeds or Sitemap XMLs. */
func (s *Service) Discover(ctx context.Context, rawURL string) (*Result, error) {
	cleanURL := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(cleanURL, "http://") && !strings.HasPrefix(cleanURL, "https://") {
		cleanURL = "https://" + cleanURL
	}

	targetURL, err := url.Parse(cleanURL)
	if err != nil || (targetURL.Scheme != "http" && targetURL.Scheme != "https") || targetURL.Hostname() == "" || targetURL.User != nil {
		return nil, errors.New("无效的网页地址")
	}

	// Fetch target URL payload with bounded response body
	body, contentType, fetchErr := s.fetchURL(ctx, targetURL.String())
	if fetchErr != nil {
		return nil, fetchErr
	}

	candidates := make([]Candidate, 0)
	seen := make(map[string]struct{})

	addCandidate := func(candidate Candidate) {
		normalized := normalizeDiscoveryURL(candidate.URL)
		if _, exists := seen[normalized]; exists {
			return
		}

		seen[normalized] = struct{}{}
		candidates = append(candidates, candidate)
	}

	// Step 1: Check if the fetched content directly is an RSS or Atom feed
	if feed, parseErr := gofeed.NewParser().Parse(bytes.NewReader(body)); parseErr == nil {
		feedType := "rss"
		if strings.Contains(strings.ToLower(feed.FeedType), "atom") {
			feedType = "atom"
		} else if strings.Contains(strings.ToLower(feed.FeedType), "json") {
			feedType = "json"
		}

		title := strings.TrimSpace(feed.Title)
		if title == "" {
			title = targetURL.Hostname()
		}

		addCandidate(Candidate{
			Type:        feedType,
			Title:       title,
			URL:         targetURL.String(),
			Recommended: true,
		})

		return &Result{
			Status:      "found",
			OriginalURL: targetURL.String(),
			Candidates:  candidates,
		}, nil
	}

	// Step 2: Check if the fetched content directly is a Sitemap XML
	if isSitemap(body) {
		addCandidate(Candidate{
			Type:        "sitemap",
			Title:       targetURL.Hostname() + " (Sitemap)",
			URL:         targetURL.String(),
			Recommended: true,
		})

		return &Result{
			Status:      "found",
			OriginalURL: targetURL.String(),
			Candidates:  candidates,
		}, nil
	}

	// Step 3: Inspect HTML document for alternate feed link tags
	doc, docErr := goquery.NewDocumentFromReader(bytes.NewReader(body))
	pageTitle := targetURL.Hostname()
	if docErr == nil {
		if rawTitle := strings.TrimSpace(doc.Find("title").First().Text()); rawTitle != "" {
			pageTitle = rawTitle
		}

		doc.Find("link[rel~='alternate']").Each(func(_ int, sel *goquery.Selection) {
			typeAttr := strings.ToLower(strings.TrimSpace(sel.AttrOr("type", "")))
			hrefAttr := strings.TrimSpace(sel.AttrOr("href", ""))
			if hrefAttr == "" {
				return
			}

			feedType := ""
			switch typeAttr {
			case "application/rss+xml", "text/xml":
				feedType = "rss"
			case "application/atom+xml":
				feedType = "atom"
			case "application/feed+json", "application/json":
				feedType = "json"
			}

			if feedType != "" {
				resolved := resolveReference(targetURL.String(), hrefAttr)
				linkTitle := strings.TrimSpace(sel.AttrOr("title", ""))
				if linkTitle == "" {
					linkTitle = pageTitle
				}

				addCandidate(Candidate{
					Type:        feedType,
					Title:       linkTitle,
					URL:         resolved,
					Recommended: true,
				})
			}
		})
	}

	// Step 4: If no alternate link tags found, probe common feed paths
	if len(candidates) == 0 {
		commonPaths := s.generateCommonFeedPaths(targetURL)
		for _, probePath := range commonPaths {
			if probeBody, _, err := s.fetchURL(ctx, probePath); err == nil {
				if probeFeed, parseErr := gofeed.NewParser().Parse(bytes.NewReader(probeBody)); parseErr == nil {
					feedType := "rss"
					if strings.Contains(strings.ToLower(probeFeed.FeedType), "atom") {
						feedType = "atom"
					}

					feedTitle := strings.TrimSpace(probeFeed.Title)
					if feedTitle == "" {
						feedTitle = pageTitle
					}

					addCandidate(Candidate{
						Type:        feedType,
						Title:       feedTitle,
						URL:         probePath,
						Recommended: true,
					})

					if len(candidates) >= 2 {
						break
					}
				}
			}
		}
	}

	// Step 5: If still no feed found, attempt Sitemap XML detection
	if len(candidates) == 0 {
		sitemapCandidates := s.generateSitemapPaths(targetURL)
		for _, sitemapURL := range sitemapCandidates {
			if smBody, _, err := s.fetchURL(ctx, sitemapURL); err == nil {
				if isSitemap(smBody) {
					resolvedSitemap := sitemapURL
					// If it is a sitemapindex, inspect child post sitemaps
					if child := findPostSitemap(smBody, sitemapURL); child != "" {
						resolvedSitemap = child
					}

					addCandidate(Candidate{
						Type:        "sitemap",
						Title:       pageTitle + " (Sitemap)",
						URL:         resolvedSitemap,
						Recommended: false,
					})
					break
				}
			}
		}
	}

	_ = contentType
	status := "found"
	if len(candidates) == 0 {
		status = "not_found"
	}

	return &Result{
		Status:      status,
		OriginalURL: targetURL.String(),
		Candidates:  candidates,
	}, nil
}

/** fetchURL performs a bounded GET request to retrieve target content safely. */
func (s *Service) fetchURL(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 MyHOT/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/rss+xml,application/atom+xml,*/*;q=0.8")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, "", errors.New("目标网页返回异常状态码")
	}

	// Cap response reading at 512KB to prevent memory exhaustion
	limitReader := io.LimitReader(resp.Body, 512*1024)
	data, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, "", err
	}

	return data, resp.Header.Get("Content-Type"), nil
}

/** generateCommonFeedPaths generates candidate feed paths on the target host. */
func (s *Service) generateCommonFeedPaths(u *url.URL) []string {
	paths := []string{
		"/feed",
		"/rss",
		"/rss.xml",
		"/atom.xml",
		"/feed.xml",
		"/index.xml",
	}

	candidates := make([]string, 0, len(paths)+2)

	// If the original URL has a directory path (e.g. /blog), test sub-path first
	cleanPath := strings.TrimSuffix(u.Path, "/")
	if cleanPath != "" && cleanPath != "/" {
		candidates = append(candidates, (&url.URL{
			Scheme: u.Scheme,
			Host:   u.Host,
			Path:   cleanPath + "/feed",
		}).String())
		candidates = append(candidates, (&url.URL{
			Scheme: u.Scheme,
			Host:   u.Host,
			Path:   cleanPath + "/rss.xml",
		}).String())
	}

	for _, p := range paths {
		candidates = append(candidates, (&url.URL{
			Scheme: u.Scheme,
			Host:   u.Host,
			Path:   p,
		}).String())
	}

	return candidates
}

/** generateSitemapPaths generates candidate sitemap URLs. */
func (s *Service) generateSitemapPaths(u *url.URL) []string {
	return []string{
		(&url.URL{
			Scheme: u.Scheme,
			Host:   u.Host,
			Path:   "/sitemap.xml",
		}).String(),
		(&url.URL{
			Scheme: u.Scheme,
			Host:   u.Host,
			Path:   "/sitemap_index.xml",
		}).String(),
	}
}

/** isSitemap checks if the payload is a Sitemap XML document. */
func isSitemap(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	snippet := trimmed
	if len(snippet) > 1024 {
		snippet = snippet[:1024]
	}

	return bytes.Contains(snippet, []byte("<urlset")) || bytes.Contains(snippet, []byte("<sitemapindex"))
}

type sitemapXMLURL struct {
	Loc string `xml:"loc"`
}

type sitemapXMLIndex struct {
	XMLName  xml.Name        `xml:"sitemapindex"`
	Sitemaps []sitemapXMLURL `xml:"sitemap"`
}

/** findPostSitemap inspects a sitemapindex document to locate sub-sitemaps for blog/articles. */
func findPostSitemap(data []byte, base string) string {
	var index sitemapXMLIndex
	if err := xml.Unmarshal(data, &index); err != nil || len(index.Sitemaps) == 0 {
		return ""
	}

	for _, entry := range index.Sitemaps {
		loc := strings.ToLower(entry.Loc)
		if strings.Contains(loc, "post") || strings.Contains(loc, "article") || strings.Contains(loc, "blog") || strings.Contains(loc, "news") {
			return resolveReference(base, entry.Loc)
		}
	}

	// Fallback to first child sitemap
	return resolveReference(base, index.Sitemaps[0].Loc)
}

/** resolveReference resolves relative URLs against a base URL. */
func resolveReference(base, raw string) string {
	parsedRaw, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	if parsedRaw.IsAbs() {
		return parsedRaw.String()
	}

	parsedBase, err := url.Parse(base)
	if err != nil {
		return raw
	}

	return parsedBase.ResolveReference(parsedRaw).String()
}

/** normalizeDiscoveryURL normalizes URLs for deduplication. */
func normalizeDiscoveryURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.ToLower(raw)
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""

	if (u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) || (u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) {
		u.Host = u.Hostname()
	}

	return path.Clean(u.String())
}
