package mpcollector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/receipts"
)

type receiptRepository struct {
	claim    receipts.Claim
	response receipts.Outcome
	failure  string
}

func (r *receiptRepository) Claim(context.Context, receipts.Request, string) (receipts.Claim, error) {
	return r.claim, nil
}
func (r *receiptRepository) RecordFailure(_ context.Context, _ receipts.Claim, status, _ string, _ time.Duration) error {
	r.failure = status
	return nil
}
func (r *receiptRepository) RecordReceived(_ context.Context, _ receipts.Claim, outcome receipts.Outcome, _ time.Duration) error {
	r.response = outcome
	return nil
}
func (*receiptRepository) Complete(context.Context, int64) error       { return nil }
func (*receiptRepository) Reject(context.Context, int64, string) error { return nil }

type scriptedResponse struct {
	status int
	header http.Header
	body   string
}

type scriptedClient struct {
	responses []scriptedResponse
	requests  []*http.Request
	bodies    []string
}

func (c *scriptedClient) Do(request *http.Request) (*http.Response, error) {
	var requestBody []byte
	if request.Body != nil {
		requestBody, _ = io.ReadAll(request.Body)
		_ = request.Body.Close()
	}
	c.requests = append(c.requests, request)
	c.bodies = append(c.bodies, string(requestBody))
	response := c.responses[0]
	c.responses = c.responses[1:]
	return &http.Response{StatusCode: response.status, Header: response.header, Body: io.NopCloser(strings.NewReader(response.body))}, nil
}

func TestDajialaHistoryRecordsPaidResponseAndUsesConfiguredCredentials(t *testing.T) {
	cost := 0.14
	client := &scriptedClient{responses: []scriptedResponse{{status: http.StatusOK, header: make(http.Header), body: `{"code":0,"data":[{"position":2,"url":"https://mp.weixin.qq.com/s/a","title":"title","post_time":123,"sn":"serial"}],"nickname":"publisher","remain_money":8.5,"cost_money":0.14}`}}}
	repository := &receiptRepository{claim: receipts.Claim{ReceiptID: 21, AttemptID: 22}}
	provider := NewDajiala(Config{Key: "secret-key", BaseURL: "https://dajiala.example"}, receipts.NewService(repository), client)
	provider.validateOrigin = func(context.Context, *url.URL) error { return nil }

	history, err := provider.History(context.Background(), "ghid", "source-1", "s:123")
	if err != nil {
		t.Fatal(err)
	}
	if history.ReceiptID != 21 || history.Nickname != "publisher" || history.RemainMoney == nil || *history.RemainMoney != 8.5 || len(history.Posts) != 1 || history.Posts[0].Serial == nil || *history.Posts[0].Serial != "serial" {
		t.Fatalf("history response differs: %+v", history)
	}
	request := client.requests[0]
	if request.Method != http.MethodPost || request.URL.Path != "/fbmain/monitor/v3/post_history" || request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected Dajiala request: %s %s headers=%v", request.Method, request.URL, request.Header)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(client.bodies[0]), &sent); err != nil || sent["key"] != "secret-key" || sent["ghid"] != "ghid" {
		t.Fatalf("request payload does not carry configured identity: %v (%v)", sent, err)
	}
	if repository.response.Cost == nil || *repository.response.Cost != cost || repository.response.Currency != "CNY" || repository.response.CostBasis != "actual" {
		t.Fatalf("provider cost was not retained: %+v", repository.response)
	}
}

func TestDajialaRedirectCannotForwardKeyToAnotherOrigin(t *testing.T) {
	client := &scriptedClient{responses: []scriptedResponse{{status: http.StatusFound, header: http.Header{"Location": []string{"https://elsewhere.example/collect"}}, body: ""}}}
	provider := NewDajiala(Config{Key: "secret-key", BaseURL: "https://dajiala.example"}, receipts.NewService(&receiptRepository{claim: receipts.Claim{ReceiptID: 31, AttemptID: 32}}), client)
	provider.validateOrigin = func(context.Context, *url.URL) error { return nil }

	_, err := provider.Article(context.Background(), "https://mp.weixin.qq.com/s/a", "source-1", "serial")
	var rejected *DajialaError
	if !errors.As(err, &rejected) || !rejected.Rejected || !strings.Contains(rejected.Message, "changed origin") {
		t.Fatalf("cross-origin redirect was not rejected: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("redirect target was requested: %d requests", len(client.requests))
	}
}

func TestDajialaProviderCodeRetriesRateLimitButRejectsAccountErrors(t *testing.T) {
	rateLimit := providerCode(-1, "", "post_history")
	var retry *DajialaError
	if !errors.As(rateLimit, &retry) || !retry.Retryable || !retry.Rejected {
		t.Fatalf("rate limit classification = %#v", rateLimit)
	}
	account := providerCode(101, "account unavailable", "post_history")
	var permanent *DajialaError
	if !errors.As(account, &permanent) || permanent.Retryable || !permanent.Rejected {
		t.Fatalf("account failure classification = %#v", account)
	}
}
