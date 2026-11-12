package httpapi

import (
	"encoding/json"
	"net/http"

	"aihot/backend-go/internal/adminselectbench"
)

func (s *Server) adminSelectBenchList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminSelectBench == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	runs, err := s.AdminSelectBench.List(r.Context())
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) adminSelectBenchRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminSelectBench == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	query := r.URL.Query()
	filters := adminselectbench.Filters{Model: query.Get("model"), Outcome: query.Get("outcome"), Disagree: query.Get("disagree") == "1"}
	if values, ok := query["stratum"]; ok && len(values) > 0 {
		filters.Stratum = &values[len(values)-1]
	}
	result, err := s.AdminSelectBench.Run(r.Context(), r.PathValue("id"), filters)
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

func (s *Server) adminSelectBenchImport(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminSelectBench == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		Label  string          `json:"label"`
		Report json.RawMessage `json:"report"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	if input.Label == "" {
		input.Label = "导入的对比运行"
	}
	id, err := s.AdminSelectBench.Import(r.Context(), input.Label, input.Report, adminActor(admin))
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}
