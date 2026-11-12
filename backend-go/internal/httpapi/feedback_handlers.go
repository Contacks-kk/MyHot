package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"aihot/backend-go/internal/adminauth"
	"aihot/backend-go/internal/feedback"
)

func (s *Server) adminFeedbackList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.Feedback == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	page := 1
	if value, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && value > 1 {
		page = value
	}
	result, err := s.Feedback.List(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("q"), page)
	if err != nil {
		writeFeedbackError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminFeedbackUpdate(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.Feedback == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	id, ok := feedbackID(w, r)
	if !ok {
		return
	}
	var input struct {
		Status  *string         `json:"status"`
		Note    json.RawMessage `json:"note"`
		Version string          `json:"version"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	patch := feedback.Patch{Status: input.Status, Version: input.Version}
	if len(input.Note) != 0 {
		patch.NoteSet = true
		if !bytes.Equal(bytes.TrimSpace(input.Note), []byte("null")) {
			var note string
			if err := json.Unmarshal(input.Note, &note); err != nil {
				writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "note must be a string or null")
				return
			}
			patch.Note = &note
		}
	}
	result, found, err := s.Feedback.Update(r.Context(), id, patch, adminActor(admin))
	if err != nil {
		writeFeedbackError(w, err)
		return
	}
	if !found {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminFeedbackErase(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.Feedback == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	id, ok := feedbackID(w, r)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	erased, err := s.Feedback.Erase(r.Context(), id, input.Reason, adminActor(admin))
	if err != nil {
		writeFeedbackError(w, err)
		return
	}
	if !erased {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"erased": true})
}

func (s *Server) adminFeedbackScreenshot(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.Feedback == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	id, ok := feedbackID(w, r)
	if !ok {
		return
	}
	file, err := s.Feedback.Screenshot(r.Context(), id)
	if err != nil {
		writeFeedbackError(w, err)
		return
	}
	if file == nil {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	w.Header().Set("Content-Type", file.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(file.Bytes)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.Bytes)
}

func (s *Server) adminFeedbackBan(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.Feedback == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		SourceHash string `json:"sourceHash"`
		Reason     string `json:"reason"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	if err := s.Feedback.Ban(r.Context(), input.SourceHash, input.Reason, adminActor(admin)); err != nil {
		writeFeedbackError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminFeedbackUnban(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.Feedback == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	if err := s.Feedback.Unban(r.Context(), r.PathValue("hash"), adminActor(admin)); err != nil {
		writeFeedbackError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func readAdminJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(destination); err != nil {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "The request could not be processed.")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "The request could not be processed.")
		return false
	}
	return true
}

func feedbackID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "id must be a positive integer")
		return 0, false
	}
	return id, true
}

func adminActor(admin *adminauth.AdminPrincipal) string {
	if admin.Dev {
		return "dev:" + admin.Name
	}
	return "admin:" + strconv.FormatInt(admin.UserID, 10)
}

func writeFeedbackError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, feedback.ErrConflict):
		writeAdminProblem(w, http.StatusConflict, "conflict", feedback.ErrConflict.Error())
	default:
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", fmt.Sprint(err))
	}
}
