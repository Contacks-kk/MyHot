package httpapi

import (
	"net/http"

	"aihot/backend-go/internal/adminsettings"
)

func (s *Server) contactAlias(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.AdminSettings == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	contact, err := s.AdminSettings.Contact(r.Context())
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	writeContactAliasRedirect(w, r, r.PathValue("slot"), contact)
}

func writeContactAliasRedirect(w http.ResponseWriter, r *http.Request, slot string, contact adminsettings.Contact) {
	var location *string
	switch slot {
	case "wechat":
		location = contact.WechatQR
	case "feishu":
		location = contact.FeishuQR
	default:
		writePrivateSiteNotFound(w, "Not found.")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Surrogate-Control", "no-store")
	w.Header().Set("Content-Length", "0")
	if location != nil {
		w.Header().Set("Location", *location)
	}
	w.WriteHeader(http.StatusFound)
}
