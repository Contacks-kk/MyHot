package personal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"aihot/backend-go/internal/auth"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/modelconfig"
)

var ErrNotFound = errors.New("personal resource not found")

type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

var ErrUnavailable = errors.New("personal service unavailable")
var ErrQueueUnavailable = errors.New("personal job queue unavailable")
var ErrEncryption = errors.New("personal credential encryption failed")
var ErrEncryptionNotConfigured = errors.New("personal credential encryption is not configured")
var ErrStoredKeyUnavailable = errors.New("stored personal credential is unavailable")
var ErrReadUnavailable = errors.New("personal settings could not be read")
var ErrSaveUnavailable = errors.New("personal settings could not be saved")
var ErrReceiptUnavailable = errors.New("personal model call receipt could not be saved")

type Service struct {
	repository *Repository
	config     config.Config
	entities   map[string]interests.Entity
}

/** NewService creates the personal service with its entity catalog. */
func NewService(repository *Repository, cfg config.Config, entities map[string]interests.Entity) *Service {
	return &Service{
		repository: repository,
		config:     cfg,
		entities:   entities,
	}
}

type ModelInput struct {
	BaseURL string
	APIKey  string
	Model   string
}

type ModelView struct {
	Configured bool
	BaseURL    string
	Model      string
	APIKeySet  bool
}

type ModelCredentials struct {
	BaseURL string
	APIKey  string
	Model   string
}

type ConnectionTest struct {
	UserID           int64
	ReceiptID        int64
	BaseURL          string
	Model            string
	APIKeyCiphertext string
}

func (s *Service) GetModel(ctx context.Context, userID int64) (ModelView, error) {
	value, err := s.repository.ModelConfig(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return ModelView{Configured: false, BaseURL: "", Model: ""}, nil
	}
	if err != nil {
		return ModelView{}, err
	}
	return ModelView{Configured: true, BaseURL: value.BaseURL, Model: value.Model, APIKeySet: true}, nil
}

func (s *Service) PutModel(ctx context.Context, userID int64, input ModelInput) (ModelView, error) {
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.Model = strings.TrimSpace(input.Model)
	input.APIKey = strings.TrimSpace(input.APIKey)
	if len(s.config.UserAPIKeyEncryptionKey) != 32 {
		return ModelView{}, ErrEncryptionNotConfigured
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	base, err := modelconfig.ValidateBaseURL(checkCtx, input.BaseURL)
	if err != nil {
		return ModelView{}, &InputError{Message: err.Error()}
	}
	if len(input.Model) < 1 || len(input.Model) > 160 || strings.ContainsAny(input.Model, "\r\n") || len(input.APIKey) > 4096 || strings.ContainsAny(input.APIKey, "\r\n") {
		return ModelView{}, &InputError{Message: "模型名或 API Key 长度无效"}
	}
	if input.APIKey == "" {
		current, getErr := s.repository.ModelConfig(ctx, userID)
		if errors.Is(getErr, ErrNotFound) {
			return ModelView{}, &InputError{Message: "首次保存时必须填写 API Key"}
		}
		if getErr != nil {
			return ModelView{}, ErrReadUnavailable
		}
		secret, decryptErr := modelconfig.Decrypt(s.config.UserAPIKeyEncryptionKey, current.APIKeyCiphertext)
		if decryptErr != nil {
			return ModelView{}, ErrStoredKeyUnavailable
		}
		input.APIKey = string(secret)
		clear(secret)
	}
	ciphertext, err := modelconfig.Encrypt(s.config.UserAPIKeyEncryptionKey, []byte(input.APIKey))
	if err != nil {
		return ModelView{}, ErrEncryption
	}
	if err = s.repository.SaveModelConfig(ctx, userID, base.String(), ciphertext, input.Model); err != nil {
		return ModelView{}, ErrSaveUnavailable
	}
	return ModelView{Configured: true, BaseURL: base.String(), Model: input.Model, APIKeySet: true}, nil
}

func (s *Service) ModelForUser(ctx context.Context, userID int64, input ModelInput) (ModelCredentials, error) {
	base := strings.TrimSpace(input.BaseURL)
	key := strings.TrimSpace(input.APIKey)
	name := strings.TrimSpace(input.Model)
	if base == "" || name == "" || len(name) > 160 || strings.ContainsAny(name, "\r\n") || len(key) > 4096 || strings.ContainsAny(key, "\r\n") {
		return ModelCredentials{}, &InputError{Message: "请填写 Base URL 和模型名"}
	}
	parsed, err := modelconfig.ValidateBaseURL(ctx, base)
	if err != nil {
		return ModelCredentials{}, &InputError{Message: err.Error()}
	}
	if key == "" {
		current, getErr := s.repository.ModelConfig(ctx, userID)
		if getErr != nil {
			return ModelCredentials{}, &InputError{Message: "请先填写并保存 API Key"}
		}
		secret, decryptErr := modelconfig.Decrypt(s.config.UserAPIKeyEncryptionKey, current.APIKeyCiphertext)
		if decryptErr != nil {
			return ModelCredentials{}, &InputError{Message: "已保存的 API Key 无法解密"}
		}
		key = string(secret)
		clear(secret)
	}
	return ModelCredentials{BaseURL: parsed.String(), APIKey: key, Model: name}, nil
}

type ConnectionTestEnqueuer func(context.Context, ConnectionTest) error

func (s *Service) StartModelTest(ctx context.Context, userID int64, input ModelInput, enqueue ConnectionTestEnqueuer) (int64, error) {
	credentials, err := s.ModelForUser(ctx, userID, input)
	if err != nil {
		return 0, err
	}
	ciphertext, err := modelconfig.Encrypt(s.config.UserAPIKeyEncryptionKey, []byte(credentials.APIKey))
	if err != nil {
		if len(s.config.UserAPIKeyEncryptionKey) != 32 {
			return 0, ErrEncryptionNotConfigured
		}
		return 0, ErrUnavailable
	}
	logical := fmt.Sprintf("test:%d:%d", userID, time.Now().UnixNano())
	requestHash := auth.HashToken("请只返回 JSON：{\"ok\":true,\"message\":\"连接成功\"}")
	receiptID, err := s.repository.StartReceipt(ctx, userID, "connection_test", "", credentials.Model, logical, requestHash)
	if err != nil {
		return 0, ErrReceiptUnavailable
	}
	args := ConnectionTest{UserID: userID, ReceiptID: receiptID, BaseURL: credentials.BaseURL, Model: credentials.Model, APIKeyCiphertext: ciphertext}
	if err := enqueue(ctx, args); err != nil {
		_ = s.repository.FailReceipt(ctx, receiptID, "测试任务无法入队，请确认 Worker 已启动")
		return 0, ErrQueueUnavailable
	}
	return receiptID, nil
}

func (s *Service) ModelTest(ctx context.Context, id, userID int64, kind string) (ReceiptStatus, error) {
	value, err := s.repository.Receipt(ctx, id, userID)
	if errors.Is(err, ErrNotFound) {
		return ReceiptStatus{}, ErrNotFound
	}
	if err != nil {
		return ReceiptStatus{}, err
	}
	if value.Status == "pending" {
		terminal, checkErr := s.repository.TestJobTerminal(ctx, id, kind)
		if checkErr == nil && terminal {
			value.Status = "failed"
			message := "测试任务未完成，请确认 Worker 运行后重试"
			value.Error = &message
			_ = s.repository.FailReceipt(ctx, id, message)
		}
	}
	return value, nil
}

func (s *Service) GetInterests(ctx context.Context, userID int64) (Interests, error) {
	value, err := s.repository.Interests(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return Interests{Keywords: []string{}, EntityIDs: []string{}, LabelIDs: []string{}, Version: 1}, nil
	}
	if err != nil {
		return Interests{}, err
	}
	if value.Keywords == nil {
		value.Keywords = []string{}
	}
	if value.EntityIDs == nil {
		value.EntityIDs = []string{}
	}
	return value, nil
}

func (s *Service) PutInterests(ctx context.Context, userID int64, input Interests) (Interests, error) {
	if len(input.Keywords) > 30 || len(input.EntityIDs) > 100 || len(input.LabelIDs) > 100 {
		return Interests{}, &InputError{Message: "关键词或实体数量超过上限"}
	}
	seen := map[string]bool{}
	keywords := make([]string, 0, len(input.Keywords))
	for _, value := range input.Keywords {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len([]rune(value)) > 100 {
			return Interests{}, &InputError{Message: "单个关键词不能超过 100 字"}
		}
		key := strings.ToLower(value)
		if !seen[key] {
			keywords = append(keywords, value)
			seen[key] = true
		}
	}
	seen = map[string]bool{}
	ids := make([]string, 0, len(input.EntityIDs))
	for _, id := range input.EntityIDs {
		id = strings.TrimSpace(id)
		if _, ok := s.entities[id]; !ok {
			return Interests{}, &InputError{Message: "包含未知公司实体"}
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if input.LabelIDs == nil {
		if err := s.repository.SaveInterests(ctx, userID, keywords, ids); err != nil {
			return Interests{}, err
		}
	} else {
		labels := make([]string, 0, len(input.LabelIDs))
		seen := map[string]bool{}
		for _, id := range input.LabelIDs {
			if !seen[id] {
				seen[id] = true
				labels = append(labels, id)
			}
		}
		if err := s.repository.SaveInterestsWithLabels(ctx, userID, keywords, ids, labels); err != nil {
			return Interests{}, err
		}
	}
	return s.GetInterests(ctx, userID)
}

func (s *Service) Entities() map[string]interests.Entity { return s.entities }

func (s *Service) Feed(ctx context.Context, userID int64) ([]Card, error) {
	return s.repository.Feed(ctx, userID)
}

type HotQuery struct {
	Range string
	Date  string
	Page  int
}

type hotWindow struct {
	Start  time.Time
	End    time.Time
	Limit  int
	Offset int
}

type HotPage struct {
	Items   []Card
	Page    int
	HasMore bool
}

/** hotWindowFor validates the selection and resolves Beijing calendar days or a rolling 48-hour window. */
func hotWindowFor(query HotQuery, now time.Time) (hotWindow, error) {
	if query.Page < 1 || query.Page > 100000 {
		return hotWindow{}, &InputError{Message: "页码无效"}
	}
	if query.Range == "" || query.Range == "48h" {
		if query.Page != 1 {
			return hotWindow{}, &InputError{Message: "48 小时榜单仅提供前 10 条"}
		}
		return hotWindow{
			Start: now.Add(-48 * time.Hour),
			End:   now,
			Limit: 10,
		}, nil
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return hotWindow{}, err
	}
	today := now.In(location).Format(time.DateOnly)
	date := today
	if query.Range == "date" {
		date = query.Date
	} else if query.Range != "today" {
		return hotWindow{}, &InputError{Message: "时间范围无效"}
	}
	start, err := time.ParseInLocation(time.DateOnly, date, location)
	if err != nil || date > today {
		return hotWindow{}, &InputError{Message: "请选择有效的历史日期"}
	}
	end := start.AddDate(0, 0, 1)
	if end.After(now) {
		end = now
	}
	return hotWindow{
		Start:  start,
		End:    end,
		Limit:  50,
		Offset: (query.Page - 1) * 50,
	}, nil
}

/** Hot reads already processed personal results in the selected window, with pagination for calendar days. */
func (s *Service) Hot(ctx context.Context, userID int64, query HotQuery, now time.Time) (HotPage, error) {
	window, err := hotWindowFor(query, now)
	if err != nil {
		return HotPage{}, err
	}
	items, err := s.repository.Hot(ctx, userID, window)
	if err != nil {
		return HotPage{}, err
	}
	hasMore := len(items) > window.Limit && window.Limit == 50
	if len(items) > window.Limit {
		items = items[:window.Limit]
	}
	return HotPage{
		Items:   items,
		Page:    query.Page,
		HasMore: hasMore,
	}, nil
}

/** RecommendedArticles retrieves personalized article recommendations from un-subscribed sources. */
func (s *Service) RecommendedArticles(ctx context.Context, userID int64, page, limit int) ([]RecommendedArticleItem, bool, error) {
	return s.repository.RecommendedArticles(ctx, userID, page, limit)
}

func (s *Service) Daily(ctx context.Context, userID int64) ([]DailyReport, error) {
	return s.repository.Daily(ctx, userID)
}

/** Article reads one personal result for the requesting user. */
func (s *Service) Article(ctx context.Context, userID int64, articleID string) (ArticleDetail, error) {
	return s.repository.Article(ctx, userID, articleID)
}

/** ManualDailyStatus returns the current user's manual report job state. */
func (s *Service) ManualDailyStatus(ctx context.Context, userID, jobID int64) (string, error) {
	return s.repository.ManualDailyStatus(ctx, userID, jobID)
}

func (s *Service) History(ctx context.Context) ([]Card, error) {
	return s.repository.History(ctx)
}

/** Funnel returns today's scan and filter funnel metrics for the user. */
func (s *Service) Funnel(ctx context.Context, userID int64) (FunnelView, error) {
	return s.repository.Funnel(ctx, userID)
}
