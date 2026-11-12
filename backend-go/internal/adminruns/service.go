package adminruns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const staleHeartbeat = 3 * time.Minute
const groupingWarnAfter = 10 * time.Minute

type Repository interface {
	Load(context.Context) (Data, error)
}

type RequeueRepository interface {
	RequeueFailed(context.Context, *string, string, string, string) (RequeueResult, error)
}

var ErrInvalidRequeue = errors.New("reason is required")
var ErrRequeueUnavailable = errors.New("failed processing requeue is unavailable")

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

type Heartbeat struct {
	PID       int    `json:"pid"`
	Host      string `json:"host"`
	Release   string `json:"release"`
	StartedAt string `json:"startedAt"`
}

type Process struct {
	Role      string    `json:"role"`
	PID       int       `json:"pid"`
	Host      string    `json:"host"`
	Release   string    `json:"release"`
	StartedAt string    `json:"startedAt"`
	At        time.Time `json:"at"`
	Alive     bool      `json:"alive"`
}

type Job struct {
	Job        string     `json:"job"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Status     string     `json:"status"`
	Error      *string    `json:"error"`
	Failed24h  int        `json:"failed_24h"`
	Runs24h    int        `json:"runs_24h"`
}

type TimelineEntry struct {
	ID         int64      `json:"id"`
	Job        string     `json:"job"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Status     string     `json:"status"`
	Error      *string    `json:"error"`
}

type Queue struct {
	Name   string    `json:"name"`
	State  string    `json:"state"`
	N      int       `json:"n"`
	Oldest time.Time `json:"oldest"`
}

type FailedJob struct {
	Name       string     `json:"name"`
	Failed     int        `json:"failed"`
	Last       *time.Time `json:"last"`
	LastOutput *string    `json:"last_output"`
}

type GroupItem struct {
	ArticleID string    `json:"articleId"`
	Title     string    `json:"title"`
	Since     time.Time `json:"since"`
	Failed    bool      `json:"failed"`
	Recovery  string    `json:"recovery"`
	ReceiptID *int64    `json:"receiptId"`
	Error     *string   `json:"error"`
}

type WaitingGroup struct {
	GroupItem
	ReceiptStatus *string `json:"-"`
	Running       bool    `json:"-"`
	Released      bool    `json:"-"`
}

type Grouping struct {
	Waiting        int         `json:"waiting"`
	NeedsAttention int         `json:"needsAttention"`
	Items          []GroupItem `json:"items"`
}

type LaggingSource struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Kind            string     `json:"kind"`
	Health          string     `json:"health"`
	FailCount       int        `json:"fail_count"`
	LastOKAt        *time.Time `json:"last_ok_at"`
	LastFetchAt     *time.Time `json:"last_fetch_at"`
	NextFetchAt     *time.Time `json:"next_fetch_at"`
	IntervalMinutes int        `json:"interval_minutes"`
	LastError       *string    `json:"last_error"`
}

type ReceiptCount struct {
	Status string `json:"status"`
	N      int    `json:"n"`
}

type ReceiptIssue struct {
	ID        int64     `json:"id"`
	Service   string    `json:"service"`
	Model     *string   `json:"model"`
	Purpose   string    `json:"purpose"`
	Subject   *string   `json:"subject"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	Error     *string   `json:"error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DeliveryIssue struct {
	ID          int64     `json:"id"`
	TargetKey   string    `json:"target_key"`
	SubjectKind string    `json:"subject_kind"`
	SubjectID   string    `json:"subject_id"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	Response    *string   `json:"response"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type FailureGroup struct {
	Error   string    `json:"error"`
	N       int       `json:"n"`
	Last    time.Time `json:"last"`
	Example string    `json:"example"`
}

type IngestEntry struct {
	Client    string          `json:"client"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Error     *string         `json:"error"`
	Summary   json.RawMessage `json:"summary"`
	CreatedAt time.Time       `json:"created_at"`
}

type Retrying struct {
	Count int        `json:"count"`
	Next  *time.Time `json:"next"`
}

type ReceiptSummary struct {
	Counts map[string]int `json:"counts"`
	Issues []ReceiptIssue `json:"issues"`
}

type Data struct {
	Heartbeats    []HeartbeatRow
	Jobs          []Job
	Timeline      []TimelineEntry
	Queues        []Queue
	FailedJobs    []FailedJob
	Grouping      []WaitingGroup
	Lagging       []LaggingSource
	Receipts      []ReceiptCount
	ReceiptIssues []ReceiptIssue
	Deliveries    []DeliveryIssue
	Errors        []FailureGroup
	Retrying      Retrying
	Ingest        []IngestEntry
}

type HeartbeatRow struct {
	Key       string
	Value     json.RawMessage
	UpdatedAt time.Time
}

type Overview struct {
	CheckedAt  time.Time       `json:"checkedAt"`
	Processes  []Process       `json:"processes"`
	Jobs       []Job           `json:"jobs"`
	Timeline   []TimelineEntry `json:"timeline"`
	Queues     []Queue         `json:"queues"`
	FailedJobs []FailedJob     `json:"failedJobs"`
	Grouping   Grouping        `json:"grouping"`
	Lagging    []LaggingSource `json:"lagging"`
	Receipts   ReceiptSummary  `json:"receipts"`
	Deliveries []DeliveryIssue `json:"deliveries"`
	Errors     []FailureGroup  `json:"errors"`
	Retrying   Retrying        `json:"retrying"`
	Ingest     []IngestEntry   `json:"ingest"`
	Modules    map[string]any  `json:"modules"`
}

func (s *Service) Overview(ctx context.Context, now time.Time) (Overview, error) {
	if now.IsZero() {
		now = time.Now()
	}
	data, err := s.repository.Load(ctx)
	if err != nil {
		return Overview{}, err
	}
	out := Overview{
		CheckedAt: now.UTC(), Jobs: nonNil(data.Jobs), Timeline: nonNil(data.Timeline), Queues: nonNil(data.Queues),
		FailedJobs: nonNil(data.FailedJobs), Lagging: nonNil(data.Lagging), Deliveries: nonNil(data.Deliveries),
		Errors: nonNil(data.Errors), Retrying: data.Retrying, Ingest: nonNil(data.Ingest), Modules: map[string]any{},
		Receipts: ReceiptSummary{Counts: make(map[string]int, len(data.Receipts)), Issues: nonNil(data.ReceiptIssues)},
	}
	for _, row := range data.Heartbeats {
		const prefix = "heartbeat."
		if len(row.Key) < len(prefix) || row.Key[:len(prefix)] != prefix {
			continue
		}
		var heartbeat Heartbeat
		if err := json.Unmarshal(row.Value, &heartbeat); err != nil {
			return Overview{}, fmt.Errorf("decode process heartbeat: %w", err)
		}
		out.Processes = append(out.Processes, Process{
			Role: row.Key[len(prefix):], PID: heartbeat.PID, Host: heartbeat.Host, Release: heartbeat.Release,
			StartedAt: heartbeat.StartedAt, At: row.UpdatedAt, Alive: now.Sub(row.UpdatedAt) < staleHeartbeat,
		})
	}
	if out.Processes == nil {
		out.Processes = []Process{}
	}
	for _, row := range data.Receipts {
		out.Receipts.Counts[row.Status] = row.N
	}
	out.Grouping = Grouping{Waiting: len(data.Grouping), Items: []GroupItem{}}
	for _, row := range data.Grouping {
		if now.Sub(row.Since) >= groupingWarnAfter {
			out.Grouping.NeedsAttention++
		}
		row.Recovery = recovery(row)
		if len(out.Grouping.Items) < 30 {
			out.Grouping.Items = append(out.Grouping.Items, row.GroupItem)
		}
	}
	return out, nil
}

type RequeueResult struct {
	Requeued int64 `json:"requeued"`
}

// RequeueFailed resets recent failed articles. The TypeScript worker's content.sweep
// observes these rows during the migration window and queues the next processing step.
func (s *Service) RequeueFailed(ctx context.Context, group *string, reason, actor string) (RequeueResult, error) {
	if strings.TrimFunc(reason, isJavaScriptWhitespace) == "" {
		return RequeueResult{}, ErrInvalidRequeue
	}
	repository, ok := s.repository.(RequeueRepository)
	if !ok {
		return RequeueResult{}, ErrRequeueUnavailable
	}
	subject := "failure:all"
	if group != nil && *group != "" {
		subject = "failure:" + utf16Slice(*group, 80)
	}
	return repository.RequeueFailed(ctx, group, reason, actor, subject)
}

func isJavaScriptWhitespace(char rune) bool {
	switch char {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	default:
		return char >= '\u2000' && char <= '\u200a'
	}
}

func utf16Slice(value string, maxUnits int) string {
	units := 0
	for index, character := range value {
		width := 1
		if character > 0xffff {
			width = 2
		}
		if units+width > maxUnits {
			return value[:index]
		}
		units += width
	}
	return value
}

func recovery(row WaitingGroup) string {
	if row.ReceiptStatus != nil && *row.ReceiptStatus == "unknown" {
		if row.Released {
			return "manual"
		}
		return "receipt"
	}
	if !row.Failed || row.Running || (row.ReceiptStatus != nil && *row.ReceiptStatus == "pending") {
		return "automatic"
	}
	return "manual"
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
