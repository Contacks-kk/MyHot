package httpapi

import (
	"net/http"
	"strconv"

	"aihot/backend-go/internal/adminlabels"
)

/** adminLabelsOverview handles requests to retrieve stable labels, candidate labels, and totals. */
func (s *Server) adminLabelsOverview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	if s.AdminLabels == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}

	// 读取标签全景诊断与候选晋升统计
	result, err := s.AdminLabels.Overview(r.Context())
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

/** adminLabelHotspots handles querying publications associated with a label with fuzzy search and ranking. */
func (s *Server) adminLabelHotspots(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	if s.AdminLabels == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}

	query := r.URL.Query()
	filter := adminlabels.HotspotsFilter{
		Label:    query.Get("label"),
		Query:    query.Get("q"),
		SortBy:   query.Get("sort"),
		Page:     1,
		PageSize: 20,
	}

	if raw := query.Get("page"); raw != "" {
		if page, err := strconv.Atoi(raw); err == nil && page > 0 {
			filter.Page = page
		}
	}

	if raw := query.Get("pageSize"); raw != "" {
		if size, err := strconv.Atoi(raw); err == nil && size > 0 && size <= 100 {
			filter.PageSize = size
		}
	}

	// 检索关联的热点文章及排名
	result, err := s.AdminLabels.Hotspots(r.Context(), filter)
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}
