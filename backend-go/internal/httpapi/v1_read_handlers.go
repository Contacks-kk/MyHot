package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"aihot/backend-go/internal/publiccontent"
)

const snapshotRequiredDetail = "Missing or invalid v1 cursor; fetch /api/v1/selected/snapshot first."

func (s *Server) v1Items(w http.ResponseWriter, r *http.Request) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	query, err := strictSelectedQuery(r, "mode", "category", "window", "by", "q", "limit", "cursor")
	if err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	mode, ok := query["mode"]
	if !ok {
		mode = "selected"
	}
	if mode != "selected" && mode != "all" {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "mode must be 'selected' or 'all'.", 0)
		return
	}
	window, ok := query["window"]
	if !ok {
		window = "7d"
	}
	if window != "24h" && window != "7d" {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "window must be '24h' or '7d'.", 0)
		return
	}
	by, ok := query["by"]
	if !ok {
		by = "timeline"
	}
	if by != "timeline" && by != "published" {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "by must be 'timeline' or 'published'.", 0)
		return
	}
	var category *string
	if raw, ok := query["category"]; ok {
		if _, found := s.resolvePublicCategory(raw); !found {
			allowed := s.publicCategoryOptions()
			if len(allowed) == 0 && len(s.PublicCategoryMembers) == 0 {
				for key := range s.CategoryKeys {
					allowed = append(allowed, key)
				}
			}
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "category must be one of: "+strings.Join(allowed, ", ")+".", 0)
			return
		}
		category = &raw
	}
	var search *string
	if raw, ok := query["q"]; ok {
		raw = strings.TrimFunc(raw, func(value rune) bool { return unicode.IsSpace(value) || value == '\uFEFF' })
		length := utf8.RuneCountInString(raw)
		if length < 2 || length > 200 {
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "q must contain 2 to 200 characters.", 0)
			return
		}
		search = &raw
	}
	limit := 50
	if raw, ok := query["limit"]; ok {
		limit, err = parsePublicV1Integer(raw, 1, 100, "limit")
		if err != nil {
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
			return
		}
	}
	var cursor *string
	if raw, ok := query["cursor"]; ok {
		if raw == "" {
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_cursor", "Invalid cursor", "The cursor is malformed.", 0)
			return
		}
		cursor = &raw
	}
	if s.PublicContent == nil {
		writeV1HotUnavailable(w)
		return
	}
	body, err := s.PublicContent.V1Items(r.Context(), publiccontent.V1ItemsQuery{
		Mode: mode, Window: window, By: by, Category: category, Search: search, Limit: limit, Cursor: cursor,
	}, time.Now(), s.Config.SiteURL)
	if err != nil {
		writeV1ItemsError(w, err)
		return
	}
	writeV1JSONWithETag(w, r, "v1-items", body, body)
}

func (s *Server) v1SelectedSnapshot(w http.ResponseWriter, r *http.Request) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	query, err := strictSelectedQuery(r, "fields", "limit", "page")
	if err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	var fields *string
	if raw, ok := query["fields"]; ok {
		if raw != "default" && raw != "minimal" {
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "fields must be 'default' or 'minimal'.", 0)
			return
		}
		fields = &raw
	}
	limit := 500
	if raw, ok := query["limit"]; ok {
		limit, err = parsePublicV1Integer(raw, 1, 1000, "limit")
		if err != nil {
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
			return
		}
	}
	if s.PublicContent == nil {
		writeV1HotUnavailable(w)
		return
	}
	var page *string
	if raw, ok := query["page"]; ok {
		page = &raw
	}
	body, err := s.PublicContent.SelectedSnapshot(r.Context(), publiccontent.SelectedSnapshotQuery{Fields: fields, Limit: limit, Page: page}, time.Now())
	if err != nil {
		writeSelectedError(w, err)
		return
	}
	etagBody := struct {
		Fields  string            `json:"fields"`
		Cursor  string            `json:"cursor"`
		Count   int               `json:"count"`
		HasMore bool              `json:"hasMore"`
		Items   []json.RawMessage `json:"items"`
	}{body.Fields, body.Cursor, body.Count, body.HasMore, body.Items}
	writeV1JSONWithETag(w, r, "v1-snapshot", body, etagBody)
}

func (s *Server) v1Story(w http.ResponseWriter, r *http.Request) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if key, ok := firstRawQueryName(r.URL.RawQuery); ok {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "Unknown query parameter: "+key+".", 0)
		return
	}
	publicID := r.PathValue("publicId")
	if len(publicID) > 128 {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "publicId must be a short opaque id.", 0)
		return
	}
	if s.PublicContent == nil {
		writeV1HotUnavailable(w)
		return
	}
	lookup, err := s.PublicContent.ResolveV1Story(r.Context(), publicID)
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	switch lookup.Kind {
	case "merged":
		w.Header().Set("Location", "/api/v1/stories/"+lookup.Target)
		w.WriteHeader(http.StatusPermanentRedirect)
	case "not_found":
		writeV1HotProblem(w, http.StatusNotFound, "not_found", "Not found", "No public story exists for "+publicID+".", 0)
	case "found":
		body, found, err := s.PublicContent.V1Story(r.Context(), lookup.ID, time.Now(), s.Config.SiteURL)
		if err != nil {
			writeV1HotUnavailable(w)
			return
		}
		if !found {
			writeV1HotProblem(w, http.StatusNotFound, "not_found", "Not found", "No public story exists for "+publicID+".", 0)
			return
		}
		writeV1JSONWithETag(w, r, "v1-story", body, body)
	default:
		writeV1HotUnavailable(w)
	}
}

func (s *Server) v1SelectedChanges(w http.ResponseWriter, r *http.Request) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	query, err := strictSelectedQuery(r, "cursor", "limit")
	if err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	limit := 100
	if raw, ok := query["limit"]; ok {
		limit, err = parsePublicV1Integer(raw, 1, 100, "limit")
		if err != nil {
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
			return
		}
	}
	cursor := query["cursor"]
	if cursor == "" {
		writeV1HotProblem(w, http.StatusConflict, "snapshot_required", "Snapshot required", snapshotRequiredDetail, 0)
		return
	}
	if s.PublicContent == nil {
		writeV1HotUnavailable(w)
		return
	}
	body, err := s.PublicContent.SelectedChanges(r.Context(), cursor, limit, time.Now())
	if err != nil {
		writeSelectedError(w, err)
		return
	}
	writeV1JSONWithETag(w, r, "v1-changes", body, body)
}

func strictSelectedQuery(r *http.Request, allowed ...string) (map[string]string, error) {
	keys := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		keys[key] = struct{}{}
	}
	values := make(map[string]string, len(allowed))
	for _, part := range strings.Split(r.URL.RawQuery, "&") {
		if part == "" {
			continue
		}
		rawKey, rawValue, hasValue := strings.Cut(part, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			key = rawKey
		}
		if _, ok := keys[key]; !ok {
			return nil, fmt.Errorf("Unknown query parameter: %s.", key)
		}
		if _, ok := values[key]; ok {
			return nil, fmt.Errorf("Query parameter must not be repeated: %s.", key)
		}
		value := ""
		if hasValue {
			value, err = url.QueryUnescape(rawValue)
			if err != nil {
				value = rawValue
			}
		}
		values[key] = value
	}
	return values, nil
}

func parsePublicV1Integer(value string, minimum, maximum int, name string) (int, error) {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0, fmt.Errorf("%s must be an integer from %d to %d.", name, minimum, maximum)
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d.", name, minimum, maximum)
	}
	return parsed, nil
}

func writeSelectedError(w http.ResponseWriter, err error) {
	var required publiccontent.SnapshotRequiredError
	if errors.As(err, &required) {
		writeV1HotProblem(w, http.StatusConflict, "snapshot_required", "Snapshot required", snapshotRequiredDetail, 0)
		return
	}
	var invalid publiccontent.InvalidCursorError
	if errors.As(err, &invalid) {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_cursor", "Invalid cursor", "The cursor is malformed.", 0)
		return
	}
	writeV1HotUnavailable(w)
}

func writeV1ItemsError(w http.ResponseWriter, err error) {
	var busy publiccontent.SearchBusyError
	if errors.As(err, &busy) {
		writeV1HotProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable", "Search is busy; retry later.", busy.RetryAfter)
		return
	}
	writeSelectedError(w, err)
}

func writeV1JSONWithETag(w http.ResponseWriter, r *http.Request, prefix string, body, etagBody any) {
	encoded, err := marshalV1JSON(body)
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	etagEncoded, err := marshalV1JSON(etagBody)
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	sum := sha256.Sum256(etagEncoded)
	etag := `W/"` + prefix + `-` + hex.EncodeToString(sum[:])[:16] + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Vary", "Accept-Encoding")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(encoded)
	}
}

func marshalV1JSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n")), nil
}

func v1SelectedPreflight(w http.ResponseWriter, _ *http.Request) {
	setPublicV1Headers(w)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusNoContent)
}

func v1SelectedMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	setPublicV1Headers(w)
	w.Header().Set("Allow", "GET, HEAD, OPTIONS")
	writeV1HotProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "This endpoint supports only GET, HEAD, and OPTIONS.", 0)
}
