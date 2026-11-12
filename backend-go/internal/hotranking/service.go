package hotranking

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"aihot/backend-go/internal/heatsnapshot"
	"aihot/backend-go/internal/interests"
	"golang.org/x/text/unicode/norm"
)

const (
	ruleVersion    = "heat-v1-48h-halflife24h"
	windowHours    = 48
	halfLifeHours  = 24
	minimumPeople  = 2
	maximumEntries = 10
	maximumPeople  = 40
)

var factSubjectSeparators = regexp.MustCompile(`[,，、;；+＋&＆/]`)

type HeatSource interface {
	HeatAt(context.Context, time.Time) ([]heatsnapshot.HeatRow, error)
	Participants(context.Context, int64, time.Time) ([]heatsnapshot.Participant, error)
}

type Repository interface {
	Reports(context.Context, int64, time.Time) ([]Report, error)
	Save(context.Context, time.Time, []Entry, int) (int64, error)
}

type Report struct {
	ID            string    `gorm:"column:id"`
	URL           string    `gorm:"column:url"`
	Title         string    `gorm:"column:title"`
	SourceID      string    `gorm:"column:source_id"`
	SourceName    string    `gorm:"column:source_name"`
	Selected      bool      `gorm:"column:selected"`
	Score         *float64  `gorm:"column:score"`
	TimelineAt    time.Time `gorm:"column:timeline_at"`
	BodyMode      string    `gorm:"column:body_mode"`
	FactID        int64     `gorm:"column:fact_id"`
	At            time.Time `gorm:"column:at"`
	SourceTier    string    `gorm:"column:source_tier"`
	PublisherRole *string   `gorm:"column:publisher_role"`
	OwnerEntityID *string   `gorm:"column:owner_entity_id"`
	FactSubject   *string   `gorm:"column:fact_subject"`
}

type Entry struct {
	Rank                 int         `json:"rank"`
	StoryID              int64       `json:"storyId"`
	StoryPublicID        string      `json:"storyPublicId"`
	Title                string      `json:"title"`
	Heat                 float64     `json:"heat"`
	Trend                string      `json:"trend"`
	TrendPct             *float64    `json:"trendPct"`
	Badges               []string    `json:"badges"`
	ParticipantCount     int         `json:"participantCount"`
	SourceCount          int         `json:"sourceCount"`
	SignalCount          int         `json:"signalCount"`
	ReportCount          int         `json:"reportCount"`
	SourceNames          []string    `json:"sourceNames"`
	LatestAt             string      `json:"latestAt"`
	FirstReportAt        string      `json:"firstReportAt"`
	RepresentativeItemID string      `json:"representativeItemId"`
	RepresentativeURL    string      `json:"representativeUrl"`
	RepresentativeSource string      `json:"representativeSource"`
	Participants         []HotPerson `json:"participants"`
}

type HotPerson struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Tier string `json:"tier"`
}

type Result struct {
	ID      int64 `json:"id"`
	Entries int   `json:"entries"`
}

type Service struct {
	repository Repository
	heat       HeatSource
	entities   map[string]interests.Entity
}

func NewService(repository Repository, heat HeatSource, entities map[string]interests.Entity) *Service {
	return &Service{repository: repository, heat: heat, entities: entities}
}

func (s *Service) Compute(ctx context.Context, at time.Time) (Result, error) {
	at = at.UTC().Truncate(time.Millisecond)
	rows, err := s.heat.HeatAt(ctx, at)
	if err != nil {
		return Result{}, err
	}
	candidates := make([]heatsnapshot.HeatRow, 0, len(rows))
	for _, row := range rows {
		if row.Participants >= minimumPeople && row.EditorialParticipants >= 1 {
			candidates = append(candidates, row)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Heat != candidates[j].Heat {
			return candidates[i].Heat > candidates[j].Heat
		}
		return timeValue(candidates[i].LatestAt).UnixMilli() > timeValue(candidates[j].LatestAt).UnixMilli()
	})

	entries := make([]Entry, 0, maximumEntries)
	for _, row := range candidates {
		if len(entries) == maximumEntries {
			break
		}
		reports, err := s.repository.Reports(ctx, row.StoryID, at)
		if err != nil {
			return Result{}, err
		}
		if len(reports) == 0 {
			continue
		}
		members := mostReportedFact(reports)
		representative := chooseRepresentative(members, s.entities)
		people, err := s.heat.Participants(ctx, row.StoryID, at)
		if err != nil {
			return Result{}, err
		}
		entries = append(entries, makeEntry(len(entries)+1, row, reports, representative, people, at))
	}
	id, err := s.repository.Save(ctx, at, entries, len(candidates))
	if err != nil {
		return Result{}, err
	}
	return Result{ID: id, Entries: len(entries)}, nil
}

func makeEntry(rank int, row heatsnapshot.HeatRow, reports []Report, representative Report, people []heatsnapshot.Participant, at time.Time) Entry {
	heat := heatIndex(row.Heat)
	previousAll := heatIndex(row.HeatPrevious)
	current, previous := heat, previousAll
	if row.Uncomparable > 0 {
		current, previous = heatIndex(row.HeatComparable), heatIndex(row.HeatPreviousComparable)
	}
	var trendPct *float64
	var rawTrendPct *float64
	if previous > 0 {
		value := (current - previous) / previous
		rawTrendPct = &value
		rounded := math.Round(value*1000) / 10
		trendPct = &rounded
	}
	trend := "new"
	if previousAll > 0 {
		trend = "unknown"
		if rawTrendPct != nil {
			if *rawTrendPct > 0.1 {
				trend = "up"
			} else if *rawTrendPct < -0.1 {
				trend = "down"
			} else {
				trend = "flat"
			}
		}
	}
	badges := make([]string, 0, 3)
	surge := row.Recent6Hours >= 3 && float64(row.Recent6Hours)/float64(row.Participants) >= 0.5
	first := row.FirstReportAt
	if first == nil {
		first = &reports[0].At
	}
	if surge {
		badges = append(badges, "surge")
	}
	if at.UnixMilli()-first.UnixMilli() < int64(6*time.Hour/time.Millisecond) {
		badges = append(badges, "new")
	}
	if !surge && rawTrendPct != nil && *rawTrendPct > 0.15 {
		badges = append(badges, "rising")
	}

	sort.SliceStable(people, func(i, j int) bool {
		if (people[i].Kind == "editorial") != (people[j].Kind == "editorial") {
			return people[i].Kind == "editorial"
		}
		if tierRank(people[i].Tier) != tierRank(people[j].Tier) {
			return tierRank(people[i].Tier) < tierRank(people[j].Tier)
		}
		return people[i].At.UnixMilli() > people[j].At.UnixMilli()
	})
	participants := make([]HotPerson, 0, min(len(people), maximumPeople))
	for _, person := range people {
		if len(participants) == maximumPeople {
			break
		}
		participants = append(participants, HotPerson{Name: person.Name, Kind: person.Kind, Tier: person.Tier})
	}
	reporting := make([]heatsnapshot.Participant, 0)
	for _, person := range people {
		if person.Kind == "editorial" {
			reporting = append(reporting, person)
		}
	}
	sort.SliceStable(reporting, func(i, j int) bool { return reporting[i].At.UnixMilli() > reporting[j].At.UnixMilli() })
	sourceNames := make([]string, 0, 8)
	seenNames := make(map[string]struct{}, 8)
	for _, person := range reporting {
		if _, exists := seenNames[person.Name]; exists {
			continue
		}
		seenNames[person.Name] = struct{}{}
		sourceNames = append(sourceNames, person.Name)
		if len(sourceNames) == 8 {
			break
		}
	}

	return Entry{
		Rank: rank, StoryID: row.StoryID, StoryPublicID: row.PublicID, Title: row.Title,
		Heat: heat, Trend: trend, TrendPct: trendPct, Badges: badges,
		ParticipantCount: row.Participants, SourceCount: row.EditorialParticipants, SignalCount: row.SignalParticipants,
		ReportCount: len(reports), SourceNames: sourceNames,
		LatestAt: formatTime(timeValue(row.LatestAt, at)), FirstReportAt: formatTime(*first),
		RepresentativeItemID: representative.ID, RepresentativeURL: representative.URL, RepresentativeSource: representative.SourceName,
		Participants: participants,
	}
}

func mostReportedFact(reports []Report) []Report {
	byFact := make(map[int64][]Report)
	for _, report := range reports {
		byFact[report.FactID] = append(byFact[report.FactID], report)
	}
	facts := make([]int64, 0, len(byFact))
	for id := range byFact {
		facts = append(facts, id)
	}
	sort.Slice(facts, func(i, j int) bool {
		a, b := byFact[facts[i]], byFact[facts[j]]
		if sourceCount(a) != sourceCount(b) {
			return sourceCount(a) > sourceCount(b)
		}
		if firstSeen(a).UnixMilli() != firstSeen(b).UnixMilli() {
			return firstSeen(a).Before(firstSeen(b))
		}
		return facts[i] < facts[j]
	})
	return byFact[facts[0]]
}

func chooseRepresentative(reports []Report, entities map[string]interests.Entity) Report {
	selected := make([]Report, 0, len(reports))
	for _, report := range reports {
		if report.Selected {
			selected = append(selected, report)
		}
	}
	if len(selected) > 0 {
		reports = selected
	}
	sort.Slice(reports, func(i, j int) bool {
		a, b := reports[i], reports[j]
		if representativePriority(a, entities) != representativePriority(b, entities) {
			return representativePriority(a, entities) < representativePriority(b, entities)
		}
		if (a.BodyMode == "full") != (b.BodyMode == "full") {
			return a.BodyMode == "full"
		}
		if scoreValue(a.Score) != scoreValue(b.Score) {
			return scoreValue(a.Score) > scoreValue(b.Score)
		}
		if a.TimelineAt.UnixMilli() != b.TimelineAt.UnixMilli() {
			return a.TimelineAt.UnixMilli() < b.TimelineAt.UnixMilli()
		}
		return a.ID < b.ID
	})
	return reports[0]
}

func representativePriority(report Report, entities map[string]interests.Entity) int {
	if report.SourceTier == "T1" {
		return 0
	}
	if report.OwnerEntityID == nil || report.FactSubject == nil {
		return 3
	}
	owner := entityIdentity(*report.OwnerEntityID, entities)
	if owner == "" {
		return 3
	}
	matched := false
	for _, subject := range factSubjectSeparators.Split(*report.FactSubject, -1) {
		identity := entityIdentity(subject, entities)
		if identity == "" {
			return 3
		}
		matched = matched || identity == owner
	}
	if !matched || report.PublisherRole == nil {
		return 3
	}
	switch *report.PublisherRole {
	case "organization":
		return 1
	case "person":
		return 2
	default:
		return 3
	}
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

func sourceCount(reports []Report) int {
	seen := make(map[string]struct{}, len(reports))
	for _, report := range reports {
		seen[report.SourceID] = struct{}{}
	}
	return len(seen)
}

func firstSeen(reports []Report) time.Time {
	first := reports[0].TimelineAt.Truncate(time.Millisecond)
	for _, report := range reports[1:] {
		candidate := report.TimelineAt.Truncate(time.Millisecond)
		if candidate.Before(first) {
			first = candidate
		}
	}
	return first
}

func scoreValue(score *float64) float64 {
	if score == nil {
		return 0
	}
	return *score
}

func tierRank(tier string) int {
	switch tier {
	case "T1":
		return 0
	case "T1_5":
		return 1
	case "T2":
		return 2
	default:
		return 3
	}
}

func timeValue(value *time.Time, fallback ...time.Time) time.Time {
	if value != nil {
		return *value
	}
	if len(fallback) > 0 {
		return fallback[0]
	}
	return time.Time{}
}

func heatIndex(value float64) float64 { return math.Round(value*100) / 10 }

func formatTime(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}
