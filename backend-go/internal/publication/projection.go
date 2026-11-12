package publication

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

type Article struct {
	ID                 string
	SourceID           string
	URL                string
	Title              string
	Language           *string
	PublishedAt        *time.Time
	DiscoveredAt       time.Time
	TimelineAt         time.Time
	Backfill           bool
	BodyStatus         string
	BodyText           *string
	XPost              json.RawMessage
	GroupingStatus     string
	SelectionAddsValue *bool
}

type Analysis struct {
	ID        int64
	Relevance *string
	Category  *string
	Tags      []string
	Subjects  []string
	TitleZH   *string
	SummaryZH *string
	ReasonZH  *string
	Score     *float64
	Selected  *bool
}

type OverrideFields struct {
	Title     *string
	Summary   *string
	Category  *string
	Tags      *[]string
	Score     *float64
	Relevance *string
	Selected  *bool
	Reason    *string
}

type Override struct {
	Fields     OverrideFields
	Visibility *string
}

type Membership struct {
	StoryID *int64
	FactID  *int64
}

// Projection is the public row derived for one article. Revision is advanced only
// when a compared projection field changes; SEO timestamps remain editor-owned.
type Projection struct {
	ArticleID          string
	AnalysisID         *int64
	Revision           int
	Visibility         string
	Eligible           bool
	Selected           bool
	SelectionCandidate bool
	Title              string
	OriginalTitle      *string
	Summary            *string
	Reason             *string
	Category           *string
	Tags               []string
	Score              *float64
	SourceID           string
	Channel            string
	FirstParty         bool
	URL                string
	PublishedAt        *time.Time
	DiscoveredAt       time.Time
	TimelineAt         time.Time
	Backfill           bool
	SelectedReadyAt    *time.Time
	VisibleAfter       *time.Time
	BodyMode           string
	Syndicate          bool
	Indexable          bool
	StoryID            *int64
	FactID             *int64
	SearchText         string
	SortAt             time.Time
	SEOIndexedAt       *time.Time
	SEOExcludedAt      *time.Time
}

type ProjectionInput struct {
	Article          Article
	Source           SourceFacts
	Analysis         *Analysis
	Override         *Override
	Membership       Membership
	Previous         *Projection
	HasSelectedState bool
	FactAnchor       *time.Time
	Now              time.Time
	ReleasedAt       *time.Time
}

type ProjectionResult struct {
	Projection    Projection
	Changed       bool
	ChangeKind    string
	Reduced       bool
	ChangedFields []string
}

// DeriveProjection mirrors the legacy publishArticleTx business decision without
// touching storage, queues, caches, or external services.
func DeriveProjection(input ProjectionInput) ProjectionResult {
	a, source := input.Article, input.Source
	analysis := input.Analysis
	fields := OverrideFields{}
	visibilityOverride := (*string)(nil)
	if input.Override != nil {
		fields = input.Override.Fields
		visibilityOverride = input.Override.Visibility
	}

	var analysisID *int64
	if analysis != nil {
		analysisID = int64Pointer(analysis.ID)
	}
	var zhTitle *string
	if analysis != nil && nonBlank(analysis.TitleZH) {
		zhTitle = analysis.TitleZH
	}
	chineseTitle := equalsString(a.Language, "zh") || containsHan(a.Title)
	var fallbackTitle *string
	switch {
	case zhTitle != nil:
		fallbackTitle = zhTitle
	case chineseTitle || len(a.XPost) > 0 && string(a.XPost) != "null":
		value := collapseWhitespace(a.Title)
		fallbackTitle = &value
	}
	title := pickString(fields.Title, fallbackTitle)
	summary := pickString(fields.Summary, analysisString(analysis, func(a *Analysis) *string { return a.SummaryZH }))
	category := pickString(fields.Category, analysisString(analysis, func(a *Analysis) *string { return a.Category }))
	tags := append([]string{}, analysisTags(analysis)...)
	if fields.Tags != nil {
		tags = append([]string{}, (*fields.Tags)...)
	}
	score := fields.Score
	if score == nil && analysis != nil {
		score = analysis.Score
	}
	relevance := analysisString(analysis, func(a *Analysis) *string { return a.Relevance })
	if fields.Relevance != nil {
		relevance = fields.Relevance
	}
	judgedSelected := analysisBool(analysis)
	if fields.Selected != nil {
		judgedSelected = fields.Selected
	}
	visibility := "public"
	if source.ParticipationMode == "isolated" {
		visibility = "withdrawn"
	} else if visibilityOverride != nil {
		visibility = *visibilityOverride
	}
	eligible := IsPoolEligible(source.ParticipationMode, stringValue(relevance), title, summary) &&
		(!a.Backfill || a.PublishedAt != nil || input.ReleasedAt != nil)
	selectionCandidate := IsSelectable(eligible, judgedSelected, source.Tier)
	addsValue := a.SelectionAddsValue == nil || *a.SelectionAddsValue
	groupingStatus := a.GroupingStatus
	if input.ReleasedAt != nil && groupingStatus == "pending" {
		groupingStatus = "complete"
	}
	selected := selectionCandidate && groupingStatus == "complete" && (equalsBool(fields.Selected, true) || addsValue)
	var reason *string
	if selected {
		reason = pickString(fields.Reason, analysisString(analysis, func(a *Analysis) *string { return a.ReasonZH }))
	}
	bodyText := a.BodyText != nil && *a.BodyText != ""
	bodyMode := BodyModeOf(source, a.BodyStatus, bodyText)
	channel := ChannelOf(source.Kind, len(a.XPost) > 0 && string(a.XPost) != "null")
	syndicate := MayRedistribute(source, bodyMode)
	originalTitle := collapseWhitespace(a.Title)
	if chineseTitle && title != nil && *title == originalTitle {
		originalTitle = ""
	}
	var originalTitleValue *string
	if originalTitle != "" {
		originalTitleValue = &originalTitle
	}

	previous := input.Previous
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	var selectedReadyAt *time.Time
	if previous != nil && previous.SelectedReadyAt != nil {
		selectedReadyAt = previous.SelectedReadyAt
	} else if selectionCandidate {
		selectedReadyAt = timePointer(releasedOrNow(input.ReleasedAt, now))
	}
	var visibleAfter *time.Time
	if selected {
		if previous != nil && previous.Selected && previous.VisibleAfter != nil {
			visibleAfter = previous.VisibleAfter
		} else {
			visibleAfter = timePointer(releasedOrNow(input.ReleasedAt, now))
		}
	}
	seoIndexedAt, seoExcludedAt := (*time.Time)(nil), (*time.Time)(nil)
	if previous != nil {
		seoIndexedAt, seoExcludedAt = previous.SEOIndexedAt, previous.SEOExcludedAt
	}
	indexable := IsIndexable(visibility, source.ParticipationMode, nonBlank(summary), selected, seoIndexedAt, seoExcludedAt)
	subjects := analysisSubjects(analysis)
	searchParts := []string{titleValue(title), stringValue(originalTitleValue), stringValue(summary), PublicSourceName(source.Name)}
	searchParts = append(searchParts, DisplayTags(tags)...)
	searchParts = append(searchParts, subjects...)
	searchText := strings.ToLower(collapseWhitespace(strings.Join(searchParts, " ")))
	sortAt := a.TimelineAt
	if selected && input.Membership.FactID != nil && input.FactAnchor != nil && input.FactAnchor.Before(sortAt) {
		sortAt = *input.FactAnchor
	}
	projection := Projection{
		ArticleID: a.ID, AnalysisID: analysisID, Visibility: visibility, Eligible: eligible, Selected: selected,
		SelectionCandidate: selectionCandidate, Title: firstNonEmpty(titleValue(title), collapseWhitespace(a.Title)),
		OriginalTitle: originalTitleValue, Summary: summary, Reason: reason, Category: category, Tags: tags,
		Score: round1(score), SourceID: source.ID, Channel: channel, FirstParty: source.Tier == "T1", URL: a.URL,
		PublishedAt: a.PublishedAt, DiscoveredAt: a.DiscoveredAt, TimelineAt: a.TimelineAt, Backfill: a.Backfill,
		SelectedReadyAt: selectedReadyAt, VisibleAfter: visibleAfter, BodyMode: bodyMode, Syndicate: syndicate,
		Indexable: indexable, StoryID: input.Membership.StoryID, FactID: input.Membership.FactID, SearchText: searchText,
		SortAt: sortAt, SEOIndexedAt: seoIndexedAt, SEOExcludedAt: seoExcludedAt,
	}
	changedFields := projectionChangedFields(previous, projection)
	changed := len(changedFields) > 0
	projection.Revision = 1
	if previous != nil {
		projection.Revision = previous.Revision
		if changed {
			projection.Revision++
		}
	}
	changeKind := "content"
	admissionFields := map[string]bool{"selected": true, "reason": true, "indexable": true, "story_id": true, "fact_id": true, "sort_at": true, "visible_after": true}
	firstAnalysisFields := map[string]bool{}
	for key := range admissionFields {
		firstAnalysisFields[key] = true
	}
	for _, key := range []string{"eligible", "selection_candidate", "title", "original_title", "summary", "category", "tags", "score"} {
		firstAnalysisFields[key] = true
	}
	if previous != nil {
		admission := !previous.Selected && selected && previousMembershipCompatible(previous.StoryID, input.Membership.StoryID) &&
			previousMembershipCompatible(previous.FactID, input.Membership.FactID) && fieldsSubset(changedFields, admissionFields)
		firstAnalysis := !previous.Eligible && previous.Summary == nil && previous.Visibility == "public" && visibility == "public" && eligible &&
			previousMembershipCompatible(previous.StoryID, input.Membership.StoryID) && previousMembershipCompatible(previous.FactID, input.Membership.FactID) &&
			fieldsSubset(changedFields, firstAnalysisFields)
		if (admission || firstAnalysis) && !input.HasSelectedState {
			changeKind = "detail"
		}
	}
	wasPublic := previous != nil && previous.Visibility != "withdrawn" && previous.Eligible
	reduced := wasPublic && (visibility == "withdrawn" || !eligible || previous.Visibility == "public" && visibility != "public" || previous.Selected && !selected || previous.BodyMode == "full" && bodyMode != "full")
	return ProjectionResult{Projection: projection, Changed: changed, ChangeKind: changeKind, Reduced: reduced, ChangedFields: changedFields}
}

func projectionChangedFields(previous *Projection, next Projection) []string {
	after := projectionComparable(next)
	keys := make([]string, 0, len(after))
	for key := range after {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if previous == nil {
		return keys
	}
	before := projectionComparable(*previous)
	changed := make([]string, 0)
	for _, key := range keys {
		left, _ := json.Marshal(before[key])
		right, _ := json.Marshal(after[key])
		if !bytes.Equal(left, right) {
			changed = append(changed, key)
		}
	}
	return changed
}

func projectionComparable(p Projection) map[string]any {
	tags := append([]string{}, p.Tags...)
	sort.Strings(tags)
	return map[string]any{
		"source_id": p.SourceID, "first_party": p.FirstParty, "visibility": p.Visibility, "eligible": p.Eligible,
		"selected": p.Selected, "selection_candidate": p.SelectionCandidate, "title": p.Title, "original_title": p.OriginalTitle,
		"summary": p.Summary, "reason": p.Reason, "category": p.Category, "tags": tags, "score": p.Score, "body_mode": p.BodyMode,
		"story_id": p.StoryID, "fact_id": p.FactID, "indexable": p.Indexable, "url": p.URL, "channel": p.Channel,
		"syndicate": p.Syndicate, "published_at": formatNullableTime(p.PublishedAt), "discovered_at": formatTime(p.DiscoveredAt), "timeline_at": formatTime(p.TimelineAt),
		"backfill": p.Backfill, "sort_at": formatTime(p.SortAt), "visible_after": formatNullableTime(p.VisibleAfter),
	}
}

func analysisTags(analysis *Analysis) []string {
	if analysis == nil {
		return []string{}
	}
	seen := make(map[string]struct{}, len(analysis.Tags)+len(analysis.Subjects))
	tags := make([]string, 0, len(analysis.Tags)+len(analysis.Subjects))
	for _, tag := range append(append([]string{}, analysis.Tags...), entityTags(analysis.Subjects)...) {
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

func entityTags(subjects []string) []string {
	seen := make(map[string]struct{}, len(subjects))
	tags := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		tag := "entity:" + subject
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

func analysisSubjects(analysis *Analysis) []string {
	if analysis == nil {
		return []string{}
	}
	return analysis.Subjects
}

func analysisString(analysis *Analysis, pick func(*Analysis) *string) *string {
	if analysis == nil {
		return nil
	}
	return pick(analysis)
}

func analysisBool(analysis *Analysis) *bool {
	if analysis == nil {
		return nil
	}
	return analysis.Selected
}

func pickString(override, fallback *string) *string {
	if override != nil && strings.TrimSpace(*override) != "" {
		value := strings.TrimSpace(*override)
		return &value
	}
	return fallback
}

func round1(value *float64) *float64 {
	if value == nil {
		return nil
	}
	rounded := math.Floor(*value*10+0.5) / 10
	return &rounded
}

func collapseWhitespace(value string) string { return strings.Join(strings.Fields(value), " ") }

func containsHan(value string) bool {
	for _, r := range value {
		if r >= '\u4e00' && r <= '\u9fff' {
			return true
		}
	}
	return false
}

func equalsString(value *string, want string) bool { return value != nil && *value == want }

func equalsBool(value *bool, want bool) bool { return value != nil && *value == want }

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func titleValue(value *string) string { return stringValue(value) }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func int64Pointer(value int64) *int64 { return &value }

func timePointer(value time.Time) *time.Time { return &value }

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func formatNullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func releasedOrNow(releasedAt *time.Time, now time.Time) time.Time {
	if releasedAt != nil {
		return *releasedAt
	}
	return now
}

func sameNullableInt(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func previousMembershipCompatible(previous, next *int64) bool {
	return previous == nil || sameNullableInt(previous, next)
}

func fieldsSubset(changed []string, allowed map[string]bool) bool {
	for _, field := range changed {
		if !allowed[field] {
			return false
		}
	}
	return true
}
