package indexnow

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"aihot/backend-go/internal/topics"
)

const maxURLs = 10_000
const maxStories = 3_000

type Repository interface {
	Watermark(context.Context) (time.Time, bool, error)
	Changes(context.Context, time.Time, time.Time, []topics.Definition, int) (Changes, error)
	Items(context.Context, time.Time, time.Time, int) ([]Item, error)
	SaveWatermark(context.Context, time.Time) error
}

type Submitter interface {
	Submit(context.Context, Submission) (int, error)
}

type Config struct {
	SiteURL string
	Key     string
	Enabled bool
}

type Service struct {
	repository Repository
	submitter  Submitter
	config     Config
	topics     []topics.Definition
}

type Report struct {
	Kind string
	Key  string
}

type Story struct {
	PublicID  string
	CreatedAt time.Time
}

type Topic struct {
	Slug      string
	Total     int
	Recent    int
	ChangedAt *time.Time
}

type Item struct {
	ID        string
	UpdatedAt time.Time
}

type Changes struct {
	Reports []Report
	Stories []Story
	Topics  []Topic
}

type Submission struct {
	Host        string   `json:"host"`
	Key         string   `json:"key"`
	KeyLocation string   `json:"keyLocation"`
	URLs        []string `json:"urlList"`
}

type Result struct {
	Status     string   `json:"status"`
	HTTPStatus *int     `json:"httpStatus"`
	URLs       int      `json:"urls"`
	More       bool     `json:"more"`
	Sample     []string `json:"sample"`
}

func NewService(repository Repository, submitter Submitter, cfg Config, definitions []topics.Definition) *Service {
	return &Service{repository: repository, submitter: submitter, config: cfg, topics: definitions}
}

func (s *Service) Run(ctx context.Context, now time.Time) (Result, error) {
	if s.repository == nil {
		return Result{}, errors.New("IndexNow repository is unavailable")
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	since, found, err := s.repository.Watermark(ctx)
	if err != nil {
		return Result{}, err
	}
	if !found {
		since = now.Add(-24 * time.Hour)
	}
	changes, err := s.repository.Changes(ctx, since, now, s.topics, maxStories+1)
	if err != nil {
		return Result{}, err
	}
	stories := changes.Stories
	if len(stories) > maxStories {
		stories = stories[:maxStories]
	}
	room := maxURLs - len(changes.Reports) - len(stories) - len(changes.Topics)
	itemLimit := max(0, room+1)
	items, err := s.repository.Items(ctx, since, now, itemLimit)
	if err != nil {
		return Result{}, err
	}
	sentItems := items
	if room <= 0 {
		sentItems = []Item{}
	} else if len(sentItems) > room {
		sentItems = sentItems[:room]
	}

	baseURL := strings.TrimRight(s.config.SiteURL, "/")
	urls := make([]string, 0, len(sentItems)+len(changes.Reports)+len(stories)+len(changes.Topics))
	for _, item := range sentItems {
		urls = append(urls, baseURL+"/items/"+item.ID)
	}
	for _, report := range changes.Reports {
		urls = append(urls, baseURL+"/"+report.Kind+"/"+report.Key)
	}
	for _, story := range stories {
		urls = append(urls, baseURL+"/story/"+story.PublicID)
	}
	for _, topic := range changes.Topics {
		urls = append(urls, baseURL+"/topics/"+topic.Slug)
	}

	result := Result{Status: "empty", URLs: len(urls), More: false, Sample: sampleURLs(urls)}
	if len(urls) > 0 {
		result.Status = "disabled"
	}
	var stops []time.Time
	if len(items) > room && len(sentItems) > 0 {
		stops = append(stops, sentItems[len(sentItems)-1].UpdatedAt.UTC().Truncate(time.Millisecond).Add(-time.Millisecond))
	}
	if len(changes.Stories) > maxStories && len(stories) > 0 {
		stops = append(stops, stories[len(stories)-1].CreatedAt.UTC().Truncate(time.Millisecond).Add(-time.Millisecond))
	}
	watermark := now
	if len(stops) > 0 {
		watermark = stops[0]
		for _, stop := range stops[1:] {
			if stop.Before(watermark) {
				watermark = stop
			}
		}
		result.More = true
	}

	if len(urls) > 0 && s.config.Enabled && s.config.Key != "" {
		if s.submitter == nil {
			return Result{}, errors.New("IndexNow submitter is unavailable")
		}
		parsed, err := url.Parse(s.config.SiteURL)
		if err != nil || parsed.Host == "" {
			return Result{}, errors.New("invalid IndexNow site URL")
		}
		status, err := s.submitter.Submit(ctx, Submission{
			Host: parsed.Host, Key: s.config.Key, KeyLocation: baseURL + "/" + s.config.Key + ".txt", URLs: urls,
		})
		if err != nil {
			return Result{}, err
		}
		result.HTTPStatus = &status
		if status >= 200 && status < 300 {
			result.Status = "sent"
		} else {
			result.Status = "failed"
		}
	}
	if result.Status != "failed" {
		if err := s.repository.SaveWatermark(ctx, watermark); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

func sampleURLs(urls []string) []string {
	if len(urls) > 3 {
		urls = urls[:3]
	}
	return append([]string{}, urls...)
}
