package httpapi

import (
	"errors"
	"net/http"

	"aihot/backend-go/internal/tagging"
)

/** getInterestLabels returns stable labels and counts scoped to the current user. */
func (s *Server) getInterestLabels(w http.ResponseWriter, r *http.Request, u userContext) {
	setPrivateSiteHeaders(w)
	rows, err := s.InterestLabels.UserCatalog(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "标签目录暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": rows})
}

/** adminInterestLabels serves the label lifecycle and shared evidence counts. */
func (s *Server) adminInterestLabels(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	rows, err := s.InterestLabels.AdminCatalog(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "标签目录暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": rows})
}

/** adminInterestLabelArticles serves the most recent shared evidence for a label. */
func (s *Server) adminInterestLabelArticles(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	rows, err := s.InterestLabels.AdminEvidence(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "关联证据暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"articles": rows})
}

/** adminCurateInterestLabel records an administrator's merge or withdrawal. */
func (s *Server) adminCurateInterestLabel(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var input struct {
		Action   string `json:"action"`
		TargetID string `json:"targetId"`
		Reason   string `json:"reason"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	err := s.InterestLabels.Curate(r.Context(), adminActor(admin), r.PathValue("id"), input.Action, input.TargetID, input.Reason)
	if errors.Is(err, tagging.ErrInvalidChange) {
		writeAdminProblem(w, http.StatusBadRequest, "invalid_request", "标签状态、合并目标或操作理由无效。")
		return
	}
	if err != nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "标签操作失败。")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
