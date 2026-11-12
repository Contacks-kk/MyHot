package publiccontent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/topics"
)

type sitemapReaderFixture struct {
	data  SitemapData
	err   error
	calls int
}

func (r *sitemapReaderFixture) ReadSitemap(context.Context, time.Time, []topics.Definition, int) (SitemapData, error) {
	r.calls++
	return r.data, r.err
}

func TestSitemapRenderKeepsEntryOrderAndXMLContract(t *testing.T) {
	selected := time.Date(2026, 10, 8, 1, 2, 3, 123456789, time.UTC)
	daily := time.Date(2026, 10, 8, 0, 30, 0, 0, time.UTC)
	changed := time.Date(2026, 10, 7, 23, 59, 0, 0, time.UTC)
	storyAt := time.Date(2026, 10, 7, 20, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 10, 7, 19, 0, 0, 0, time.UTC)
	service := NewSitemapService(nil, SitemapConfig{SiteURL: "https://myhot.example/"})
	body, err := service.render(SitemapData{
		LatestSelectedAt: &selected, LatestDailyAt: &daily,
		Reports: []SitemapReport{{Kind: "daily", Key: "2026-10-08", GeneratedAt: daily}},
		Topics:  []SitemapTopic{{Slug: "indexable", Pages: 2, Indexable: true, ChangedAt: &changed}, {Slug: "thin", Pages: 1}},
		Stories: []SitemapStory{{PublicID: "story-id", LatestAt: &storyAt}},
		Items:   []SitemapItem{{ID: "item-id", UpdatedAt: updated}},
	}, selected)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.HasPrefix(text, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n") || !strings.HasSuffix(text, "\n</urlset>\n") {
		t.Fatalf("sitemap document envelope changed: %q", text[:min(len(text), 140)])
	}
	for _, fragment := range []string{
		"<loc>https://myhot.example/</loc>",
		"<lastmod>2026-10-08T01:02:03.123Z</lastmod>",
		"<loc>https://myhot.example/daily/2026-10-08</loc>",
		"<changefreq>never</changefreq>",
		"<loc>https://myhot.example/topics/indexable/page/2</loc>",
		"<loc>https://myhot.example/story/story-id</loc>",
		"<loc>https://myhot.example/items/item-id</loc>",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("sitemap is missing %q", fragment)
		}
	}
	if strings.Contains(text, "/topics/thin") {
		t.Fatal("non-indexable topic was included")
	}
	if strings.Index(text, "/daily/2026-10-08") > strings.Index(text, "/topics/indexable") || strings.Index(text, "/topics/indexable") > strings.Index(text, "/story/story-id") || strings.Index(text, "/story/story-id") > strings.Index(text, "/items/item-id") {
		t.Fatal("sitemap entry groups changed order")
	}
	if got := sitemapDocument(body, selected.Add(sitemapLifetime)).ETag; !strings.HasPrefix(got, `W/"sitemap-`) {
		t.Fatalf("ETag=%q", got)
	}
}

func TestSitemapLoadCachesAndRestoresOnlyWithinOriginalLifetime(t *testing.T) {
	directory := t.TempDir()
	reader := &sitemapReaderFixture{data: SitemapData{}}
	service := NewSitemapService(reader, SitemapConfig{SiteURL: "https://myhot.example", DataDir: directory})
	first, err := service.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Load(context.Background())
	if err != nil || reader.calls != 1 || string(second.XML) != string(first.XML) || second.ExpiresAt != first.ExpiresAt {
		t.Fatalf("in-process cache changed: calls=%d second=%#v err=%v", reader.calls, second, err)
	}
	cachePath := filepath.Join(directory, "sitemap-last.xml")
	if saved, err := os.ReadFile(cachePath); err != nil || string(saved) != string(first.XML) {
		t.Fatalf("saved sitemap = %q, err=%v", saved, err)
	}

	restoring := NewSitemapService(&sitemapReaderFixture{err: errors.New("database unavailable")}, SitemapConfig{SiteURL: "https://myhot.example", DataDir: directory})
	restored, err := restoring.Load(context.Background())
	if err != nil || string(restored.XML) != string(first.XML) || restored.ExpiresAt.Sub(first.ExpiresAt) > time.Second || restored.ExpiresAt.Before(first.ExpiresAt.Add(-time.Second)) {
		t.Fatalf("fresh saved sitemap was not restored: expires=%s want=%s err=%v", restored.ExpiresAt, first.ExpiresAt, err)
	}

	old := time.Now().Add(-sitemapLifetime - time.Second)
	if err := os.Chtimes(cachePath, old, old); err != nil {
		t.Fatal(err)
	}
	expired := NewSitemapService(&sitemapReaderFixture{err: errors.New("database unavailable")}, SitemapConfig{SiteURL: "https://myhot.example", DataDir: directory})
	if _, err := expired.Load(context.Background()); err == nil {
		t.Fatal("expired sitemap was used as a fallback")
	}
}
