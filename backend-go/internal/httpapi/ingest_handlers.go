package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"aihot/backend-go/internal/ingest"
)

var ingestPlaceholder = regexp.MustCompile(`(?i)^(|changeme|change-me|placeholder|xxx+|todo|test|dev|your[-_]?token.*)$`)

type ingestErrorResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func (s *Server) ingestItems(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.authorizeIngest(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "Unauthorized")
		return
	}
	if s.Config.IngestRateLimit > 0 && s.limiter.limited("items:"+clientKey(r, s.Config.TrustProxy), s.Config.IngestRateLimit, time.Minute) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, ingestErrorResponse{OK: false, Error: "rate limited"})
		return
	}
	if s.Ingest == nil {
		writeJSON(w, http.StatusInternalServerError, ingestErrorResponse{OK: false, Error: "internal error"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAdminProblem(w, http.StatusRequestEntityTooLarge, "invalid_request", "The request could not be processed.")
			return
		}
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "The request could not be processed.")
		return
	}
	if !json.Valid(body) {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "The request could not be processed.")
		return
	}
	result, err := s.Ingest.Ingest(r.Context(), body)
	if err != nil {
		var requestError *ingest.RequestError
		if errors.As(err, &requestError) {
			writeJSON(w, requestError.Status, ingestErrorResponse{OK: false, Error: requestError.Message})
			return
		}
		if ingest.IsPaused(err) {
			writeJSON(w, http.StatusConflict, ingestErrorResponse{OK: false, Error: err.Error()})
			return
		}
		log.Printf("ingest items failed: %T", err)
		writeJSON(w, http.StatusInternalServerError, ingestErrorResponse{OK: false, Error: "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) authorizeIngest(r *http.Request) bool {
	expected := s.Config.IngestToken
	if ingestPlaceholder.MatchString(expected) || len(expected) < 16 {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	space := strings.IndexAny(header, " \t\r\n")
	if space < 0 || !strings.EqualFold(header[:space], "Bearer") {
		return false
	}
	provided := strings.TrimSpace(header[space:])
	if len(provided) == 0 || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
