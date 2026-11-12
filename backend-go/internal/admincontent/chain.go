package admincontent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrChainRepositoryUnavailable = errors.New("content chain repository is unavailable")

type ChainRepository interface {
	Chain(context.Context, string) (*Chain, error)
}

type ArticleDetail struct {
	ID                string     `json:"id"`
	SourceID          string     `json:"source_id"`
	URL               string     `json:"url"`
	IdentityKey       string     `json:"identity_key"`
	Title             string     `json:"title"`
	Author            *string    `json:"author"`
	Language          *string    `json:"language"`
	PublishedAt       *time.Time `json:"published_at"`
	PublishedAtClaim  *time.Time `json:"published_at_claim"`
	DiscoveredAt      time.Time  `json:"discovered_at"`
	TimelineAt        time.Time  `json:"timeline_at"`
	Backfill          bool       `json:"backfill"`
	BodyStatus        string     `json:"body_status"`
	Revision          int        `json:"revision"`
	ProcessingState   string     `json:"processing_state"`
	ProcessingError   *string    `json:"processing_error"`
	GroupedAt         *time.Time `json:"grouped_at"`
	BodyChars         *int       `json:"body_chars"`
	SourceName        string     `json:"source_name"`
	SourceKind        string     `json:"source_kind"`
	Tier              string     `json:"tier"`
	ParticipationMode string     `json:"participation_mode"`
	SiteFulltext      bool       `json:"site_fulltext"`
	SyndicateFulltext bool       `json:"syndicate_fulltext"`
}

type Discovery struct {
	SourceID     string    `json:"source_id"`
	Via          string    `json:"via"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

type Revision struct {
	Revision    int       `json:"revision"`
	Title       string    `json:"title"`
	ContentHash *string   `json:"content_hash"`
	CreatedAt   time.Time `json:"created_at"`
}

type AnalysisReceipt struct {
	ID      int64     `json:"id"`
	Status  string    `json:"status"`
	Service string    `json:"service"`
	Model   *string   `json:"model"`
	Cost    *float64  `json:"cost"`
	At      time.Time `json:"at"`
}

type Analysis struct {
	ID            int64             `json:"id"`
	Origin        string            `json:"origin"`
	Model         *string           `json:"model"`
	PromptVersion *string           `json:"prompt_version"`
	InputRevision int               `json:"input_revision"`
	Relevance     *string           `json:"relevance"`
	Category      *string           `json:"category"`
	Score         *float64          `json:"score"`
	Selected      *bool             `json:"selected"`
	TitleZH       *string           `json:"title_zh"`
	ReasonZH      *string           `json:"reason_zh"`
	CreatedAt     time.Time         `json:"created_at"`
	Receipts      []AnalysisReceipt `json:"receipts"`
}

type Publication struct {
	ArticleID       string     `json:"article_id"`
	AnalysisID      *int64     `json:"analysis_id"`
	Revision        int        `json:"revision"`
	Visibility      string     `json:"visibility"`
	Eligible        bool       `json:"eligible"`
	Selected        bool       `json:"selected"`
	Seat            bool       `json:"seat"`
	Title           string     `json:"title"`
	OriginalTitle   *string    `json:"original_title"`
	Summary         *string    `json:"summary"`
	Reason          *string    `json:"reason"`
	Category        *string    `json:"category"`
	Tags            []string   `json:"tags"`
	Score           *float64   `json:"score"`
	SourceID        string     `json:"source_id"`
	Channel         string     `json:"channel"`
	FirstParty      bool       `json:"first_party"`
	URL             string     `json:"url"`
	PublishedAt     *time.Time `json:"published_at"`
	DiscoveredAt    time.Time  `json:"discovered_at"`
	TimelineAt      time.Time  `json:"timeline_at"`
	SortAt          *time.Time `json:"sort_at"`
	Backfill        bool       `json:"backfill"`
	SelectedReadyAt *time.Time `json:"selected_ready_at"`
	VisibleAfter    *time.Time `json:"visible_after"`
	BodyMode        string     `json:"body_mode"`
	Syndicate       bool       `json:"syndicate"`
	Indexable       bool       `json:"indexable"`
	SEOIndexedAt    *time.Time `json:"seo_indexed_at"`
	SEOExcludedAt   *time.Time `json:"seo_excluded_at"`
	StoryID         *int64     `json:"story_id"`
	FactID          *int64     `json:"fact_id"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type Override struct {
	Fields     json.RawMessage `json:"fields"`
	Visibility *string         `json:"visibility"`
	Reason     *string         `json:"reason"`
	Version    int             `json:"version"`
	UpdatedBy  string          `json:"updated_by"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type LedgerEntry struct {
	Seq       int64     `json:"seq"`
	Op        string    `json:"op"`
	VisibleAt time.Time `json:"visible_at"`
	ChangedAt time.Time `json:"changed_at"`
}

type Membership struct {
	FactID        int64   `json:"fact_id"`
	Role          string  `json:"role"`
	Manual        bool    `json:"manual"`
	FactPublicID  string  `json:"fact_public_id"`
	FactTitle     string  `json:"fact_title"`
	StoryID       *int64  `json:"story_id"`
	StoryPublicID *string `json:"story_public_id"`
	StoryTitle    *string `json:"story_title"`
}

type Decision struct {
	Verdict    string          `json:"verdict"`
	FactID     *int64          `json:"fact_id"`
	StoryID    *int64          `json:"story_id"`
	ReceiptID  *int64          `json:"receipt_id"`
	Candidates json.RawMessage `json:"candidates"`
	CreatedAt  time.Time       `json:"created_at"`
}

type Delivery struct {
	TargetKey string     `json:"target_key"`
	DedupeKey string     `json:"dedupe_key"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	Response  *string    `json:"response"`
	CreatedAt time.Time  `json:"created_at"`
	SentAt    *time.Time `json:"sent_at"`
}

type AuditEntry struct {
	CreatedAt time.Time       `json:"created_at"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Reason    *string         `json:"reason"`
	Before    json.RawMessage `json:"before"`
	After     json.RawMessage `json:"after"`
}

type Chain struct {
	Article     ArticleDetail `json:"article"`
	Discoveries []Discovery   `json:"discoveries"`
	Revisions   []Revision    `json:"revisions"`
	Analyses    []Analysis    `json:"analyses"`
	Publication *Publication  `json:"publication"`
	Override    *Override     `json:"override"`
	Ledger      []LedgerEntry `json:"ledger"`
	Membership  []Membership  `json:"membership"`
	Decisions   []Decision    `json:"decisions"`
	Deliveries  []Delivery    `json:"deliveries"`
	History     []AuditEntry  `json:"history"`
}

func (s *Service) Chain(ctx context.Context, id string) (*Chain, error) {
	repository, ok := s.repository.(ChainRepository)
	if !ok {
		return nil, ErrChainRepositoryUnavailable
	}
	chain, err := repository.Chain(ctx, id)
	if err != nil || chain == nil {
		return chain, err
	}
	if chain.Discoveries == nil {
		chain.Discoveries = []Discovery{}
	}
	if chain.Revisions == nil {
		chain.Revisions = []Revision{}
	}
	if chain.Analyses == nil {
		chain.Analyses = []Analysis{}
	}
	if chain.Ledger == nil {
		chain.Ledger = []LedgerEntry{}
	}
	if chain.Membership == nil {
		chain.Membership = []Membership{}
	}
	if chain.Decisions == nil {
		chain.Decisions = []Decision{}
	}
	if chain.Deliveries == nil {
		chain.Deliveries = []Delivery{}
	}
	if chain.History == nil {
		chain.History = []AuditEntry{}
	}
	return chain, nil
}
