package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"
)

func (s *Server) rssFeed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "GET, HEAD, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	setPrivateSiteHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("Method not allowed"))
		}
		return
	}
	if s.Feeds == nil {
		writeFeedUnavailable(w, r)
		return
	}

	path := r.URL.Path
	var body string
	var err error
	switch path {
	case "/feed.xml":
		body, err = s.Feeds.ItemFeed(r.Context(), "selected", "", time.Now())
	case "/feed/full.xml":
		body, err = s.Feeds.ItemFeed(r.Context(), "selected-full", "", time.Now())
	case "/feed/all.xml":
		body, err = s.Feeds.ItemFeed(r.Context(), "all", "", time.Now())
	case "/feed/daily.xml":
		body, err = s.Feeds.ReportFeed(r.Context(), "daily", time.Now())
	case "/feed/weekly.xml":
		body, err = s.Feeds.ReportFeed(r.Context(), "weekly", time.Now())
	case "/feed/monthly.xml":
		body, err = s.Feeds.ReportFeed(r.Context(), "monthly", time.Now())
	default:
		kind, category, found := parseItemFeedPath(path)
		if !found || !s.Feeds.IsCategory(category) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte("Not found"))
			}
			return
		}
		body, err = s.Feeds.ItemFeed(r.Context(), kind, category, time.Now())
	}
	if err != nil {
		log.Printf("RSS feed generation failed: %T", err)
		writeFeedUnavailable(w, r)
		return
	}
	writeRSSWithETag(w, r, body)
}

func (s *Server) rssRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		s.rssFeed(w, r)
		return
	}
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	s.rssFeed(w, r)
}

func parseItemFeedPath(path string) (kind, category string, found bool) {
	full := false
	prefix := "/feed/category/"
	if strings.HasPrefix(path, "/feed/full/category/") {
		full = true
		prefix = "/feed/full/category/"
	} else if strings.HasPrefix(path, prefix) {
		full = false
	} else {
		return "", "", false
	}
	file := strings.TrimPrefix(path, prefix)
	if strings.Contains(file, "/") || !strings.HasSuffix(file, ".xml") {
		return "", "", false
	}
	category = strings.TrimSuffix(file, ".xml")
	if category == "" {
		return "", "", false
	}
	kind = "selected"
	if full {
		kind = "selected-full"
	}
	return kind, category, true
}

func writeRSSWithETag(w http.ResponseWriter, r *http.Request, body string) {
	sum := sha256.Sum256([]byte(body))
	etag := `W/"rss-` + hex.EncodeToString(sum[:])[:16] + `"`
	w.Header().Set("ETag", etag)
	setPrivateSiteHeaders(w)
	w.Header().Set("Vary", "Accept-Encoding")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}

func writeFeedUnavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	setPrivateSiteHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte("Feed temporarily unavailable"))
	}
}
