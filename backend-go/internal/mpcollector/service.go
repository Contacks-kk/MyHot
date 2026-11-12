package mpcollector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"sort"
	"strings"
	"time"

	"aihot/backend-go/internal/bodyhtml"
	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/receipts"
)

const (
	maxNewPerCheck   = 8
	firstCheckWindow = 7 * 24 * time.Hour
	bodyRetryLimit   = 3
	bodyRetryWindow  = 3 * 24 * time.Hour
)

var ErrFetchRecorded = errors.New("WeChat source fetch failure recorded")

type KnownArticle struct {
	ID           string
	BodyStatus   string
	DiscoveredAt time.Time
	Retry        *BodyRetry
}

type BodyRetry struct {
	Attempts int    `json:"attempts"`
	Error    string `json:"error,omitempty"`
}

type Source struct {
	ID                string
	Name              string
	Config            json.RawMessage
	Cursor            json.RawMessage
	Enabled           bool
	ParticipationMode string
	IntervalMinutes   int
	FailCount         int
	FirstCheck        bool
}

type Run struct {
	ID     int64
	Source Source
}

type Repository interface {
	DueSources(context.Context) ([]Source, error)
	Begin(context.Context, string, string) (*Run, bool, error)
	KnownArticle(context.Context, string) (*KnownArticle, error)
	UpdateBodyRetry(context.Context, string, BodyRetry) error
	ClearBodyRetry(context.Context, string) error
	FinishSuccess(context.Context, *Run, int, int, json.RawMessage) error
	FinishFailure(context.Context, *Run, int, int, bool, string) error
}

type Service struct {
	repository Repository
	materials  *materials.Service
	provider   Provider
	now        func() time.Time
}

func NewService(repository Repository, materialService *materials.Service, provider Provider) *Service {
	return &Service{repository: repository, materials: materialService, provider: provider, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) DueSourceIDs(ctx context.Context) ([]string, error) {
	if s.provider == nil {
		return []string{}, nil
	}
	sources, err := s.repository.DueSources(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	ids := make([]string, 0, len(sources))
	for _, source := range sources {
		last, ok := lastChecked(source.Cursor)
		if ok && now.Sub(last) <= time.Duration(source.IntervalMinutes)*time.Minute {
			continue
		}
		ids = append(ids, source.ID)
	}
	return ids, nil
}

func (s *Service) CheckAccount(ctx context.Context, sourceID string) error {
	if s.provider == nil {
		return fmt.Errorf("%w: Dajiala is not configured", ErrFetchRecorded)
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	run, found, err := s.repository.Begin(ctx, sourceID, "schedule")
	if err != nil || !found {
		return err
	}
	now := s.now()
	var created, foundCount int
	var detail json.RawMessage
	if err = s.collect(ctx, run, now, &foundCount, &created, &detail); err == nil {
		finishCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		return s.repository.FinishSuccess(finishCtx, run, foundCount, created, detail)
	}
	var budget *receipts.BudgetExceededError
	var providerError *DajialaError
	soft := errors.As(err, &budget) || (errors.As(err, &providerError) && providerError.Retryable)
	message := safeError(err)
	finishCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if finishErr := s.repository.FinishFailure(finishCtx, run, foundCount, created, soft, message); finishErr != nil {
		return finishErr
	}
	if soft {
		return err
	}
	return fmt.Errorf("%w: %s", ErrFetchRecorded, message)
}

func (s *Service) collect(ctx context.Context, run *Run, now time.Time, found, created *int, detail *json.RawMessage) error {
	var sourceConfig struct {
		GHID     string `json:"ghid"`
		WXID     string `json:"wxid"`
		Nickname string `json:"nickname"`
	}
	if err := json.Unmarshal(run.Source.Config, &sourceConfig); err != nil {
		return errors.New("WeChat source configuration is invalid")
	}
	ghid := strings.TrimSpace(sourceConfig.GHID)
	if ghid == "" {
		ghid = strings.TrimSpace(sourceConfig.WXID)
	}
	if ghid == "" {
		return errors.New("WeChat source has no ghid")
	}
	cursor := decodeCursor(run.Source.Cursor)
	window := fmt.Sprintf("s:%d", now.Unix()/600)
	history, err := s.provider.History(ctx, ghid, run.Source.ID, window)
	if err != nil {
		return err
	}
	posts := append([]Post(nil), history.Posts...)
	sort.SliceStable(posts, func(i, j int) bool { return posts[i].PostTime > posts[j].PostTime })
	*found = len(posts)
	fetched := 0
	for _, post := range posts {
		if post.URL == "" || post.Title == "" || fetched >= maxNewPerCheck {
			continue
		}
		var publishedAt *time.Time
		if post.PostTime != 0 {
			value := time.Unix(post.PostTime, 0).UTC()
			publishedAt = &value
		}
		if run.Source.FirstCheck && publishedAt != nil && now.Sub(*publishedAt) > firstCheckWindow {
			continue
		}
		identity, validIdentity := materials.IdentityKeyForURL(post.URL)
		if !validIdentity {
			continue
		}
		known, err := s.repository.KnownArticle(ctx, identity)
		if err != nil {
			return err
		}
		retry := known != nil && known.Retry != nil && known.BodyStatus == "none" && known.Retry.Attempts < bodyRetryLimit && now.Sub(known.DiscoveredAt) < bodyRetryWindow
		if known != nil && !retry {
			continue
		}
		fetched++
		receiptIdentity := post.URL
		if post.Serial != nil {
			receiptIdentity = *post.Serial
		}
		article, passing, err := s.fetchBody(ctx, post.URL, run.Source.ID, receiptIdentity)
		if err != nil {
			return err
		}
		if known != nil && (article == nil || strings.TrimSpace(article.Content) == "") {
			if passing != "" {
				next := BodyRetry{Attempts: known.Retry.Attempts + 1, Error: passing}
				if err := s.repository.UpdateBodyRetry(ctx, known.ID, next); err != nil {
					return err
				}
			} else if err := s.repository.ClearBodyRetry(ctx, known.ID); err != nil {
				return err
			}
			continue
		}
		if article == nil {
			article = &Article{}
		}
		title := post.Title
		author := article.Author
		html, text := cleanArticle(article.Content, post.URL)
		bodyStatus := "none"
		if text != "" {
			bodyStatus = "ok"
		}
		raw := map[string]any{"dajiala": map[string]any{
			"position": post.Position, "sn": post.Serial, "original": post.Original,
			"itemShowType": post.ItemShowType, "cover": post.CoverURL,
		}}
		if passing != "" {
			attempts := 1
			if known != nil && known.Retry != nil {
				attempts = known.Retry.Attempts + 1
			}
			raw["dajiala"].(map[string]any)["bodyRetry"] = BodyRetry{Attempts: attempts, Error: passing}
		}
		stored, err := s.materials.Store(ctx, materials.Input{
			SourceID: run.Source.ID, URL: post.URL, IdentityKey: identity, Title: title, Author: author, PublishedAt: publishedAt,
			Backfill: backfillFor(run.Source.FirstCheck), Excerpt: firstNonNil(post.Digest, article.Desc), BodyText: text, BodyHTML: html,
			BodyStatus: bodyStatus, Raw: mustJSON(raw), Via: "fetch",
		})
		if err != nil {
			return err
		}
		if stored.Created {
			*created++
		}
		if known != nil {
			if err := s.repository.ClearBodyRetry(ctx, known.ID); err != nil {
				return err
			}
		}
	}
	cursor["lastCheckedAt"] = now.Format(time.RFC3339Nano)
	if len(posts) > 0 {
		cursor["lastPostTime"] = posts[0].PostTime
	} else if previous, ok := cursor["lastPostTime"]; ok {
		cursor["lastPostTime"] = previous
	} else {
		cursor["lastPostTime"] = nil
	}
	cursor["remainMoney"] = history.RemainMoney
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return errors.New("WeChat cursor could not be encoded")
	}
	*detail, _ = json.Marshal(map[string]any{"reason": "schedule", "reused": history.Reused})
	run.Source.Cursor = encoded
	return nil
}

func (s *Service) fetchBody(ctx context.Context, articleURL, sourceID, identity string) (*Article, string, error) {
	article, err := s.provider.Article(ctx, articleURL, sourceID, identity)
	if err == nil {
		return &article, "", nil
	}
	var budget *receipts.BudgetExceededError
	if errors.As(err, &budget) {
		return nil, "", err
	}
	var providerError *DajialaError
	if errors.As(err, &providerError) && providerError.Rejected && !providerError.Retryable {
		return nil, "", nil
	}
	return nil, safeError(err), nil
}

func lastChecked(cursor json.RawMessage) (time.Time, bool) {
	var values map[string]any
	if json.Unmarshal(cursor, &values) != nil {
		return time.Time{}, false
	}
	value, ok := values["lastCheckedAt"].(string)
	if !ok || value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func decodeCursor(raw json.RawMessage) map[string]any {
	values := map[string]any{}
	_ = json.Unmarshal(raw, &values)
	return values
}

func parseDajialaTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, strings.TrimSpace(raw), time.FixedZone("CST", 8*60*60)); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("invalid Dajiala publication time")
}

func cleanArticle(raw, baseURL string) (string, string) {
	if strings.TrimSpace(raw) == "" {
		return "", ""
	}
	clean, _, err := bodyhtml.Sanitize(raw, baseURL, "", time.Now().UTC())
	if err != nil {
		return "", strings.TrimSpace(raw)
	}
	return clean, plainText(clean)
}

func plainText(value string) string {
	var builder strings.Builder
	inside := false
	for _, r := range value {
		if r == '<' {
			inside = true
			builder.WriteByte(' ')
			continue
		}
		if r == '>' {
			inside = false
			continue
		}
		if !inside {
			builder.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(stdhtml.UnescapeString(builder.String())), " ")
}

func safeError(err error) string {
	var safe interface{ SafeMessage() string }
	if errors.As(err, &safe) && safe.SafeMessage() != "" {
		return safe.SafeMessage()
	}
	return "Dajiala request failed"
}

func backfillFor(first bool) string {
	if first {
		return "first-import"
	}
	return ""
}

func firstNonNil(primary *string, fallback string) string {
	if primary != nil {
		return *primary
	}
	return fallback
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
