package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"aihot/backend-go/internal/adminops"
	"aihot/backend-go/internal/receiptrecovery"
)

func (s *Server) adminNavCounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminOps == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	counts, err := s.AdminOps.NavCounts(r.Context())
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

func (s *Server) adminReceiptRelease(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.ReceiptRecovery == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "Invalid receipt ID.")
		return
	}
	var input struct {
		Billed bool   `json:"billed"`
		Note   string `json:"note"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	result, found, err := s.ReceiptRecovery.ReleaseManual(r.Context(), id, input.Billed, input.Note, adminActor(admin))
	if err != nil {
		switch {
		case errors.Is(err, receiptrecovery.ErrInvalidRequest):
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, receiptrecovery.ErrNotUnknown):
			writeAdminProblem(w, http.StatusConflict, "conflict", err.Error())
		default:
			writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}
	if !found || result == nil {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminOps == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	query := r.URL.Query()
	filter := adminops.AuditFilter{Page: 1}
	if value, err := strconv.Atoi(query.Get("page")); err == nil && value > 1 {
		filter.Page = value
	}
	if query.Has("subject") {
		value := query.Get("subject")
		filter.Subject = &value
	}
	if query.Has("action") {
		value := query.Get("action")
		filter.Action = &value
	}
	result, err := s.AdminOps.Audit(r.Context(), filter)
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
