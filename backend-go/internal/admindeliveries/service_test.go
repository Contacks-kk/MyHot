package admindeliveries

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memoryRepository struct {
	id      int64
	outcome string
	note    string
	actor   string
	cutoff  time.Time
}

func (r *memoryRepository) Resolve(_ context.Context, id int64, outcome, note, actor string) (*Result, bool, error) {
	r.id, r.outcome, r.note, r.actor = id, outcome, note, actor
	return &Result{ID: id, Status: "sent"}, true, nil
}

func (r *memoryRepository) MarkStale(_ context.Context, cutoff time.Time) (StaleCounts, error) {
	r.cutoff = cutoff
	return StaleCounts{Unknown: 2, Failed: 3}, nil
}

func TestResolveValidatesAndTrimsOperatorNote(t *testing.T) {
	repository := &memoryRepository{}
	service := NewService(repository, Config{}, nil)
	for _, input := range []struct {
		id      int64
		outcome string
		note    string
	}{{0, "sent", "note"}, {1, "typo", "note"}, {1, "drop", "  "}} {
		if _, _, err := service.Resolve(context.Background(), input.id, input.outcome, input.note, "admin:1"); err != ErrInvalidRequest {
			t.Fatalf("invalid resolution %#v returned %v", input, err)
		}
	}
	result, found, err := service.Resolve(context.Background(), 17, "drop", " reviewed ", "admin:1")
	if err != nil || !found || result == nil || result.ID != 17 || repository.id != 17 || repository.outcome != "drop" || repository.note != "reviewed" || repository.actor != "admin:1" {
		t.Fatalf("resolution = %#v, found=%v, err=%v, repository=%#v", result, found, err, repository)
	}
}

func TestRecoverStaleUsesFifteenMinuteCutoff(t *testing.T) {
	repository := &memoryRepository{}
	service := NewService(repository, Config{}, nil)
	now := time.Date(2026, 10, 8, 12, 30, 0, 0, time.UTC)
	counts, err := service.RecoverStale(context.Background(), now)
	if err != nil || counts != (StaleCounts{Unknown: 2, Failed: 3}) {
		t.Fatalf("recovery = %+v, err=%v", counts, err)
	}
	if want := now.Add(-15 * time.Minute); !repository.cutoff.Equal(want) {
		t.Fatalf("stale cutoff = %s, want %s", repository.cutoff, want)
	}
}

type resendFixture struct {
	record       *ResendRecord
	content      *SelectedContent
	claimVersion string
	claimed      bool
	payload      json.RawMessage
	finish       []string
}

type selectedPushFixture struct {
	content   *SelectedContent
	holder    string
	targets   []ContentTarget
	siblings  []string
	claim     ContentDeliveryClaim
	claimed   bool
	claimedID int64
	starts    []int64
	finished  []string
	loadCount int
}

func (r *selectedPushFixture) Resolve(context.Context, int64, string, string, string) (*Result, bool, error) {
	return nil, false, errors.New("unexpected resolve call")
}
func (*selectedPushFixture) MarkStale(context.Context, time.Time) (StaleCounts, error) {
	return StaleCounts{}, nil
}
func (r *selectedPushFixture) LoadResend(context.Context, int64) (*ResendRecord, bool, error) {
	return nil, false, errors.New("unexpected resend call")
}
func (r *selectedPushFixture) LoadSelectedContent(context.Context, string) (*SelectedContent, error) {
	r.loadCount++
	return r.content, nil
}
func (r *selectedPushFixture) AcquireTitleLease(_ context.Context, _, holder string, _ time.Time) (string, error) {
	if r.holder != "" {
		return r.holder, nil
	}
	r.holder = holder
	return holder, nil
}
func (r *selectedPushFixture) SelectedSiblings(context.Context, int64, string) ([]string, error) {
	return r.siblings, nil
}
func (r *selectedPushFixture) ContentTargets(context.Context) ([]ContentTarget, error) {
	return r.targets, nil
}
func (r *selectedPushFixture) ClaimContentDelivery(_ context.Context, _ string, claim ContentDeliveryClaim) (int64, ContentTarget, bool, error) {
	r.claim = claim
	target := r.targets[0]
	return r.claimedID, target, r.claimed, nil
}
func (r *selectedPushFixture) StartContentDelivery(_ context.Context, id int64) error {
	r.starts = append(r.starts, id)
	return nil
}
func (r *selectedPushFixture) FinishContentDelivery(_ context.Context, _ int64, status, response string) error {
	r.finished = append(r.finished, status, response)
	return nil
}

func (r *resendFixture) Resolve(context.Context, int64, string, string, string) (*Result, bool, error) {
	return nil, false, errors.New("unexpected resolve call")
}

func (*resendFixture) MarkStale(context.Context, time.Time) (StaleCounts, error) {
	return StaleCounts{}, nil
}

func (r *resendFixture) LoadResend(context.Context, int64) (*ResendRecord, bool, error) {
	return r.record, r.record != nil, nil
}

func (r *resendFixture) LoadSelectedContent(context.Context, string) (*SelectedContent, error) {
	return r.content, nil
}

func (r *resendFixture) ClaimResend(_ context.Context, _ int64, version string, payload json.RawMessage) (string, bool, error) {
	if version != r.record.Version {
		return "", false, nil
	}
	r.payload = append(json.RawMessage(nil), payload...)
	return r.claimVersion, r.claimed, nil
}

func (r *resendFixture) FinishResend(_ context.Context, id int64, claimVersion, beforeStatus, status, response, actor, note string) error {
	r.finish = []string{string(rune(id)), claimVersion, beforeStatus, status, response, actor, note}
	return nil
}

type webhookFixture struct {
	url    string
	card   any
	result WebhookResult
	err    error
}

func (f *webhookFixture) Send(_ context.Context, url string, card any) (WebhookResult, error) {
	f.url, f.card = url, card
	return f.result, f.err
}

func TestResendUsesCurrentSelectedCardAndAuditsResult(t *testing.T) {
	now := time.Now()
	repository := &resendFixture{
		record:       &ResendRecord{Status: "unknown", Version: "v1", Payload: []byte(`{"old":true}`), ConfigRef: "FEISHU_PUSH_WEBHOOK_URL", Kind: "feishu_webhook", Enabled: true, SubjectKind: "selected", SubjectID: "article-1"},
		content:      &SelectedContent{ArticleID: "article-1", Selected: true, Visibility: "public", Title: "Current headline", Summary: resendStringPointer("Current summary"), Reason: resendStringPointer("Current reason"), Category: resendStringPointer("models"), SourceName: "X：Example (@sample)", SourceTier: "T1", URL: "https://example.invalid/story", TimelineAt: now.Add(-time.Hour), PublishedAt: &now, DiscoveredAt: now},
		claimVersion: "v2", claimed: true,
	}
	sender := &webhookFixture{result: WebhookResult{Status: "sent", Body: `{"code":0}`}}
	service := NewService(repository, Config{
		ContentPushEnabled: true, WebhookURLs: map[string]string{"FEISHU_PUSH_WEBHOOK_URL": "https://hooks.example.invalid/content"},
		SiteName: "Example Site", SiteURL: "https://site.example", CategoryLabels: map[string]string{"models": "模型"},
	}, sender)

	result, found, err := service.Resolve(context.Background(), 17, "resend", "  checked the group  ", "admin:41")
	if err != nil || !found || result == nil || result.Status != "sent" || sender.url != "https://hooks.example.invalid/content" {
		t.Fatalf("resend = %#v, found=%t, err=%v, sender=%#v", result, found, err, sender)
	}
	var card map[string]any
	if err := json.Unmarshal(repository.payload, &card); err != nil {
		t.Fatal(err)
	}
	encoded := string(repository.payload)
	for _, want := range []string{"Current headline", "Current summary", "Current reason", "模型", "Example Site 查看", "https://site.example/items/article-1", "来源：Example"} {
		if !strings.Contains(encoded, want) {
			t.Errorf("current card omitted %q: %s", want, encoded)
		}
	}
	if strings.Contains(encoded, `"old":true`) || len(repository.finish) != 7 || repository.finish[1] != "v2" || repository.finish[2] != "unknown" || repository.finish[3] != "sent" || repository.finish[5] != "admin:41" || repository.finish[6] != "checked the group" {
		t.Fatalf("resend audit or current payload = %s, %#v", encoded, repository.finish)
	}
}

func TestPushSelectedReservesAndSendsOneCardAcrossTargets(t *testing.T) {
	now := time.Now()
	factID := int64(42)
	repository := &selectedPushFixture{
		content: &SelectedContent{ArticleID: "article-1", FactID: &factID, Selected: true, Visibility: "public", Title: "Headline!", Summary: resendStringPointer("Summary"),
			SourceName: "X：Example (@sample)", SourceTier: "T1", URL: "https://example.invalid/story", TimelineAt: now.Add(-time.Hour), PublishedAt: &now, DiscoveredAt: now},
		targets: []ContentTarget{{Key: "main", Kind: "feishu_webhook", ConfigRef: "push"}}, siblings: []string{"article-0"}, claimed: true, claimedID: 19,
	}
	sender := &webhookFixture{result: WebhookResult{Status: "sent", Body: `{"code":0}`}}
	service := NewService(repository, Config{ContentPushEnabled: true, WebhookURLs: map[string]string{"push": "https://hooks.example.invalid"}, SiteName: "Example", SiteURL: "https://site.example"}, sender)
	result, err := service.PushSelected(context.Background(), "article-1", now)
	if err != nil || result.Status != "pushed" || len(result.Targets) != 1 || result.Targets[0].Status != "sent" {
		t.Fatalf("push = %#v, err=%v", result, err)
	}
	if repository.claim.DedupeKey != "selected:fact:42" || len(repository.claim.SentSubjects) != 2 || repository.claim.SentSubjects[1] != "article-0" || repository.loadCount != 2 || len(repository.starts) != 1 || repository.finished[0] != "sent" {
		t.Fatalf("selected delivery claim = %#v, loads=%d starts=%v finished=%v", repository.claim, repository.loadCount, repository.starts, repository.finished)
	}
	encoded, _ := json.Marshal(sender.card)
	if !strings.Contains(sender.url, "hooks.example.invalid") || !strings.Contains(string(encoded), "Summary") {
		t.Fatalf("selected card was not sent to the current target: %#v", sender)
	}
}

func TestPushSelectedWaitsForUnresolvedSameTitleAndNeverRetriesUnknown(t *testing.T) {
	now := time.Now()
	content := &SelectedContent{ArticleID: "article-1", Selected: true, Visibility: "public", Title: "Same title", SourceTier: "T1", URL: "https://example.invalid/story", TimelineAt: now.Add(-time.Hour), PublishedAt: &now, DiscoveredAt: now}
	blocked := &selectedPushFixture{content: content, holder: "article-0", targets: []ContentTarget{{Key: "main", Kind: "feishu_webhook"}}}
	service := NewService(blocked, Config{ContentPushEnabled: true}, &webhookFixture{})
	result, err := service.PushSelected(context.Background(), "article-1", now)
	if err != nil || result.Status != "retry" || result.RetryAfter != 2*time.Minute || blocked.loadCount != 1 {
		t.Fatalf("same-title lease = %#v, loads=%d err=%v", result, blocked.loadCount, err)
	}

	factID := int64(2)
	content.FactID = &factID
	unknownRepo := &selectedPushFixture{content: content, targets: []ContentTarget{{Key: "main", Kind: "feishu_webhook", ConfigRef: "push"}}, claimed: true, claimedID: 20}
	unknownSender := &webhookFixture{err: errors.New("connection timed out")}
	unknownService := NewService(unknownRepo, Config{ContentPushEnabled: true, WebhookURLs: map[string]string{"push": "https://hooks.example.invalid"}}, unknownSender)
	unknown, err := unknownService.PushSelected(context.Background(), "article-1", now)
	if err != nil || unknown.Status != "skipped" || len(unknown.Targets) != 1 || unknown.Targets[0].Status != "unknown" || unknownRepo.finished[0] != "unknown" {
		t.Fatalf("unknown delivery must not auto-retry: %#v, finished=%v, err=%v", unknown, unknownRepo.finished, err)
	}
}

func TestResendStopsWhenSelectedContentIsNoLongerEligible(t *testing.T) {
	now := time.Now()
	repository := &resendFixture{
		record:  &ResendRecord{Status: "failed", Version: "v1", ConfigRef: "push", Kind: "feishu_webhook", Enabled: true, SubjectKind: "selected", SubjectID: "article-1"},
		content: &SelectedContent{ArticleID: "article-1", Selected: false, Visibility: "public", SourceTier: "T1", TimelineAt: now, DiscoveredAt: now},
		claimed: true,
	}
	sender := &webhookFixture{}
	service := NewService(repository, Config{ContentPushEnabled: true, WebhookURLs: map[string]string{"push": "https://hooks.example.invalid"}}, sender)
	if _, found, err := service.Resolve(context.Background(), 17, "resend", "checked", "admin:41"); !found || !errors.Is(err, ErrContentNotEligible) {
		t.Fatalf("ineligible content resend = found:%t err:%v", found, err)
	}
	if repository.payload != nil || sender.url != "" {
		t.Fatalf("ineligible content was claimed or sent: payload=%s sender=%#v", repository.payload, sender)
	}
}

func TestHTTPWebhookSenderClassifiesAcknowledgements(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   string
	}{{"accepted", http.StatusOK, `{"code":0}`, "sent"}, {"rejected", http.StatusOK, `{"code":123}`, "failed"}, {"client error", http.StatusTooManyRequests, `busy`, "failed"}, {"no acknowledgement", http.StatusOK, `accepted`, "unknown"}, {"server error", http.StatusBadGateway, `upstream`, "unknown"}} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			result, err := (HTTPWebhookSender{Client: server.Client()}).Send(context.Background(), server.URL, map[string]string{"key": "value"})
			if err != nil || result.Status != test.want || result.Body != test.body {
				t.Fatalf("send = %#v, err=%v", result, err)
			}
		})
	}
}

func resendStringPointer(value string) *string { return &value }
