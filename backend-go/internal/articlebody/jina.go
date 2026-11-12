package articlebody

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aihot/backend-go/internal/collector"
	"aihot/backend-go/internal/receipts"
)

const jinaContentMarker = "\nMarkdown Content:\n"

type jinaReader struct {
	key      string
	baseURL  string
	client   collector.HTTPDoer
	receipts *receipts.Service
}

func NewJinaReader(key, baseURL string, client collector.HTTPDoer, receiptService *receipts.Service) PaidReader {
	if strings.TrimSpace(key) == "" || receiptService == nil {
		return nil
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://r.jina.ai"
	}
	if client == nil {
		client = collector.NewHTTPClientWithTimeout(60 * time.Second)
	}
	return &jinaReader{key: key, baseURL: strings.TrimRight(baseURL, "/"), client: client, receipts: receiptService}
}

func (r *jinaReader) Read(ctx context.Context, targetURL, articleID string) (string, int64, error) {
	day := time.Now().UTC().Format("2006-01-02")
	result, err := r.receipts.Do(ctx, receipts.Request{
		Service: "jina", Purpose: "body_fallback", Subject: "article:" + articleID,
		Identity: map[string]string{"url": targetURL, "day": day, "format": "markdown"},
		Summary:  map[string]any{"url": targetURL},
	}, func(ctx context.Context) (receipts.Outcome, error) {
		requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, r.baseURL+"/"+targetURL, nil)
		if err != nil {
			return receipts.Outcome{}, providerFailure{message: "Jina request could not be created", rejected: true}
		}
		request.Header.Set("Authorization", "Bearer "+r.key)
		request.Header.Set("X-Return-Format", "markdown")
		request.Header.Set("Accept", "text/plain")
		response, err := r.client.Do(request)
		if err != nil {
			return receipts.Outcome{}, providerFailure{message: "Jina request transport failed"}
		}
		defer response.Body.Close()
		content, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes+1))
		if err != nil {
			return receipts.Outcome{}, providerFailure{message: "Jina response could not be read"}
		}
		if len(content) > maxPageBytes {
			return receipts.Outcome{}, providerFailure{message: "Jina response exceeded the body size limit", rejected: true}
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return receipts.Outcome{}, providerFailure{message: fmt.Sprintf("Jina returned HTTP %d", response.StatusCode), rejected: true}
		}
		var parsed struct {
			Text   string `json:"text"`
			Status int    `json:"status"`
		}
		if err := json.Unmarshal(content, &parsed); err == nil && parsed.Text != "" {
			content = []byte(parsed.Text)
		}
		text := string(content)
		responseText := truncateUTF16(text, 2_000_000)
		var usage json.RawMessage
		tokens := strings.TrimSpace(response.Header.Get("X-Usage-Tokens"))
		usageValues := map[string]any{"bytes": len(content)}
		var cost *float64
		currency, costBasis := "", ""
		if tokenCount, parseErr := strconv.Atoi(tokens); parseErr == nil && tokenCount > 0 {
			usageValues["tokens"] = tokenCount
			value := float64(tokenCount) / 1_000_000 * 0.36
			cost = &value
			currency, costBasis = "CNY", "estimated"
		}
		usage, _ = json.Marshal(usageValues)
		outcome := receipts.Outcome{Response: json.RawMessage(mustJSON(map[string]any{"text": responseText, "status": response.StatusCode})),
			RequestID: response.Header.Get("X-Request-ID"), Usage: usage, Cost: cost, Currency: currency, CostBasis: costBasis}
		return outcome, nil
	})
	if err != nil {
		return "", 0, err
	}
	var saved struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Response, &saved); err != nil {
		return "", result.ReceiptID, err
	}
	markdown := saved.Text
	if index := strings.Index(markdown, jinaContentMarker); index >= 0 {
		markdown = markdown[index+len(jinaContentMarker):]
	}
	return strings.TrimSpace(markdown), result.ReceiptID, nil
}

func (r *jinaReader) Complete(ctx context.Context, receiptID int64) error {
	return r.receipts.Complete(ctx, receiptID)
}

type providerFailure struct {
	message  string
	rejected bool
}

func (e providerFailure) Error() string          { return e.message }
func (e providerFailure) SafeMessage() string    { return e.message }
func (e providerFailure) ProviderRejected() bool { return e.rejected }

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"text":""}`)
	}
	return encoded
}

func truncateUTF16(value string, limit int) string {
	units := 0
	for offset, current := range value {
		width := 1
		if current > 0xffff {
			width = 2
		}
		if units+width > limit {
			return value[:offset]
		}
		units += width
	}
	return value
}
