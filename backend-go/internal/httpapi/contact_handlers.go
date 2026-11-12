package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var contactImageName = regexp.MustCompile(`^[\w.-]+\.(?:png|jpe?g|webp)$`)
var contactImageNote = regexp.MustCompile(`-[0-9a-f]{8}\.`)

func (s *Server) contactImage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Cache-Control", "public, max-age=60")
		notFoundText(w, false)
		return
	}
	if !s.requireSiteUser(w, r) {
		return
	}
	setPrivateSiteHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		notFoundText(w, false)
		return
	}
	name := r.PathValue("file")
	if name == "" || strings.Contains(name, "/") {
		notFoundText(w, r.Method == http.MethodHead)
		return
	}
	if !contactImageName.MatchString(name) || filepath.Base(name) != name {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.Config.DataDir, "uploads", name))
	if err != nil {
		data, err = os.ReadFile(filepath.Join(s.ContactDir, name))
	}
	if err != nil {
		notFoundText(w, r.Method == http.MethodHead)
		return
	}
	contentType := contactImageType(filepath.Ext(name))
	sum := sha256.Sum256(data)
	etag := `W/"` + hex.EncodeToString(sum[:])[:16] + `"`
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", etag)
	if contactImageNote.MatchString(name) {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", stringLength(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func notFoundText(w http.ResponseWriter, head bool) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if head {
		w.Header().Set("Content-Length", "9")
	}
	w.WriteHeader(http.StatusNotFound)
	if !head {
		_, _ = w.Write([]byte("Not found"))
	}
}

func contactImageType(extension string) string {
	switch strings.ToLower(extension) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

func stringLength(value int) string { return strconv.Itoa(value) }
