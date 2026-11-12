package publication

import (
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
)

func TestPoolSelectionBodyAndIndexRules(t *testing.T) {
	title, summary, blank, empty := "标题", "摘要", "  ", ""
	if !IsPoolEligible("editorial", "pass", &title, &summary) {
		t.Fatal("complete editorial pass should enter the public pool")
	}
	if !IsPoolEligible("editorial", "pass", &blank, &summary) {
		t.Fatal("publication rule must retain JavaScript string truthiness for whitespace text")
	}
	for _, input := range []struct {
		mode, relevance string
		title, summary  *string
	}{
		{"isolated", "pass", &title, &summary},
		{"editorial", "block", &title, &summary},
		{"editorial", "pass", &empty, &summary},
		{"editorial", "pass", &title, nil},
	} {
		if IsPoolEligible(input.mode, input.relevance, input.title, input.summary) {
			t.Errorf("unexpectedly eligible: %+v", input)
		}
	}

	judged := true
	if !IsSelectable(true, &judged, "T2") || IsSelectable(true, &judged, "EXCLUDE_MP") || IsSelectable(false, &judged, "T1") || IsSelectable(true, nil, "T1") {
		t.Fatal("selection gate did not preserve eligibility, model decision, and excluded tier")
	}
	source := SourceFacts{SiteFulltext: true, SyndicateFulltext: true}
	if mode := BodyModeOf(source, "ok", true); mode != "full" || !MayRedistribute(source, mode) {
		t.Fatalf("licensed full body mode = %q", mode)
	}
	for _, test := range []struct {
		status string
		has    bool
	}{{"pending", true}, {"ok", false}, {"failed", true}} {
		if mode := BodyModeOf(source, test.status, test.has); mode != "summary" {
			t.Errorf("BodyModeOf(%q, %v) = %q", test.status, test.has, mode)
		}
	}
	if BodyModeOf(SourceFacts{}, "ok", true) != "summary" || MayRedistribute(source, "summary") {
		t.Fatal("full text must require both site permission and a confirmed body")
	}

	now := time.Now()
	if !IsIndexable("public", "editorial", true, true, nil, nil) || !IsIndexable("public", "editorial", true, false, &now, nil) {
		t.Fatal("selected or manually indexed article should be indexable")
	}
	if IsIndexable("summary-only", "editorial", true, true, nil, nil) || IsIndexable("public", "isolated", true, true, nil, nil) || IsIndexable("public", "editorial", false, true, nil, nil) || IsIndexable("public", "editorial", true, true, nil, &now) {
		t.Fatal("visibility, source mode, summary, and explicit exclusion must gate indexing")
	}
}

func TestPublicSourceNameAndDisplayTags(t *testing.T) {
	for input, want := range map[string]string{
		"OpenAI（官网动态）（RSS）":        "OpenAI",
		"X：Sam Altman (@sama)（热点）": "Sam Altman",
		"X（说明）":                    "X",
		"（仅备注）":                    "（仅备注）",
	} {
		if got := PublicSourceName(input); got != want {
			t.Errorf("PublicSourceName(%q) = %q, want %q", input, got, want)
		}
	}
	tags := DisplayTags([]string{"模型", "entity:openai", "公司"})
	if len(tags) != 2 || tags[0] != "模型" || tags[1] != "公司" {
		t.Fatalf("display tags = %#v", tags)
	}
}

func TestPickRepresentativeUsesVerifiedAuthorityThenBodyScoreAndTime(t *testing.T) {
	entities := map[string]interests.Entity{
		"openai":    {Name: "OpenAI", Aliases: []string{"ChatGPT"}},
		"anthropic": {Name: "Anthropic", Aliases: []string{"Claude"}},
	}
	organization, person := "organization", "person"
	base := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	full := 30.0
	high := 95.0
	rows := []Representative{
		{ArticleID: "high-score", Identity: RepresentativeIdentity{SourceTier: "T2"}, BodyMode: "full", Score: &high, TimelineAt: base},
		{ArticleID: "verified-person", Identity: RepresentativeIdentity{SourceTier: "T2", PublisherRole: person, OwnerEntityID: "openai", FactSubject: "ChatGPT"}, BodyMode: "full", Score: &high, TimelineAt: base},
		{ArticleID: "verified-org-summary", Identity: RepresentativeIdentity{SourceTier: "T2", PublisherRole: organization, OwnerEntityID: "openai", FactSubject: "OpenAI"}, BodyMode: "summary", Score: &high, TimelineAt: base},
		{ArticleID: "verified-org-full", Identity: RepresentativeIdentity{SourceTier: "T2", PublisherRole: organization, OwnerEntityID: "openai", FactSubject: "OpenAI"}, BodyMode: "full", Score: &full, TimelineAt: base.Add(time.Minute)},
		{ArticleID: "first-party", Identity: RepresentativeIdentity{SourceTier: "T1"}, BodyMode: "summary", Score: &full, TimelineAt: base.Add(time.Hour)},
	}
	got, ok := PickRepresentative(rows, entities)
	if !ok || got.ArticleID != "first-party" {
		t.Fatalf("T1 representative = %+v, ok=%v", got, ok)
	}

	rows = rows[:4]
	got, ok = PickRepresentative(rows, entities)
	if !ok || got.ArticleID != "verified-org-full" {
		t.Fatalf("verified organization/full-text representative = %+v, ok=%v", got, ok)
	}

	rows = []Representative{
		{ArticleID: "later", BodyMode: "full", Score: &high, TimelineAt: base.Add(time.Minute)},
		{ArticleID: "earlier", BodyMode: "full", Score: &high, TimelineAt: base},
		{ArticleID: "summary", BodyMode: "summary", Score: &high, TimelineAt: base.Add(-time.Hour)},
	}
	got, ok = PickRepresentative(rows, entities)
	if !ok || got.ArticleID != "earlier" {
		t.Fatalf("full text then score then time ordering = %+v, ok=%v", got, ok)
	}
	if _, ok := PickRepresentative(nil, entities); ok {
		t.Fatal("empty candidates must not select a representative")
	}
}

func TestRepresentativeIdentityFailsClosedOnUnknownOrMismatchedSubjects(t *testing.T) {
	entities := map[string]interests.Entity{"openai": {Name: "OpenAI"}, "anthropic": {Name: "Anthropic"}}
	for _, identity := range []RepresentativeIdentity{
		{SourceTier: "T2", PublisherRole: "organization", OwnerEntityID: "openai", FactSubject: "OpenAI,Unknown"},
		{SourceTier: "T2", PublisherRole: "organization", OwnerEntityID: "openai", FactSubject: "Anthropic"},
		{SourceTier: "T2", PublisherRole: "person", OwnerEntityID: "unknown", FactSubject: "OpenAI"},
	} {
		if priority := RepresentativePriority(identity, entities); priority != 3 {
			t.Errorf("unverified identity %+v priority = %d, want 3", identity, priority)
		}
	}
}
