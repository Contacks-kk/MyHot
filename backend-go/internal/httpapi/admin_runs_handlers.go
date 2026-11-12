package httpapi

import (
	"errors"
	"net/http"
	"time"

	"aihot/backend-go/internal/adminruns"
)

func (s *Server) adminRunsOverview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminRuns == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	result, err := s.AdminRuns.Overview(r.Context(), time.Now())
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminProcessingRequeue(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminRuns == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		Group  *string `json:"group"`
		Reason string  `json:"reason"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	result, err := s.AdminRuns.RequeueFailed(r.Context(), input.Group, input.Reason, adminActor(admin))
	if err != nil {
		switch {
		case errors.Is(err, adminruns.ErrInvalidRequeue):
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, adminruns.ErrRequeueUnavailable):
			writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		default:
			writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}
