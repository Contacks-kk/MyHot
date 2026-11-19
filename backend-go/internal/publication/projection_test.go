package publication

import (
	"strings"
	"testing"
	"time"
)

func TestDeriveProjectionPreservesPendingSelectionUntilGroupingCompletes(t *testing.T) {
	now := time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC)
	input := fixtureProjectionInput(now)
	first := DeriveProjection(input)
	p := first.Projection
	if !p.Eligible || !p.SelectionCandidate || p.Selected || p.VisibleAfter != nil || p.SelectedReadyAt == nil || !p.SelectedReadyAt.Equal(now) {
		t.Fatalf("pending grouped article projection = %+v", p)
	}
	if p.Title != "中文标题" || p.OriginalTitle == nil || *p.OriginalTitle != "Original title" || p.Summary == nil || *p.Summary != "中文摘要" {
		t.Fatalf("translated copy projection = %+v", p)
	}
	if p.Score == nil || *p.Score != 91.6 || p.BodyMode != "full" || !p.Syndicate || p.Channel != "news" || !p.FirstParty {
		t.Fatalf("score, body, or source projection = %+v", p)
	}
	if p.SearchText != "中文标题 original title 中文摘要 新闻 事件 编辑 openai" {
		t.Fatalf("search text = %q", p.SearchText)
	}

	input.Article.GroupingStatus = "complete"
	input.Previous = &p
	input.Now = now.Add(time.Hour)
	second := DeriveProjection(input)
	if !second.Projection.Selected || !second.Projection.Indexable || second.Projection.VisibleAfter == nil || !second.Projection.VisibleAfter.Equal(now.Add(time.Hour)) {
		t.Fatalf("completed grouping projection = %+v", second.Projection)
	}
	if second.ChangeKind != "detail" || !second.Changed || !fieldsSubset(second.ChangedFields, map[string]bool{
		"selected": true, "reason": true, "indexable": true, "sort_at": true, "visible_after": true,
	}) {
		t.Fatalf("selection admission change = %+v", second)
	}

	input.Previous = &second.Projection
	input.Now = now.Add(48 * time.Hour)
	unchanged := DeriveProjection(input)
	if unchanged.Changed || unchanged.Projection.Revision != second.Projection.Revision || unchanged.Projection.VisibleAfter == nil || !unchanged.Projection.VisibleAfter.Equal(now.Add(time.Hour)) {
		t.Fatalf("stable projection changed or moved release time: %+v", unchanged)
	}
}

/** TestDeriveProjectionKeepsFailedAndRedundantSelectionOutUntilExplicitlySelected locks the grouping and editorial override gate. */
func TestDeriveProjectionKeepsFailedAndRedundantSelectionOutUntilExplicitlySelected(t *testing.T) {
	now := time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC)
	input := fixtureProjectionInput(now)
	input.Article.GroupingStatus = "failed"

	failed := DeriveProjection(input).Projection
	if !failed.Eligible || !failed.SelectionCandidate || failed.Selected || failed.VisibleAfter != nil {
		t.Fatalf("failed grouping escaped the selection gate: %+v", failed)
	}

	input.Previous = &failed
	input.Article.GroupingStatus = "complete"
	input.Article.SelectionAddsValue = boolPointer(false)
	input.Now = now.Add(time.Hour)
	redundant := DeriveProjection(input).Projection
	if !redundant.SelectionCandidate || redundant.Selected || redundant.VisibleAfter != nil {
		t.Fatalf("redundant explanation entered selection: %+v", redundant)
	}

	input.Previous = &redundant
	input.Override = &Override{Fields: OverrideFields{Selected: boolPointer(true)}}
	input.Now = now.Add(2 * time.Hour)
	manual := DeriveProjection(input).Projection
	if !manual.Selected || manual.VisibleAfter == nil || !manual.VisibleAfter.Equal(input.Now) {
		t.Fatalf("explicit editorial selection did not publish: %+v", manual)
	}
}

func TestDeriveProjectionUsesOverridesAndReleaseSemantics(t *testing.T) {
	now := time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC)
	releasedAt := now.Add(-30 * time.Minute)
	input := fixtureProjectionInput(now)
	input.Article.Backfill = true
	input.Article.PublishedAt = nil
	input.Article.GroupingStatus = "pending"
	withoutRelease := DeriveProjection(input)
	if withoutRelease.Projection.Eligible || withoutRelease.Projection.Selected {
		t.Fatalf("undated backfill should not be admitted: %+v", withoutRelease.Projection)
	}

	input.ReleasedAt = &releasedAt
	input.Override = &Override{Visibility: stringPointer("summary-only"), Fields: OverrideFields{
		Title: stringPointer("人工标题"), Summary: stringPointer("人工摘要"), Selected: boolPointer(true),
		Tags: &[]string{"运营"}, Score: floatPointer(80.04), Reason: stringPointer("人工理由"),
	}}
	result := DeriveProjection(input)
	p := result.Projection
	if !p.Eligible || !p.SelectionCandidate || !p.Selected || p.Visibility != "summary-only" {
		t.Fatalf("released manual projection = %+v", p)
	}
	if p.Title != "人工标题" || p.Summary == nil || *p.Summary != "人工摘要" || p.Reason == nil || *p.Reason != "人工理由" || len(p.Tags) != 1 || p.Tags[0] != "运营" {
		t.Fatalf("manual fields not applied = %+v", p)
	}
	if p.Score == nil || *p.Score != 80 || p.SelectedReadyAt == nil || !p.SelectedReadyAt.Equal(releasedAt) || p.VisibleAfter == nil || !p.VisibleAfter.Equal(releasedAt) {
		t.Fatalf("release timestamp or score rounding = %+v", p)
	}
	if p.Indexable {
		t.Fatal("summary-only content must not be indexable")
	}

	input.Source.ParticipationMode = "isolated"
	withdrawn := DeriveProjection(input)
	if withdrawn.Projection.Visibility != "withdrawn" || withdrawn.Projection.Eligible || withdrawn.Projection.Selected {
		t.Fatalf("isolated content escaped withdrawal: %+v", withdrawn.Projection)
	}
}

func TestDeriveProjectionReportsReducedFullTextAndIgnoresTagOrder(t *testing.T) {
	now := time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC)
	input := fixtureProjectionInput(now)
	input.Article.GroupingStatus = "complete"
	previous := DeriveProjection(input).Projection

	input.Previous = &previous
	input.Source.SiteFulltext = false
	input.Source.SyndicateFulltext = false
	revoked := DeriveProjection(input)
	if !revoked.Reduced || revoked.Projection.BodyMode != "summary" || revoked.Projection.Syndicate {
		t.Fatalf("full-text revocation = %+v", revoked)
	}

	input.Source.SiteFulltext, input.Source.SyndicateFulltext = true, true
	input.Previous = &previous
	input.Analysis.Tags = []string{"编辑", "事件"}
	stable := DeriveProjection(input)
	if stable.Changed {
		t.Fatalf("tag order alone changed the public projection: %v", stable.ChangedFields)
	}
	if strings.Contains(stable.Projection.SearchText, "entity:entity:") {
		t.Fatalf("entity tags were duplicated: %q", stable.Projection.SearchText)
	}
}

func fixtureProjectionInput(now time.Time) ProjectionInput {
	selected := true
	addsValue := true
	relevance, titleZH, summaryZH, reason := "pass", "中文标题", "中文摘要", "值得关注"
	category := "发布"
	score := 91.56
	storyID, factID := int64(22), int64(33)
	body := "这是完整正文。"
	language := "en"
	return ProjectionInput{
		Article: Article{ID: "article-1", SourceID: "source-1", URL: "https://news.example/article", Title: "Original title", Language: &language,
			PublishedAt: timePointer(now.Add(-time.Hour)), DiscoveredAt: now.Add(-time.Minute), TimelineAt: now.Add(-time.Hour),
			BodyStatus: "ok", BodyText: &body, GroupingStatus: "pending", SelectionAddsValue: &addsValue},
		Source: SourceFacts{ID: "source-1", Name: "新闻（编辑）", Kind: "rss", Tier: "T1", ParticipationMode: "editorial", SiteFulltext: true, SyndicateFulltext: true},
		Analysis: &Analysis{ID: 7, Relevance: &relevance, Category: &category, Tags: []string{"事件", "编辑"}, Subjects: []string{"openai"},
			TitleZH: &titleZH, SummaryZH: &summaryZH, ReasonZH: &reason, Score: &score, Selected: &selected},
		Membership: Membership{StoryID: &storyID, FactID: &factID}, FactAnchor: timePointer(now.Add(-2 * time.Hour)), Now: now,
	}
}

func stringPointer(value string) *string { return &value }

func boolPointer(value bool) *bool { return &value }

func floatPointer(value float64) *float64 { return &value }
