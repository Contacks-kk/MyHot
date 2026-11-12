package reports

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"aihot/backend-go/internal/interests"
	"golang.org/x/text/unicode/norm"
)

var subjectSeparators = regexp.MustCompile(`[,，、;；+＋&＆/]`)

const (
	mainEntries       = 12
	flashEntries      = 10
	perSource         = 2
	followUpSources   = 4
	introEvents       = 3
	weeklyEventLimit  = 20
	monthlyEventLimit = 30
)

type Entry struct {
	ItemID        string         `json:"itemId"`
	FactID        *string        `json:"factId"`
	StoryPublicID *string        `json:"storyPublicId"`
	Title         string         `json:"title"`
	Summary       string         `json:"summary"`
	SourceName    string         `json:"sourceName"`
	SourceURL     string         `json:"sourceUrl"`
	SourceID      string         `json:"sourceId"`
	FirstParty    bool           `json:"firstParty"`
	Role          string         `json:"role"`
	Score         *int           `json:"score"`
	PublishedAt   string         `json:"publishedAt"`
	Sources       int            `json:"sources,omitempty"`
	Related       []RelatedEntry `json:"related,omitempty"`
	FollowUp      string         `json:"followUp,omitempty"`
	FillIn        bool           `json:"fillIn,omitempty"`
}

type RelatedEntry struct {
	ItemID        string  `json:"itemId"`
	FactID        *string `json:"factId"`
	StoryPublicID *string `json:"storyPublicId"`
	Title         string  `json:"title"`
	SourceName    string  `json:"sourceName"`
	SourceURL     string  `json:"sourceUrl"`
	SourceID      string  `json:"sourceId"`
}

type Candidate struct {
	Entry
	Category *string `json:"category"`
	FactKey  string  `json:"-"`
}

type DailyMemory struct {
	Keys    map[string]struct{}
	Stories map[int64]PreviousStory
}

type PreviousStory struct {
	Key   string
	Title string
}

type EditionEntry struct {
	Entry      Entry
	Category   *string
	Tags       []string
	StoryID    *int64
	Mentions   map[int64]struct{}
	SourceIDs  map[string]struct{}
	Authority  int
	Importance float64
	Previous   *PreviousStory
}

type Edition struct {
	Entries []EditionEntry
	Stats   map[string]int
}

type Arranged struct {
	Main    []EditionEntry
	Flashes []EditionEntry
	Stats   map[string]int
}

type rawFact struct {
	key      string
	rows     []ReportRow
	selected bool
	sources  []string
	at       time.Time
}

func reportEntry(row ReportRow, entities map[string]interests.Entity) Entry {
	role := "媒体"
	if row.SourceKind == "x_search" {
		role = "X·KOL"
	} else if row.SourceKind == "mp_account" {
		role = "公众号"
	}
	if priority(row, entities) < 3 {
		if row.SourceKind == "x_search" {
			role = "X·官方"
		} else {
			role = "官方"
		}
	}
	summary := ""
	if row.Summary != nil {
		summary = *row.Summary
	}
	return Entry{ItemID: row.ID, FactID: row.FactPublicID, StoryPublicID: row.StoryPublicID, Title: row.Title, Summary: summary,
		SourceName: row.SourceName, SourceURL: row.URL, SourceID: row.SourceID, FirstParty: row.FirstParty, Role: role,
		Score: row.Score, PublishedAt: row.TimelineAt.UTC().Format("2006-01-02T15:04:05.000Z")}
}

func relatedEntry(entry Entry) RelatedEntry {
	return RelatedEntry{ItemID: entry.ItemID, FactID: entry.FactID, StoryPublicID: entry.StoryPublicID, Title: entry.Title,
		SourceName: entry.SourceName, SourceURL: entry.SourceURL, SourceID: entry.SourceID}
}

func factKey(row ReportRow) string {
	if row.FactPublicID != nil {
		return *row.FactPublicID
	}
	return "a:" + row.ID
}

func priority(row ReportRow, entities map[string]interests.Entity) int {
	if row.SourceTier == "T1" {
		return 0
	}
	owner := entityIdentity(row.OwnerEntityID, entities)
	if owner == "" || row.FactSubject == nil {
		return 3
	}
	matched := false
	for _, value := range subjectSeparators.Split(*row.FactSubject, -1) {
		identity := entityIdentity(&value, entities)
		if identity == "" {
			return 3
		}
		matched = matched || identity == owner
	}
	if !matched || row.PublisherRole == nil {
		return 3
	}
	switch *row.PublisherRole {
	case "organization":
		return 1
	case "person":
		return 2
	default:
		return 3
	}
}

func entityIdentity(value *string, entities map[string]interests.Entity) string {
	if value == nil {
		return ""
	}
	want := normalizedIdentity(*value)
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
	return strings.Join(strings.Fields(strings.ToLower(norm.NFKC.String(value))), "")
}

func pickRepresentative(rows []ReportRow, entities map[string]interests.Entity) ReportRow {
	selected := make([]ReportRow, 0, len(rows))
	for _, row := range rows {
		if row.Selected {
			selected = append(selected, row)
		}
	}
	if len(selected) > 0 {
		rows = selected
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if priority(a, entities) != priority(b, entities) {
			return priority(a, entities) < priority(b, entities)
		}
		if (a.BodyMode == "full") != (b.BodyMode == "full") {
			return a.BodyMode == "full"
		}
		if score(a.Score) != score(b.Score) {
			return score(a.Score) > score(b.Score)
		}
		if !a.TimelineAt.Equal(b.TimelineAt) {
			return a.TimelineAt.Before(b.TimelineAt)
		}
		return a.ID < b.ID
	})
	return rows[0]
}

func score(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func importance(value int, participants int, official, followUp bool) float64 {
	result := float64(value) + 5*math.Log2(1+float64(participants))
	if official {
		result += 5
	}
	if followUp {
		result -= 6
	}
	return result
}

func sectionOf(category *string, sectionByCategory map[string]string, sectionOrder []string) string {
	if category != nil {
		if section := sectionByCategory[*category]; section != "" {
			return section
		}
	}
	if section := sectionByCategory["industry"]; section != "" {
		return section
	}
	return sectionOrder[len(sectionOrder)-1]
}

func arrangeDaily(entries []EditionEntry, sectionByCategory map[string]string, categories []Category) Arranged {
	byStory := make(map[int64]EditionEntry)
	for _, entry := range entries {
		if entry.StoryID != nil {
			byStory[*entry.StoryID] = entry
		}
	}
	under := make(map[string][]EditionEntry)
	for _, entry := range entries {
		named := make([]EditionEntry, 0)
		for id := range entry.Mentions {
			if host, ok := byStory[id]; ok {
				named = append(named, host)
			}
		}
		if len(named) == 0 {
			continue
		}
		host := named[0]
		for _, candidate := range named[1:] {
			if candidate.Importance > host.Importance {
				host = candidate
			}
		}
		under[host.Entry.ItemID] = append(under[host.Entry.ItemID], entry)
	}
	folded := make(map[string]struct{})
	for _, reports := range under {
		for _, entry := range reports {
			folded[entry.Entry.ItemID] = struct{}{}
		}
	}
	live := make([]EditionEntry, 0, len(entries))
	for _, entry := range entries {
		if _, isFolded := folded[entry.Entry.ItemID]; isFolded {
			continue
		}
		roundups := under[entry.Entry.ItemID]
		if len(roundups) > 0 {
			copyEntry := entry
			copyEntry.SourceIDs = cloneSet(entry.SourceIDs)
			related := append([]RelatedEntry{}, entry.Entry.Related...)
			for _, roundup := range roundups {
				for sourceID := range roundup.SourceIDs {
					copyEntry.SourceIDs[sourceID] = struct{}{}
				}
				related = append(related, relatedEntry(roundup.Entry))
				related = append(related, roundup.Entry.Related...)
			}
			copyEntry.Entry.Sources = len(copyEntry.SourceIDs)
			copyEntry.Entry.Related = related
			entry = copyEntry
		}
		live = append(live, entry)
	}
	commentary := make(map[string]bool)
	for _, category := range categories {
		if category.Commentary {
			commentary[category.Key] = true
		}
	}
	earned := func(entry EditionEntry) bool {
		return entry.Previous == nil || (entry.Authority < 3 && !commentary[stringValue(entry.Category)]) || entry.Entry.Sources >= followUpSources
	}
	hasEarned := false
	for _, entry := range live {
		hasEarned = hasEarned || earned(entry)
	}
	main := make([]EditionEntry, 0, mainEntries)
	rest := make([]EditionEntry, 0)
	per := make(map[string]int)
	followUpsAsFlashes := 0
	for _, entry := range live {
		full := earned(entry)
		if !hasEarned {
			full = true
		}
		if !full {
			followUpsAsFlashes++
		}
		if len(main) < mainEntries && per[entry.Entry.SourceID] < perSource && full {
			main = append(main, entry)
			per[entry.Entry.SourceID]++
		} else {
			rest = append(rest, entry)
		}
	}
	flashCount := min(len(rest), flashEntries)
	return Arranged{Main: main, Flashes: append([]EditionEntry{}, rest[:flashCount]...), Stats: map[string]int{
		"roundupsFolded": len(folded), "followUpsAsFlashes": followUpsAsFlashes, "left": len(rest) - flashCount,
	}}
}

func cloneSet(values map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(values))
	for value := range values {
		cloned[value] = struct{}{}
	}
	return cloned
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validateSections(categories []Category) (map[string]string, []string, error) {
	byCategory := make(map[string]string, len(categories))
	sections := make([]string, 0)
	seen := make(map[string]struct{})
	for _, category := range categories {
		if strings.TrimSpace(category.Section) == "" {
			return nil, nil, fmt.Errorf("category %s has no report section", category.Key)
		}
		byCategory[category.Key] = category.Section
		if _, ok := seen[category.Section]; !ok {
			seen[category.Section] = struct{}{}
			sections = append(sections, category.Section)
		}
	}
	if len(sections) == 0 {
		return nil, nil, fmt.Errorf("report sections are not configured")
	}
	return byCategory, sections, nil
}
