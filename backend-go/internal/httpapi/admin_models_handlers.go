package httpapi

import (
	"math"
	"net/http"
	"strconv"

	"aihot/backend-go/internal/adminmodels"
)

func (s *Server) adminModelsOverview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminModels == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	days := 7.0
	if raw := r.URL.Query().Get("days"); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil && value != 0 && !math.IsNaN(value) {
			days = math.Min(90, value)
		}
	}
	result, err := s.AdminModels.Overview(r.Context(), days)
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminSwitchModel(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminModels == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		Model  *string `json:"model"`
		Reason string  `json:"reason"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	result, err := s.AdminModels.Switch(r.Context(), r.PathValue("capability"), input.Model, input.Reason, adminActor(admin))
	if err != nil {
		if adminmodels.IsInvalid(err) {
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
