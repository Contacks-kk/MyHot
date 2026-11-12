package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Server) v1HotTopics(w http.ResponseWriter, r *http.Request) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if key, ok := firstRawQueryName(r.URL.RawQuery); ok {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "Unknown query parameter: "+key+".", 0)
		return
	}
	if s.PublicContent == nil {
		writeV1HotUnavailable(w)
		return
	}
	body, err := s.PublicContent.HotTopics(r.Context(), time.Now(), s.Config.SiteURL, s.Config.SiteName)
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		writeV1HotUnavailable(w)
		return
	}
	encoded := bytes.TrimSuffix(output.Bytes(), []byte("\n"))
	sum := sha256.Sum256(encoded)
	etag := `W/"v1-hot-` + hex.EncodeToString(sum[:])[:16] + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Vary", "Accept-Encoding")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func firstRawQueryName(query string) (string, bool) {
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		rawKey, _, _ := strings.Cut(part, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			key = rawKey
		}
		return key, true
	}
	return "", false
}

func setPublicV1Headers(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, If-None-Match, If-Modified-Since")
	w.Header().Set("Access-Control-Expose-Headers", "ETag, Last-Modified, Retry-After, X-Request-Id, Deprecation, Sunset, Link")
	w.Header().Set("Access-Control-Max-Age", "86400")
}

func v1HotTopicsPreflight(w http.ResponseWriter, _ *http.Request) {
	setPublicV1Headers(w)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusNoContent)
}

func v1HotTopicsMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	setPublicV1Headers(w)
	w.Header().Set("Allow", "GET, HEAD, OPTIONS")
	writeV1HotProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "This endpoint supports only GET, HEAD, and OPTIONS.", 0)
}

func writeV1HotUnavailable(w http.ResponseWriter) {
	writeV1HotProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable", "The service is temporarily unavailable; retry later.", 30)
}

func writeV1HotProblem(w http.ResponseWriter, status int, code, title, detail string, retryAfter int) {
	requestID := siteRequestID()
	body := map[string]any{
		"type": "/problems/" + strings.ReplaceAll(code, "_", "-"), "title": title, "status": status,
		"detail": detail, "code": code, "requestId": requestID,
	}
	if retryAfter > 0 {
		body["retryAfter"] = retryAfter
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", requestID)
	setPrivateSiteHeaders(w)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
