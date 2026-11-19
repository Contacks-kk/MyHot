package admincontent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type publicationDateCorrectionRepository struct {
	plan    *PublicationDateCorrectionPlan
	input   PublicationDateCorrectionInput
	article string
	actor   string
	preview int
	correct int
	result  *PublicationDateCorrectionResult
	found   bool
}

/** PreviewPublicationDateCorrection returns the fixed plan used by the service contract test. */
func (r *publicationDateCorrectionRepository) PreviewPublicationDateCorrection(context.Context, string, string) (*PublicationDateCorrectionPlan, error) {
	r.preview++
	return r.plan, nil
}

/** CorrectPublicationDate records the approval data forwarded by the service. */
func (r *publicationDateCorrectionRepository) CorrectPublicationDate(_ context.Context, article string, input PublicationDateCorrectionInput, actor string) (*PublicationDateCorrectionResult, bool, error) {
	r.article, r.input, r.actor = article, input, actor
	r.correct++
	return r.result, r.found, nil
}

type commandRepository struct {
	visibility VisibilityInput
	override   OverrideInput
	step       string
	requestID  string
	actor      string
	calls      int
}

func (r *commandRepository) SetVisibility(_ context.Context, _ string, input VisibilityInput, actor string) (*PublishResult, bool, error) {
	r.visibility, r.actor, r.calls = input, actor, r.calls+1
	return &PublishResult{ArticleID: "article-1"}, true, nil
}
func (r *commandRepository) OverrideFields(_ context.Context, _ string, input OverrideInput, actor string) (*PublishResult, bool, error) {
	r.override, r.actor, r.calls = input, actor, r.calls+1
	return &PublishResult{ArticleID: "article-1"}, true, nil
}
func (r *commandRepository) Rerun(_ context.Context, _, step, requestID, actor string) (*RerunResult, bool, error) {
	r.step, r.requestID, r.actor, r.calls = step, requestID, actor, r.calls+1
	return &RerunResult{}, true, nil
}
func (*commandRepository) Detach(context.Context, string, string, string) (map[string]int, bool, error) {
	return map[string]int{"detached": 1}, true, nil
}
func (*commandRepository) MergeStories(context.Context, int64, int64, string, string) (map[string]int, bool, error) {
	return map[string]int{"moved": 1}, true, nil
}

type memoryRepository struct {
	calls      []string
	id         string
	urls       []string
	identities []string
	title      string
	rows       []Row
}

func (r *memoryRepository) ByID(_ context.Context, id string) ([]Row, error) {
	r.calls = append(r.calls, "id")
	r.id = id
	return r.rows, nil
}
func (r *memoryRepository) ByURL(_ context.Context, urls, identities []string) ([]Row, error) {
	r.calls = append(r.calls, "url")
	r.urls, r.identities = urls, identities
	return r.rows, nil
}
func (r *memoryRepository) ByTitle(_ context.Context, title string) ([]Row, error) {
	r.calls = append(r.calls, "title")
	r.title = title
	return r.rows, nil
}
func TestSearchPrefersExactIDsAndSkipsEmptyQueries(t *testing.T) {
	repo := &memoryRepository{rows: []Row{{ID: "article-1"}}}
	service := NewService(repo)
	rows, err := service.Search(context.Background(), " article-1 ")
	if err != nil || len(rows) != 1 || repo.id != "article-1" || len(repo.calls) != 1 || repo.calls[0] != "id" {
		t.Fatalf("exact ID lookup did not short circuit: rows=%#v calls=%v err=%v", rows, repo.calls, err)
	}
	repo.calls = nil
	rows, err = service.Search(context.Background(), "  ")
	if err != nil || len(rows) != 0 || rows == nil || len(repo.calls) != 0 {
		t.Fatalf("empty search should return a stable empty result: rows=%#v calls=%v err=%v", rows, repo.calls, err)
	}
}

func TestSearchNormalizesArticleURLsAndKeepsFragmentIdentity(t *testing.T) {
	repo := &memoryRepository{}
	_, err := NewService(repo).Search(context.Background(), "http://www.example.com/a?utm_source=fixture&b=2#part")
	if err != nil || len(repo.calls) != 2 || repo.calls[0] != "url" || repo.calls[1] != "title" {
		t.Fatalf("URL lookup fallback changed: calls=%v err=%v", repo.calls, err)
	}
	if len(repo.urls) != 2 || repo.urls[0] != "http://www.example.com/a?utm_source=fixture&b=2#part" || repo.urls[1] != "https://example.com/a?b=2" {
		t.Fatalf("URL candidates changed: %#v", repo.urls)
	}
	if len(repo.identities) != 2 || repo.identities[0] != "url:https://example.com/a?b=2" || repo.identities[1] != "url:https://example.com/a?b=2#part" {
		t.Fatalf("normalized material identities changed: %#v", repo.identities)
	}
}

func TestTwitterStatusUsesMaterialIdentity(t *testing.T) {
	urls, identities, ok := contentURLKeys("https://mobile.x.com/example/status/123456?ref=share")
	if !ok || len(urls) != 1 || len(identities) != 1 || identities[0] != "x:123456" {
		t.Fatalf("X URL identity changed: urls=%#v identities=%#v ok=%v", urls, identities, ok)
	}
}

/** TestGroupingMergeAllowedPreservesManualStoryIdentity checks the in-transaction auto-merge guard. */
func TestGroupingMergeAllowedPreservesManualStoryIdentity(t *testing.T) {
	for _, test := range []struct {
		name       string
		actor      string
		fromOrigin string
		intoOrigin string
		want       bool
	}{
		{name: "model stories", actor: "grouping", fromOrigin: "model", intoOrigin: "model", want: true},
		{name: "manual source", actor: "grouping", fromOrigin: "manual", intoOrigin: "model"},
		{name: "manual target", actor: "grouping", fromOrigin: "model", intoOrigin: "manual"},
		{name: "explicit editor merge", actor: "admin:41", fromOrigin: "manual", intoOrigin: "model", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := groupingMergeAllowed(test.actor, test.fromOrigin, test.intoOrigin); got != test.want {
				t.Fatalf("grouping merge allowed = %v, want %v", got, test.want)
			}
		})
	}
}

func TestContentCommandsValidateBeforeRepositoryAndKeepRequestIdentity(t *testing.T) {
	repo := &commandRepository{}
	service := NewService(&memoryRepository{})
	service.SetCommands(repo, []string{"ai", "business"})
	ctx := context.Background()
	if _, _, err := service.SetVisibility(ctx, "article-1", VisibilityInput{Visibility: "hidden", Reason: "reviewed", Version: 0}, "admin:41"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unsupported visibility accepted: %v", err)
	}
	if _, _, err := service.OverrideFields(ctx, "article-1", OverrideInput{Fields: map[string]json.RawMessage{"category": json.RawMessage(`"unknown"`)}, Reason: "reviewed"}, "admin:41"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unknown category accepted: %v", err)
	}
	if _, _, err := service.Rerun(ctx, "article-1", "analyze", "bad", "admin:41"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("invalid idempotency key accepted: %v", err)
	}
	if _, _, err := service.Rerun(ctx, "article-1", "typo", "admin-request-123", "admin:41"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("invalid rerun step accepted: %v", err)
	}
	if repo.calls != 0 {
		t.Fatalf("invalid commands reached the repository: calls=%d", repo.calls)
	}
	if _, _, err := service.Rerun(ctx, "article-1", "analyze", "admin-request-123", "admin:41"); err != nil {
		t.Fatal(err)
	}
	if repo.calls != 1 || repo.step != "analyze" || repo.requestID != "admin-request-123" || repo.actor != "admin:41" {
		t.Fatalf("valid idempotent command was not preserved: %#v", repo)
	}
}

/** TestPlanPublicationDateCorrectionPreservesIndependentDiscoveryOrderAndRejectsUnsafeDates protects the legacy correction rules. */
func TestPlanPublicationDateCorrectionPreservesIndependentDiscoveryOrderAndRejectsUnsafeDates(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	publication := now.Add(-20 * 24 * time.Hour).Truncate(time.Millisecond)
	claim := publication.Add(-time.Hour)
	material := PublicationDateCorrectionMaterial{
		ID: "article-1", Identity: "url:https://example.test/a", SourceID: "source-1", URL: "https://example.test/a",
		Revision: 7, ContentHash: stringPointer(strings.Repeat("a", 64)), PublishedAt: &publication, PublishedAtClaim: &claim,
		DiscoveredAt: now.Add(-19 * 24 * time.Hour), TimelineAt: publication, Backfill: true, BackfillReason: stringPointer("verified archive"),
		UpdatedAt: now.Add(-18 * 24 * time.Hour),
	}
	requested := now.Add(-10 * 24 * time.Hour).Format(publicationDateLayout)
	plan, err := PlanPublicationDateCorrection(material, requested, now)
	if err != nil || !plan.Changed || !plan.TimelineFollowsPublication || plan.After.TimelineAt != requested || plan.After.PublishedAtClaim == nil || *plan.After.PublishedAtClaim != requested {
		t.Fatalf("publication-following plan changed: plan=%#v err=%v", plan, err)
	}
	material.TimelineAt = material.DiscoveredAt
	independent, err := PlanPublicationDateCorrection(material, requested, now)
	if err != nil || independent.TimelineFollowsPublication || independent.After.TimelineAt != formatPublicationDate(material.DiscoveredAt) {
		t.Fatalf("independent discovery order changed: plan=%#v err=%v", independent, err)
	}
	if independent.Hash == plan.Hash {
		t.Fatal("approval hash did not bind the independent timeline value")
	}
	for _, invalid := range []string{
		"2026-11-09T12:00:00Z",
		"2026-11-09T12:00:00.000+00:00",
		now.Add(-2 * 24 * time.Hour).Format(publicationDateLayout),
		now.Add(time.Hour).Format(publicationDateLayout),
	} {
		if _, err := PlanPublicationDateCorrection(material, invalid, now); err == nil {
			t.Fatalf("unsafe publication date accepted: %q", invalid)
		}
	}
	material.PublishedAt = nil
	if _, err := PlanPublicationDateCorrection(material, requested, now); err == nil {
		t.Fatal("article without a trusted publication date was eligible for correction")
	}
}

/** TestPublicationDateCorrectionValidatesAndForwardsTheReviewedRequest locks down service validation. */
func TestPublicationDateCorrectionValidatesAndForwardsTheReviewedRequest(t *testing.T) {
	repo := &publicationDateCorrectionRepository{
		plan:   &PublicationDateCorrectionPlan{ArticleID: "article-1"},
		result: &PublicationDateCorrectionResult{ArticleID: "article-1", Status: "corrected"},
		found:  true,
	}
	service := NewService(nil)
	service.SetPublicationDateCorrectionRepository(repo)
	ctx := context.Background()
	if _, err := service.PreviewPublicationDateCorrection(ctx, "article-1", "2026-11-09T12:00:00Z"); !errors.Is(err, ErrInvalidPublicationDateCorrection) {
		t.Fatalf("noncanonical preview date passed validation: %v", err)
	}
	invalid := PublicationDateCorrectionInput{Version: 1, Hash: "BAD", PublishedAt: "2026-11-09T12:00:00.000Z", RequestID: "bad", Reason: "review"}
	if _, _, err := service.CorrectPublicationDate(ctx, "article-1", invalid, "admin:41"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("invalid correction approval passed validation: %v", err)
	}
	if repo.preview != 0 || repo.correct != 0 {
		t.Fatalf("invalid inputs reached the repository: preview=%d correct=%d", repo.preview, repo.correct)
	}
	input := PublicationDateCorrectionInput{
		Version: 1, Hash: strings.Repeat("a", 64), PublishedAt: "2026-11-09T12:00:00.000Z", RequestID: "admin-request-123", Reason: "  verified source timestamp  ",
	}
	if _, err := service.PreviewPublicationDateCorrection(ctx, "article-1", input.PublishedAt); err != nil || repo.preview != 1 {
		t.Fatalf("valid preview did not reach repository: preview=%d err=%v", repo.preview, err)
	}
	if _, found, err := service.CorrectPublicationDate(ctx, "article-1", input, "admin:41"); err != nil || !found || repo.correct != 1 || repo.article != "article-1" || repo.input.Reason != "verified source timestamp" || repo.actor != "admin:41" {
		t.Fatalf("valid correction was not forwarded as reviewed: found=%t err=%v repo=%#v", found, err, repo)
	}
}

/** TestPlanXEncodingRepairPreservesNonproviderTextAndLongFormMaterial verifies the reviewed repair boundary. */
func TestPlanXEncodingRepairPreservesNonproviderTextAndLongFormMaterial(t *testing.T) {
	title := "Research &amp; development"
	body := "Research &amp; development\nLiteral &amp;lt; and <code>\nhttps://example.test/?value=&amp;keep=1\n\n【引用 @quoted】value &lt; 3 &amp; value &gt; 0\n\n# A literal &amp; heading\n\nThe long article deliberately writes &amp; and &lt;."
	excerpt := "Saved excerpt &amp; literal"
	material := XEncodingMaterial{
		ID:       "article-1",
		Kind:     "x_search",
		Identity: "x:123",
		URL:      "https://x.com/example/status/123",
		Revision: 1,
		Title:    title,
		BodyText: &body,
		Excerpt:  &excerpt,
		XPost:    json.RawMessage(`{"tweetId":"123","text":"Research &amp; development\nLiteral &amp;lt; and <code>\nhttps://example.test/?value=&amp;keep=1","quoted":{"handle":"quoted","text":"value &lt; 3 &amp; value &gt; 0","url":"https://x.com/quoted/status/123"},"lang":"en"}`),
		XArticle: json.RawMessage(`{"title":"A literal &amp; heading","text":"The long article deliberately writes &amp; and &lt;."}`),
	}
	plan, err := PlanXEncodingRepair(material)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Changed || plan.PreservedTitle || plan.After.Title != "Research & development" {
		t.Fatalf("unexpected title repair plan: %#v", plan)
	}
	var post struct {
		Text   string                     `json:"text"`
		Lang   json.RawMessage            `json:"lang"`
		Quoted map[string]json.RawMessage `json:"quoted"`
	}
	if err := json.Unmarshal(plan.After.XPost, &post); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(post.Text, "Literal &lt; and <code>") || !strings.Contains(post.Text, "?value=&amp;keep=1") || string(post.Lang) != `"en"` {
		t.Fatalf("post decoding changed literal entity or expanded URL: %q", post.Text)
	}
	var quotedURL string
	if json.Unmarshal(post.Quoted["url"], &quotedURL) != nil || quotedURL != "https://x.com/quoted/status/123" {
		t.Fatalf("quote metadata was not preserved: %#v", post.Quoted)
	}
	if !strings.HasSuffix(*plan.After.BodyText, "# A literal &amp; heading\n\nThe long article deliberately writes &amp; and &lt;.") {
		t.Fatalf("long-form bytes were modified: %q", *plan.After.BodyText)
	}
	if plan.Hash == "" || plan.After.ContentHash == nil || *plan.After.ContentHash == "" {
		t.Fatalf("repair plan omitted its optimistic hash or content hash: %#v", plan)
	}
}

/** TestPlanXEncodingRepairPreservesCustomTitlesAndBindsAllInputs prevents guessed title edits and stale approvals. */
func TestPlanXEncodingRepairPreservesCustomTitlesAndBindsAllInputs(t *testing.T) {
	title := "An independently edited &amp; title"
	body := "Research &amp; development"
	material := XEncodingMaterial{
		ID:       "article-2",
		Kind:     "x_search",
		Identity: "x:456",
		URL:      "https://x.com/example/status/456",
		Revision: 3,
		Title:    title,
		BodyText: &body,
		XPost:    json.RawMessage(`{"tweetId":"456","text":"Research &amp; development"}`),
	}
	plan, err := PlanXEncodingRepair(material)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.PreservedTitle || plan.After.Title != title || !plan.Changed {
		t.Fatalf("custom title was not preserved: %#v", plan)
	}
	material.Excerpt = stringPointer("changed excerpt")
	changedPlan, err := PlanXEncodingRepair(material)
	if err != nil {
		t.Fatal(err)
	}
	if changedPlan.Hash == plan.Hash {
		t.Fatal("approval hash did not include the excerpt used when calculating the plan")
	}
}

/** TestPlanXEncodingRepairRejectsUnprovedMaterialKeepsTheOperatorFromRewritingOtherSourcesOrBodies. */
func TestPlanXEncodingRepairRejectsUnprovedMaterial(t *testing.T) {
	body := "Research &amp; development"
	material := XEncodingMaterial{
		ID:       "article-3",
		Kind:     "x_search",
		Identity: "x:789",
		URL:      "https://x.com/example/status/789",
		Revision: 1,
		Title:    body,
		BodyText: &body,
		XPost:    json.RawMessage(`{"tweetId":"789","text":"Research &amp; development"}`),
	}
	for _, mutate := range []func(*XEncodingMaterial){
		func(material *XEncodingMaterial) { material.Kind = "rss" },
		func(material *XEncodingMaterial) { material.Identity = "x:wrong" },
		func(material *XEncodingMaterial) { material.BodyText = stringPointer("unrelated body") },
		func(material *XEncodingMaterial) { material.XPost = json.RawMessage(`{"tweetId":"789"}`) },
	} {
		candidate := material
		mutate(&candidate)
		if _, err := PlanXEncodingRepair(candidate); err == nil {
			t.Fatalf("unproved X material was accepted: %#v", candidate)
		}
	}
}

/** TestPlanXEncodingRepairTrimsUnicodeWhitespaceLikeTheProviderPathPreservesLegacyBoundarySemantics. */
func TestPlanXEncodingRepairTrimsUnicodeWhitespaceLikeTheProviderPath(t *testing.T) {
	title := "Research &amp; development"
	body := title
	material := XEncodingMaterial{
		ID:       "article-4",
		Kind:     "x_search",
		Identity: "x:987",
		URL:      "https://x.com/example/status/987",
		Revision: 1,
		Title:    title,
		BodyText: &body,
		XPost:    json.RawMessage(`{"tweetId":"987","text":"\uFEFFResearch &amp; development\uFEFF"}`),
	}
	plan, err := PlanXEncodingRepair(material)
	if err != nil {
		t.Fatal(err)
	}
	if plan.After.Title != "Research & development" || plan.After.BodyText == nil || *plan.After.BodyText != plan.After.Title {
		t.Fatalf("provider whitespace normalization changed: plan=%#v", plan)
	}
}

/** TestHasNonClassificationOverrideSeparatesTaxonomyEditsFromDigestRelevantContentEdits. */
func TestHasNonClassificationOverride(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]json.RawMessage
		clear  []string
		want   bool
	}{
		{name: "classification fields", fields: map[string]json.RawMessage{"category": json.RawMessage(`"ai-products"`), "tags": json.RawMessage(`[]`)}},
		{name: "cleared classification fields", clear: []string{"category", "tags"}},
		{name: "mixed content field", fields: map[string]json.RawMessage{"category": json.RawMessage(`"ai-products"`), "title": json.RawMessage(`"Corrected"`)}, want: true},
		{name: "cleared content field", clear: []string{"tags", "summary"}, want: true},
		{name: "empty edit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasNonClassificationOverride(test.fields, test.clear); got != test.want {
				t.Fatalf("hasNonClassificationOverride()=%t, want %t", got, test.want)
			}
		})
	}
}

/** stringPointer creates a pointer for immutable test fixture text. */
func stringPointer(value string) *string {
	return &value
}
