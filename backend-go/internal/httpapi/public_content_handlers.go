package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"aihot/backend-go/internal/publiccontent"
)

var (
	siteReportNavigationKeyPattern = regexp.MustCompile(`^\d{4}-(\d{2}(-\d{2})?|W\d{2})$`)
	siteReportMonthPattern         = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
)

func (s *Server) itemAvailability(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	result, err := s.PublicContent.ItemAvailability(r.Context(), strings.Split(r.URL.Query().Get("ids"), ","))
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	writePrivateSiteJSON(w, http.StatusOK, result)
}

func (s *Server) siteHot(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	result, err := s.PublicContent.SiteHot(r.Context(), time.Now(), s.Config.ImageProxySignSecret)
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	writeV1JSONWithETag(w, r, "hot", result, result)
}

func (s *Server) siteTimeline(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	filters, err := s.parseSiteTimelineFilters(r.URL.Query())
	if err != nil {
		writePrivateSiteBadRequest(w, err.Error())
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	queryValues := r.URL.Query()
	query := publiccontent.SiteTimelineQuery{
		Channel: filters.Channel, Category: optionalNonemptyString(filters.Category),
		Tag: optionalNonemptyString(filters.Tag), Cursor: optionalNonemptyString(queryValues.Get("cursor")),
		Limit: siteTimelineLimit(queryValues.Get("limit")),
	}
	now := time.Now()
	w.Header().Set("X-Accel-Expires", "@"+strconv.FormatInt(now.Unix()+60, 10))
	result, err := s.PublicContent.SiteTimeline(r.Context(), query, now, s.Config.ImageProxySignSecret, s.Config.XPostIsFullText)
	if err != nil {
		var invalid publiccontent.SiteTimelineInvalidCursorError
		if errors.As(err, &invalid) {
			writePrivateSiteTimelineProblem(w, http.StatusBadRequest, "invalid_cursor", "Invalid cursor", invalid.Error(), 0)
			return
		}
		var busy publiccontent.SearchBusyError
		if errors.As(err, &busy) {
			writePrivateSiteTimelineProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable", "search busy", busy.RetryAfter)
			return
		}
		writePrivateSiteUnavailable(w)
		return
	}
	writeV1JSONWithETag(w, r, "tl", result, result)
}

func (s *Server) sitePool(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	filters, err := s.parseSiteTimelineFilters(r.URL.Query())
	if err != nil {
		writePrivateSiteBadRequest(w, err.Error())
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	values := r.URL.Query()
	page := sitePoolPage(values.Get("page"))
	search := strings.TrimFunc(values.Get("q"), func(char rune) bool { return unicode.IsSpace(char) || char == '\uFEFF' })
	var searchValue *string
	if search != "" {
		search = truncateGroupTag(search, 200)
		searchValue = &search
	}
	tab := "time"
	if searchValue != nil && values.Get("tab") == "relevance" {
		tab = "relevance"
	}
	query := publiccontent.SitePoolQuery{
		Channel: filters.Channel, Category: optionalNonemptyString(filters.Category), Tag: optionalNonemptyString(filters.Tag),
		Search: searchValue, Tab: tab, Page: page,
	}
	result, err := s.PublicContent.SitePool(r.Context(), query, time.Now(), s.Config.ImageProxySignSecret, s.Config.XPostIsFullText)
	if err != nil {
		var busy publiccontent.SearchBusyError
		if errors.As(err, &busy) {
			writePrivateSiteTimelineProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable", "search busy", busy.RetryAfter)
			return
		}
		if err.Error() == "invalid channel" || err.Error() == "invalid category" || err.Error() == "invalid page" {
			writePrivateSiteBadRequest(w, err.Error())
			return
		}
		writePrivateSiteUnavailable(w)
		return
	}
	writeV1JSONWithETag(w, r, "pool", result, result)
}

func (s *Server) siteItemDetail(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	language, prefix := "zh", "item"
	if strings.HasSuffix(r.URL.Path, "/original") {
		language, prefix = "original", "item-original"
	}
	result, err := s.PublicContent.SiteItem(r.Context(), r.PathValue("id"), language, time.Now(), s.Config.ImageProxySignSecret, s.Config.XPostIsFullText)
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	if !result.Found || result.Item == nil {
		writePrivateSiteNotFound(w, "item not found")
		return
	}
	writeV1JSONWithETag(w, r, prefix, result.Item, result.Item)
}

func (s *Server) siteItemMarkdown(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	if !publiccontent.ValidSiteItemID(r.PathValue("id")) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Del("Surrogate-Control")
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	export, err := s.PublicContent.SiteItemMarkdown(
		r.Context(), r.PathValue("id"), s.Config.SiteName, s.Config.SiteURL, s.Config.SiteMCPPrefix, s.Config.XPostIsFullText,
	)
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	if export == nil {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Del("Surrogate-Control")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "Not found")
		return
	}
	w.Header().Del("Surrogate-Control")
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", export.Filename))
	w.Header().Set("Cache-Control", "public, max-age=300, s-maxage=300")
	w.Header().Set("X-Robots-Tag", "noindex")
	_, _ = io.WriteString(w, export.Body)
}

func (s *Server) siteStoryDetail(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	result, err := s.PublicContent.SiteStory(r.Context(), r.PathValue("publicId"), time.Now())
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	if result.Kind == "merged" {
		w.Header().Set("Location", "/api/site/stories/"+result.Target)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusPermanentRedirect)
		_ = json.NewEncoder(w).Encode(map[string]string{"mergedInto": result.Target})
		return
	}
	if result.Kind != "found" || result.Story == nil {
		writePrivateSiteNotFound(w, "story not found")
		return
	}
	writeV1JSONWithETag(w, r, "story", result.Story, result.Story)
}

func sitePoolPage(raw string) int {
	value := 1.0
	if strings.TrimSpace(raw) != "" {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			value = 1
		} else if parsed != 0 && !math.IsNaN(parsed) {
			value = parsed
		}
	}
	if value < 1 {
		value = 1
	}
	if value > 50 || math.IsInf(value, 1) {
		value = 50
	}
	return int(value)
}

func (s *Server) parseSiteTimelineFilters(values map[string][]string) (groupReportsFilters, error) {
	filters := groupReportsFilters{Channel: "all"}
	if raw, ok := values["channel"]; ok {
		if len(raw) == 0 {
			return groupReportsFilters{}, errors.New("invalid channel")
		}
		filters.Channel = raw[0]
	}
	if filters.Channel != "all" && filters.Channel != "news" && filters.Channel != "x" && filters.Channel != "firstParty" {
		return groupReportsFilters{}, errors.New("invalid channel")
	}
	if raw, ok := values["category"]; ok {
		if len(raw) == 0 {
			return groupReportsFilters{}, errors.New("invalid category")
		}
		filters.Category = raw[0]
		if _, ok := s.CategoryKeys[filters.Category]; !ok {
			return groupReportsFilters{}, errors.New("invalid category")
		}
	}
	if raw, ok := values["tag"]; ok && len(raw) > 0 {
		filters.Tag = truncateGroupTag(trimGroupTag(raw[0]), 60)
		if filters.Tag == "" {
			filters.Tag = ""
		}
	}
	return filters, nil
}

func siteTimelineLimit(raw string) int {
	value := 0.0
	if strings.TrimSpace(raw) != "" {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			value = math.NaN()
		} else {
			value = parsed
		}
	}
	if value == 0 || math.IsNaN(value) {
		value = 20
	}
	value = math.Min(math.Max(value, 1), 40)
	return int(value)
}

func optionalNonemptyString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func writePrivateSiteTimelineProblem(w http.ResponseWriter, status int, code, title, detail string, retryAfter int) {
	requestID := siteRequestID()
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", requestID)
	setPrivateSiteHeaders(w)
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.WriteHeader(status)
	body := map[string]any{
		"type": "/problems/" + strings.ReplaceAll(code, "_", "-"), "title": title, "status": status,
		"detail": detail, "code": code, "requestId": requestID,
	}
	if retryAfter > 0 {
		body["retryAfter"] = retryAfter
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) siteTopics(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.Topics == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	result, err := s.Topics.List(r.Context(), time.Now())
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	body := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	sum := sha256.Sum256(body)
	etag := `W/"topics-` + hex.EncodeToString(sum[:])[:16] + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Vary", "Accept-Encoding")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) siteTopicPage(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.Topics == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	page := 1.0
	if _, ok := r.URL.Query()["page"]; ok {
		raw := strings.TrimSpace(r.URL.Query().Get("page"))
		if raw == "" {
			page = 0
		} else if value, err := strconv.ParseFloat(raw, 64); err == nil {
			page = value
		} else {
			page = math.NaN()
		}
	}
	result, err := s.Topics.Page(r.Context(), r.PathValue("slug"), page, time.Now())
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	if result == nil {
		writePrivateSiteNotFound(w, "topic page not found")
		return
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	body := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	sum := sha256.Sum256(body)
	etag := `W/"topic-` + hex.EncodeToString(sum[:])[:16] + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Vary", "Accept-Encoding")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) siteReportIndex(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	kind := r.PathValue("kind")
	if kind != "daily" && kind != "weekly" && kind != "monthly" {
		writePrivateSiteNotFound(w, "unknown report kind")
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	result, err := s.PublicContent.SiteReportIndex(r.Context(), kind, time.Now(), s.Config.SiteName)
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	writeV1JSONWithETag(w, r, "reports", result, result)
}

func (s *Server) siteReportLatestPage(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	kind := r.PathValue("kind")
	if kind != "daily" && kind != "weekly" && kind != "monthly" {
		writePrivateSiteNotFound(w, "unknown report kind")
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	result, err := s.PublicContent.SiteReportLatestPage(r.Context(), kind, time.Now(), s.siteReportConfig())
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	writeV1JSONWithETag(w, r, "report-latest", result, result)
}

func (s *Server) siteReportDetail(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	kind, key := r.PathValue("kind"), r.PathValue("key")
	if (kind != "daily" && kind != "weekly" && kind != "monthly") || !siteReportNavigationKeyPattern.MatchString(key) {
		writePrivateSiteNotFound(w, "report not found")
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	result, err := s.PublicContent.SiteReport(r.Context(), kind, key, time.Now(), s.siteReportConfig())
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	if result == nil {
		writePrivateSiteNotFound(w, "report not found")
		return
	}
	writeV1JSONWithETag(w, r, "report", result, result)
}

func (s *Server) siteReportConfig() publiccontent.SiteReportConfig {
	return publiccontent.SiteReportConfig{
		SiteName: s.Config.SiteName, SiteURL: s.Config.SiteURL, Subject: s.ReportSubject,
		Measure: s.Config.SiteReportEntryMeasure, Noun: s.Config.SiteReportEntryNoun,
		ImageProxySecret: s.Config.ImageProxySignSecret,
	}
}

func (s *Server) siteReportNavigation(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	kind, key := r.PathValue("kind"), r.PathValue("key")
	if (kind != "daily" && kind != "weekly" && kind != "monthly") || !siteReportNavigationKeyPattern.MatchString(key) {
		writePrivateSiteNotFound(w, "report not found")
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	result, err := s.PublicContent.SiteReportNavigation(r.Context(), kind, key, time.Now(), s.Config.SiteName)
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	writeV1JSONWithETag(w, r, "report-navigation", result, result)
}

func (s *Server) siteDailyReportMonth(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	month := r.PathValue("month")
	if !siteReportMonthPattern.MatchString(month) {
		writePrivateSiteNotFound(w, "month not found")
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	result, err := s.PublicContent.SiteDailyReportMonth(r.Context(), month, time.Now(), s.Config.SiteName)
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	writeV1JSONWithETag(w, r, "report-month", result, result)
}

func (s *Server) storyFollowups(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	result, err := s.PublicContent.StoryFollowups(r.Context(), r.PathValue("publicId"), time.Now())
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	if result == nil {
		writePrivateSiteNotFound(w, "story not found")
		return
	}
	writePrivateSiteJSON(w, http.StatusOK, result)
}

func (s *Server) groupReports(w http.ResponseWriter, r *http.Request) {
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if s.PublicContent == nil {
		writePrivateSiteUnavailable(w)
		return
	}
	filters, err := s.parseGroupReportsFilters(r.URL.Query())
	if err != nil {
		writePrivateSiteBadRequest(w, err.Error())
		return
	}
	result, err := s.PublicContent.GroupReports(r.Context(), publiccontent.GroupReportsQuery{
		FactPublicID: r.PathValue("factId"), Channel: filters.Channel, Category: filters.Category, Tag: filters.Tag,
	}, time.Now())
	if err != nil {
		writePrivateSiteUnavailable(w)
		return
	}
	if result == nil {
		writePrivateSiteNotFound(w, "reading group unavailable")
		return
	}
	writePrivateSiteJSON(w, http.StatusOK, result)
}

type groupReportsFilters struct {
	Channel  string
	Category string
	Tag      string
}

func (s *Server) parseGroupReportsFilters(values map[string][]string) (groupReportsFilters, error) {
	filters := groupReportsFilters{Channel: "all"}
	if raw, ok := values["channel"]; ok {
		if len(raw) == 0 {
			return groupReportsFilters{}, errors.New("invalid channel")
		}
		filters.Channel = raw[0]
	}
	if filters.Channel != "all" && filters.Channel != "news" && filters.Channel != "x" && filters.Channel != "firstParty" {
		return groupReportsFilters{}, errors.New("invalid channel")
	}
	if raw, ok := values["category"]; ok {
		if len(raw) == 0 {
			return groupReportsFilters{}, errors.New("invalid category")
		}
		filters.Category = raw[0]
		if _, ok := s.CategoryKeys[filters.Category]; !ok {
			return groupReportsFilters{}, errors.New("invalid category")
		}
	}
	if raw, ok := values["tag"]; ok && len(raw) > 0 {
		filters.Tag = truncateGroupTag(trimGroupTag(raw[0]), 60)
	}
	return filters, nil
}

func trimGroupTag(value string) string {
	return strings.TrimFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
}

func truncateGroupTag(value string, limit int) string {
	units := utf16.Encode([]rune(value))
	if len(units) <= limit {
		return value
	}
	return string(utf16.Decode(units[:limit]))
}

func writePrivateSiteJSON(w http.ResponseWriter, status int, value any) {
	setPrivateSiteHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writePrivateSiteBadRequest(w http.ResponseWriter, detail string) {
	requestID := siteRequestID()
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", requestID)
	setPrivateSiteHeaders(w)
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "/problems/invalid-request", "title": "Bad request", "status": http.StatusBadRequest,
		"detail": detail, "code": "invalid_request", "requestId": requestID,
	})
}
