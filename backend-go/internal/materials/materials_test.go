package materials

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type recordingRepository struct {
	input    Input
	decision TimelineDecision
	hash     string
}

func (r *recordingRepository) StorePrepared(_ context.Context, input Input, decision TimelineDecision, hash string) (Result, error) {
	r.input, r.decision, r.hash = input, decision, hash
	return Result{ArticleID: "article-test"}, nil
}

func TestServicePreparesMaterialBeforePersistence(t *testing.T) {
	discovered := time.Date(2026, 10, 8, 10, 0, 0, 0, time.FixedZone("test", 8*60*60))
	claimed := discovered.Add(-49 * time.Hour)
	repository := &recordingRepository{}
	service := NewService(repository)
	result, err := service.Store(context.Background(), Input{
		URL: "https://source.example/article", Title: "  A\uFEFF title\nwith spaces  ",
		PublishedAt: &claimed, DiscoveredAt: discovered, BodyText: "body", Excerpt: "summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ArticleID != "article-test" {
		t.Fatalf("result = %#v", result)
	}
	if repository.input.Title != "A title with spaces" || repository.input.IdentityKey != "url:https://source.example/article" {
		t.Fatalf("prepared input = %#v", repository.input)
	}
	if repository.input.Via != "fetch" || repository.input.BodyStatus != "ok" {
		t.Fatalf("prepared defaults = %#v", repository.input)
	}
	if repository.input.DiscoveredAt.Location() != time.UTC || repository.input.PublishedAt.Location() != time.UTC {
		t.Fatal("prepared timestamps were not normalized to UTC")
	}
	if repository.decision.BackfillReason != "stale-on-discovery" || !repository.decision.Backfill {
		t.Fatalf("timeline decision = %#v", repository.decision)
	}
	if repository.hash != ContentHash("A title with spaces", "body", "summary") {
		t.Fatalf("content hash = %s", repository.hash)
	}
	if got := truncateUTF16(strings.Repeat("🙂", 501), 1000); got != strings.Repeat("🙂", 500) {
		t.Fatalf("UTF-16 bounded title contains %d runes", len([]rune(got)))
	}
}

func TestDecideTimeline(t *testing.T) {
	discovered := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	date := func(offset time.Duration) *time.Time {
		value := discovered.Add(offset)
		return &value
	}
	tests := []struct {
		name       string
		claimed    *time.Time
		explicit   string
		wantReason string
		wantAt     time.Time
		wantDate   bool
	}{
		{name: "unknown date is a backfill", wantReason: "unknown-publication-time", wantAt: discovered},
		{name: "future date beyond tolerance is unknown", claimed: date(FutureTolerance + time.Second), wantReason: "unknown-publication-time", wantAt: discovered},
		{name: "future date at tolerance is trusted", claimed: date(FutureTolerance), wantAt: discovered, wantDate: true},
		{name: "stale threshold is strict", claimed: date(-StaleOnDiscovery), wantAt: discovered, wantDate: true},
		{name: "old date is stale", claimed: date(-StaleOnDiscovery - time.Second), wantReason: "stale-on-discovery", wantAt: discovered.Add(-StaleOnDiscovery - time.Second), wantDate: true},
		{name: "first import reason takes precedence", claimed: date(-time.Hour), explicit: "first-import", wantReason: "first-import", wantAt: discovered.Add(-time.Hour), wantDate: true},
		{name: "undated first import keeps explicit reason", explicit: "first-import", wantReason: "first-import", wantAt: discovered},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DecideTimeline(test.claimed, discovered, test.explicit)
			if got.BackfillReason != test.wantReason || got.Backfill != (test.wantReason != "") || !got.TimelineAt.Equal(test.wantAt) {
				t.Fatalf("decision = %#v", got)
			}
			if (got.PublishedAt != nil) != test.wantDate {
				t.Fatalf("publishedAt = %v, want date %t", got.PublishedAt, test.wantDate)
			}
		})
	}
}

func TestContentHashCollapsesWhitespace(t *testing.T) {
	plain := ContentHash("A title", "first second\nthird", "summary")
	spaced := ContentHash(" A\ttitle ", " first  second\tthird ", " summary\n")
	if plain != spaced {
		t.Fatalf("normalized content hashes differ: %s != %s", plain, spaced)
	}
	if len(plain) != 64 || strings.Trim(plain, "0123456789abcdef") != "" {
		t.Fatalf("hash is not lower-case SHA-256 hex: %q", plain)
	}
}

func TestSameBarringLoss(t *testing.T) {
	for _, pair := range [][2]string{
		{"A�B", "A汉B"},
		{"A��B", "A汉B"},
		{"A汉B", "A�B"},
		{"A\tB", " A B "},
	} {
		if !SameBarringLoss(pair[0], pair[1]) {
			t.Errorf("expected %q and %q to be equivalent", pair[0], pair[1])
		}
	}
	if SameBarringLoss("A�B", "A汉汉B") {
		t.Fatal("loss marker matched a different rune count")
	}
}

func TestNormalizeURLAndIdentityKeyForURL(t *testing.T) {
	for _, test := range []struct {
		input, normalized, identity string
	}{
		{"http://www.Example.com/news/?utm_source=mail&b=2&a=1#part", "https://example.com/news/?a=1&b=2", "url:https://example.com/news/?a=1&b=2"},
		{"http://mp.weixin.qq.com/s?sn=hash&utm_source=x&__biz=biz&mid=1&idx=2#anchor", "https://mp.weixin.qq.com/s?__biz=biz&mid=1&idx=2&sn=hash", "url:https://mp.weixin.qq.com/s?__biz=biz&mid=1&idx=2&sn=hash"},
	} {
		normalized, ok := NormalizeURL(test.input)
		identity, identityOK := IdentityKeyForURL(test.input)
		if !ok || !identityOK || normalized != test.normalized || identity != test.identity {
			t.Errorf("URL %q normalized=%q identity=%q valid=%t/%t", test.input, normalized, identity, ok, identityOK)
		}
	}
	for _, test := range []struct{ input, want string }{
		{"https://mobile.x.com/person/status/1234567890?s=20", "x:1234567890"},
		{"https://twitter.com/person/statuses/1234567890", "x:1234567890"},
	} {
		got, ok := IdentityKeyForURL(test.input)
		if !ok || got != test.want {
			t.Errorf("identity for %q = %q, %t", test.input, got, ok)
		}
	}
	for _, input := range []string{"", "javascript:alert(1)", "/relative/path", "https://"} {
		if _, ok := NormalizeURL(input); ok {
			t.Errorf("invalid URL %q was accepted", input)
		}
	}
}

/** TestOwnershipPathMatchesPreservesVerifiedOrigins checks publisher scope boundaries and URL credentials. */
func TestOwnershipPathMatchesPreservesVerifiedOrigins(t *testing.T) {
	tests := []struct {
		name  string
		scope string
		url   string
		want  bool
	}{
		{name: "root scope", scope: "https://publisher.example/", url: "https://publisher.example/news/item", want: true},
		{name: "exact path", scope: "https://publisher.example/news", url: "https://publisher.example/news", want: true},
		{name: "child path", scope: "https://publisher.example/news", url: "https://publisher.example/news/item", want: true},
		{name: "path boundary", scope: "https://publisher.example/news", url: "https://publisher.example/newsroom/item"},
		{name: "scheme stays distinct", scope: "https://publisher.example/news", url: "http://publisher.example/news/item"},
		{name: "www stays distinct", scope: "https://publisher.example/news", url: "https://www.publisher.example/news/item"},
		{name: "default port matches origin", scope: "https://publisher.example:443/news", url: "https://publisher.example/news/item", want: true},
		{name: "encoded slash is not a path separator", scope: "https://publisher.example/news", url: "https://publisher.example/news%2Fitem"},
		{name: "query scope is invalid", scope: "https://publisher.example/news?source=official", url: "https://publisher.example/news/item"},
		{name: "empty query scope is invalid", scope: "https://publisher.example/news?", url: "https://publisher.example/news/item"},
		{name: "fragment scope is invalid", scope: "https://publisher.example/news#official", url: "https://publisher.example/news/item"},
		{name: "scope credentials are invalid", scope: "https://user@publisher.example/news", url: "https://publisher.example/news/item"},
		{name: "target credentials are invalid", scope: "https://publisher.example/news", url: "https://user@publisher.example/news/item"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ownershipPathMatches(test.scope, test.url); got != test.want {
				t.Fatalf("ownershipPathMatches(%q, %q) = %t, want %t", test.scope, test.url, got, test.want)
			}
		})
	}
}

/** TestSourceOwnsMaterialURLUsesOnlyVerifiedPublisherEvidence rejects distributor-only URLs. */
func TestSourceOwnsMaterialURLUsesOnlyVerifiedPublisherEvidence(t *testing.T) {
	articleURL := "https://publisher.example/news/item"
	for _, test := range []struct {
		name      string
		candidate publisherCandidate
		want      bool
	}{
		{
			name:      "explicit T1 scope",
			candidate: publisherCandidate{id: "official", config: json.RawMessage(`{"publisherUrlPrefixes":["https://publisher.example/news"]}`)},
			want:      true,
		},
		{
			name:      "valid scope survives a malformed sibling",
			candidate: publisherCandidate{id: "official", config: json.RawMessage(`{"publisherUrlPrefixes":[17,"https://publisher.example/news"]}`)},
			want:      true,
		},
		{
			name:      "observed web list owns its listing path",
			candidate: publisherCandidate{id: "listing", kind: "web_list", config: json.RawMessage(`{"url":"https://publisher.example/news"}`)},
			want:      true,
		},
		{
			name:      "RSS host alone is not ownership",
			candidate: publisherCandidate{id: "feed", kind: "rss", config: json.RawMessage(`{"url":"https://publisher.example/news"}`)},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sourceOwnsMaterialURL(test.candidate, articleURL); got != test.want {
				t.Fatalf("sourceOwnsMaterialURL(%s) = %t, want %t", test.candidate.id, got, test.want)
			}
		})
	}
}
