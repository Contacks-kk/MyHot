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
	ArticleID   string               `json:"articleId"`
	Score       float64              `json:"score"`
	Title       string               `json:"title"`
	Summary     string               `json:"summary"`
	Reason      *string              `json:"reason"`
	URL         string               `json:"url"`
	Source      string               `json:"source"`
	PublishedAt *time.Time           `json:"publishedAt"`
	CreatedAt   time.Time            `json:"createdAt"`
	Historical  bool                 `json:"historical"`
	Media       []mediaurl.MediaView `json:"media"`
}

/** contentResponses projects personal result rows into API cards with signed media proxy URLs. */
func contentResponses(items []personal.Card, now time.Time, imageSecret, thumbMode string, mediaLimit int) []contentCard {
	responses := make([]contentCard, 0, len(items))
	for _, item := range items {
		mediaViews := mediaurl.BuildViews(item.MediaJSON, thumbMode, now, imageSecret, mediaLimit)
		responses = append(responses, contentCard{
			ArticleID:   item.ArticleID,
			Score:       item.Score,
			Title:       item.Title,
			Summary:     item.Summary,
			Reason:      item.Reason,
			URL:         item.URL,
			Source:      item.Source,
			PublishedAt: item.PublishedAt,
			CreatedAt:   item.CreatedAt,
			Historical:  item.Historical,
			Media:       mediaViews,
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

/** hot returns the authenticated user's top 48-hour hotspots. */
func (s *Server) hot(w http.ResponseWriter, r *http.Request, u userContext) {
	items, err := s.Personal.Hot(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "个人热点暂不可用")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": contentResponses(items, time.Now(), s.Config.ImageProxySignSecret, "image-336", 4),
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

func (s *Server) daily(w http.ResponseWriter, r *http.Request, u userContext) {
	items, err := s.Personal.Daily(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "日报暂不可用")
		return
	}
	reports := make([]map[string]any, 0, len(items))
	for _, item := range items {
		reports = append(reports, map[string]any{"id": item.ID, "date": item.Date, "model": item.Model, "title": item.Title, "summary": item.Summary, "entries": item.Entries, "createdAt": item.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": reports})
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
