package collector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aihot/backend-go/internal/receipts"
)

type XPage struct {
	Tweets     []tweet `json:"tweets"`
	NextCursor string  `json:"next_cursor"`
	ReceiptID  int64   `json:"-"`
}

type XProvider interface {
	Search(context.Context, string, string, string, string, string) (XPage, error)
}

type SocialData struct {
	Key      string
	BaseURL  string
	Receipts *receipts.Service
	Client   HTTPDoer
}

func NewSocialData(key, baseURL string, service *receipts.Service, client HTTPDoer) *SocialData {
	if baseURL == "" {
		baseURL = "https://api.socialdata.tools"
	}
	return &SocialData{Key: key, BaseURL: strings.TrimRight(baseURL, "/"), Receipts: service, Client: client}
}

type xProviderError struct {
	status  int
	message string
}

func (e xProviderError) Error() string          { return e.message }
func (e xProviderError) ProviderRejected() bool { return true }
func (e xProviderError) SafeMessage() string    { return e.message }

func (p *SocialData) Search(ctx context.Context, query, searchType, cursor, subject, window string) (XPage, error) {
	if strings.TrimSpace(p.Key) == "" {
		return XPage{}, errors.New("SOCIALDATA_API_KEY is not configured")
	}
	if p.Receipts == nil {
		return XPage{}, errors.New("SocialData receipt service is not configured")
	}
	if searchType == "" {
		searchType = "Latest"
	}
	identity := map[string]any{"query": query, "type": searchType, "cursor": nil, "window": window}
	if cursor != "" {
		identity["cursor"] = cursor
	}
	result, err := p.Receipts.Do(ctx, receipts.Request{
		Service: "socialdata", Purpose: "source_fetch", Subject: subject, Identity: identity,
		Summary: map[string]any{"query": query, "type": searchType},
	}, func(callCtx context.Context) (receipts.Outcome, error) {
		endpoint, err := url.Parse(p.BaseURL + "/twitter/search")
		if err != nil || endpoint.Scheme == "" || endpoint.Hostname() == "" {
			return receipts.Outcome{}, errors.New("SocialData base URL is invalid")
		}
		params := endpoint.Query()
		params.Set("query", query)
		params.Set("type", searchType)
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		endpoint.RawQuery = params.Encode()
		requestCtx, cancel := context.WithTimeout(callCtx, 60*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return receipts.Outcome{}, errors.New("SocialData request could not be built")
		}
		req.Header.Set("Authorization", "Bearer "+p.Key)
		req.Header.Set("Accept", "application/json")
		client := p.Client
		if client == nil {
			client = publicClient()
		}
		res, err := client.Do(req)
		if err != nil {
			return receipts.Outcome{}, errors.New("SocialData request failed")
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 12*1024*1024+1))
		if err != nil {
			return receipts.Outcome{}, errors.New("SocialData response could not be read")
		}
		if len(body) > 12*1024*1024 {
			return receipts.Outcome{}, errors.New("SocialData response is too large")
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return receipts.Outcome{}, xProviderError{status: res.StatusCode, message: fmt.Sprintf("socialdata HTTP %d", res.StatusCode)}
		}
		var response struct {
			Tweets     []tweet `json:"tweets"`
			NextCursor string  `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return receipts.Outcome{}, errors.New("SocialData response was invalid JSON")
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return receipts.Outcome{}, errors.New("SocialData response could not be recorded")
		}
		count := len(response.Tweets)
		usage, _ := json.Marshal(map[string]int{"tweets": count})
		cost := float64(count) * 0.0002
		return receipts.Outcome{
			Response: encoded, Usage: usage, Cost: &cost, Currency: "USD", CostBasis: "estimated",
		}, nil
	})
	if err != nil {
		return XPage{}, err
	}
	var page XPage
	if err := json.Unmarshal(result.Response, &page); err != nil {
		return XPage{}, errors.New("saved SocialData response was invalid")
	}
	page.ReceiptID = result.ReceiptID
	return page, nil
}

// ReadArticle retrieves the long-form article attached to a post. The post id, rather than the
// number in its /i/article link, is the provider lookup key. The saved response is reusable across
// River retries until the caller commits the body change.
func (p *SocialData) ReadArticle(ctx context.Context, tweetID, articleID string) (json.RawMessage, int64, error) {
	if strings.TrimSpace(p.Key) == "" || p.Receipts == nil {
		return nil, 0, errors.New("SocialData article reader is not configured")
	}
	result, err := p.Receipts.Do(ctx, receipts.Request{
		Service: "socialdata", Purpose: "x_article", Subject: "article:" + articleID,
		Identity: map[string]string{"article": tweetID}, Summary: map[string]any{"article": tweetID},
	}, func(callCtx context.Context) (receipts.Outcome, error) {
		endpoint, err := url.Parse(p.BaseURL + "/twitter/article/" + url.PathEscape(tweetID))
		if err != nil || endpoint.Scheme == "" || endpoint.Hostname() == "" {
			return receipts.Outcome{}, errors.New("SocialData base URL is invalid")
		}
		requestCtx, cancel := context.WithTimeout(callCtx, 30*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return receipts.Outcome{}, errors.New("SocialData article request could not be built")
		}
		request.Header.Set("Authorization", "Bearer "+p.Key)
		request.Header.Set("Accept", "application/json")
		client := p.Client
		if client == nil {
			client = publicClient()
		}
		response, err := client.Do(request)
		if err != nil {
			return receipts.Outcome{}, errors.New("SocialData article request failed")
		}
		defer response.Body.Close()
		content, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
		if err != nil {
			return receipts.Outcome{}, errors.New("SocialData article response could not be read")
		}
		if len(content) > 4*1024*1024 {
			return receipts.Outcome{}, errors.New("SocialData article response is too large")
		}
		if response.StatusCode == http.StatusNotFound {
			return receipts.Outcome{Response: json.RawMessage("null"), Usage: json.RawMessage(`{"articles":0}`)}, nil
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return receipts.Outcome{}, xProviderError{status: response.StatusCode, message: fmt.Sprintf("socialdata HTTP %d", response.StatusCode)}
		}
		var payload struct {
			Article json.RawMessage `json:"article"`
			Status  string          `json:"status"`
		}
		if err := json.Unmarshal(content, &payload); err != nil {
			return receipts.Outcome{}, errors.New("SocialData article response was invalid JSON")
		}
		article := payload.Article
		count := 0
		if payload.Status == "error" || len(article) == 0 || string(article) == "null" {
			article = json.RawMessage("null")
		} else {
			count = 1
		}
		usage, _ := json.Marshal(map[string]int{"articles": count})
		cost := 0.0002
		return receipts.Outcome{Response: article, Usage: usage, Cost: &cost, Currency: "USD", CostBasis: "estimated"}, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return result.Response, result.ReceiptID, nil
}

func xWindowKey(now time.Time) string {
	window := time.UnixMilli((now.UnixMilli() / 1_800_000) * 1_800_000).UTC()
	return window.Format("2006-01-02T15:04:05.000Z")
}

func xTweetIDAt(at time.Time) string {
	const epoch = int64(1288834974657)
	value := (at.UnixMilli() - epoch) << 22
	return fmt.Sprintf("%d", value)
}

func xHashIDs(ids []string) string {
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return fmt.Sprintf("%x", sum[:])
}
