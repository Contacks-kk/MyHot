package mpcollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aihot/backend-go/internal/modelconfig"
	"aihot/backend-go/internal/receipts"
)

const defaultDajialaURL = "https://www.dajiala.com"

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Provider interface {
	History(context.Context, string, string, string) (History, error)
	Article(context.Context, string, string, string) (Article, error)
}

type History struct {
	Posts       []Post   `json:"data"`
	Nickname    string   `json:"nickname"`
	RemainMoney *float64 `json:"remain_money"`
	ReceiptID   int64    `json:"-"`
	Reused      bool     `json:"-"`
}

type Post struct {
	Position     *int    `json:"position"`
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	PostTime     int64   `json:"post_time"`
	Digest       *string `json:"digest"`
	Serial       *string `json:"sn"`
	CoverURL     *string `json:"cover_url"`
	Original     *int    `json:"original"`
	ItemShowType *int    `json:"item_show_type"`
}

type Article struct {
	Title     string `json:"title"`
	Content   string `json:"content"`
	Author    string `json:"author"`
	Desc      string `json:"desc"`
	Pubtime   string `json:"pubtime"`
	ReceiptID int64  `json:"-"`
}

type Config struct {
	Key     string
	BaseURL string
}

type Dajiala struct {
	config         Config
	receipts       *receipts.Service
	client         HTTPDoer
	validateOrigin func(context.Context, *url.URL) error
}

func NewDajiala(config Config, receiptService *receipts.Service, client HTTPDoer) *Dajiala {
	config.Key = strings.TrimSpace(config.Key)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.BaseURL == "" {
		config.BaseURL = defaultDajialaURL
	}
	if client == nil {
		transport := &http.Transport{Proxy: nil, DialContext: modelconfig.DialPublic, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
		client = &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Dajiala{config: config, receipts: receiptService, client: client, validateOrigin: validatePublicOrigin}
}

func (d *Dajiala) Configured() bool { return d != nil && d.config.Key != "" && d.receipts != nil }

func (d *Dajiala) History(ctx context.Context, ghid, subject, window string) (History, error) {
	if !d.Configured() {
		return History{}, errors.New("dajiala is not configured")
	}
	request := receipts.Request{Service: "dajiala", Purpose: "mp_history", Subject: subject,
		Identity: map[string]string{"ghid": ghid, "window": window}, Summary: map[string]any{"ghid": ghid}}
	result, err := d.receipts.Do(ctx, request, func(callCtx context.Context) (receipts.Outcome, error) {
		body, err := json.Marshal(map[string]string{"ghid": ghid, "key": d.config.Key, "verifycode": ""})
		if err != nil {
			return receipts.Outcome{}, &DajialaError{Message: "Dajiala request could not be encoded", Rejected: true}
		}
		response, err := d.request(callCtx, http.MethodPost, d.config.BaseURL+"/fbmain/monitor/v3/post_history", body, "application/json", d.config.BaseURL)
		if err != nil {
			return receipts.Outcome{}, err
		}
		var parsed struct {
			Code      int      `json:"code"`
			Message   string   `json:"msg"`
			CostMoney *float64 `json:"cost_money"`
			Data      []Post   `json:"data"`
		}
		if err := json.Unmarshal(response, &parsed); err != nil {
			return receipts.Outcome{}, &DajialaError{Message: "Dajiala returned invalid JSON"}
		}
		if err := providerCode(parsed.Code, parsed.Message, "post_history"); err != nil {
			return receipts.Outcome{}, err
		}
		var cost *float64
		if parsed.CostMoney != nil {
			cost = parsed.CostMoney
		}
		usage, _ := json.Marshal(map[string]int{"posts": len(parsed.Data)})
		outcome := receipts.Outcome{Response: response, Usage: usage}
		if cost != nil {
			outcome.Cost, outcome.Currency, outcome.CostBasis = cost, "CNY", "actual"
		}
		return outcome, nil
	})
	if err != nil {
		return History{}, err
	}
	var history History
	if err := json.Unmarshal(result.Response, &history); err != nil {
		return History{}, errors.New("Dajiala history response could not be decoded")
	}
	history.ReceiptID, history.Reused = result.ReceiptID, result.Reused
	return history, nil
}

func (d *Dajiala) Article(ctx context.Context, rawURL, subject, identity string) (Article, error) {
	if !d.Configured() {
		return Article{}, errors.New("dajiala is not configured")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return Article{}, &DajialaError{Message: "Dajiala article URL is invalid", Rejected: true}
	}
	query := parsed.Query()
	query.Set("url", rawURL)
	query.Set("key", d.config.Key)
	query.Set("mode", "1")
	query.Set("verifycode", "")
	endpoint := d.config.BaseURL + "/fbmain/monitor/v3/article_detail?" + query.Encode()
	request := receipts.Request{Service: "dajiala", Purpose: "mp_article", Subject: subject,
		Identity: map[string]string{"article": identity}, Summary: map[string]any{"url": rawURL}}
	result, err := d.receipts.Do(ctx, request, func(callCtx context.Context) (receipts.Outcome, error) {
		response, err := d.request(callCtx, http.MethodGet, endpoint, nil, "application/json", d.config.BaseURL)
		if err != nil {
			return receipts.Outcome{}, err
		}
		var parsed struct {
			Code      int      `json:"code"`
			Message   string   `json:"msg"`
			CostMoney *float64 `json:"cost_money"`
			Content   string   `json:"content"`
		}
		if err := json.Unmarshal(response, &parsed); err != nil {
			return receipts.Outcome{}, &DajialaError{Message: "Dajiala returned invalid JSON"}
		}
		if err := providerCode(parsed.Code, parsed.Message, "article_detail"); err != nil {
			return receipts.Outcome{}, err
		}
		usage, _ := json.Marshal(map[string]int{"chars": len([]rune(parsed.Content))})
		outcome := receipts.Outcome{Response: response, Usage: usage}
		if parsed.CostMoney != nil {
			outcome.Cost, outcome.Currency, outcome.CostBasis = parsed.CostMoney, "CNY", "actual"
		}
		return outcome, nil
	})
	if err != nil {
		return Article{}, err
	}
	var article Article
	if err := json.Unmarshal(result.Response, &article); err != nil {
		return Article{}, errors.New("Dajiala article response could not be decoded")
	}
	article.ReceiptID = result.ReceiptID
	return article, nil
}

type DajialaError struct {
	Message   string
	Retryable bool
	Rejected  bool
}

func (e *DajialaError) Error() string          { return e.Message }
func (e *DajialaError) SafeMessage() string    { return e.Message }
func (e *DajialaError) ProviderRejected() bool { return e.Rejected }

func providerCode(code int, message, operation string) error {
	if code == 0 {
		return nil
	}
	if code == -1 {
		return &DajialaError{Message: "Dajiala " + operation + " rate limited", Retryable: true, Rejected: true}
	}
	if message != "" {
		return &DajialaError{Message: fmt.Sprintf("Dajiala %s code %d: %s", operation, code, strings.TrimSpace(message)), Rejected: true}
	}
	return &DajialaError{Message: fmt.Sprintf("Dajiala %s code %d", operation, code), Rejected: true}
}

func (d *Dajiala) request(ctx context.Context, method, endpoint string, body []byte, contentType, expectedOrigin string) ([]byte, error) {
	current, err := url.Parse(endpoint)
	if err != nil {
		return nil, &DajialaError{Message: "Dajiala endpoint is invalid", Rejected: true}
	}
	origin, err := url.Parse(expectedOrigin)
	if err != nil || current.Scheme != origin.Scheme || !strings.EqualFold(current.Host, origin.Host) {
		return nil, &DajialaError{Message: "Dajiala endpoint origin is invalid", Rejected: true}
	}
	for redirects := 0; ; redirects++ {
		if err := d.validateOrigin(ctx, current); err != nil {
			return nil, &DajialaError{Message: "Dajiala endpoint is not publicly reachable", Rejected: true}
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, current.String(), reader)
		if err != nil {
			return nil, &DajialaError{Message: "Dajiala request could not be created", Rejected: true}
		}
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		response, err := d.client.Do(req)
		if err != nil {
			return nil, &DajialaError{Message: "Dajiala request outcome is unknown"}
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			location := response.Header.Get("Location")
			response.Body.Close()
			if redirects >= 3 || location == "" {
				return nil, &DajialaError{Message: "Dajiala redirect was rejected", Rejected: true}
			}
			next, err := current.Parse(location)
			if err != nil || next.Scheme != origin.Scheme || !strings.EqualFold(next.Host, origin.Host) {
				return nil, &DajialaError{Message: "Dajiala redirect changed origin", Rejected: true}
			}
			current = next
			if response.StatusCode == http.StatusSeeOther || ((response.StatusCode == http.StatusMovedPermanently || response.StatusCode == http.StatusFound) && method == http.MethodPost) {
				method, body, contentType = http.MethodGet, nil, ""
			}
			continue
		}
		defer response.Body.Close()
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		if readErr != nil {
			return nil, &DajialaError{Message: "Dajiala response could not be read"}
		}
		if len(data) > 8<<20 {
			return nil, &DajialaError{Message: "Dajiala response is too large", Rejected: true}
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, &DajialaError{Message: fmt.Sprintf("Dajiala HTTP %d", response.StatusCode), Retryable: response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500, Rejected: true}
		}
		return data, nil
	}
}

func validatePublicOrigin(ctx context.Context, value *url.URL) error {
	_, err := modelconfig.ValidateBaseURL(ctx, value.Scheme+"://"+value.Host)
	return err
}
