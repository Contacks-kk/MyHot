package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"aihot/backend-go/internal/admincontent"
)

/** adminContentXEncodingPreview returns the current X repair plan to an authenticated administrator. */
func (s *Server) adminContentXEncodingPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	plan, err := s.AdminContent.PreviewXEncodingRepair(r.Context(), r.PathValue("id"))
	if err != nil {
		switch {
		case errors.Is(err, admincontent.ErrInvalidCommand):
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, admincontent.ErrXEncodingRepairUnavailable):
			writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		default:
			writeAdminProblem(w, http.StatusUnprocessableEntity, "invalid_material", err.Error())
		}
		return
	}
	if plan == nil {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

/** adminContentXEncodingNormalize commits the exact preview approved by the operator. */
func (s *Server) adminContentXEncodingNormalize(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input admincontent.XEncodingRepairInput
	if !readAdminJSON(w, r, &input) {
		return
	}
	result, found, err := s.AdminContent.NormalizeXEncoding(r.Context(), r.PathValue("id"), input, adminActor(admin))
	if !adminContentCommandResult(w, err, found) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

/** adminContentPublicationDatePreview returns the proof-bound plan for an administrator-reviewed date change. */
func (s *Server) adminContentPublicationDatePreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	publishedAt := r.URL.Query().Get("publishedAt")
	if publishedAt == "" {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "publishedAt is required")
		return
	}
	plan, err := s.AdminContent.PreviewPublicationDateCorrection(r.Context(), r.PathValue("id"), publishedAt)
	if err != nil {
		switch {
		case errors.Is(err, admincontent.ErrInvalidCommand), errors.Is(err, admincontent.ErrInvalidPublicationDateCorrection):
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, admincontent.ErrPublicationDateCorrectionUnavailable):
			writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		default:
			writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}
	if plan == nil {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

/** adminContentPublicationDateCorrect commits the date approved through the preview endpoint. */
func (s *Server) adminContentPublicationDateCorrect(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input admincontent.PublicationDateCorrectionInput
	if !readAdminJSON(w, r, &input) {
		return
	}
	result, found, err := s.AdminContent.CorrectPublicationDate(r.Context(), r.PathValue("id"), input, adminActor(admin))
	if err != nil {
		switch {
		case errors.Is(err, admincontent.ErrInvalidCommand), errors.Is(err, admincontent.ErrInvalidPublicationDateCorrection):
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, admincontent.ErrCommandConflict):
			writeAdminProblem(w, http.StatusConflict, "conflict", err.Error())
		case errors.Is(err, admincontent.ErrPublicationDateCorrectionUnavailable):
			writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		default:
			writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}
	if !found {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminContentVisibility(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		Visibility *string `json:"visibility"`
		Reason     string  `json:"reason"`
		Version    *int    `json:"version"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	if input.Visibility == nil || input.Version == nil {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "visibility and version are required")
		return
	}
	result, found, err := s.AdminContent.SetVisibility(r.Context(), r.PathValue("id"), admincontent.VisibilityInput{Visibility: *input.Visibility, Reason: input.Reason, Version: *input.Version}, adminActor(admin))
	if !adminContentCommandResult(w, err, found) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminContentOverride(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		Fields  json.RawMessage `json:"fields"`
		Clear   json.RawMessage `json:"clear"`
		Reason  string          `json:"reason"`
		Version *int            `json:"version"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	if input.Version == nil {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "version is required")
		return
	}
	fields := make(map[string]json.RawMessage)
	if len(input.Fields) > 0 && string(input.Fields) != "null" {
		if input.Fields[0] != '{' || json.Unmarshal(input.Fields, &fields) != nil {
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "fields: expected an object")
			return
		}
	}
	clear := []string(nil)
	if len(input.Clear) > 0 {
		if string(input.Clear) == "null" || json.Unmarshal(input.Clear, &clear) != nil || clear == nil {
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "clear: expected an array")
			return
		}
	}
	result, found, err := s.AdminContent.OverrideFields(r.Context(), r.PathValue("id"), admincontent.OverrideInput{Fields: fields, Clear: clear, Reason: input.Reason, Version: *input.Version}, adminActor(admin))
	if !adminContentCommandResult(w, err, found) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminContentRerun(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		Step string `json:"step"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	result, found, err := s.AdminContent.Rerun(r.Context(), r.PathValue("id"), input.Step, r.Header.Get("Idempotency-Key"), adminActor(admin))
	if !adminContentCommandResult(w, err, found) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminContentDetach(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	result, found, err := s.AdminContent.Detach(r.Context(), r.PathValue("id"), input.Reason, adminActor(admin))
	if !adminContentCommandResult(w, err, found) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminStoriesMerge(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		From   *int64 `json:"from"`
		Into   *int64 `json:"into"`
		Reason string `json:"reason"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	if input.From == nil || input.Into == nil {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "from and into are required")
		return
	}
	result, found, err := s.AdminContent.MergeStories(r.Context(), *input.From, *input.Into, input.Reason, adminActor(admin))
	if !adminContentCommandResult(w, err, found) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func adminContentCommandResult(w http.ResponseWriter, err error, found bool) bool {
	if err != nil {
		switch {
		case errors.Is(err, admincontent.ErrInvalidCommand):
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, admincontent.ErrCommandConflict):
			writeAdminProblem(w, http.StatusConflict, "conflict", err.Error())
		case errors.Is(err, admincontent.ErrCommandsUnavailable):
			writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		case errors.Is(err, admincontent.ErrXEncodingRepairUnavailable):
			writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		case errors.Is(err, admincontent.ErrPublicationDateCorrectionUnavailable):
			writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		default:
			writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return false
	}
	if !found {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return false
	}
	return true
}
