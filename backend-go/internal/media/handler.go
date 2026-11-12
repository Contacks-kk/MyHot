package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aihot/backend-go/internal/mediaurl"
)

type Enqueuer func(context.Context, string, string) error

type Handler struct {
	service *Service
	enqueue Enqueuer
}

func NewHandler(service *Service, enqueue Enqueuer) *Handler {
	return &Handler{service: service, enqueue: enqueue}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"type":"about:blank","title":"Not Found","status":404,"detail":"No such endpoint."}`)
		return
	}
	query := r.URL.Query()
	verdict := mediaurl.VerifyProxyRequest(query, time.Now(), h.service.config.SignSecret)
	if !verdict.OK {
		malformed := verdict.Reason == "missing" || verdict.Reason == "bad-url"
		w.Header().Set("Cache-Control", "no-store")
		if verdict.Reason == "expired" {
			w.Header().Set("X-Img-Proxy-Sig", "expired")
		} else {
			w.Header().Set("X-Img-Proxy-Sig", "invalid")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if malformed {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "invalid query\n")
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "forbidden\n")
		return
	}
	w.Header().Set("X-Img-Proxy-Sig", "valid")
	image, err := h.service.Produce(r.Context(), verdict.URL, verdict.Mode)
	if err != nil {
		if errors.Is(err, ErrBudgetExceeded) {
			w.Header().Set("Retry-After", "60")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "Image proxy is busy; try again later")
			return
		}
		if host, parseErr := url.Parse(verdict.URL); parseErr == nil {
			log.Printf("img-proxy upstream failed host=%s err=%v", host.Hostname(), err)
		}
		w.Header().Set("Cache-Control", imageCacheControl(query.Get("exp"), 60))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "Upstream image unavailable")
		return
	}
	queued := false
	if image.PendingAnimation && h.enqueue != nil {
		if err := h.enqueue(r.Context(), verdict.URL, verdict.Mode); err != nil {
			log.Printf("img-proxy preparation enqueue failed: %v", err)
		} else {
			queued = true
		}
	}
	etag := `"` + imageETag(image.Type, image.Body) + `"`
	w.Header().Set("Content-Type", image.Type)
	if image.PendingAnimation && !queued {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		limit := 7 * 24 * 60 * 60
		if image.PendingAnimation {
			limit = 60
		}
		w.Header().Set("Cache-Control", imageCacheControl(query.Get("exp"), limit))
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(image.Body)
}

func imageCacheControl(exp string, limit int) string {
	seconds, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return "no-store"
	}
	remaining := seconds - time.Now().Unix()
	if remaining > int64(limit) {
		remaining = int64(limit)
	}
	if remaining <= 0 {
		return "no-store"
	}
	return fmt.Sprintf("public, max-age=%d, s-maxage=%d", remaining, remaining)
}

func imageETag(typeName string, body []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(typeName))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
