package materials

import (
	"context"
	"time"
	"unicode/utf16"
)

type repository interface {
	StorePrepared(context.Context, Input, TimelineDecision, string) (Result, error)
}

// Service prepares new material consistently before persistence. The repository owns
// the PostgreSQL transaction that inserts or revises an article and records discovery.
type Service struct{ repository repository }

func NewService(repository repository) *Service { return &Service{repository: repository} }

func (s *Service) Store(ctx context.Context, input Input) (Result, error) {
	input = prepare(input)
	decision := DecideTimeline(input.PublishedAt, input.DiscoveredAt, input.Backfill)
	hash := ContentHash(input.Title, input.BodyText, input.Excerpt)
	return s.repository.StorePrepared(ctx, input, decision, hash)
}

func prepare(input Input) Input {
	if len(input.Media) == 0 {
		input.Media = []byte("[]")
	}
	if input.DiscoveredAt.IsZero() {
		input.DiscoveredAt = time.Now().UTC()
	} else {
		input.DiscoveredAt = input.DiscoveredAt.UTC()
	}
	if input.PublishedAt != nil {
		if input.PublishedAt.IsZero() {
			input.PublishedAt = nil
		} else {
			publishedAt := input.PublishedAt.UTC()
			input.PublishedAt = &publishedAt
		}
	}
	if input.Via == "" {
		input.Via = "fetch"
	}
	if input.BodyStatus == "" {
		input.BodyStatus = "pending"
		if input.BodyText != "" {
			input.BodyStatus = "ok"
		}
	}
	if input.IdentityKey == "" {
		input.IdentityKey = "url:" + input.URL
	}
	input.Title = CollapseWhitespace(input.Title)
	if input.Title == "" {
		input.Title = input.URL
	}
	input.Title = truncateUTF16(input.Title, 1000)
	return input
}

func truncateUTF16(value string, max int) string {
	units := 0
	end := len(value)
	for offset, current := range value {
		width := utf16.RuneLen(current)
		if units+width > max {
			end = offset
			break
		}
		units += width
	}
	return value[:end]
}
