package publication

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type RepositoryPort interface {
	Publish(context.Context, string, PublishOptions) (*Result, error)
}

type RepublishRepository interface {
	RepositoryPort
	CountSourcePublications(context.Context, string) (int, error)
	SourcePublicationBatch(context.Context, string, string, int) ([]string, error)
	SetSourceRepublishStatus(context.Context, string, json.RawMessage) error
	QueuedSourceRepublishes(context.Context) ([]string, error)
}

type RepublishResult struct {
	Total   int `json:"total"`
	Changed int `json:"changed"`
	Reduced int `json:"reduced"`
}

// Service coordinates public projection refreshes requested by shared content workflows.
type Service struct{ repository RepositoryPort }

func NewService(repository RepositoryPort) *Service { return &Service{repository: repository} }

func (s *Service) Publish(ctx context.Context, articleID string, options PublishOptions) (*Result, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("publication service is unavailable")
	}
	return s.repository.Publish(ctx, articleID, options)
}

// RepublishSource re-derives every current public projection for one source without model calls.
// Progress remains in settings so the Web admin page can resume displaying it across restarts.
func (s *Service) RepublishSource(ctx context.Context, sourceID string, onProgress func(done, total int) error, beforeDone func(RepublishResult) error) (RepublishResult, error) {
	if s == nil || s.repository == nil {
		return RepublishResult{}, errors.New("source republish is unavailable")
	}
	repository, ok := s.repository.(RepublishRepository)
	if !ok {
		return RepublishResult{}, errors.New("source republish is unavailable")
	}
	startedAt := time.Now().UTC()
	if err := repository.SetSourceRepublishStatus(ctx, sourceID, republishStatus("running", 0, nil, RepublishResult{}, startedAt, nil)); err != nil {
		return RepublishResult{}, err
	}
	total, err := repository.CountSourcePublications(ctx, sourceID)
	if err != nil {
		return RepublishResult{}, err
	}
	result := RepublishResult{Total: total}
	lastID := ""
	done := 0
	if err := repository.SetSourceRepublishStatus(ctx, sourceID, republishStatus("running", 0, &total, result, startedAt, nil)); err != nil {
		return RepublishResult{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return RepublishResult{}, err
		}
		articleIDs, err := repository.SourcePublicationBatch(ctx, sourceID, lastID, 500)
		if err != nil {
			return RepublishResult{}, err
		}
		if len(articleIDs) == 0 {
			break
		}
		for _, articleID := range articleIDs {
			published, err := repository.Publish(ctx, articleID, PublishOptions{})
			if err != nil {
				return RepublishResult{}, err
			}
			if published != nil && published.Changed {
				result.Changed++
			}
			if published != nil && published.Reduced {
				result.Reduced++
			}
		}
		lastID = articleIDs[len(articleIDs)-1]
		done += len(articleIDs)
		if onProgress != nil {
			if err := onProgress(done, total); err != nil {
				return RepublishResult{}, err
			}
		}
		if err := repository.SetSourceRepublishStatus(ctx, sourceID, republishStatus("running", done, &total, result, startedAt, nil)); err != nil {
			return RepublishResult{}, err
		}
	}
	if beforeDone != nil {
		if err := beforeDone(result); err != nil {
			return RepublishResult{}, err
		}
	}
	finishedAt := time.Now().UTC()
	if err := repository.SetSourceRepublishStatus(ctx, sourceID, republishStatus("done", total, &total, result, startedAt, &finishedAt)); err != nil {
		return RepublishResult{}, err
	}
	return result, nil
}

func (s *Service) QueuedSourceRepublishes(ctx context.Context) ([]string, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("source republish is unavailable")
	}
	repository, ok := s.repository.(RepublishRepository)
	if !ok {
		return nil, errors.New("source republish is unavailable")
	}
	return repository.QueuedSourceRepublishes(ctx)
}

func republishStatus(status string, done int, total *int, result RepublishResult, startedAt time.Time, finishedAt *time.Time) json.RawMessage {
	value := map[string]any{"status": status, "startedAt": startedAt.Format(time.RFC3339Nano)}
	if status == "done" {
		value["total"], value["changed"], value["reduced"] = result.Total, result.Changed, result.Reduced
	} else {
		value["done"], value["total"] = done, total
	}
	if finishedAt != nil {
		value["finishedAt"] = finishedAt.Format(time.RFC3339Nano)
	}
	encoded, _ := json.Marshal(value)
	return encoded
}
