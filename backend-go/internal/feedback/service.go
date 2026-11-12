package feedback

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var feedbackStatuses = map[string]struct{}{"new": {}, "triaged": {}, "replied": {}, "resolved": {}, "spam": {}}
var screenshotName = regexp.MustCompile(`^[\w.-]+$`)

type Config struct {
	DataDir               string
	SessionSecret         string
	FeishuInternalEnabled bool
	FeishuInternalChatID  string
}

type Service struct {
	repo        Repository
	submissions SubmissionRepository
	cfg         Config
	forwarder   Forwarder
	enqueue     func(context.Context, int64) error
	limiter     *submissionLimiter
}

func NewService(repo Repository, cfg Config, forwarders ...Forwarder) *Service {
	service := &Service{repo: repo, cfg: cfg, limiter: newSubmissionLimiter()}
	service.submissions, _ = repo.(SubmissionRepository)
	if len(forwarders) > 0 {
		service.forwarder = forwarders[0]
	}
	return service
}

func (s *Service) SetForwardEnqueuer(enqueue func(context.Context, int64) error) { s.enqueue = enqueue }

type FeedbackRow struct {
	ID           int64      `json:"id"`
	Content      string     `json:"content"`
	Email        *string    `json:"email"`
	PageURL      *string    `json:"page_url"`
	Screenshot   *string    `json:"screenshot"`
	SourceHash   string     `json:"source_hash"`
	Status       string     `json:"status"`
	Note         *string    `json:"note"`
	ForwardedAt  *time.Time `json:"forwarded_at"`
	ForwardError *string    `json:"forward_error"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Banned       bool       `json:"banned"`
	FromSource   int        `json:"from_source"`
}

type BanRow struct {
	SourceHash string    `json:"source_hash"`
	Reason     *string   `json:"reason"`
	CreatedBy  *string   `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
}

type ListResult struct {
	Page   int            `json:"page"`
	Rows   []FeedbackRow  `json:"rows"`
	Counts map[string]int `json:"counts"`
	Bans   []BanRow       `json:"bans"`
}

type Patch struct {
	Status  *string
	Note    *string
	NoteSet bool
	Version string
}

type Screenshot struct {
	Bytes       []byte
	ContentType string
}

func (s *Service) List(ctx context.Context, status, query string, page int) (ListResult, error) {
	if page < 1 {
		page = 1
	}
	result, err := s.repo.List(ctx, status, strings.TrimSpace(query), page)
	if err != nil {
		return ListResult{}, err
	}
	result.Page = page
	if result.Rows == nil {
		result.Rows = []FeedbackRow{}
	}
	if result.Counts == nil {
		result.Counts = map[string]int{}
	}
	if result.Bans == nil {
		result.Bans = []BanRow{}
	}
	return result, nil
}

func (s *Service) Update(ctx context.Context, id int64, patch Patch, actor string) (map[string]any, bool, error) {
	if patch.Status != nil && *patch.Status != "" {
		if _, ok := feedbackStatuses[*patch.Status]; !ok {
			return nil, false, fmt.Errorf("unknown status %s", *patch.Status)
		}
	}
	return s.repo.Update(ctx, id, patch, actor)
}

func (s *Service) Screenshot(ctx context.Context, id int64) (*Screenshot, error) {
	key, err := s.repo.ScreenshotKey(ctx, id)
	if err != nil || key == nil {
		return nil, err
	}
	path := s.screenshotPath(*key)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	contentType := mime.TypeByExtension(ext)
	if ext == ".jpg" || ext == ".jpeg" {
		contentType = "image/jpeg"
	} else if contentType == "" {
		contentType = "image/" + strings.TrimPrefix(ext, ".")
	}
	return &Screenshot{Bytes: data, ContentType: contentType}, nil
}

func (s *Service) Erase(ctx context.Context, id int64, reason, actor string) (bool, error) {
	if strings.TrimSpace(reason) == "" {
		return false, errors.New("reason is required")
	}
	key, err := s.repo.ScreenshotKey(ctx, id)
	if err != nil {
		return false, err
	}
	if key != nil {
		if path := s.screenshotPath(*key); path != "" {
			_ = os.Remove(path)
		}
	}
	return s.repo.Erase(ctx, id, reason, actor)
}

func (s *Service) Ban(ctx context.Context, sourceHash, reason, actor string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("reason is required")
	}
	return s.repo.Ban(ctx, sourceHash, reason, actor)
}

func (s *Service) Unban(ctx context.Context, sourceHash, actor string) error {
	return s.repo.Unban(ctx, sourceHash, actor)
}

func (s *Service) screenshotPath(key string) string {
	if !strings.HasPrefix(key, "local:") {
		return ""
	}
	name := strings.TrimPrefix(key, "local:")
	if !screenshotName.MatchString(name) {
		return ""
	}
	return filepath.Join(s.cfg.DataDir, "feedback-screenshots", name)
}
