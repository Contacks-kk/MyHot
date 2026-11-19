package admincontent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

const publicationDateLayout = "2006-01-02T15:04:05.000Z"

var publicationDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
var publicationDateHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var (
	ErrPublicationDateCorrectionUnavailable = errors.New("publication date correction repository is unavailable")
	ErrInvalidPublicationDateCorrection     = errors.New("发布时间必须是完整、有效的 UTC ISO 日期；只允许校正七天前的历史内容")
)

type PublicationDateCorrectionRepository interface {
	/** PreviewPublicationDateCorrection loads the material used to review a historical date change. */
	PreviewPublicationDateCorrection(context.Context, string, string) (*PublicationDateCorrectionPlan, error)
	/** CorrectPublicationDate applies the approved historical date change and records its audit. */
	CorrectPublicationDate(context.Context, string, PublicationDateCorrectionInput, string) (*PublicationDateCorrectionResult, bool, error)
}

type PublicationDateSnapshot struct {
	PublishedAt      string  `json:"publishedAt"`
	PublishedAtClaim *string `json:"publishedAtClaim"`
	TimelineAt       string  `json:"timelineAt"`
}

type PublicationDateCorrectionMaterial struct {
	ID               string
	Identity         string
	SourceID         string
	URL              string
	Revision         int
	ContentHash      *string
	PublishedAt      *time.Time
	PublishedAtClaim *time.Time
	DiscoveredAt     time.Time
	TimelineAt       time.Time
	Backfill         bool
	BackfillReason   *string
	UpdatedAt        time.Time
}

type PublicationDateCorrectionPlan struct {
	ArticleID                  string                  `json:"articleId"`
	Version                    int                     `json:"version"`
	Hash                       string                  `json:"hash"`
	Before                     PublicationDateSnapshot `json:"before"`
	After                      PublicationDateSnapshot `json:"after"`
	TimelineFollowsPublication bool                    `json:"timelineFollowsPublication"`
	Changed                    bool                    `json:"changed"`
}

type PublicationDateCorrectionInput struct {
	Version     int    `json:"version"`
	Hash        string `json:"hash"`
	PublishedAt string `json:"publishedAt"`
	RequestID   string `json:"requestId"`
	Reason      string `json:"reason"`
}

type PublicationDateCorrectionResult struct {
	ArticleID   string `json:"articleId"`
	Revision    int    `json:"revision"`
	Status      string `json:"status"`
	PublishedAt string `json:"publishedAt"`
}

/** SetPublicationDateCorrectionRepository installs the audited historical date correction path. */
func (s *Service) SetPublicationDateCorrectionRepository(repository PublicationDateCorrectionRepository) {
	s.publicationDate = repository
}

/** PreviewPublicationDateCorrection returns a version-bound plan without changing article data. */
func (s *Service) PreviewPublicationDateCorrection(ctx context.Context, id, publishedAt string) (*PublicationDateCorrectionPlan, error) {
	if s.publicationDate == nil {
		return nil, ErrPublicationDateCorrectionUnavailable
	}
	if !articleIDPattern.MatchString(id) {
		return nil, ErrInvalidCommand
	}
	if _, err := parsePublicationDate(publishedAt); err != nil {
		return nil, ErrInvalidPublicationDateCorrection
	}
	return s.publicationDate.PreviewPublicationDateCorrection(ctx, id, publishedAt)
}

/** CorrectPublicationDate validates the reviewed plan before forwarding the approved request. */
func (s *Service) CorrectPublicationDate(ctx context.Context, id string, input PublicationDateCorrectionInput, actor string) (*PublicationDateCorrectionResult, bool, error) {
	if s.publicationDate == nil {
		return nil, false, ErrPublicationDateCorrectionUnavailable
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if !articleIDPattern.MatchString(id) || input.Version < 1 || !publicationDateHashPattern.MatchString(input.Hash) ||
		!requestIDPattern.MatchString(input.RequestID) || input.Reason == "" {
		return nil, false, ErrInvalidCommand
	}
	if _, err := parsePublicationDate(input.PublishedAt); err != nil {
		return nil, false, ErrInvalidPublicationDateCorrection
	}
	return s.publicationDate.CorrectPublicationDate(ctx, id, input, actor)
}

/** PlanPublicationDateCorrection validates a historical date and binds approval to current article metadata. */
func PlanPublicationDateCorrection(material PublicationDateCorrectionMaterial, publishedAt string, now time.Time) (PublicationDateCorrectionPlan, error) {
	requested, err := parsePublicationDate(publishedAt)
	if err != nil || material.PublishedAt == nil {
		return PublicationDateCorrectionPlan{}, ErrInvalidPublicationDateCorrection
	}
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Millisecond)
	}
	now = now.UTC().Truncate(time.Millisecond)
	recent := now.Add(-7 * 24 * time.Hour)
	if requested.After(now) || material.PublishedAt.UTC().Truncate(time.Millisecond).Compare(recent) >= 0 || requested.Compare(recent) >= 0 {
		return PublicationDateCorrectionPlan{}, ErrInvalidPublicationDateCorrection
	}
	before := PublicationDateSnapshot{
		PublishedAt:      formatPublicationDate(*material.PublishedAt),
		PublishedAtClaim: formatNullablePublicationDate(material.PublishedAtClaim),
		TimelineAt:       formatPublicationDate(material.TimelineAt),
	}
	timelineFollowsPublication := before.TimelineAt == before.PublishedAt
	after := PublicationDateSnapshot{
		PublishedAt:      publishedAt,
		PublishedAtClaim: publicationDateStringPointer(publishedAt),
		TimelineAt:       before.TimelineAt,
	}
	if timelineFollowsPublication {
		after.TimelineAt = publishedAt
	}
	hashInput := map[string]any{
		"id": material.ID, "identity": material.Identity, "source": material.SourceID, "url": material.URL,
		"version": material.Revision, "contentHash": material.ContentHash, "updatedAt": formatPublicationDate(material.UpdatedAt),
		"discoveredAt": formatPublicationDate(material.DiscoveredAt), "backfill": material.Backfill,
		"backfillReason": material.BackfillReason, "before": before, "requested": publishedAt,
	}
	encoded, err := json.Marshal(hashInput)
	if err != nil {
		return PublicationDateCorrectionPlan{}, err
	}
	hash := sha256.Sum256(encoded)
	return PublicationDateCorrectionPlan{
		ArticleID:                  material.ID,
		Version:                    material.Revision,
		Hash:                       hex.EncodeToString(hash[:]),
		Before:                     before,
		After:                      after,
		TimelineFollowsPublication: timelineFollowsPublication,
		Changed:                    before.PublishedAt != publishedAt,
	}, nil
}

/** parsePublicationDate accepts only canonical UTC dates with millisecond precision. */
func parsePublicationDate(value string) (time.Time, error) {
	if !publicationDatePattern.MatchString(value) {
		return time.Time{}, ErrInvalidPublicationDateCorrection
	}
	parsed, err := time.Parse(publicationDateLayout, value)
	if err != nil || parsed.Format(publicationDateLayout) != value {
		return time.Time{}, ErrInvalidPublicationDateCorrection
	}
	return parsed.UTC(), nil
}

/** formatPublicationDate matches the historical TypeScript UTC millisecond representation. */
func formatPublicationDate(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format(publicationDateLayout)
}

/** formatNullablePublicationDate formats an optional stored publication claim. */
func formatNullablePublicationDate(value *time.Time) *string {
	if value == nil {
		return nil
	}
	return publicationDateStringPointer(formatPublicationDate(*value))
}

/** publicationDateStringPointer returns a pointer for a date snapshot field. */
func publicationDateStringPointer(value string) *string {
	return &value
}
