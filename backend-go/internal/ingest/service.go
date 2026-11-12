package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"aihot/backend-go/internal/materials"
)

const MaxItems = 50

var (
	ErrRequired     = errors.New("sourceId and items[] required")
	ErrSourcePaused = errors.New("source paused")
)

type RequestError struct {
	Status  int
	Message string
}

func (e *RequestError) Error() string { return e.Message }

type Repository interface {
	EnsureSource(context.Context, string, string) error
	RecordSuccess(context.Context, string, int, int) error
}

type MaterialWriter interface {
	Store(context.Context, materials.Input) (materials.Result, error)
}

type Service struct {
	repository Repository
	materials  MaterialWriter
}

func NewService(repository Repository, materialWriter MaterialWriter) *Service {
	return &Service{repository: repository, materials: materialWriter}
}

type Result struct {
	OK      bool `json:"ok"`
	Created int  `json:"created"`
}

func (s *Service) Ingest(ctx context.Context, body json.RawMessage) (Result, error) {
	var input map[string]json.RawMessage
	if err := json.Unmarshal(body, &input); err != nil {
		if json.Valid(body) {
			return Result{}, &RequestError{Status: 400, Message: ErrRequired.Error()}
		}
		return Result{}, &RequestError{Status: 400, Message: "The request could not be processed."}
	}
	sourceID := strings.TrimSpace(jsonString(input["sourceId"]))
	if sourceID == "" {
		return Result{}, &RequestError{Status: 400, Message: ErrRequired.Error()}
	}
	sourceID = truncateUTF16(sourceID, 120)
	var items []json.RawMessage
	if len(input["items"]) > 0 {
		_ = json.Unmarshal(input["items"], &items)
	}
	if len(items) == 0 {
		return Result{}, &RequestError{Status: 400, Message: ErrRequired.Error()}
	}
	if len(items) > MaxItems {
		return Result{}, &RequestError{Status: 413, Message: fmt.Sprintf("items[] exceeds max %d per request", MaxItems)}
	}
	sourceName := strings.TrimSpace(jsonString(input["sourceName"]))
	if sourceName == "" {
		sourceName = sourceID
	}
	if err := s.repository.EnsureSource(ctx, sourceID, truncateUTF16(sourceName, 200)); err != nil {
		return Result{}, err
	}

	seen := make(map[string]struct{}, len(items))
	created := 0
	for _, rawItem := range items {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &item); err != nil || item == nil {
			continue
		}
		title := strings.TrimSpace(jsonString(item["title"]))
		rawURL := strings.TrimSpace(jsonString(item["url"]))
		if title == "" || rawURL == "" {
			continue
		}
		identity, ok := materials.IdentityKeyForURL(rawURL)
		if !ok {
			continue
		}
		canonical, ok := materials.NormalizeURL(rawURL)
		if !ok {
			continue
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}

		var sourceRaw json.RawMessage
		if value, exists := item["raw"]; exists && len(value) > 0 {
			sourceRaw = append(json.RawMessage(nil), value...)
		}
		backfill := ""
		var rawObject map[string]json.RawMessage
		if json.Unmarshal(sourceRaw, &rawObject) == nil {
			var flags struct {
				Backfill json.RawMessage `json:"backfill"`
				Baseline json.RawMessage `json:"baseline"`
			}
			if json.Unmarshal(rawObject["_aihot"], &flags) == nil {
				if jsonTruthy(flags.Backfill) {
					backfill = "reported-backfill"
				} else if jsonTruthy(flags.Baseline) {
					backfill = "reported-baseline"
				}
			}
		}
		publishedAt := parseDate(jsonString(item["publishedAt"]))
		author := truncateUTF16(jsonString(item["author"]), 200)
		material, err := s.materials.Store(ctx, materials.Input{
			SourceID: sourceID, URL: rawURL, IdentityKey: identity, Title: title,
			Author: author, PublishedAt: publishedAt, Backfill: backfill, Raw: sourceRaw, Via: "ingest",
		})
		if err != nil {
			return Result{}, err
		}
		if material.Created {
			created++
		}
	}
	if err := s.repository.RecordSuccess(ctx, sourceID, len(items), created); err != nil {
		return Result{}, err
	}
	return Result{OK: true, Created: created}, nil
}

func jsonString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func jsonTruthy(raw json.RawMessage) bool {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value == nil {
		return false
	}
	switch value := value.(type) {
	case bool:
		return value
	case float64:
		return value != 0
	case string:
		return value != ""
	default:
		return true
	}
}

func parseDate(value string) *time.Time {
	if value == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02", "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			if layout == "2006-01-02" || layout == "2006-01-02 15:04:05" {
				parsed = parsed.UTC()
			}
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}

func truncateUTF16(value string, max int) string {
	units := 0
	for offset, current := range value {
		width := utf16.RuneLen(current)
		if units+width > max {
			return value[:offset]
		}
		units += width
	}
	return value
}
