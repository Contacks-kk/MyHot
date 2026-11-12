package indexnow

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

const endpoint = "https://api.indexnow.org/indexnow"

type HTTPSubmitter struct {
	client   *http.Client
	endpoint string
}

func NewHTTPSubmitter(client *http.Client) *HTTPSubmitter {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPSubmitter{client: client, endpoint: endpoint}
}

func (s *HTTPSubmitter) Submit(ctx context.Context, payload Submission) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := s.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}
