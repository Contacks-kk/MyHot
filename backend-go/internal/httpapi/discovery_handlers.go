package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) llmsTxt(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.Discovery == nil {
		writeLLMSUnavailable(w)
		return
	}
	body, err := s.Discovery.LLMSTxt(r.Context(), time.Now().UTC())
	if err != nil {
		writeLLMSUnavailable(w)
		return
	}
	writeLLMSTxtResponse(w, r, body)
}

func (s *Server) llmsMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", "9")
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte("Not found"))
	}
}

func writeLLMSUnavailable(w http.ResponseWriter) {
	requestID := siteRequestID()
	setPrivateSiteHeaders(w)
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("Retry-After", "30")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "/problems/temporarily-unavailable", "title": "Temporarily unavailable", "status": http.StatusServiceUnavailable,
		"detail": "Temporarily unavailable.", "code": "temporarily_unavailable", "requestId": requestID, "retryAfter": 30,
	})
}

func writeLLMSTxtResponse(w http.ResponseWriter, r *http.Request, body string) {
	sum := sha256.Sum256([]byte(body))
	etag := `W/"llms-` + hex.EncodeToString(sum[:])[:16] + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Surrogate-Control", "no-store")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}
