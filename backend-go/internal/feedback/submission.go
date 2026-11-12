package feedback

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"

	_ "golang.org/x/image/webp"
)

const (
	feedbackMaxScreenshot = 8 << 20
	feedbackMaxPixels     = 60_000_000
)

var userAgentFamily = regexp.MustCompile(`Chrome|Safari|Firefox|Edg|MicroMessenger|Mobile|Android|iPhone|iPad|Mac OS X|Windows`)
var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

type Rejected struct {
	Status     int
	Code       string
	Message    string
	RetryAfter int
}

func (e *Rejected) Error() string { return e.Message }

type SubmitInput struct {
	Content       string
	Email         string
	PageURL       string
	Screenshot    []byte
	HasScreenshot bool
	IP            string
	UserAgent     string
}

type SubmissionResult struct {
	ID int64 `json:"id"`
}

func (s *Service) Submit(ctx context.Context, input SubmitInput) (SubmissionResult, error) {
	if s.submissions == nil {
		return SubmissionResult{}, errors.New("feedback submission repository is not configured")
	}
	content := trimJS(input.Content)
	if utf16Length(content) < 2 {
		return SubmissionResult{}, reject(400, "invalid_request", "请写下反馈内容。", 0)
	}
	if utf16Length(content) > 5000 {
		return SubmissionResult{}, reject(400, "invalid_request", "反馈内容最多 5000 字。", 0)
	}
	email := trimJS(input.Email)
	if email != "" && (utf16Length(email) > 200 || !emailPattern.MatchString(email)) {
		return SubmissionResult{}, reject(400, "invalid_request", "邮箱格式不正确。", 0)
	}
	pageURL := truncateUTF16(trimJS(input.PageURL), 500)
	source := sourceHash(s.cfg.SessionSecret, input.IP, input.UserAgent)
	banned, err := s.submissions.Banned(ctx, []string{source})
	if err != nil {
		return SubmissionResult{}, err
	}
	if banned {
		return SubmissionResult{}, reject(403, "forbidden", "暂时无法提交反馈。", 0)
	}
	if s.limiter == nil {
		s.limiter = newSubmissionLimiter()
	}
	if !s.limiter.allow(source, time.Now()) {
		return SubmissionResult{}, reject(429, "rate_limited", "提交太频繁，请稍后再试。", 60)
	}

	var emailValue, pageURLValue *string
	if email != "" {
		emailValue = &email
	}
	if pageURL != "" {
		pageURLValue = &pageURL
	}
	var screenshotKey *string
	if input.HasScreenshot {
		key, path, err := s.storeScreenshot(input.Screenshot)
		if err != nil {
			return SubmissionResult{}, err
		}
		screenshotKey = &key
		id, err := s.submissions.Create(ctx, Submission{
			Content: content, Email: emailValue, PageURL: pageURLValue, ScreenshotKey: screenshotKey, SourceHash: source,
		})
		if err != nil {
			_ = os.Remove(path)
			return SubmissionResult{}, err
		}
		s.enqueueForward(ctx, id)
		return SubmissionResult{ID: id}, nil
	}
	id, err := s.submissions.Create(ctx, Submission{Content: content, Email: emailValue, PageURL: pageURLValue, SourceHash: source})
	if err != nil {
		return SubmissionResult{}, err
	}
	s.enqueueForward(ctx, id)
	return SubmissionResult{ID: id}, nil
}

func (s *Service) enqueueForward(ctx context.Context, id int64) {
	if s.enqueue != nil && s.cfg.FeishuInternalEnabled && strings.TrimSpace(s.cfg.FeishuInternalChatID) != "" {
		// The periodic recovery scan will retry rows left pending if River is temporarily unavailable.
		_ = s.enqueue(ctx, id)
	}
}

type Forwarder interface {
	UploadImage(context.Context, []byte, string) (string, error)
	SendFeedback(context.Context, string, int64, string, string, string, string, *string, *string) error
}

func (s *Service) Forward(ctx context.Context, id int64) error {
	if !s.cfg.FeishuInternalEnabled || strings.TrimSpace(s.cfg.FeishuInternalChatID) == "" || s.forwarder == nil {
		return nil
	}
	if s.submissions == nil {
		return errors.New("feedback submission repository is not configured")
	}
	row, err := s.submissions.ForwardRecord(ctx, id)
	if err != nil {
		return s.saveForwardError(ctx, id, err)
	}
	if row == nil {
		return nil
	}
	var imageKey, imageNote *string
	if row.ScreenshotKey != nil {
		key := *row.ScreenshotKey
		switch {
		case strings.HasPrefix(key, "feishu:"):
			value := strings.TrimPrefix(key, "feishu:")
			imageKey = &value
		case key == "gone:upload":
			imageNote = stringPtr("（截图未能上传，已删除）")
		case key == "gone:missing":
			imageNote = stringPtr("（截图文件已不存在）")
		case strings.HasPrefix(key, "local:"):
			name := strings.TrimPrefix(key, "local:")
			if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
				return s.saveForwardError(ctx, id, errors.New("invalid feedback screenshot key"))
			}
			path := filepath.Join(s.cfg.DataDir, "feedback-screenshots", name)
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				if err := s.submissions.SetScreenshotKey(ctx, id, "gone:missing"); err != nil {
					return s.saveForwardError(ctx, id, err)
				}
				imageNote = stringPtr("（截图文件已不存在）")
				break
			}
			uploaded, uploadErr := s.forwarder.UploadImage(ctx, data, name)
			if uploadErr != nil {
				if !errors.Is(uploadErr, ErrImageRefused) && time.Since(row.CreatedAt) < 24*time.Hour {
					return s.saveForwardError(ctx, id, uploadErr)
				}
				if err := s.submissions.SetScreenshotKey(ctx, id, "gone:upload"); err != nil {
					return s.saveForwardError(ctx, id, err)
				}
				_ = os.Remove(path)
				imageNote = stringPtr("（截图未能上传，已删除）")
				break
			}
			if err := s.submissions.SetScreenshotKey(ctx, id, "feishu:"+uploaded); err != nil {
				return s.saveForwardError(ctx, id, err)
			}
			_ = os.Remove(path)
			imageKey = &uploaded
		}
	}
	if err := s.forwarder.SendFeedback(ctx, s.cfg.FeishuInternalChatID, row.ID, row.Content, valueOrEmpty(row.PageURL), valueOrEmpty(row.Email), valueOrEmpty(row.Note), imageKey, imageNote); err != nil {
		return s.saveForwardError(ctx, id, err)
	}
	if err := s.submissions.MarkForwarded(ctx, id, time.Now().UTC()); err != nil {
		return s.saveForwardError(ctx, id, err)
	}
	return nil
}

// ForwardPending matches the old ten-minute retry sweep: oldest first, at most twenty, and only
// unsent rows between five minutes and seven days old.
func (s *Service) ForwardPending(ctx context.Context, now time.Time) (int, int, error) {
	if !s.cfg.FeishuInternalEnabled || strings.TrimSpace(s.cfg.FeishuInternalChatID) == "" || s.forwarder == nil || s.submissions == nil {
		return 0, 0, nil
	}
	ids, err := s.submissions.PendingForwardIDs(ctx, now.Add(-5*time.Minute), now.Add(-7*24*time.Hour), 20)
	if err != nil {
		return 0, 0, err
	}
	var sent, failed int
	for _, id := range ids {
		if err := s.Forward(ctx, id); err != nil {
			failed++
		} else {
			sent++
		}
	}
	return sent, failed, nil
}

func (s *Service) PendingForwardIDs(ctx context.Context, now time.Time) ([]int64, error) {
	if !s.cfg.FeishuInternalEnabled || strings.TrimSpace(s.cfg.FeishuInternalChatID) == "" || s.forwarder == nil || s.submissions == nil {
		return []int64{}, nil
	}
	return s.submissions.PendingForwardIDs(ctx, now.Add(-5*time.Minute), now.Add(-7*24*time.Hour), 20)
}

func (s *Service) saveForwardError(ctx context.Context, id int64, cause error) error {
	message := truncateUTF16(cause.Error(), 300)
	if err := s.submissions.SetForwardError(ctx, id, message); err != nil {
		return fmt.Errorf("%v; record forwarding error: %w", cause, err)
	}
	return cause
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *Service) storeScreenshot(data []byte) (string, string, error) {
	if len(data) > feedbackMaxScreenshot {
		return "", "", reject(400, "invalid_request", "截图最大 8MB。", 0)
	}
	mimeType, err := sniffImage(data)
	if err != nil {
		return "", "", reject(400, "invalid_request", "截图需要是 PNG、JPG、WebP 或 GIF。", 0)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > feedbackMaxPixels {
		return "", "", reject(400, "invalid_request", "截图无法识别，请换一张图片。", 0)
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return "", "", reject(400, "invalid_request", "截图无法识别，请换一张图片。", 0)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", err
	}
	encoded := hex.EncodeToString(random[:])
	name := fmt.Sprintf("%s-%s-%s-%s-%s.%s", encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:], strings.TrimPrefix(mimeType, "image/"))
	directory := filepath.Join(s.cfg.DataDir, "feedback-screenshots")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", "", err
	}
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return "", "", writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", "", closeErr
	}
	return "local:" + name, path, nil
}

func sniffImage(data []byte) (string, error) {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", nil
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", nil
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif", nil
	default:
		return "", errors.New("unknown image signature")
	}
}

func sourceHash(secret, ip, userAgent string) string {
	if secret == "" {
		secret = "dev-feedback-secret"
	}
	family := userAgentFamily.FindAllString(userAgent, 4)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(mac, ip+"|"+strings.Join(family, "/"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))[:24]
}

type submissionLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newSubmissionLimiter() *submissionLimiter {
	return &submissionLimiter{attempts: map[string][]time.Time{}}
}

func (l *submissionLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.attempts[key][:0]
	for _, at := range l.attempts[key] {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	if len(recent) >= 5 {
		l.attempts[key] = recent
		return false
	}
	recent = append(recent, now)
	l.attempts[key] = recent
	if len(l.attempts) > 5000 {
		for candidate, values := range l.attempts {
			fresh := false
			for _, at := range values {
				if now.Sub(at) < time.Minute {
					fresh = true
					break
				}
			}
			if !fresh {
				delete(l.attempts, candidate)
			}
		}
	}
	return true
}

func reject(status int, code, message string, retryAfter int) error {
	return &Rejected{Status: status, Code: code, Message: message, RetryAfter: retryAfter}
}

func trimJS(value string) string {
	return strings.TrimFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func truncateUTF16(value string, limit int) string {
	units := utf16.Encode([]rune(value))
	if len(units) <= limit {
		return value
	}
	return string(utf16.Decode(units[:limit]))
}

func stringPtr(value string) *string { return &value }
