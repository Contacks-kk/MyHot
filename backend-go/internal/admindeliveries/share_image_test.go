package admindeliveries

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPShareImageWarmerSignsCurrentArticleCard(t *testing.T) {
	secret := strings.Repeat("s", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		if got := r.Header.Get("X-OG-Prewarm-Signature"); got != hex.EncodeToString(mac.Sum(nil)) {
			t.Errorf("signature = %q", got)
		}
		var request shareImageRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
		}
		if request.ID != "article-1" || request.Item.ID != "article-1" || request.Item.Title != "Current headline" ||
			request.Item.TimelineAt != "2026-10-09T08:30:00+08:00" || request.Item.Score == nil || *request.Item.Score != 92 ||
			request.Item.Category == nil || *request.Item.Category != "models" || request.Item.CategoryLabel != "模型" || request.Item.Source.Name != "Example" || !request.Item.Selected {
			t.Errorf("prewarm request = %#v", request)
		}
		if request.Item.Summary == nil || *request.Item.Summary != "Summary" {
			t.Errorf("summary = %#v", request.Item.Summary)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	score := 91.6
	content := SelectedContent{ArticleID: "article-1", Selected: true, Title: "Current headline", Summary: shareImageStringPointer("Summary"), Category: shareImageStringPointer("models"),
		SourceName: "Example", TimelineAt: time.Date(2026, 10, 9, 0, 30, 0, 0, time.UTC), Score: &score}
	warmer := HTTPShareImageWarmer{
		URL:            server.URL,
		Secret:         secret,
		CategoryLabels: map[string]string{"models": "模型"},
	}
	if err := warmer.Warm(context.Background(), content); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPShareImageWarmerRequiresSignedPrewarmEndpoint(t *testing.T) {
	for _, warmer := range []HTTPShareImageWarmer{{URL: "http://web.invalid"}, {Secret: strings.Repeat("x", 32)}} {
		if err := warmer.Warm(context.Background(), SelectedContent{ArticleID: "article-1"}); err == nil {
			t.Fatal("incomplete prewarm configuration was accepted")
		}
	}
}

func shareImageStringPointer(value string) *string { return &value }

type shareImageWarmerFixture struct {
	content SelectedContent
	calls   int
}

func (f *shareImageWarmerFixture) Warm(_ context.Context, content SelectedContent) error {
	f.content = content
	f.calls++
	return nil
}

/** TestWarmSelectedChecksCurrentEligibilityBeforeCallingWeb keeps private or stale items out of share-image preparation. */
func TestWarmSelectedChecksCurrentEligibilityBeforeCallingWeb(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	content := SelectedContent{
		ArticleID:    "article-1",
		Selected:     true,
		Visibility:   "public",
		Title:        "Headline",
		SourceTier:   "T1",
		TimelineAt:   now.Add(-time.Hour),
		PublishedAt:  &now,
		DiscoveredAt: now,
	}
	repository := &selectedPushFixture{content: &content}
	warmer := &shareImageWarmerFixture{}
	service := NewService(repository, Config{}, nil, warmer)
	if err := service.WarmSelected(context.Background(), "article-1", now); err != nil || warmer.content.ArticleID != "article-1" || warmer.calls != 1 {
		t.Fatalf("eligible content was not warmed: content=%#v err=%v", warmer.content, err)
	}

	cases := []struct {
		name string
		edit func(*SelectedContent)
	}{
		{
			name: "unselected",
			edit: func(value *SelectedContent) {
				value.Selected = false
			},
		},
		{
			name: "withdrawn",
			edit: func(value *SelectedContent) {
				value.Visibility = "withdrawn"
			},
		},
		{
			name: "silent",
			edit: func(value *SelectedContent) {
				value.Silent = true
			},
		},
		{
			name: "unpublished",
			edit: func(value *SelectedContent) {
				value.PublishedAt = nil
			},
		},
		{
			name: "unsupported source tier",
			edit: func(value *SelectedContent) {
				value.SourceTier = "T2"
			},
		},
		{
			name: "backfill",
			edit: func(value *SelectedContent) {
				value.Backfill = true
			},
		},
		{
			name: "outside freshness window",
			edit: func(value *SelectedContent) {
				value.TimelineAt = now.Add(-13 * time.Hour)
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := content
			testCase.edit(&candidate)
			repository.content = &candidate
			calls := warmer.calls
			if err := service.WarmSelected(context.Background(), "article-1", now); err != nil || warmer.calls != calls {
				t.Fatalf("ineligible content invoked warming or errored: calls=%d want=%d err=%v", warmer.calls, calls, err)
			}
		})
	}
	if repository.loadCount != len(cases)+1 || warmer.calls != 1 {
		t.Fatalf("eligibility checks = loads %d, warm calls %d", repository.loadCount, warmer.calls)
	}
}
