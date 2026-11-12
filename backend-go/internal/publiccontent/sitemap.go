package publiccontent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aihot/backend-go/internal/topics"
)

const SitemapMaxURLs = 45_000
const sitemapLifetime = 5 * time.Minute

type SitemapReport struct {
	Kind        string
	Key         string
	GeneratedAt time.Time
}

type SitemapTopic struct {
	Slug      string
	Pages     int
	Indexable bool
	ChangedAt *time.Time
}

type SitemapStory struct {
	PublicID string
	LatestAt *time.Time
}

type SitemapItem struct {
	ID        string
	UpdatedAt time.Time
}

type SitemapData struct {
	LatestSelectedAt *time.Time
	LatestDailyAt    *time.Time
	Reports          []SitemapReport
	Topics           []SitemapTopic
	Stories          []SitemapStory
	Items            []SitemapItem
}

type SitemapReader interface {
	ReadSitemap(context.Context, time.Time, []topics.Definition, int) (SitemapData, error)
}

type SitemapConfig struct {
	SiteURL string
	DataDir string
	Topics  []topics.Definition
}

type SitemapDocument struct {
	XML       []byte
	ETag      string
	ExpiresAt time.Time
}

type sitemapEntry struct {
	loc         string
	lastmod     *time.Time
	changefreq  string
	priority    float64
	hasPriority bool
}

type SitemapService struct {
	reader SitemapReader
	cfg    SitemapConfig
	mu     sync.Mutex
	last   *SitemapDocument
}

func NewSitemapService(reader SitemapReader, cfg SitemapConfig) *SitemapService {
	return &SitemapService{reader: reader, cfg: cfg}
}

func (s *SitemapService) Load(ctx context.Context) (SitemapDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.last != nil && s.last.ExpiresAt.After(now) {
		return *s.last, nil
	}
	if s.reader == nil {
		return s.restoreOrError(now, fmt.Errorf("sitemap reader is unavailable"))
	}
	data, err := s.reader.ReadSitemap(ctx, now, s.cfg.Topics, SitemapMaxURLs)
	if err != nil {
		return s.restoreOrError(now, err)
	}
	body, err := s.render(data, now)
	if err != nil {
		return s.restoreOrError(now, err)
	}
	document := sitemapDocument(body, now.Add(sitemapLifetime))
	s.last = &document
	s.save(document)
	return document, nil
}

func (s *SitemapService) restoreOrError(now time.Time, cause error) (SitemapDocument, error) {
	if s.last != nil && s.last.ExpiresAt.After(now) {
		return *s.last, nil
	}
	path := filepath.Join(s.cfg.DataDir, "sitemap-last.xml")
	info, err := os.Stat(path)
	if err == nil {
		expiresAt := info.ModTime().Add(sitemapLifetime)
		if expiresAt.After(now) {
			body, readErr := os.ReadFile(path)
			if readErr == nil {
				document := sitemapDocument(body, expiresAt)
				s.last = &document
				return document, nil
			}
		}
	}
	return SitemapDocument{}, cause
}

func (s *SitemapService) save(document SitemapDocument) {
	path := filepath.Join(s.cfg.DataDir, "sitemap-last.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if err := os.WriteFile(path, document.XML, 0o600); err != nil {
		return
	}
	createdAt := document.ExpiresAt.Add(-sitemapLifetime)
	_ = os.Chtimes(path, createdAt, createdAt)
}

func (s *SitemapService) render(data SitemapData, now time.Time) ([]byte, error) {
	entries := make([]sitemapEntry, 0, 128)
	latest := data.LatestSelectedAt
	if latest == nil {
		value := now
		latest = &value
	}
	add := func(path string, lastmod *time.Time, frequency string, priority float64) {
		entries = append(entries, sitemapEntry{loc: path, lastmod: lastmod, changefreq: frequency, priority: priority, hasPriority: true})
	}
	add("/", latest, "hourly", 1)
	add("/all", latest, "hourly", 0.9)
	add("/daily", data.LatestDailyAt, "daily", 0.9)
	add("/hot", latest, "hourly", 0.9)
	add("/daily/archive", data.LatestDailyAt, "daily", 0.7)
	add("/weekly", nil, "weekly", 0.7)
	add("/monthly", nil, "monthly", 0.6)
	add("/topics", nil, "daily", 0.7)
	add("/agent", latest, "weekly", 0.7)
	add("/about", nil, "monthly", 0.5)
	add("/terms", nil, "monthly", 0.4)
	add("/privacy", nil, "monthly", 0.4)
	add("/changelog", latest, "weekly", 0.5)
	for _, report := range data.Reports {
		frequency := "monthly"
		if report.Kind == "daily" {
			frequency = "never"
		}
		add("/"+report.Kind+"/"+report.Key, &report.GeneratedAt, frequency, 0.6)
	}
	for _, topic := range data.Topics {
		if !topic.Indexable {
			continue
		}
		add("/topics/"+topic.Slug, topic.ChangedAt, "daily", 0.6)
		for page := 2; page <= topic.Pages; page++ {
			add(fmt.Sprintf("/topics/%s/page/%d", topic.Slug, page), topic.ChangedAt, "weekly", 0.3)
		}
	}
	for _, story := range data.Stories {
		add("/story/"+story.PublicID, story.LatestAt, "daily", 0.5)
	}
	for _, item := range data.Items {
		add("/items/"+item.ID, &item.UpdatedAt, "monthly", 0.5)
	}
	if len(entries) > SitemapMaxURLs {
		entries = entries[:SitemapMaxURLs]
	}
	var output strings.Builder
	output.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")
	for index, entry := range entries {
		location := strings.TrimRight(s.cfg.SiteURL, "/") + entry.loc
		output.WriteString("<url>\n<loc>")
		output.WriteString(escapeSitemapXML(location))
		output.WriteString("</loc>\n")
		if entry.lastmod != nil {
			output.WriteString("<lastmod>")
			output.WriteString(entry.lastmod.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"))
			output.WriteString("</lastmod>\n")
		}
		if entry.changefreq != "" {
			output.WriteString("<changefreq>")
			output.WriteString(entry.changefreq)
			output.WriteString("</changefreq>\n")
		}
		if entry.hasPriority {
			output.WriteString("<priority>")
			output.WriteString(strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", entry.priority), "0"), "."))
			output.WriteString("</priority>\n")
		}
		output.WriteString("</url>")
		if index < len(entries)-1 {
			output.WriteByte('\n')
		}
	}
	output.WriteString("\n</urlset>\n")
	return []byte(output.String()), nil
}

func escapeSitemapXML(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(value)
}

func sitemapDocument(body []byte, expiresAt time.Time) SitemapDocument {
	sum := sha256.Sum256(body)
	return SitemapDocument{XML: append([]byte(nil), body...), ETag: `W/"sitemap-` + hex.EncodeToString(sum[:])[:16] + `"`, ExpiresAt: expiresAt}
}

func encodeSitemapTopics(definitions []topics.Definition) (string, error) {
	type sitemapTopicConfig struct {
		Slug    string   `json:"slug"`
		Tags    []string `json:"tags"`
		Pattern *string  `json:"pattern"`
	}
	configuration := make([]sitemapTopicConfig, 0, len(definitions))
	for _, definition := range definitions {
		tags := definition.Tags
		if tags == nil {
			tags = []string{}
		}
		configuration = append(configuration, sitemapTopicConfig{Slug: definition.Slug, Tags: tags, Pattern: definition.Pattern})
	}
	encoded, err := json.Marshal(configuration)
	return string(encoded), err
}

func remainingSitemapItems(data SitemapData, maxURLs int) int {
	entries := 13 + len(data.Reports) + len(data.Stories)
	for _, topic := range data.Topics {
		if topic.Indexable {
			entries += max(1, topic.Pages)
		}
	}
	return max(0, min(maxURLs-entries, SitemapMaxURLs))
}

func sitemapPageCount(total int) int {
	return max(1, (total+19)/20)
}
