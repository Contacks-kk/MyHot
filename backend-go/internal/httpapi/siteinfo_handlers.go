package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"aihot/backend-go/internal/account"
	"aihot/backend-go/internal/publiccontent"
)

func (s *Server) siteMeta(w http.ResponseWriter, r *http.Request) {
	s.writeSiteInfo(w, r, "meta")
}

func (s *Server) siteChangelog(w http.ResponseWriter, r *http.Request) {
	s.writeSiteInfo(w, r, "changelog")
}

func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.Sitemap == nil {
		writeSitemapUnavailable(w)
		return
	}
	document, err := s.Sitemap.Load(r.Context())
	if err != nil {
		writeSitemapUnavailable(w)
		return
	}
	writeSitemapDocument(w, r, document)
}

func writeSitemapDocument(w http.ResponseWriter, r *http.Request, document publiccontent.SitemapDocument) {
	setPrivateSiteHeaders(w)
	w.Header().Set("ETag", document.ETag)
	w.Header().Set("Vary", "Accept-Encoding")
	if matchesETag(r.Header.Get("If-None-Match"), document.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(document.XML)
	}
}

func writeSitemapUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "300")
	setPrivateSiteHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("Sitemap temporarily unavailable"))
}

func (s *Server) writeSiteInfo(w http.ResponseWriter, r *http.Request, kind string) {
	if kind == "changelog" {
		setPrivateSiteHeaders(w)
		if !s.requireSiteUser(w, r) {
			return
		}
	}
	s.writeSiteInfoResponse(w, r, kind)
}

func (s *Server) writeSiteInfoResponse(w http.ResponseWriter, r *http.Request, kind string) {
	if s.SiteInfo == nil {
		if kind == "changelog" {
			writePrivateSiteUnavailable(w)
		} else {
			writeSiteUnavailable(w)
		}
		return
	}
	var body []byte
	var etag string
	var err error
	if kind == "meta" {
		body, etag, err = s.SiteInfo.Meta()
	} else {
		body, etag, err = s.SiteInfo.Changelog()
	}
	if err != nil {
		if kind == "changelog" {
			writePrivateSiteUnavailable(w)
		} else {
			writeSiteUnavailable(w)
		}
		return
	}
	cacheControl := "public, max-age=60, s-maxage=60"
	if kind == "changelog" {
		cacheControl = "private, no-store"
		w.Header().Set("Surrogate-Control", "no-store")
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Vary", "Accept-Encoding")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeSiteUnavailable(w http.ResponseWriter) {
	writeSiteFailure(w, "no-store", false)
}

func writePrivateSiteUnavailable(w http.ResponseWriter) {
	writeSiteFailure(w, "private, no-store", true)
}

func writePrivateSiteNotFound(w http.ResponseWriter, detail string) {
	requestID := siteRequestID()
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", requestID)
	setPrivateSiteHeaders(w)
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "/problems/not-found", "title": "Not found", "status": http.StatusNotFound,
		"detail": detail, "code": "not_found", "requestId": requestID,
	})
}

func writeSiteFailure(w http.ResponseWriter, cacheControl string, surrogate bool) {
	requestID := siteRequestID()
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("Cache-Control", cacheControl)
	if surrogate {
		w.Header().Set("Surrogate-Control", "no-store")
	}
	w.Header().Set("Retry-After", "10")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "/problems/temporarily-unavailable", "title": "Temporarily unavailable", "status": http.StatusServiceUnavailable,
		"detail": "temporarily unavailable", "code": "temporarily_unavailable", "requestId": requestID, "retryAfter": 10,
	})
}

func (s *Server) requireSiteUser(w http.ResponseWriter, r *http.Request) bool {
	if _, err := r.Cookie(sessionCookie); err != nil {
		writeSiteUnauthorized(w)
		return false
	}
	if s.Accounts == nil {
		writePrivateSiteUnavailable(w)
		return false
	}
	if _, err := s.userFromRequest(r); err == nil {
		return true
	} else if errors.Is(err, errNoSession) || errors.Is(err, account.ErrNoSession) {
		writeSiteUnauthorized(w)
	} else {
		writePrivateSiteUnavailable(w)
	}
	return false
}

func writeSiteUnauthorized(w http.ResponseWriter) {
	requestID := siteRequestID()
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", requestID)
	setPrivateSiteHeaders(w)
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "/problems/unauthorized", "title": "Error", "status": http.StatusUnauthorized,
		"detail": "Sign in to view content.", "code": "unauthorized", "requestId": requestID,
	})
}

func setPrivateSiteHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Surrogate-Control", "no-store")
}

func siteRequestID() string {
	requestID := "00000000-0000-4000-8000-000000000000"
	var id [16]byte
	if _, err := rand.Read(id[:]); err == nil {
		id[6] = id[6]&0x0f | 0x40
		id[8] = id[8]&0x3f | 0x80
		encoded := hex.EncodeToString(id[:])
		requestID = encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
	}
	return requestID
}

func matchesETag(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		candidate = strings.TrimPrefix(candidate, "W/")
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(strings.TrimPrefix(etag, "W/"))) == 1 {
			return true
		}
	}
	return false
}
