package httpapi

import (
	"encoding/base64"
	"net/http"
	"strings"

	"aihot/backend-go/internal/adminsettings"
)

func (s *Server) adminSettingsOverview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminSettings == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	result, err := s.AdminSettings.Overview(r.Context())
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminReplaceContactQR(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminSettings == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		Slot  string `json:"slot"`
		Image string `json:"image"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	data, valid := decodeContactQRDataURL(input.Image)
	if !valid {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "image must be a PNG, JPEG or WebP data URL")
		return
	}
	contact, err := s.AdminSettings.ReplaceContactQR(r.Context(), input.Slot, data, adminActor(admin))
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, contact)
}

func decodeContactQRDataURL(value string) ([]byte, bool) {
	prefix := "data:image/"
	if !strings.HasPrefix(value, prefix) {
		return nil, false
	}
	media, encoded, ok := strings.Cut(strings.TrimPrefix(value, prefix), ";base64,")
	if !ok || (media != "png" && media != "jpeg" && media != "webp") || encoded == "" {
		return nil, false
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	return data, err == nil
}

func (s *Server) adminSetNotifyTarget(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminSettings == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	result, found, err := s.AdminSettings.SetTargetEnabled(r.Context(), r.PathValue("key"), input.Enabled, input.Reason, adminActor(admin))
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminUpdateBudget(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminSettings == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input adminsettings.BudgetInput
	if !readAdminJSON(w, r, &input) {
		return
	}
	result, err := s.AdminSettings.UpdateBudget(r.Context(), r.PathValue("service"), input, adminActor(admin))
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
