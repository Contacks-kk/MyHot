package collector

import (
	"bytes"
	"encoding/xml"
	"net/url"
	"path"
	"strings"
	"time"
)

type sitemapXMLURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

type sitemapXMLURLSet struct {
	XMLName xml.Name        `xml:"urlset"`
	URLs    []sitemapXMLURL `xml:"url"`
}

type sitemapXMLIndex struct {
	XMLName  xml.Name        `xml:"sitemapindex"`
	Sitemaps []sitemapXMLURL `xml:"sitemap"`
}

/** isSitemapXML checks if the raw payload is a Sitemap XML document. */
func isSitemapXML(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	snippet := trimmed
	if len(snippet) > 1024 {
		snippet = snippet[:1024]
	}

	return bytes.Contains(snippet, []byte("<urlset")) || bytes.Contains(snippet, []byte("<sitemapindex"))
}

/** parseSitemap parses a Sitemap XML document into normalized article items. */
func parseSitemap(data []byte, base string) ([]item, error) {
	// Try parsing as standard urlset
	var urlset sitemapXMLURLSet
	if err := xml.Unmarshal(data, &urlset); err == nil && len(urlset.URLs) > 0 {
		return convertSitemapURLs(urlset.URLs, base), nil
	}

	// Try parsing as sitemapindex
	var index sitemapXMLIndex
	if err := xml.Unmarshal(data, &index); err == nil && len(index.Sitemaps) > 0 {
		return convertSitemapURLs(index.Sitemaps, base), nil
	}

	return nil, errUnsupported
}

/** convertSitemapURLs maps sitemap URL entries into candidate items within the retention cutoff. */
func convertSitemapURLs(entries []sitemapXMLURL, base string) []item {
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -30)

	items := make([]item, 0, len(entries))

	// Iterate through sitemap URL entries
	for _, entry := range entries {
		rawLoc := strings.TrimSpace(entry.Loc)
		if rawLoc == "" {
			continue
		}

		resolved := resolveURL(base, rawLoc)

		var published *time.Time
		if trimmedDate := strings.TrimSpace(entry.LastMod); trimmedDate != "" {
			published = parseDate(trimmedDate, settings{})
		}

		// Filter out items older than 30 days if lastmod is provided
		if published != nil {
			if published.Before(cutoff) {
				continue
			}
		}

		title := slugToTitle(resolved)

		items = append(items, item{
			URL:        resolved,
			Title:      title,
			Published:  published,
			BodyStatus: "pending",
		})

		// Cap maximum items per sitemap fetch to protect server performance
		if len(items) >= 50 {
			break
		}
	}

	return items
}

/** slugToTitle generates an initial readable title from a URL path slug. */
func slugToTitle(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	cleanPath := strings.Trim(u.Path, "/")
	if cleanPath == "" {
		return u.Host
	}

	baseName := path.Base(cleanPath)

	// Remove common web file extensions
	for _, ext := range []string{".html", ".htm", ".shtml", ".php", ".jsp", ".asp"} {
		if strings.HasSuffix(strings.ToLower(baseName), ext) {
			baseName = baseName[:len(baseName)-len(ext)]
			break
		}
	}

	// Split words by common delimiters
	words := strings.FieldsFunc(baseName, func(r rune) bool {
		return r == '-' || r == '_'
	})

	if len(words) == 0 {
		return baseName
	}

	// Capitalize words for title display
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}

	return strings.Join(words, " ")
}
