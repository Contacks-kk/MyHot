package admindeliveries

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"aihot/backend-go/internal/publication"
)

var ErrInvalidRequest = errors.New("invalid delivery resolution")
var ErrNotResolvable = errors.New("delivery does not need resolution")
var ErrChanged = errors.New("delivery was handled by another request")
var ErrContentPushDisabled = errors.New("content push is disabled in this environment")
var ErrTargetDisabled = errors.New("this content target is disabled")
var ErrContentNotEligible = errors.New("this content no longer qualifies for delivery")

const staleDeliveryAfter = 15 * time.Minute

type StaleCounts struct {
	Unknown int64 `json:"unknown"`
	Failed  int64 `json:"failed"`
}

type Result struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

type Repository interface {
	Resolve(context.Context, int64, string, string, string) (*Result, bool, error)
	MarkStale(context.Context, time.Time) (StaleCounts, error)
}

type ResendRepository interface {
	LoadResend(context.Context, int64) (*ResendRecord, bool, error)
	LoadSelectedContent(context.Context, string) (*SelectedContent, error)
	ClaimResend(context.Context, int64, string, json.RawMessage) (string, bool, error)
	FinishResend(context.Context, int64, string, string, string, string, string, string) error
}

type ContentDeliveryRepository interface {
	LoadSelectedContent(context.Context, string) (*SelectedContent, error)
	AcquireTitleLease(context.Context, string, string, time.Time) (string, error)
	SelectedSiblings(context.Context, int64, string) ([]string, error)
	ContentTargets(context.Context) ([]ContentTarget, error)
	ClaimContentDelivery(context.Context, string, ContentDeliveryClaim) (int64, ContentTarget, bool, error)
	StartContentDelivery(context.Context, int64) error
	FinishContentDelivery(context.Context, int64, string, string) error
}

type WebhookSender interface {
	Send(context.Context, string, any) (WebhookResult, error)
}

type ShareImageWarmer interface {
	Warm(context.Context, SelectedContent) error
}

type WebhookResult struct {
	Status string
	Body   string
}

type Config struct {
	ContentPushEnabled bool
	WebhookURLs        map[string]string
	SiteName           string
	SiteURL            string
	CategoryLabels     map[string]string
}

type Service struct {
	repository Repository
	config     Config
	sender     WebhookSender
	warmer     ShareImageWarmer
}

func NewService(repository Repository, config Config, sender WebhookSender, warmers ...ShareImageWarmer) *Service {
	service := &Service{repository: repository, config: config, sender: sender}
	if len(warmers) > 0 {
		service.warmer = warmers[0]
	}
	return service
}

func (s *Service) Resolve(ctx context.Context, id int64, outcome, note, actor string) (*Result, bool, error) {
	note = strings.TrimFunc(note, isJavaScriptWhitespace)
	if id < 1 || (outcome != "sent" && outcome != "drop" && outcome != "resend") || note == "" {
		return nil, false, ErrInvalidRequest
	}
	if outcome == "resend" {
		return s.resend(ctx, id, note, actor)
	}
	return s.repository.Resolve(ctx, id, outcome, note, actor)
}

func (s *Service) resend(ctx context.Context, id int64, note, actor string) (*Result, bool, error) {
	if !s.config.ContentPushEnabled {
		return nil, false, ErrContentPushDisabled
	}
	repository, ok := s.repository.(ResendRepository)
	if !ok || s.sender == nil {
		return nil, false, errors.New("delivery resend is unavailable")
	}
	delivery, found, err := repository.LoadResend(ctx, id)
	if err != nil || !found || delivery == nil {
		return nil, found, err
	}
	if delivery.Status != "unknown" && delivery.Status != "failed" {
		return nil, true, ErrNotResolvable
	}
	if delivery.Kind != "feishu_webhook" {
		return nil, true, ErrContentPushDisabled
	}
	if !delivery.Enabled {
		return nil, true, ErrTargetDisabled
	}
	webhook := strings.TrimSpace(s.config.WebhookURLs[delivery.ConfigRef])
	if webhook == "" {
		return nil, true, errors.New("webhook not configured")
	}
	payload := json.RawMessage(delivery.Payload)
	if delivery.SubjectKind == "selected" {
		content, err := repository.LoadSelectedContent(ctx, delivery.SubjectID)
		if err != nil {
			return nil, true, err
		}
		if !selectedContentReady(content, delivery.EnabledAt, time.Now()) {
			return nil, true, ErrContentNotEligible
		}
		payload, err = json.Marshal(selectedCard(*content, s.config))
		if err != nil {
			return nil, true, err
		}
	}
	claimVersion, claimed, err := repository.ClaimResend(ctx, id, delivery.Version, payload)
	if err != nil {
		return nil, true, err
	}
	if !claimed {
		return nil, true, ErrChanged
	}
	sendContext, cancelSend := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancelSend()
	result, sendErr := s.sender.Send(sendContext, webhook, json.RawMessage(payload))
	if sendErr != nil {
		result = WebhookResult{Status: "unknown", Body: sendErr.Error()}
	}
	if result.Status != "sent" && result.Status != "failed" {
		result.Status = "unknown"
	}
	result.Body = truncateUTF16(result.Body, 500)
	finishContext, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelFinish()
	if err := repository.FinishResend(finishContext, id, claimVersion, delivery.Status, result.Status, result.Body, actor, note); err != nil {
		return nil, true, err
	}
	return &Result{ID: id, Status: result.Status}, true, nil
}

type ResendRecord struct {
	Status      string
	Version     string
	Payload     []byte
	TargetKey   string
	ConfigRef   string
	Kind        string
	Enabled     bool
	EnabledAt   *time.Time
	SubjectKind string
	SubjectID   string
}

type SelectedContent struct {
	ArticleID    string
	Selected     bool
	Visibility   string
	Title        string
	Summary      *string
	Reason       *string
	Category     *string
	SourceName   string
	SourceTier   string
	URL          string
	TimelineAt   time.Time
	PublishedAt  *time.Time
	DiscoveredAt time.Time
	Score        *float64
	Backfill     bool
	Silent       bool
	FactID       *int64
}

// WarmSelected prepares the share card used by the first notification attempt. Rendering is best
// effort, matching the former worker; an unavailable Web renderer must not block delivery.
func (s *Service) WarmSelected(ctx context.Context, articleID string, now time.Time) error {
	if s.warmer == nil {
		return nil
	}
	repository, ok := s.repository.(ContentDeliveryRepository)
	if !ok {
		return errors.New("content delivery is unavailable")
	}
	if now.IsZero() {
		now = time.Now()
	}
	content, err := repository.LoadSelectedContent(ctx, articleID)
	if err != nil || !selectedContentReady(content, nil, now) {
		return err
	}
	return s.warmer.Warm(ctx, *content)
}

type ContentTarget struct {
	Key       string
	Kind      string
	ConfigRef string
}

type ContentDeliveryClaim struct {
	ArticleID    string
	DedupeKey    string
	ContentAt    time.Time
	SentSubjects []string
	Card         any
}

type ContentPushResult struct {
	Status     string                 `json:"status"`
	Reason     string                 `json:"reason,omitempty"`
	RetryAfter time.Duration          `json:"-"`
	Targets    []ContentTargetOutcome `json:"targets,omitempty"`
}

type ContentTargetOutcome struct {
	Target string `json:"target"`
	Status string `json:"status"`
}

// PushSelected sends one newly selected item to enabled content targets. The repository reserves
// each target before network I/O; an unknown acknowledgement remains unknown and is never retried.
func (s *Service) PushSelected(ctx context.Context, articleID string, now time.Time) (ContentPushResult, error) {
	repository, ok := s.repository.(ContentDeliveryRepository)
	if !ok || s.sender == nil {
		return ContentPushResult{}, errors.New("content delivery is unavailable")
	}
	if now.IsZero() {
		now = time.Now()
	}
	content, err := repository.LoadSelectedContent(ctx, articleID)
	if err != nil {
		return ContentPushResult{}, err
	}
	if !selectedContentReady(content, nil, now) {
		return ContentPushResult{Status: "skipped", Reason: "content is not eligible"}, nil
	}
	leaseKey := "selected-title:" + shortHash(normalizedTitle(content.Title))
	holder, err := repository.AcquireTitleLease(ctx, leaseKey, articleID, now.Add(10*time.Minute))
	if err != nil {
		return ContentPushResult{}, err
	}
	if holder != articleID && content.FactID == nil {
		return ContentPushResult{Status: "retry", Reason: "same title in flight", RetryAfter: 2 * time.Minute}, nil
	}
	dedupeKey := "selected:article:" + articleID
	sentSubjects := []string{articleID}
	if content.FactID != nil {
		dedupeKey = fmt.Sprintf("selected:fact:%d", *content.FactID)
		siblings, err := repository.SelectedSiblings(ctx, *content.FactID, articleID)
		if err != nil {
			return ContentPushResult{}, err
		}
		sentSubjects = append(sentSubjects, siblings...)
	}
	targets, err := repository.ContentTargets(ctx)
	if err != nil {
		return ContentPushResult{}, err
	}
	result := ContentPushResult{Status: "skipped", Targets: []ContentTargetOutcome{}}
	for _, target := range targets {
		// Re-read before each target: a withdrawal or correction during the previous send applies to mirrors too.
		current, err := repository.LoadSelectedContent(ctx, articleID)
		if err != nil {
			return ContentPushResult{}, err
		}
		if !selectedContentReady(current, nil, time.Now()) {
			break
		}
		card := selectedCard(*current, s.config)
		id, claimedTarget, claimed, err := repository.ClaimContentDelivery(ctx, target.Key, ContentDeliveryClaim{
			ArticleID: articleID, DedupeKey: dedupeKey, ContentAt: current.DiscoveredAt,
			SentSubjects: sentSubjects, Card: card,
		})
		if err != nil {
			return ContentPushResult{}, err
		}
		if !claimed {
			continue
		}
		outcome := ContentTargetOutcome{Target: claimedTarget.Key}
		if !s.config.ContentPushEnabled || claimedTarget.Kind != "feishu_webhook" {
			outcome.Status = "skipped"
			if err := repository.FinishContentDelivery(ctx, id, "skipped", "content push disabled"); err != nil {
				return ContentPushResult{}, err
			}
			result.Targets = append(result.Targets, outcome)
			continue
		}
		webhook := strings.TrimSpace(s.config.WebhookURLs[claimedTarget.ConfigRef])
		if webhook == "" {
			outcome.Status = "failed"
			if err := repository.FinishContentDelivery(ctx, id, "failed", "webhook not configured"); err != nil {
				return ContentPushResult{}, err
			}
			result.Targets = append(result.Targets, outcome)
			continue
		}
		if err := repository.StartContentDelivery(ctx, id); err != nil {
			return ContentPushResult{}, err
		}
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		sent, sendErr := s.sender.Send(sendCtx, webhook, card)
		cancel()
		if sendErr != nil {
			sent = WebhookResult{Status: "unknown", Body: sendErr.Error()}
		}
		if sent.Status != "sent" && sent.Status != "failed" {
			sent.Status = "unknown"
		}
		sent.Body = truncateUTF16(sent.Body, 500)
		if err := repository.FinishContentDelivery(ctx, id, sent.Status, sent.Body); err != nil {
			return ContentPushResult{}, err
		}
		outcome.Status = sent.Status
		result.Targets = append(result.Targets, outcome)
		if sent.Status == "sent" {
			result.Status = "pushed"
		}
	}
	if len(result.Targets) == 0 {
		result.Reason = "no new target"
	}
	return result, nil
}

func normalizedTitle(value string) string {
	var out strings.Builder
	for _, char := range strings.ToLower(value) {
		if unicode.IsSpace(char) || unicode.IsPunct(char) || unicode.IsSymbol(char) {
			continue
		}
		out.WriteRune(char)
	}
	return out.String()
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:24]
}

func selectedContentReady(content *SelectedContent, enabledAt *time.Time, now time.Time) bool {
	if content == nil || !content.Selected || content.Visibility != "public" || content.Silent || content.PublishedAt == nil {
		return false
	}
	if content.SourceTier != "T1" && content.SourceTier != "T1_5" {
		return false
	}
	if content.Backfill || now.Sub(content.TimelineAt) > 12*time.Hour {
		return false
	}
	return enabledAt == nil || !content.DiscoveredAt.Before(*enabledAt)
}

func selectedCard(content SelectedContent, cfg Config) map[string]any {
	var category any
	if content.Category != nil {
		if label := cfg.CategoryLabels[*content.Category]; label != "" {
			category = label
		}
	}
	lines := make([]string, 0, 3)
	if content.Summary != nil && *content.Summary != "" {
		lines = append(lines, *content.Summary)
	}
	if content.Reason != nil && *content.Reason != "" {
		lines = append(lines, "**推荐理由**："+*content.Reason)
	}
	lines = append(lines, "来源："+publication.PublicSourceName(content.SourceName))
	elements := make([]any, 0, 3)
	if category != nil {
		elements = append(elements, map[string]any{"tag": "note", "elements": []any{map[string]any{"tag": "plain_text", "content": category}}})
	}
	elements = append(elements,
		map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": strings.Join(lines, "\n\n")}},
		map[string]any{"tag": "action", "actions": []any{
			map[string]any{"tag": "button", "text": map[string]any{"tag": "plain_text", "content": cfg.SiteName + " 查看"}, "url": strings.TrimRight(cfg.SiteURL, "/") + "/items/" + content.ArticleID, "type": "primary"},
			map[string]any{"tag": "button", "text": map[string]any{"tag": "plain_text", "content": "原文"}, "url": content.URL, "type": "default"},
		}},
	)
	return map[string]any{"header": map[string]any{"title": map[string]any{"tag": "plain_text", "content": content.Title}, "template": "turquoise"}, "elements": elements}
}

func truncateUTF16(value string, limit int) string {
	units := []rune(value)
	count := 0
	for index, char := range units {
		if char > 0xffff {
			if count+2 > limit {
				return string(units[:index])
			}
			count += 2
		} else {
			if count+1 > limit {
				return string(units[:index])
			}
			count++
		}
	}
	return value
}

// RecoverStale marks interrupted sends as uncertain and interrupted reservations as failed.
// Neither outcome is resent automatically.
func (s *Service) RecoverStale(ctx context.Context, now time.Time) (StaleCounts, error) {
	if now.IsZero() {
		now = time.Now()
	}
	return s.repository.MarkStale(ctx, now.Add(-staleDeliveryAfter))
}

func isJavaScriptWhitespace(char rune) bool {
	switch char {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	default:
		return char >= '\u2000' && char <= '\u200a'
	}
}
