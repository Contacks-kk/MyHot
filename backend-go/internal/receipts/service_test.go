package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryRepository struct {
	claim       Claim
	claimErr    error
	failure     string
	failureText string
	response    json.RawMessage
	completed   int64
	rejected    int64
}

func (r *memoryRepository) Claim(_ context.Context, _ Request, _ string) (Claim, error) {
	return r.claim, r.claimErr
}

func (r *memoryRepository) RecordFailure(_ context.Context, _ Claim, status, message string, _ time.Duration) error {
	r.failure, r.failureText = status, message
	return nil
}

func (r *memoryRepository) RecordReceived(_ context.Context, _ Claim, outcome Outcome, _ time.Duration) error {
	r.response = outcome.Response
	return nil
}

func (r *memoryRepository) Complete(_ context.Context, id int64) error {
	r.completed = id
	return nil
}

func (r *memoryRepository) Reject(_ context.Context, id int64, _ string) error {
	r.rejected = id
	return nil
}

type rejectedProviderError struct{ message string }

func (e rejectedProviderError) Error() string        { return "unsafe provider response" }
func (e rejectedProviderError) SafeMessage() string  { return e.message }
func (rejectedProviderError) ProviderRejected() bool { return true }

func TestDoRecordsResponseAndReusesIt(t *testing.T) {
	repository := &memoryRepository{claim: Claim{ReceiptID: 14, AttemptID: 27}}
	service := NewService(repository)
	request := Request{Service: "vendor", Model: "model-a", Purpose: "summarize", Identity: map[string]any{"revision": 3}}
	calls := 0
	result, err := service.Do(context.Background(), request, func(context.Context) (Outcome, error) {
		calls++
		return Outcome{Response: json.RawMessage(`{"answer":"ok"}`)}, nil
	})
	if err != nil || result.ReceiptID != 14 || result.Reused || calls != 1 || string(repository.response) != `{"answer":"ok"}` {
		t.Fatalf("first request = %#v, calls=%d, error=%v", result, calls, err)
	}
	if err := service.Complete(context.Background(), result.ReceiptID); err != nil || repository.completed != 14 {
		t.Fatalf("complete receipt = %d, %v", repository.completed, err)
	}

	repository.claim = Claim{ReceiptID: 14, Response: json.RawMessage(`{"answer":"ok"}`), Reused: true}
	result, err = service.Do(context.Background(), request, func(context.Context) (Outcome, error) {
		t.Fatal("reused response called provider")
		return Outcome{}, nil
	})
	if err != nil || !result.Reused || result.ReceiptID != 14 || calls != 1 {
		t.Fatalf("reused request = %#v, calls=%d, error=%v", result, calls, err)
	}
}

func TestDoPreservesMalformedProviderBodyForDiagnosis(t *testing.T) {
	repository := &memoryRepository{claim: Claim{ReceiptID: 5, AttemptID: 6}}
	service := NewService(repository)
	result, err := service.Do(context.Background(), Request{Service: "vendor", Purpose: "score"}, func(context.Context) (Outcome, error) {
		return Outcome{Response: json.RawMessage("not json")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]string
	if err := json.Unmarshal(repository.response, &saved); err != nil || saved["unparsable"] != "not json" || string(result.Response) != string(repository.response) {
		t.Fatalf("saved malformed response = %s: %v", repository.response, err)
	}
}

func TestDoDoesNotRepeatBusyOrUnknownRequests(t *testing.T) {
	for _, test := range []struct {
		name  string
		claim Claim
		want  error
	}{
		{name: "busy", claim: Claim{ReceiptID: 7, Busy: true}, want: ErrBusy},
		{name: "unknown", claim: Claim{ReceiptID: 8, Unknown: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(&memoryRepository{claim: test.claim})
			calls := 0
			_, err := service.Do(context.Background(), Request{Service: "vendor", Purpose: "score"}, func(context.Context) (Outcome, error) {
				calls++
				return Outcome{}, nil
			})
			if calls != 0 {
				t.Fatalf("provider calls = %d", calls)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.name == "unknown" {
				var unknown *OutcomeUnknownError
				if !errors.As(err, &unknown) || unknown.ReceiptID != 8 {
					t.Fatalf("unknown error = %#v", err)
				}
			}
		})
	}
}

func TestDoDistinguishesDefinitiveRejectionFromUnknownOutcome(t *testing.T) {
	for _, test := range []struct {
		name        string
		err         error
		wantStatus  string
		wantUnknown bool
	}{
		{name: "rejected", err: rejectedProviderError{message: "provider rejected request"}, wantStatus: "failed"},
		{name: "transport outcome unknown", err: errors.New("secret key must not be persisted"), wantStatus: "unknown", wantUnknown: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &memoryRepository{claim: Claim{ReceiptID: 22, AttemptID: 33}}
			service := NewService(repository)
			_, err := service.Do(context.Background(), Request{Service: "vendor", Purpose: "score"}, func(context.Context) (Outcome, error) {
				return Outcome{}, test.err
			})
			if repository.failure != test.wantStatus {
				t.Fatalf("receipt status = %q, want %q", repository.failure, test.wantStatus)
			}
			if strings.Contains(repository.failureText, "secret key") {
				t.Fatalf("persisted error contains sensitive text: %q", repository.failureText)
			}
			var unknown *OutcomeUnknownError
			if errors.As(err, &unknown) != test.wantUnknown {
				t.Fatalf("error = %v, unknown=%t", err, test.wantUnknown)
			}
		})
	}
}

func TestLogicalKeySortsIdentityObjects(t *testing.T) {
	first, err := LogicalKeyFor(Request{Service: "llm", Purpose: "score", Model: "m", Identity: map[string]any{
		"revision": 4, "prompt": "v1", "parts": []any{map[string]any{"b": true, "a": 1}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := LogicalKeyFor(Request{Service: "llm", Purpose: "score", Model: "m", Identity: map[string]any{
		"parts": []any{map[string]any{"a": 1, "b": true}}, "prompt": "v1", "revision": 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same identity produced different keys: %s != %s", first, second)
	}
}
