package admindeliveries

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

type HTTPWebhookSender struct {
	Client *http.Client
}

func (s HTTPWebhookSender) Send(ctx context.Context, url string, card any) (WebhookResult, error) {
	body, err := json.Marshal(map[string]any{"msg_type": "interactive", "card": card})
	if err != nil {
		return WebhookResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return WebhookResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return WebhookResult{}, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return WebhookResult{}, err
	}
	result := WebhookResult{Status: "unknown", Body: string(content)}
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		result.Status = "failed"
	}
	var acknowledgement struct {
		Code       *int `json:"code"`
		StatusCode *int `json:"StatusCode"`
	}
	if json.Unmarshal(content, &acknowledgement) == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		code := acknowledgement.Code
		if code == nil {
			code = acknowledgement.StatusCode
		}
		if code != nil {
			if *code == 0 {
				result.Status = "sent"
			} else {
				result.Status = "failed"
			}
		}
	}
	result.Body = truncateUTF16(result.Body, 500)
	return result, nil
}
