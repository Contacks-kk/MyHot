package httpapi

import (
	"net/http"
)

func (s *Server) openAPIV1(w http.ResponseWriter, r *http.Request) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if _, err := strictSelectedQuery(r); err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	if s.SiteInfo == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	body, etag, err := s.SiteInfo.OpenAPIV1()
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not found"))
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) openAPIMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	w.Header().Set("Allow", "GET, HEAD, OPTIONS")
	writeV1HotProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "This endpoint supports only GET, HEAD, and OPTIONS.", 0)
}
