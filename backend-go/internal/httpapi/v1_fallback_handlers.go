package httpapi

import (
	"net/http"
	"strings"
)

func (s *Server) v1UnknownFallback(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		v1SelectedPreflight(w, r)
		return
	}
	setPublicV1Headers(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		writeV1HotProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "This endpoint supports only GET, HEAD, and OPTIONS.", 0)
		return
	}
	path := r.URL.Path
	detail := "No public API v1 operation exists at " + path + ". Recent items are at /api/v1/items; every operation is listed at " + strings.TrimRight(s.Config.SiteURL, "/") + "/openapi-v1.json"
	writeV1HotProblem(w, http.StatusNotFound, "not_found", "Not found", detail, 0)
}
