package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aihot/backend-go/internal/receipts"
)

const (
	jinaSourcePrefix    = "https://r.jina.ai/"
	jinaContentMarker   = "\nMarkdown Content:\n"
	maxJinaListingBytes = 6 * 1024 * 1024
)

/** JinaPage contains both provider framing and rendered page content from one paid read. */
type JinaPage struct {
	Raw       string
	Content   string
	ReceiptID int64
}

/** JinaReader makes paid Jina reads available to source collection and preview. */
type JinaReader interface {
	/** Read fetches a rendering and persists its response before returning it. */
	Read(context.Context, string, string, string, string, *int) (JinaPage, error)
	/** Complete marks a rendering receipt used by a successful source operation. */
	Complete(context.Context, int64) error
}

/** Jina calls the paid Jina rendering API through the shared receipt system. */
type Jina struct {
	key      string
	baseURL  string
	client   HTTPDoer
	receipts *receipts.Service
}

/** NewJina configures paid Jina listing and detail reads behind the receipt service. */
func NewJina(key, baseURL string, client HTTPDoer, receiptService *receipts.Service) *Jina {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://r.jina.ai"
	}
	if client == nil {
		client = NewHTTPClientWithTimeout(60 * time.Second)
	}
	return &Jina{key: key, baseURL: strings.TrimRight(baseURL, "/"), client: client, receipts: receiptService}
}

/** Read records one Jina rendering before returning its content for collection. */
func (j *Jina) Read(ctx context.Context, targetURL, sourceID, purpose, format string, cacheToleranceSeconds *int) (JinaPage, error) {
	if strings.TrimSpace(j.key) == "" || j.receipts == nil {
		return JinaPage{}, errors.New("Jina reader is not configured")
	}
	target, err := url.Parse(targetURL)
	if err != nil || validURL(target) != nil {
		return JinaPage{}, errors.New("Jina target must be a public HTTP or HTTPS URL without credentials")
	}
	if format != "html" {
		format = "markdown"
	}
	readID := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := j.receipts.Do(ctx, receipts.Request{
		Service: "jina",
		Purpose: purpose,
		Subject: "source:" + sourceID,
		Identity: map[string]string{
			"url": targetURL, "day": readID, "format": format,
		},
		Summary: map[string]any{"url": targetURL},
	}, func(callCtx context.Context) (receipts.Outcome, error) {
		return j.request(callCtx, targetURL, format, cacheToleranceSeconds)
	})
	if err != nil {
		return JinaPage{}, err
	}
	var saved struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Response, &saved); err != nil {
		return JinaPage{}, errors.New("saved Jina response was invalid")
	}
	content := saved.Text
	if index := strings.Index(content, jinaContentMarker); index >= 0 {
		content = content[index+len(jinaContentMarker):]
	}
	return JinaPage{Raw: saved.Text, Content: strings.TrimSpace(content), ReceiptID: result.ReceiptID}, nil
}

/** request calls Jina and records the provider response and usage on its paid receipt. */
func (j *Jina) request(ctx context.Context, targetURL, format string, cacheToleranceSeconds *int) (receipts.Outcome, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, j.baseURL+"/"+targetURL, nil)
	if err != nil {
		return receipts.Outcome{}, jinaProviderError{message: "Jina request could not be created", rejected: true}
	}
	request.Header.Set("Authorization", "Bearer "+j.key)
	request.Header.Set("X-Return-Format", format)
	request.Header.Set("Accept", "text/plain")
	if cacheToleranceSeconds != nil && *cacheToleranceSeconds >= 0 {
		request.Header.Set("X-Cache-Tolerance", strconv.Itoa(*cacheToleranceSeconds))
	}
	response, err := j.client.Do(request)
	if err != nil {
		return receipts.Outcome{}, jinaProviderError{message: "Jina request transport failed"}
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, maxJinaListingBytes+1))
	if err != nil {
		return receipts.Outcome{}, jinaProviderError{message: "Jina response could not be read"}
	}
	if len(content) > maxJinaListingBytes {
		return receipts.Outcome{}, jinaProviderError{message: "Jina response exceeded the listing size limit", rejected: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return receipts.Outcome{}, jinaProviderError{message: fmt.Sprintf("Jina returned HTTP %d", response.StatusCode), rejected: true}
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parsed); err == nil && parsed.Text != "" {
		content = []byte(parsed.Text)
	}
	text := truncateUTF16(string(content), 2_000_000)
	usageValues := map[string]any{"bytes": len(content)}
	var cost *float64
	if tokenCount, parseErr := strconv.Atoi(strings.TrimSpace(response.Header.Get("X-Usage-Tokens"))); parseErr == nil && tokenCount > 0 {
		usageValues["tokens"] = tokenCount
		amount := float64(tokenCount) / 1_000_000 * 0.36
		cost = &amount
	}
	usage, _ := json.Marshal(usageValues)
	encoded, err := json.Marshal(map[string]any{"text": text, "status": response.StatusCode})
	if err != nil {
		return receipts.Outcome{}, errors.New("Jina response could not be recorded")
	}
	currency, basis := "", ""
	if cost != nil {
		currency, basis = "CNY", "estimated"
	}
	return receipts.Outcome{
		Response:  encoded,
		RequestID: response.Header.Get("X-Request-ID"),
		Usage:     usage,
		Cost:      cost,
		Currency:  currency,
		CostBasis: basis,
	}, nil
}

/** Complete marks a Jina receipt used by a successful collection transaction. */
func (j *Jina) Complete(ctx context.Context, receiptID int64) error {
	return j.receipts.Complete(ctx, receiptID)
}

type jinaProviderError struct {
	message  string
	rejected bool
}

/** Error returns the safe diagnostic stored on a receipt. */
func (e jinaProviderError) Error() string { return e.message }

/** SafeMessage exposes the provider-safe message without request credentials or body data. */
func (e jinaProviderError) SafeMessage() string { return e.message }

/** ProviderRejected identifies errors for which Jina did not accept a billable request. */
func (e jinaProviderError) ProviderRejected() bool { return e.rejected }

/** jinaTargetURL extracts the page URL embedded in a Jina-backed source URL. */
func jinaTargetURL(address string) (string, bool) {
	if !strings.HasPrefix(address, jinaSourcePrefix) {
		return "", false
	}
	return strings.TrimPrefix(address, jinaSourcePrefix), true
}

/** enrichJinaDetail applies configured regular expressions to a paid Jina rendering. */
func enrichJinaDetail(candidate *item, page JinaPage, detail settings, title, date bool) {
	for _, content := range []string{page.Raw, page.Content} {
		if title && detail.text("titleRegex") != "" {
			if expression, err := regexp.Compile(detail.text("titleRegex")); err == nil {
				if match := expression.FindStringSubmatch(content); len(match) > 1 && strings.TrimSpace(match[1]) != "" {
					candidate.Title = strings.TrimSpace(match[1])
					title = false
				}
			}
		}
		if date && detail.text("publishedAtRegex") != "" {
			if expression, err := regexp.Compile(detail.text("publishedAtRegex")); err == nil {
				if match := expression.FindStringSubmatch(content); len(match) > 1 {
					if parsed := parseDate(match[1], detail); parsed != nil {
						candidate.Published = parsed
						date = false
					}
				}
			}
		}
		if !title && !date {
			return
		}
	}
}
