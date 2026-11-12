package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"aihot/backend-go/internal/adminsources"
)

func (s *Server) adminSourcesList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminSources == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	query := r.URL.Query()
	filter := adminsources.Filter{Page: 1}
	if raw := query.Get("page"); raw != "" {
		if page, err := strconv.Atoi(raw); err == nil {
			filter.Page = page
		}
	}
	if value := strings.TrimSpace(query.Get("q")); value != "" {
		pattern := "%" + value + "%"
		filter.Query = &pattern
	}
	if value, ok := query["kind"]; ok && len(value) > 0 {
		filter.Kind = &value[len(value)-1]
	}
	if value, ok := query["health"]; ok && len(value) > 0 {
		filter.Health = &value[len(value)-1]
	}
	if value, ok := query["mode"]; ok && len(value) > 0 {
		filter.Mode = &value[len(value)-1]
	}
	if value, ok := query["enabled"]; ok && len(value) > 0 {
		enabled := value[len(value)-1] == "true"
		filter.Enabled = &enabled
	}
	result, err := s.AdminSources.List(r.Context(), filter)
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminSourceDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminSources == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	result, err := s.AdminSources.Detail(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if result == nil {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
