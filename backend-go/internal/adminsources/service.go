package adminsources

import (
	"context"
	"encoding/json"
	"time"
)

// Repository exposes source diagnostics to administrators without allowing changes.
type Repository interface {
	List(context.Context, Filter) (ListResult, error)
	Detail(context.Context, string) (*Detail, error)
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

type Filter struct {
	Query   *string
	Kind    *string
	Health  *string
	Mode    *string
	Enabled *bool
	Page    int
}

type Row struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Kind              string     `json:"kind"`
	Tier              string     `json:"tier"`
	ParticipationMode string     `json:"participation_mode"`
	Enabled           bool       `json:"enabled"`
	Health            string     `json:"health"`
	FailCount         int        `json:"fail_count"`
	IntervalMinutes   int        `json:"interval_minutes"`
	LastOKAt          *time.Time `json:"last_ok_at"`
	LastFetchAt       *time.Time `json:"last_fetch_at"`
	LastError         *string    `json:"last_error"`
	FirstParty        bool       `json:"first_party"`
	NextFetchAt       *time.Time `json:"next_fetch_at"`
	Items7d           int        `json:"items_7d"`
	Selected30d       int        `json:"selected_30d"`
}

type Totals struct {
	Total    int `json:"total"`
	Enabled  int `json:"enabled"`
	Failing  int `json:"failing"`
	Degraded int `json:"degraded"`
}

type ListResult struct {
	Page   int    `json:"page"`
	Rows   []Row  `json:"rows"`
	Totals Totals `json:"totals"`
}

type Source struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Kind              string          `json:"kind"`
	Config            json.RawMessage `json:"config"`
	Tags              []string        `json:"tags"`
	FirstParty        bool            `json:"first_party"`
	OwnerEntityID     *string         `json:"owner_entity_id"`
	Tier              string          `json:"tier"`
	ParticipationMode string          `json:"participation_mode"`
	SignalGroupID     *string         `json:"signal_group_id"`
	IntervalMinutes   int             `json:"interval_minutes"`
	SiteFulltext      bool            `json:"site_fulltext"`
	SyndicateFulltext bool            `json:"syndicate_fulltext"`
	Enabled           bool            `json:"enabled"`
	Health            string          `json:"health"`
	FailCount         int             `json:"fail_count"`
	LastFetchAt       *time.Time      `json:"last_fetch_at"`
	LastOKAt          *time.Time      `json:"last_ok_at"`
	LastError         *string         `json:"last_error"`
	Cursor            json.RawMessage `json:"cursor"`
	NextFetchAt       *time.Time      `json:"next_fetch_at"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type FetchRun struct {
	ID         int64           `json:"id"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	Status     string          `json:"status"`
	FoundCount *int            `json:"found_count"`
	NewCount   *int            `json:"new_count"`
	Error      *string         `json:"error"`
	Detail     json.RawMessage `json:"detail"`
}

type Item struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	URL             string     `json:"url"`
	DiscoveredAt    time.Time  `json:"discovered_at"`
	PublishedAt     *time.Time `json:"published_at"`
	ProcessingState string     `json:"processing_state"`
	Selected        *bool      `json:"selected"`
	Visibility      *string    `json:"visibility"`
	TitleZH         *string    `json:"title_zh"`
}

type Stats struct {
	Total    int `json:"total"`
	Last7d   int `json:"last7d"`
	Selected int `json:"selected"`
}

type AuditEntry struct {
	CreatedAt time.Time       `json:"created_at"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Reason    *string         `json:"reason"`
	Before    json.RawMessage `json:"before"`
	After     json.RawMessage `json:"after"`
}

type Detail struct {
	Source    Source         `json:"source"`
	Runs      []FetchRun     `json:"runs"`
	Items     []Item         `json:"items"`
	Stats     Stats          `json:"stats"`
	History   []AuditEntry   `json:"history"`
	Republish map[string]any `json:"republish"`
}

func (s *Service) List(ctx context.Context, filter Filter) (ListResult, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	result, err := s.repository.List(ctx, filter)
	if err != nil {
		return ListResult{}, err
	}
	result.Page = filter.Page
	if result.Rows == nil {
		result.Rows = []Row{}
	}
	return result, nil
}

func (s *Service) Detail(ctx context.Context, id string) (*Detail, error) {
	return s.repository.Detail(ctx, id)
}
