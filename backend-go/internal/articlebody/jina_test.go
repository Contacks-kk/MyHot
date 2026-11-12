package articlebody

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/receipts"
)

type memoryReceiptRepository struct {
	request   receipts.Request
	response  json.RawMessage
	completed int64
}

func (r *memoryReceiptRepository) Claim(_ context.Context, request receipts.Request, _ string) (receipts.Claim, error) {
	r.request = request
	return receipts.Claim{ReceiptID: 23, AttemptID: 31}, nil
}
func (*memoryReceiptRepository) RecordFailure(context.Context, receipts.Claim, string, string, time.Duration) error {
	return nil
}
func (r *memoryReceiptRepository) RecordReceived(_ context.Context, _ receipts.Claim, outcome receipts.Outcome, _ time.Duration) error {
	r.response = outcome.Response
	return nil
}
func (r *memoryReceiptRepository) Complete(_ context.Context, receiptID int64) error {
	r.completed = receiptID
	return nil
}
func (*memoryReceiptRepository) Reject(context.Context, int64, string) error { return nil }

func TestJinaReaderUsesReceiptAndReturnsMarkdownAfterProviderHeader(t *testing.T) {
	var sawRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		sawRequest = true
		if request.Method != http.MethodGet || request.URL.Path != "/https://example.com/article" || request.URL.Query().Get("q") != "1" {
			t.Errorf("unexpected Jina request target: %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer jina-test-key" || request.Header.Get("X-Return-Format") != "markdown" || request.Header.Get("Accept") != "text/plain" {
			t.Errorf("missing Jina request headers: %#v", request.Header)
		}
		w.Header().Set("X-Request-ID", "jina-request-1")
		w.Header().Set("X-Usage-Tokens", "100")
		_, _ = w.Write([]byte("Title: sample\nURL Source: https://example.com/article\n\nMarkdown Content:\n# Hello\n\nArticle body"))
	}))
	defer server.Close()

	repository := &memoryReceiptRepository{}
	reader := NewJinaReader("jina-test-key", server.URL, server.Client(), receipts.NewService(repository))
	markdown, receiptID, err := reader.Read(context.Background(), "https://example.com/article?q=1", "article-1")
	if err != nil {
		t.Fatal(err)
	}
	if !sawRequest || receiptID != 23 || !strings.HasPrefix(markdown, "# Hello") || strings.Contains(markdown, "URL Source") {
		t.Fatalf("Read() = %q, %d; request seen=%t", markdown, receiptID, sawRequest)
	}
	var saved map[string]any
	if err := json.Unmarshal(repository.response, &saved); err != nil || saved["status"] != float64(http.StatusOK) {
		t.Fatalf("receipt response = %s, %v", repository.response, err)
	}
	if repository.request.Service != "jina" || repository.request.Purpose != "body_fallback" || repository.request.Subject != "article:article-1" {
		t.Fatalf("receipt request = %#v", repository.request)
	}
	if err := reader.Complete(context.Background(), receiptID); err != nil || repository.completed != receiptID {
		t.Fatalf("Complete() = %v, receipt %d", err, repository.completed)
	}
}
