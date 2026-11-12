package receiptrecovery

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	AutoReleaseAfter  = 30 * time.Minute
	StalePendingAfter = 10 * time.Minute
	AutoReleaseNote   = "自动放行：结果未知超过 30 分钟，未核对是否计费"
)

var ErrInvalidRequest = errors.New("note is required")
var ErrNotUnknown = errors.New("只有结果未知的回执需要人工核对")

type Result struct {
	ID       int64   `json:"id"`
	Status   string  `json:"status"`
	Subject  *string `json:"subject"`
	Purpose  string  `json:"purpose"`
	Requeued bool    `json:"requeued"`
}

type RecoveryCounts struct {
	Stale    int64 `json:"stale"`
	Released int64 `json:"released"`
	Requeued int64 `json:"requeued"`
}

type Repository interface {
	Status(context.Context, int64) (string, bool, error)
	Release(context.Context, int64, string, string, string, *bool) (*Result, bool, error)
	MarkStalePending(context.Context, time.Time) (int64, error)
	DueUnknown(context.Context, time.Time, int) ([]int64, error)
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) ReleaseManual(ctx context.Context, id int64, billed bool, note, actor string) (*Result, bool, error) {
	note = strings.TrimFunc(note, isJavaScriptWhitespace)
	if id < 1 || note == "" {
		return nil, false, ErrInvalidRequest
	}
	status, found, err := s.repository.Status(ctx, id)
	if err != nil || !found {
		return nil, found, err
	}
	if status != "unknown" {
		return nil, true, ErrNotUnknown
	}
	errorText := "人工核对：供应商未计费。" + note
	if billed {
		errorText = "人工核对：供应商已计费但结果未取回。" + note
	}
	result, found, err := s.repository.Release(ctx, id, errorText, actor, note, &billed)
	if err == nil && !found {
		return nil, false, nil
	}
	return result, found, err
}

func (s *Service) Recover(ctx context.Context, now time.Time) (RecoveryCounts, error) {
	if now.IsZero() {
		now = time.Now()
	}
	stale, err := s.repository.MarkStalePending(ctx, now.Add(-StalePendingAfter))
	if err != nil {
		return RecoveryCounts{}, err
	}
	ids, err := s.repository.DueUnknown(ctx, now.Add(-AutoReleaseAfter), 200)
	if err != nil {
		return RecoveryCounts{}, err
	}
	counts := RecoveryCounts{Stale: stale}
	for _, id := range ids {
		result, found, err := s.repository.Release(ctx, id, AutoReleaseNote, "ops.recover", "结果未知，自动放行一次", nil)
		if err != nil {
			return counts, err
		}
		if !found || result == nil {
			continue
		}
		counts.Released++
		if result.Requeued {
			counts.Requeued++
		}
	}
	return counts, nil
}

func isJavaScriptWhitespace(char rune) bool {
	switch char {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	default:
		return char >= '\u2000' && char <= '\u200a'
	}
}
