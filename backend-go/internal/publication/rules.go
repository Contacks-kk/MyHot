package publication

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"aihot/backend-go/internal/interests"
	"golang.org/x/text/unicode/norm"
)

// SourceFacts contains the fields that determine the public form of a source.
// It deliberately does not mirror the full sources table.
type SourceFacts struct {
	ID                string
	Name              string
	Kind              string
	Tier              string
	ParticipationMode string
	SiteFulltext      bool
	SyndicateFulltext bool
}

func ChannelOf(sourceKind string, hasXPost bool) string {
	if sourceKind == "x_search" || hasXPost {
		return "x"
	}
	return "news"
}

func IsPoolEligible(participationMode, relevance string, title, summary *string) bool {
	return participationMode == "editorial" && relevance == "pass" && nonBlank(title) && nonBlank(summary)
}

func IsSelectable(eligible bool, judgedSelected *bool, tier string) bool {
	return eligible && judgedSelected != nil && *judgedSelected && tier != "EXCLUDE_MP"
}

func BodyModeOf(source SourceFacts, bodyStatus string, hasBody bool) string {
	if source.SiteFulltext && bodyStatus == "ok" && hasBody {
		return "full"
	}
	return "summary"
}

func MayRedistribute(source SourceFacts, bodyMode string) bool {
	return source.SyndicateFulltext && bodyMode == "full"
}

func IsIndexable(visibility, sourceMode string, hasSummary, selected bool, seoIndexedAt, seoExcludedAt *time.Time) bool {
	return visibility == "public" && sourceMode == "editorial" && hasSummary && seoExcludedAt == nil && (selected || seoIndexedAt != nil)
}

func DisplayTags(tags []string) []string {
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !strings.HasPrefix(tag, "entity:") {
			result = append(result, tag)
		}
	}
	return result
}

var (
	sourceNotePattern  = regexp.MustCompile(`（[^（）]*）`)
	xSourceNamePattern = regexp.MustCompile(`^X[:：]\s*(.*?)\s*(?:\(@[^)]*\))?\s*$`)
	xHandlePattern     = regexp.MustCompile(`@\w+`)
)

// PublicSourceName strips editor notes and keeps the display name used by public
// pages, feeds, search, and Agent responses.
func PublicSourceName(name string) string {
	bare := name
	for sourceNotePattern.MatchString(bare) {
		bare = sourceNotePattern.ReplaceAllString(bare, " ")
	}
	if matches := xSourceNamePattern.FindStringSubmatch(bare); len(matches) > 1 {
		bare = matches[1]
	}
	bare = strings.Join(strings.Fields(bare), " ")
	if bare != "" {
		return bare
	}
	if handle := xHandlePattern.FindString(name); handle != "" {
		return handle
	}
	return strings.TrimSpace(name)
}

type RepresentativeIdentity struct {
	SourceTier    string
	PublisherRole string
	OwnerEntityID string
	FactSubject   string
}

type Representative struct {
	ArticleID  string
	Identity   RepresentativeIdentity
	BodyMode   string
	Score      *float64
	TimelineAt time.Time
}

// RepresentativePriority keeps source authority separate from source tier:
// T1 wins first; otherwise a verified owner speaking for the fact subject wins.
func RepresentativePriority(identity RepresentativeIdentity, entities map[string]interests.Entity) int {
	if identity.SourceTier == "T1" {
		return 0
	}
	owner := entityIdentity(identity.OwnerEntityID, entities)
	if owner == "" {
		return 3
	}
	separators := strings.NewReplacer("，", ",", "、", ",", ";", ",", "；", ",", "+", ",", "＋", ",", "&", ",", "＆", ",", "/", ",")
	subjects := strings.Split(separators.Replace(identity.FactSubject), ",")
	if len(subjects) == 0 {
		return 3
	}
	matched := false
	for _, subject := range subjects {
		id := entityIdentity(subject, entities)
		if id == "" {
			return 3
		}
		matched = matched || id == owner
	}
	if !matched {
		return 3
	}
	switch identity.PublisherRole {
	case "organization":
		return 1
	case "person":
		return 2
	default:
		return 3
	}
}

// PickRepresentative sorts a copy and applies the ledger's public representative order.
func PickRepresentative(rows []Representative, entities map[string]interests.Entity) (Representative, bool) {
	if len(rows) == 0 {
		return Representative{}, false
	}
	ordered := append([]Representative(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if pa, pb := RepresentativePriority(a.Identity, entities), RepresentativePriority(b.Identity, entities); pa != pb {
			return pa < pb
		}
		if (a.BodyMode == "full") != (b.BodyMode == "full") {
			return a.BodyMode == "full"
		}
		as, bs := score(a.Score), score(b.Score)
		if as != bs {
			return as > bs
		}
		if !a.TimelineAt.Equal(b.TimelineAt) {
			return a.TimelineAt.Before(b.TimelineAt)
		}
		return a.ArticleID < b.ArticleID
	})
	return ordered[0], true
}

func entityIdentity(value string, entities map[string]interests.Entity) string {
	want := normalizedIdentity(value)
	if want == "" {
		return ""
	}
	matched := ""
	for id, entity := range entities {
		aliases := append([]string{id, entity.Name}, entity.Aliases...)
		aliases = append(aliases, entity.OtherNames...)
		for _, alias := range aliases {
			if normalizedIdentity(alias) != want {
				continue
			}
			if matched != "" && matched != id {
				return ""
			}
			matched = id
		}
	}
	return matched
}

func normalizedIdentity(value string) string {
	value = strings.ToLower(norm.NFKC.String(strings.TrimSpace(value)))
	return strings.Join(strings.Fields(value), "")
}

func nonBlank(value *string) bool { return value != nil && *value != "" }

func score(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
