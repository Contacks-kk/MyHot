package httpapi

import (
	"errors"
	"net/http"

	"aihot/backend-go/internal/admincontent"
)

func (s *Server) adminContentSearch(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	rows, err := s.AdminContent.Search(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (s *Server) adminContentDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	chain, err := s.AdminContent.Chain(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if chain == nil {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, chain)
}

func (s *Server) adminContentSEO(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.AdminContent == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	var input struct {
		Indexed *bool  `json:"indexed"`
		Reason  string `json:"reason"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	if input.Indexed == nil {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "indexed: expected boolean")
		return
	}
	result, err := s.AdminContent.SetSEO(r.Context(), r.PathValue("id"), admincontent.SEOInput{Indexed: *input.Indexed, Reason: input.Reason}, adminActor(admin))
	if err != nil {
		switch {
		case errors.Is(err, admincontent.ErrInvalidSEO):
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, admincontent.ErrSEORepositoryUnavailable):
			writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		default:
			writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}
	if result == nil {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
