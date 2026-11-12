package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

func (s *Server) indexNowKey(w http.ResponseWriter, r *http.Request) {
	body := []byte(s.Config.IndexNowKey)
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:])[:16] + `"`
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Length", stringLength(len(body)))
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
