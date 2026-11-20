package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aihot/backend-go/internal/personal"
)

type modelInput struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

type modelResponse struct {
	Configured bool   `json:"configured"`
	BaseURL    string `json:"baseUrl"`
	Model      string `json:"model"`
	APIKeySet  bool   `json:"apiKeySet,omitempty"`
}

func (s *Server) getModel(w http.ResponseWriter, r *http.Request, u userContext) {
	value, err := s.Personal.GetModel(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "模型设置暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, modelResponse{Configured: value.Configured, BaseURL: value.BaseURL, Model: value.Model, APIKeySet: value.APIKeySet})
}

func (s *Server) putModel(w http.ResponseWriter, r *http.Request, u userContext) {
	var input modelInput
	if !readJSON(w, r, &input) {
		return
	}
	value, err := s.Personal.PutModel(r.Context(), u.ID, personal.ModelInput{BaseURL: input.BaseURL, APIKey: input.APIKey, Model: input.Model})
	var invalid *personal.InputError
	switch {
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, invalid.Message)
	case errors.Is(err, personal.ErrEncryptionNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "模型密钥加密尚未配置")
	case errors.Is(err, personal.ErrStoredKeyUnavailable):
		writeError(w, http.StatusServiceUnavailable, "已保存的 API Key 无法解密")
	case errors.Is(err, personal.ErrReadUnavailable):
		writeError(w, http.StatusServiceUnavailable, "模型设置暂不可用")
	case errors.Is(err, personal.ErrSaveUnavailable):
		writeError(w, http.StatusServiceUnavailable, "模型设置暂不可保存")
	case errors.Is(err, personal.ErrEncryption):
		writeError(w, http.StatusInternalServerError, "模型设置暂不可保存")
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "模型设置暂不可保存")
	default:
		writeJSON(w, http.StatusOK, modelResponse{Configured: value.Configured, BaseURL: value.BaseURL, Model: value.Model, APIKeySet: value.APIKeySet})
	}
}

func (s *Server) testModel(w http.ResponseWriter, r *http.Request, u userContext) {
	if s.limiter.limited("model-test:"+strconv.FormatInt(u.ID, 10), 10, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "模型测试次数过多，请稍后重试")
		return
	}
	var input modelInput
	if !readJSON(w, r, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	receipt, err := s.Personal.StartModelTest(ctx, u.ID, personal.ModelInput{BaseURL: input.BaseURL, APIKey: input.APIKey, Model: input.Model}, s.EnqueueConnectionTest)
	var invalid *personal.InputError
	if errors.As(err, &invalid) {
		writeError(w, http.StatusBadRequest, invalid.Message)
		return
	}
	if errors.Is(err, personal.ErrQueueUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "测试任务无法入队，请确认 Worker 已启动")
		return
	}
	if errors.Is(err, personal.ErrEncryptionNotConfigured) {
		writeError(w, http.StatusServiceUnavailable, "模型密钥加密尚未配置")
		return
	}
	if errors.Is(err, personal.ErrReceiptUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "调用记录暂不可用")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "调用记录或模型密钥暂不可用")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"testId": receipt, "status": "pending"})
}

func (s *Server) getModelTest(w http.ResponseWriter, r *http.Request, u userContext) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "测试记录不存在")
		return
	}
	value, err := s.Personal.ModelTest(r.Context(), id, u.ID, s.ConnectionTestKind)
	if errors.Is(err, personal.ErrNotFound) {
		writeError(w, http.StatusNotFound, "测试记录不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "测试记录暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"testId": id, "status": value.Status, "usage": value.Usage, "error": value.Error})
}

func (s *Server) getInterests(w http.ResponseWriter, r *http.Request, u userContext) {
	value, err := s.Personal.GetInterests(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "关注项暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keywords": value.Keywords, "entityIds": value.EntityIDs, "labelIds": value.LabelIDs, "version": value.Version})
}

func (s *Server) putInterests(w http.ResponseWriter, r *http.Request, u userContext) {
	var input struct {
		Keywords  []string `json:"keywords"`
		EntityIDs []string `json:"entityIds"`
		LabelIDs  []string `json:"labelIds"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	value, err := s.Personal.PutInterests(r.Context(), u.ID, personal.Interests{Keywords: input.Keywords, EntityIDs: input.EntityIDs, LabelIDs: input.LabelIDs})
	var invalid *personal.InputError
	if errors.As(err, &invalid) {
		writeError(w, http.StatusBadRequest, invalid.Message)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "关注项暂不可保存")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keywords": value.Keywords, "entityIds": value.EntityIDs, "labelIds": value.LabelIDs})
}

func (s *Server) getEntities(w http.ResponseWriter, _ *http.Request, _ userContext) {
	setPrivateSiteHeaders(w)
	type entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	entities := s.Personal.Entities()
	items := make([]entry, 0, len(entities))
	for id, e := range entities {
		items = append(items, entry{ID: id, Name: e.Name})
	}
	// Keep deterministic ordering for SSR and browser forms.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && strings.ToLower(items[j].Name) < strings.ToLower(items[j-1].Name); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": items})
}
