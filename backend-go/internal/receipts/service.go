package receipts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const PendingStaleAfter = 10 * time.Minute

var ErrBusy = errors.New("paid request is already in flight")

type OutcomeUnknownError struct{ ReceiptID int64 }

func (e *OutcomeUnknownError) Error() string {
	return fmt.Sprintf("paid request receipt %d has an unknown outcome", e.ReceiptID)
}

type BudgetExceededError struct {
	Service          string
	Window           string
	RetryAfterSecond int
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("budget for %s is exhausted (%s)", e.Service, e.Window)
}

type Request struct {
	Service    string
	Model      string
	Purpose    string
	Subject    string
	Identity   any
	Summary    map[string]any
	AttemptTag string
}

type Outcome struct {
	Response  json.RawMessage
	RequestID string
	Usage     json.RawMessage
	Cost      *float64
	Currency  string
	CostBasis string
}

type Result struct {
	ReceiptID int64
	Response  json.RawMessage
	Reused    bool
}

type Claim struct {
	ReceiptID int64
	AttemptID int64
	Response  json.RawMessage
	Reused    bool
	Busy      bool
	Unknown   bool
}

type Repository interface {
	Claim(context.Context, Request, string) (Claim, error)
	RecordFailure(context.Context, Claim, string, string, time.Duration) error
	RecordReceived(context.Context, Claim, Outcome, time.Duration) error
	Complete(context.Context, int64) error
	Reject(context.Context, int64, string) error
}

type Caller func(context.Context) (Outcome, error)

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

// Do persists the request identity before calling a paid provider. Received responses
// are reusable until the caller commits its business result with Complete or Reject.
func (s *Service) Do(ctx context.Context, request Request, call Caller) (Result, error) {
	if strings.TrimSpace(request.Service) == "" || strings.TrimSpace(request.Purpose) == "" || call == nil {
		return Result{}, errors.New("paid request is incomplete")
	}
	key, err := LogicalKeyFor(request)
	if err != nil {
		return Result{}, err
	}
	claim, err := s.repository.Claim(ctx, request, key)
	if err != nil {
		return Result{}, err
	}
	if claim.Busy {
		return Result{}, ErrBusy
	}
	if claim.Unknown {
		return Result{}, &OutcomeUnknownError{ReceiptID: claim.ReceiptID}
	}
	if claim.Reused {
		return Result{ReceiptID: claim.ReceiptID, Response: claim.Response, Reused: true}, nil
	}

	started := time.Now()
	outcome, callErr := call(ctx)
	latency := time.Since(started)
	if callErr != nil {
		status, message := failure(callErr)
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		persistErr := s.repository.RecordFailure(persistCtx, claim, status, message, latency)
		cancel()
		if persistErr != nil {
			return Result{}, persistErr
		}
		if status == "unknown" {
			return Result{}, &OutcomeUnknownError{ReceiptID: claim.ReceiptID}
		}
		return Result{}, callErr
	}
	if !json.Valid(outcome.Response) {
		outcome.Response, err = json.Marshal(map[string]string{"unparsable": truncate(string(outcome.Response), 20000)})
		if err != nil {
			return Result{}, errors.New("provider response could not be recorded")
		}
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	err = s.repository.RecordReceived(persistCtx, claim, outcome, latency)
	cancel()
	if err != nil {
		return Result{}, err
	}
	return Result{ReceiptID: claim.ReceiptID, Response: outcome.Response}, nil
}

func (s *Service) Complete(ctx context.Context, receiptID int64) error {
	return s.repository.Complete(ctx, receiptID)
}

func (s *Service) Reject(ctx context.Context, receiptID int64, reason string) error {
	return s.repository.Reject(ctx, receiptID, reason)
}

// ProviderRejection marks failures known not to have reached a billable provider.
type ProviderRejection interface {
	error
	ProviderRejected() bool
}

type safeFailure interface{ SafeMessage() string }

func failure(err error) (string, string) {
	var rejected ProviderRejection
	var safe safeFailure
	message := "provider request outcome is unknown"
	if errors.As(err, &safe) && safe.SafeMessage() != "" {
		message = safe.SafeMessage()
	}
	if errors.As(err, &rejected) && rejected.ProviderRejected() {
		return "failed", truncate(message, 2000)
	}
	return "unknown", truncate(message, 2000)
}

func LogicalKeyFor(request Request) (string, error) {
	identity, err := canonicalJSON(request.Identity)
	if err != nil {
		return "", fmt.Errorf("encode paid request identity: %w", err)
	}
	sum := sha256.Sum256(identity)
	modelName := request.Model
	if modelName == "" {
		modelName = "-"
	}
	attemptTag := request.AttemptTag
	if attemptTag == "" {
		attemptTag = "0"
	}
	return strings.Join([]string{request.Service, request.Purpose, modelName, hex.EncodeToString(sum[:]), attemptTag}, ":"), nil
}

func canonicalJSON(value any) ([]byte, error) {
	var encoded strings.Builder
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	result := strings.TrimSuffix(encoded.String(), "\n")
	return []byte(result), nil
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
