package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"aihot/backend-go/internal/agentmd"
	"aihot/backend-go/internal/publiccontent"
)

func (s *Server) v1AgentHot(w http.ResponseWriter, r *http.Request) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	query, err := strictSelectedQuery(r, "limit")
	if err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	limit := 10
	if raw, exists := query["limit"]; exists {
		limit, err = parsePublicV1Integer(raw, 1, 10, "limit")
		if err != nil {
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
			return
		}
	}
	if s.Agent == nil {
		writeV1HotUnavailable(w)
		return
	}
	now := time.Now()
	body, err := s.Agent.Hot(r.Context(), limit, now)
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	writeAgentMarkdownWithETag(w, r, "agent-hot", body)
}

func (s *Server) v1AgentGuide(w http.ResponseWriter, r *http.Request) {
	if !s.beginAgentRequest(w, r) {
		return
	}
	if _, err := strictSelectedQuery(r); err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	if s.Agent == nil {
		writeV1HotUnavailable(w)
		return
	}
	writeAgentMarkdownWithETag(w, r, "agent-guide", s.Agent.Guide())
}

func (s *Server) v1AgentLatest(w http.ResponseWriter, r *http.Request) {
	s.v1AgentItems(w, r, false)
}

func (s *Server) v1AgentSearch(w http.ResponseWriter, r *http.Request) {
	s.v1AgentItems(w, r, true)
}

func (s *Server) v1AgentStory(w http.ResponseWriter, r *http.Request) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	query, err := strictSelectedQuery(r, "limit")
	if err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	limit := 20
	if raw, exists := query["limit"]; exists {
		limit, err = parsePublicV1Integer(raw, 1, 50, "limit")
		if err != nil {
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
			return
		}
	}
	publicID := r.PathValue("publicId")
	if len(publicID) > 128 {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "publicId must be a short opaque id.", 0)
		return
	}
	if s.Agent == nil {
		writeV1HotUnavailable(w)
		return
	}
	body, found, err := s.Agent.Story(r.Context(), publicID, limit, time.Now())
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	if !found {
		writeV1HotProblem(w, http.StatusNotFound, "not_found", "Not found", "没有这个公开事件。只使用热点结果里给出的「来龙去脉」地址，不要猜。", 0)
		return
	}
	writeAgentMarkdownWithETag(w, r, "agent-story", body)
}

func (s *Server) v1AgentDailyLatest(w http.ResponseWriter, r *http.Request) {
	s.v1AgentDaily(w, r, "latest", true)
}

func (s *Server) v1AgentDailyByDate(w http.ResponseWriter, r *http.Request) {
	s.v1AgentDaily(w, r, r.PathValue("date"), false)
}

func (s *Server) v1AgentDaily(w http.ResponseWriter, r *http.Request, key string, latest bool) {
	if !s.beginAgentRequest(w, r) {
		return
	}
	if _, err := strictSelectedQuery(r); err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	if !latest {
		parsed, err := time.Parse("2006-01-02", key)
		if err != nil || parsed.Format("2006-01-02") != key {
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "date must be a real YYYY-MM-DD calendar date.", 0)
			return
		}
	}
	if s.Agent == nil {
		writeV1HotUnavailable(w)
		return
	}
	body, found, err := s.Agent.Daily(r.Context(), key, latest, time.Now())
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	if !found {
		detail := "还没有发布过日报。"
		if !latest {
			detail = "没有 " + key + " 的日报；不要换一天冒充。"
		}
		writeV1HotProblem(w, http.StatusNotFound, "not_found", "Not found", detail, 0)
		return
	}
	writeAgentMarkdownWithETag(w, r, "agent-daily", body)
}

func (s *Server) v1AgentWeeklyLatest(w http.ResponseWriter, r *http.Request) {
	s.v1AgentPeriod(w, r, "weekly", "latest", true)
}

func (s *Server) v1AgentWeeklyByWeek(w http.ResponseWriter, r *http.Request) {
	s.v1AgentPeriod(w, r, "weekly", r.PathValue("week"), false)
}

func (s *Server) v1AgentMonthlyLatest(w http.ResponseWriter, r *http.Request) {
	s.v1AgentPeriod(w, r, "monthly", "latest", true)
}

func (s *Server) v1AgentMonthlyByMonth(w http.ResponseWriter, r *http.Request) {
	s.v1AgentPeriod(w, r, "monthly", r.PathValue("month"), false)
}

func (s *Server) v1AgentPeriod(w http.ResponseWriter, r *http.Request, kind, key string, latest bool) {
	if !s.beginAgentRequest(w, r) {
		return
	}
	if _, err := strictSelectedQuery(r); err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	if !latest {
		if _, _, valid := publiccontent.ReportPeriodRange(kind, key); !valid {
			name, form := "week", "a real ISO week such as 2026-W39"
			if kind == "monthly" {
				name, form = "month", "a real month such as 2026-09"
			}
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", name+" must be "+form+".", 0)
			return
		}
	}
	if s.Agent == nil {
		writeV1HotUnavailable(w)
		return
	}
	body, found, err := s.Agent.Period(r.Context(), kind, key, latest, time.Now())
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	if !found {
		name := "周报"
		if kind == "monthly" {
			name = "月报"
		}
		detail := "还没有发布过" + name + "。"
		if !latest {
			detail = "没有 " + key + " 的" + name + "；不要换一期冒充。"
		}
		writeV1HotProblem(w, http.StatusNotFound, "not_found", "Not found", detail, 0)
		return
	}
	writeAgentMarkdownWithETag(w, r, "agent-"+kind, body)
}

func (s *Server) beginAgentRequest(w http.ResponseWriter, r *http.Request) bool {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	return s.requireSiteUser(w, r)
}

func (s *Server) v1AgentItems(w http.ResponseWriter, r *http.Request, search bool) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	parsed, err := s.parseAgentItemsRequest(r, search)
	if err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	if s.Agent == nil {
		writeV1HotUnavailable(w)
		return
	}
	now := time.Now()
	var body string
	if search {
		body, err = s.Agent.Search(r.Context(), agentmd.SearchQuery{Window: parsed.Window, Category: parsed.Category, Limit: parsed.Limit, Text: parsed.Text}, now)
	} else {
		body, err = s.Agent.Latest(r.Context(), agentmd.LatestQuery{Window: parsed.Window, Mode: parsed.Mode, Category: parsed.Category, Limit: parsed.Limit}, now)
	}
	if err != nil {
		if search {
			writeV1ItemsError(w, err)
		} else {
			writeV1HotUnavailable(w)
		}
		return
	}
	prefix := "agent-latest"
	if search {
		prefix = "agent-search"
	}
	writeAgentMarkdownWithETag(w, r, prefix, body)
}

type agentItemsRequest struct {
	Window   string
	Mode     string
	Category *string
	Limit    int
	Text     string
}

func (s *Server) parseAgentItemsRequest(r *http.Request, search bool) (agentItemsRequest, error) {
	var parsed agentItemsRequest
	allowed := []string{"window", "category", "limit"}
	if search {
		allowed = append(allowed, "q")
	} else {
		allowed = append(allowed, "mode")
	}
	query, err := strictSelectedQuery(r, allowed...)
	if err != nil {
		return parsed, err
	}
	parsed.Window = query["window"]
	if parsed.Window == "" {
		parsed.Window = "24h"
		if search {
			parsed.Window = "7d"
		}
	}
	if parsed.Window != "24h" && parsed.Window != "7d" {
		return parsed, errors.New("window must be '24h' or '7d'.")
	}
	if raw, exists := query["category"]; exists {
		if _, found := s.resolvePublicCategory(raw); !found {
			allowedCategories := s.publicCategoryOptions()
			if len(allowedCategories) == 0 && len(s.PublicCategoryMembers) == 0 {
				for key := range s.CategoryKeys {
					allowedCategories = append(allowedCategories, key)
				}
			}
			return parsed, errors.New("category must be one of: " + strings.Join(allowedCategories, ", ") + ".")
		}
		parsed.Category = &raw
	}
	parsed.Limit = 10
	if raw, exists := query["limit"]; exists {
		parsed.Limit, err = parsePublicV1Integer(raw, 1, 30, "limit")
		if err != nil {
			return parsed, err
		}
	}
	if search {
		parsed.Text = strings.TrimFunc(query["q"], func(value rune) bool { return unicode.IsSpace(value) || value == '\uFEFF' })
		length := utf8.RuneCountInString(parsed.Text)
		if length < 2 || length > 200 {
			return parsed, errors.New("q must contain 2 to 200 characters.")
		}
	} else {
		parsed.Mode = query["mode"]
		if parsed.Mode == "" {
			parsed.Mode = "selected"
		}
		if parsed.Mode != "selected" && parsed.Mode != "all" {
			return parsed, errors.New("mode must be 'selected' or 'all'.")
		}
	}
	return parsed, nil
}

func writeAgentMarkdownWithETag(w http.ResponseWriter, r *http.Request, prefix, body string) {
	sum := sha256.Sum256([]byte(body))
	etag := `W/"` + prefix + `-` + hex.EncodeToString(sum[:])[:16] + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Vary", "Accept-Encoding")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(strings.TrimSuffix(body, "\n") + "\n"))
	}
}
