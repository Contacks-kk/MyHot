package publiccontent

import (
	"context"
	"regexp"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/publication"
	"aihot/backend-go/internal/topics"
)

var articleIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type ItemAvailability struct {
	Status     string `json:"status"`
	SourceName string `json:"sourceName,omitempty"`
}

type Record struct {
	ID         string
	Visibility string
	SourceMode string
	SourceName string
}

type Repository interface {
	Availability(context.Context, []string) ([]Record, error)
	StoryFollowups(context.Context, string, time.Time) ([]FollowupMember, error)
	GroupReports(context.Context, GroupReportsQuery, time.Time) ([]GroupReportRow, error)
}

type HotTopicRepository interface {
	HotTopics(context.Context, time.Time) ([]HotTopicRecord, error)
}

type SelectedSyncRepository interface {
	LedgerEpoch(context.Context) (string, error)
	LedgerWatermark(context.Context) (int64, error)
	SelectedSnapshot(context.Context, int, int64, string, bool) ([]SelectedSnapshotRecord, error)
	SelectedChanges(context.Context, int, int64, int64, time.Time, bool) ([]SelectedChangeRecord, error)
}

type Service struct {
	repository Repository
	entities   map[string]interests.Entity
	topics     []topics.Definition
}

func (s *Service) SetTopics(definitions []topics.Definition) {
	s.topics = append([]topics.Definition(nil), definitions...)
}

func NewService(repository Repository, entities ...map[string]interests.Entity) *Service {
	service := &Service{repository: repository}
	if len(entities) > 0 {
		service.entities = entities[0]
	}
	return service
}

func (s *Service) ItemAvailability(ctx context.Context, ids []string) (map[string]ItemAvailability, error) {
	clean := make([]string, 0, min(len(ids), 500))
	seen := make(map[string]struct{}, min(len(ids), 500))
	for _, id := range ids {
		if !articleIDPattern.MatchString(id) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
		if len(clean) == 500 {
			break
		}
	}

	out := make(map[string]ItemAvailability, len(clean))
	for _, id := range clean {
		out[id] = ItemAvailability{Status: "unavailable"}
	}
	if len(clean) == 0 {
		return out, nil
	}
	rows, err := s.repository.Availability(ctx, clean)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Visibility == "withdrawn" || row.SourceMode != "editorial" {
			continue
		}
		if row.Visibility == "summary-only" {
			out[row.ID] = ItemAvailability{Status: "summary-only"}
			continue
		}
		out[row.ID] = ItemAvailability{Status: "public", SourceName: publicSourceName(row.SourceName)}
	}
	return out, nil
}

func publicSourceName(name string) string {
	return publication.PublicSourceName(name)
}

// PublicSourceName is shared by public outlets that render feed authors.
func PublicSourceName(name string) string { return publicSourceName(name) }
