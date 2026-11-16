package httpapi

import (
	"encoding/json"
	"net/http"
)

/** userCatalog serves the catalog to a verified user. */
func (s *Server) userCatalog(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	s.writeCatalog(w)
}

/** adminCatalog serves the catalog to an authenticated administrator. */
func (s *Server) adminCatalog(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	s.writeCatalog(w)
}

/** writeCatalog writes the configured display catalog. */
func (s *Server) writeCatalog(w http.ResponseWriter) {
	if s.Catalog == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(s.Catalog.Display())
}
