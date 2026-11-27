package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"aihot/backend-go/internal/mediaurl"
	"aihot/backend-go/internal/personal"
)

type contentCard struct {
	ArticleID     string               `json:"articleId"`
	Score         float64              `json:"score"`
	Title         string               `json:"title"`
	Summary       string               `json:"summary"`
	Reason        *string              `json:"reason"`
	URL           string               `json:"url"`
	Source        string               `json:"source"`
	SourceID      string               `json:"sourceId,omitempty"`
	SourceIconURL *string              `json:"sourceIconUrl,omitempty"`
	IsSubscribed  bool                 `json:"isSubscribed"`
	PublishedAt   *time.Time           `json:"publishedAt"`
	CreatedAt     time.Time            `json:"createdAt"`
	Historical    bool                 `json:"historical"`
	Media         []mediaurl.MediaView `json:"media"`
}

/** contentResponses projects personal result rows into API cards with signed media proxy URLs. */
func contentResponses(items []personal.Card, now time.Time, imageSecret, thumbMode string, mediaLimit int) []contentCard {
	responses := make([]contentCard, 0, len(items))
	for _, item := range items {
		mediaViews := mediaurl.BuildViews(item.MediaJSON, thumbMode, now, imageSecret, mediaLimit)
		responses = append(responses, contentCard{
			ArticleID:     item.ArticleID,
			Score:         item.Score,
			Title:         item.Title,
			Summary:       item.Summary,
			Reason:        item.Reason,
			URL:           item.URL,
			Source:        item.Source,
			SourceID:      item.SourceID,
			SourceIconURL: mediaurl.SourceIconDisplayURL(item.SourceIconURL, now, imageSecret),
			IsSubscribed:  item.IsSubscribed,
			PublishedAt:   item.PublishedAt,
			CreatedAt:     item.CreatedAt,
			Historical:    item.Historical,
			Media:         mediaViews,
		})
	}

	return responses
}

/** feed returns the authenticated user's latest personalized article summaries. */
func (s *Server) feed(w http.ResponseWriter, r *http.Request, u userContext) {
	items, err := s.Personal.Feed(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "个人信息暂不可用")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": contentResponses(items, time.Now(), s.Config.ImageProxySignSecret, "image-336", 4),
	})
}

/** funnel returns today's scan and filter statistics for the authenticated user. */
func (s *Server) funnel(w http.ResponseWriter, r *http.Request, u userContext) {
	view, err := s.Personal.Funnel(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "漏斗统计暂不可用")
		return
	}

	writeJSON(w, http.StatusOK, view)
}

/** hot returns only the authenticated user's processed hotspots for the requested time range. */
func (s *Server) hot(w http.ResponseWriter, r *http.Request, u userContext) {
	query := r.URL.Query()
	page := 1
	if raw := query.Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "页码无效")
			return
		}
		page = parsed
	}
	selection := personal.HotQuery{
		Range: query.Get("range"),
		Date:  query.Get("date"),
		Page:  page,
	}
	result, err := s.Personal.Hot(r.Context(), u.ID, selection, time.Now())
	var inputError *personal.InputError
	if errors.As(err, &inputError) {
		writeError(w, http.StatusBadRequest, inputError.Message)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "个人热点暂不可用")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":   contentResponses(result.Items, time.Now(), s.Config.ImageProxySignSecret, "image-336", 4),
		"page":    result.Page,
		"hasMore": result.HasMore,
	})
}

type recommendedArticleResponse struct {
	ArticleID     string               `json:"articleId"`
	Score         *float64             `json:"score"`
	Title         string               `json:"title"`
	Summary       string               `json:"summary"`
	Reason        string               `json:"reason"`
	URL           string               `json:"url"`
	SourceID      string               `json:"sourceId"`
	SourceName    string               `json:"sourceName"`
	SourceKind    string               `json:"sourceKind"`
	SourceIconURL *string              `json:"sourceIconUrl,omitempty"`
	IsSubscribed  bool                 `json:"isSubscribed"`
	PublishedAt   *time.Time           `json:"publishedAt"`
	CreatedAt     time.Time            `json:"createdAt"`
	Media         []mediaurl.MediaView `json:"media"`
	MatchedTags   []string             `json:"matchedTags"`
}

/** recommendedArticles returns one short list of unseen articles from approved feeds. */
func (s *Server) recommendedArticles(w http.ResponseWriter, r *http.Request, u userContext) {
	if s.Personal == nil {
		writeError(w, http.StatusServiceUnavailable, "服务暂不可用")
		return
	}

	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		if val, err := strconv.Atoi(raw); err == nil && val >= 1 {
			page = val
		}
	}

	items, hasMore, err := s.Personal.RecommendedArticles(r.Context(), u.ID, page)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "获取推荐文章失败")
		return
	}

	now := time.Now()
	responses := make([]recommendedArticleResponse, 0, len(items))
	for _, item := range items {
		responses = append(responses, recommendedArticleResponse{
			ArticleID:     item.ArticleID,
			Score:         item.Score,
			Title:         item.Title,
			Summary:       item.Summary,
			URL:           item.URL,
			SourceID:      item.SourceID,
			SourceName:    item.SourceName,
			SourceKind:    item.SourceKind,
			SourceIconURL: mediaurl.SourceIconDisplayURL(item.SourceIconURL, now, s.Config.ImageProxySignSecret),
			IsSubscribed:  item.IsSubscribed,
			PublishedAt:   item.PublishedAt,
			CreatedAt:     item.CreatedAt,
			Media:         []mediaurl.MediaView{},
			MatchedTags:   item.MatchedTags,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":   responses,
		"page":    page,
		"hasMore": hasMore,
	})
}

/** personalArticle returns a single result owned by the authenticated user. */
func (s *Server) personalArticle(w http.ResponseWriter, r *http.Request, u userContext) {
	item, err := s.Personal.Article(r.Context(), u.ID, r.PathValue("id"))
	if errors.Is(err, personal.ErrNotFound) {
		writeError(w, http.StatusNotFound, "文章不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "个人文章暂不可用")
		return
	}

	card := contentResponses([]personal.Card{item.Card}, time.Now(), s.Config.ImageProxySignSecret, "image-720", 9)[0]
	writeJSON(w, http.StatusOK, struct {
		contentCard
		OriginalTitle string  `json:"originalTitle"`
		Author        *string `json:"author"`
	}{
		contentCard:   card,
		OriginalTitle: item.OriginalTitle,
		Author:        item.Author,
	})
}

/** dailyView serves the owner's report directory and one selected issue. */
func (s *Server) dailyView(w http.ResponseWriter, r *http.Request, u userContext) {
	query := r.URL.Query()
	view, err := s.Personal.DailyView(r.Context(), u.ID, query.Get("id"), query.Get("date"))
	if errors.Is(err, personal.ErrNotFound) {
		writeError(w, http.StatusNotFound, "日报不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "日报暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

/** generateDaily queues a manual report for the authenticated user. */
func (s *Server) generateDaily(w http.ResponseWriter, r *http.Request, u userContext) {
	if !s.Config.PersonalizationEnabled || !s.Config.ModelCallsEnabled || s.EnqueueManualDaily == nil {
		writeError(w, http.StatusServiceUnavailable, "日报生成功能当前已关闭")
		return
	}
	jobID, err := s.EnqueueManualDaily(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法提交日报任务")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"jobId": jobID})
}

/** manualDailyStatus returns the state of a user's own manual report job. */
func (s *Server) manualDailyStatus(w http.ResponseWriter, r *http.Request, u userContext) {
	jobID, err := strconv.ParseInt(r.PathValue("jobId"), 10, 64)
	if err != nil || jobID < 1 {
		writeError(w, http.StatusBadRequest, "任务编号无效")
		return
	}
	status, err := s.Personal.ManualDailyStatus(r.Context(), u.ID, jobID)
	if errors.Is(err, personal.ErrNotFound) {
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法读取日报任务")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobId": jobID, "status": status})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request, u userContext) {
	items, err := s.Personal.History(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "历史信息暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    contentResponses(items, time.Now(), s.Config.ImageProxySignSecret, "image-336", 4),
		"readOnly": true,
	})
}
