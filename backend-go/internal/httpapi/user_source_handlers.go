package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"aihot/backend-go/internal/usersources"
)

func (s *Server) userSourcesList(w http.ResponseWriter, r *http.Request, user userContext) {
	if s.UserSources == nil {
		writeError(w, http.StatusServiceUnavailable, "服务暂不可用")
		return
	}
	result, err := s.UserSources.List(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "暂时无法读取信息源")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": result})
}

func (s *Server) userSourcesCreate(w http.ResponseWriter, r *http.Request, user userContext) {
	var input usersources.Input
	if !readUserSourceInput(w, r, &input) {
		return
	}
	result, err := s.UserSources.Create(r.Context(), user.ID, input)
	if err != nil {
		writeUserSourceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

type userSourceDiscoverInput struct {
	URL string `json:"url"`
}

/** userSourcesDiscover handles automatic discovery of RSS/Atom feeds or Sitemap XMLs from a given website URL. */
func (s *Server) userSourcesDiscover(w http.ResponseWriter, r *http.Request, user userContext) {
	if s.FeedDiscovery == nil {
		writeError(w, http.StatusServiceUnavailable, "探测服务暂不可用")
		return
	}

	var input userSourceDiscoverInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}

	rawURL := input.URL
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "请输入需要探测的网址")
		return
	}

	// Execute feed and sitemap discovery with context timeout
	result, err := s.FeedDiscovery.Discover(r.Context(), rawURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) userSourceGet(w http.ResponseWriter, r *http.Request, user userContext) {
	id, ok := userSourceID(w, r)
	if !ok {
		return
	}
	result, err := s.UserSources.Get(r.Context(), user.ID, id)
	if err != nil {
		writeUserSourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) userSourceUpdate(w http.ResponseWriter, r *http.Request, user userContext) {
	id, ok := userSourceID(w, r)
	if !ok {
		return
	}
	var input usersources.Input
	if !readUserSourceInput(w, r, &input) {
		return
	}
	result, err := s.UserSources.Update(r.Context(), user.ID, id, input)
	if err != nil {
		writeUserSourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) userSourceDelete(w http.ResponseWriter, r *http.Request, user userContext) {
	id, ok := userSourceID(w, r)
	if !ok {
		return
	}
	if err := s.UserSources.Delete(r.Context(), user.ID, id); err != nil {
		writeUserSourceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) userSourceFetch(w http.ResponseWriter, r *http.Request, user userContext) {
	id, ok := userSourceID(w, r)
	if !ok {
		return
	}
	result, err := s.UserSources.EnqueueFetch(r.Context(), user.ID, id)
	if err != nil {
		if result.Status == "disabled" {
			writeJSON(w, http.StatusServiceUnavailable, result)
			return
		}
		writeUserSourceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) userSourceFetchStatus(w http.ResponseWriter, r *http.Request, user userContext) {
	id, ok := userSourceID(w, r)
	if !ok {
		return
	}
	jobID, err := strconv.ParseInt(r.PathValue("jobId"), 10, 64)
	if err != nil || jobID < 1 {
		writeError(w, http.StatusBadRequest, "请求参数无效")
		return
	}
	result, err := s.UserSources.FetchStatus(r.Context(), user.ID, id, jobID)
	if err != nil {
		writeUserSourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func readUserSourceInput(w http.ResponseWriter, r *http.Request, dst *usersources.Input) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "请检查信息源设置")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "请求内容无效")
		return false
	}
	if err := usersources.ValidateInput(*dst); err != nil {
		writeError(w, http.StatusBadRequest, "请检查名称、信源地址和时间参数")
		return false
	}
	return true
}

func userSourceID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "信息源编号无效")
		return 0, false
	}
	return id, true
}

/** userIngestArticle handles direct manual ingestion of a single article into the user's isolated external source. */
func (s *Server) userIngestArticle(w http.ResponseWriter, r *http.Request, user userContext) {
	if s.UserSources == nil {
		writeError(w, http.StatusServiceUnavailable, "服务暂不可用")
		return
	}

	var input usersources.IngestInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}

	// Persist single article via usersources service
	result, err := s.UserSources.IngestDirect(r.Context(), user.ID, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func writeUserSourceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, usersources.ErrNotFound):
		writeError(w, http.StatusNotFound, "信息源不存在")
	case errors.Is(err, usersources.ErrInvalid):
		writeError(w, http.StatusBadRequest, "请检查名称、信源地址和时间参数")
	case errors.Is(err, usersources.ErrAlreadySubscribed):
		writeError(w, http.StatusConflict, "你已添加这个信源，请直接编辑已有订阅")
	default:
		writeError(w, http.StatusInternalServerError, "信息源操作失败")
	}
}
