package publiccontent

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/topics"
)

type SiteStoryRepository interface {
	SiteStory(context.Context, int64, time.Time, []topics.Definition) (SiteStoryData, error)
}

type SiteStoryData struct {
	Story         v1StoryDatabase
	Reports       []SiteStoryReport
	DigestReports []v1StoryDigestRecord
	Facts         []SiteStoryFact
	Related       []v1StoryRelatedRecord
	Summary       *string
	Latest        *SiteStoryLatest
	WhyHot        SiteStoryWhyHot
	Heat          []SiteStoryHeatPoint
	Signals       []SiteStorySignal
}

type SiteStoryReport struct {
	ID            string
	Title         string
	Summary       *string
	URL           string
	Selected      bool
	At            time.Time
	SourceID      string
	SourceName    string
	FirstParty    bool
	FactID        int64
	Role          string
	SourceTier    string
	PublisherRole *string
	OwnerEntityID *string
	FactSubject   *string
	BodyMode      string
	Score         *float64
	TimelineAt    time.Time
	TopicSlugs    []string
}

type SiteStoryFact struct {
	ID       int64
	PublicID string
	Title    string
}

type SiteStoryLatest struct {
	ID    string
	Title string
	At    time.Time
}

type SiteStoryWhyHot struct {
	Participants48h     int
	NewParticipants6h   int
	RecentReports24h    int
	ObservationComplete bool
	Rank                *int
}

type SiteStoryHeatPoint struct {
	Hour         time.Time
	Heat         float64
	Participants int
}

type SiteStorySignal struct {
	ParticipantKey string
	ObservedAt     time.Time
	SourceSince    time.Time
}

type SiteStoryReportView struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Summary     *string         `json:"summary"`
	Source      SiteStorySource `json:"source"`
	PublishedAt string          `json:"publishedAt"`
	Selected    bool            `json:"selected"`
}

type SiteStorySource struct {
	Name       string `json:"name"`
	FirstParty bool   `json:"firstParty"`
}

type SiteStoryFactView struct {
	FactID         string              `json:"factId"`
	Title          string              `json:"title"`
	FirstReportAt  string              `json:"firstReportAt"`
	ReportCount    int                 `json:"reportCount"`
	Representative SiteStoryReportView `json:"representative"`
}

type SiteStoryHeat struct {
	Hour         string  `json:"hour"`
	Heat         float64 `json:"heat"`
	Participants int     `json:"participants"`
}

type SiteStoryWhyHotView struct {
	Participants48h     int  `json:"participants48h"`
	NewParticipants6h   int  `json:"newParticipants6h"`
	RecentReports24h    int  `json:"recentReports24h"`
	ObservationComplete bool `json:"observationComplete"`
	Rank                *int `json:"rank"`
}

type SiteStoryRelated struct {
	PublicID string `json:"publicId"`
	Title    string `json:"title"`
	Relation string `json:"relation"`
}

type SiteStoryDetail struct {
	PublicID        string                `json:"publicId"`
	Title           string                `json:"title"`
	Status          string                `json:"status"`
	ReportCount     int                   `json:"reportCount"`
	SourceCount     int                   `json:"sourceCount"`
	FirstReportAt   *string               `json:"firstReportAt"`
	LatestAt        *string               `json:"latestAt"`
	Digest          *string               `json:"digest"`
	DigestUpdatedAt *string               `json:"digestUpdatedAt"`
	Summary         *string               `json:"summary"`
	Excerpt         *SiteStoryExcerpt     `json:"excerpt"`
	Latest          *string               `json:"latest"`
	LatestReport    *SiteStoryLatestRef   `json:"latestReport"`
	WhyHot          SiteStoryWhyHotView   `json:"whyHot"`
	Developments    []SiteStoryFactView   `json:"developments"`
	OfficialReports []SiteStoryReportView `json:"officialReports"`
	Timeline        []SiteStoryReportView `json:"timeline"`
	Heat            []SiteStoryHeat       `json:"heat"`
	Related         []SiteStoryRelated    `json:"related"`
	Topics          []SiteItemTopic       `json:"topics"`
}

type SiteStoryExcerpt struct {
	Text       string `json:"text"`
	SourceName string `json:"sourceName"`
}

type SiteStoryLatestRef struct {
	ID string `json:"id"`
}

type SiteStoryLookup struct {
	Kind   string
	Target string
	Story  *SiteStoryDetail
}

func (s *Service) SiteStory(ctx context.Context, publicID string, now time.Time) (SiteStoryLookup, error) {
	lookup, err := s.ResolveV1Story(ctx, publicID)
	if err != nil || lookup.Kind != "found" {
		return SiteStoryLookup{Kind: lookup.Kind, Target: lookup.Target}, err
	}
	repository, ok := s.repository.(SiteStoryRepository)
	if !ok {
		return SiteStoryLookup{}, errors.New("site story repository is unavailable")
	}
	data, err := repository.SiteStory(ctx, lookup.ID, now, s.topics)
	if err != nil {
		return SiteStoryLookup{}, err
	}
	story, found := buildSiteStory(data, s.entities, s.topics, now)
	if !found {
		return SiteStoryLookup{Kind: "not_found"}, nil
	}
	return SiteStoryLookup{Kind: "found", Story: &story}, nil
}

func buildSiteStory(data SiteStoryData, entities map[string]interests.Entity, definitions []topics.Definition, now time.Time) (SiteStoryDetail, bool) {
	if len(data.Reports) == 0 {
		return SiteStoryDetail{}, false
	}
	reports := append([]SiteStoryReport(nil), data.Reports...)
	sort.SliceStable(reports, func(i, j int) bool {
		if !reports[i].At.Equal(reports[j].At) {
			return reports[i].At.After(reports[j].At)
		}
		return reports[i].ID < reports[j].ID
	})
	primary := make([]SiteStoryReport, 0, len(reports))
	sources := make(map[string]struct{}, len(reports))
	byFact := make(map[int64][]SiteStoryReport)
	for _, report := range reports {
		sources[report.SourceID] = struct{}{}
		if report.Role != "mention" {
			primary = append(primary, report)
			byFact[report.FactID] = append(byFact[report.FactID], report)
		}
	}
	if len(primary) == 0 {
		return SiteStoryDetail{}, false
	}
	digest, digestUpdatedAt := currentStoryDigest(data.Story, data.DigestReports)
	latest := SiteStoryLatest{ID: primary[0].ID, Title: primary[0].Title, At: primary[0].At}
	if data.Latest != nil {
		latest = *data.Latest
	}
	status := "active"
	if age := now.Sub(latest.At); age >= 72*time.Hour {
		status = "settled"
	} else if age >= 24*time.Hour {
		status = "watching"
	}
	firstAt := primary[len(primary)-1].At
	story := SiteStoryDetail{
		PublicID: data.Story.PublicID, Title: data.Story.Title, Status: status,
		ReportCount: len(reports), SourceCount: len(sources), FirstReportAt: stringPointer(formatV1Time(firstAt)), LatestAt: stringPointer(formatV1Time(latest.At)),
		Digest: digest, DigestUpdatedAt: digestUpdatedAt, Summary: data.Summary, Latest: stringPointer(latest.Title), LatestReport: &SiteStoryLatestRef{ID: latest.ID},
		WhyHot:       SiteStoryWhyHotView{Participants48h: data.WhyHot.Participants48h, NewParticipants6h: data.WhyHot.NewParticipants6h, RecentReports24h: data.WhyHot.RecentReports24h, ObservationComplete: data.WhyHot.ObservationComplete, Rank: data.WhyHot.Rank},
		Developments: []SiteStoryFactView{}, OfficialReports: []SiteStoryReportView{}, Timeline: []SiteStoryReportView{},
		Heat: []SiteStoryHeat{}, Related: []SiteStoryRelated{}, Topics: []SiteItemTopic{},
	}
	for _, fact := range data.Facts {
		members := byFact[fact.ID]
		if len(members) == 0 {
			continue
		}
		candidates := append([]SiteStoryReport(nil), members...)
		selected := make([]SiteStoryReport, 0, len(members))
		for _, member := range members {
			if member.Selected {
				selected = append(selected, member)
			}
		}
		if len(selected) > 0 {
			candidates = selected
		}
		timelineMembers := make([]SiteTimelineMember, 0, len(candidates))
		for _, candidate := range candidates {
			chosen := candidate.Selected
			timelineMembers = append(timelineMembers, SiteTimelineMember{
				FactID: fact.ID, ArticleID: candidate.ID, SourceTier: candidate.SourceTier,
				PublisherRole: candidate.PublisherRole, OwnerEntityID: candidate.OwnerEntityID, FactSubject: candidate.FactSubject,
				Item: topics.PageItemRow{ID: candidate.ID, BodyMode: candidate.BodyMode, Score: candidate.Score, TimelineAt: candidate.TimelineAt, Selected: &chosen},
			})
		}
		sortTimelineMembers(timelineMembers, entities)
		var representative SiteStoryReport
		for _, candidate := range candidates {
			if candidate.ID == timelineMembers[0].ArticleID {
				representative = candidate
				break
			}
		}
		first := members[0].At
		for _, member := range members[1:] {
			if member.At.Before(first) {
				first = member.At
			}
		}
		story.Developments = append(story.Developments, SiteStoryFactView{
			FactID: fact.PublicID, Title: fact.Title, FirstReportAt: formatV1Time(first), ReportCount: len(members), Representative: siteStoryReportView(representative),
		})
	}
	sort.SliceStable(story.Developments, func(i, j int) bool {
		if story.Developments[i].FirstReportAt != story.Developments[j].FirstReportAt {
			return story.Developments[i].FirstReportAt > story.Developments[j].FirstReportAt
		}
		return story.Developments[i].FactID < story.Developments[j].FactID
	})
	if digest == nil && data.Summary == nil && len(story.Developments) > 0 {
		origin := story.Developments[len(story.Developments)-1].Representative
		if origin.Summary != nil && *origin.Summary != "" {
			story.Excerpt = &SiteStoryExcerpt{Text: *origin.Summary, SourceName: origin.Source.Name}
		}
	}
	for _, report := range reports {
		view := siteStoryReportView(report)
		if report.Role != "mention" && report.FirstParty && len(story.OfficialReports) < 12 {
			story.OfficialReports = append(story.OfficialReports, view)
		}
		if len(story.Timeline) < 100 {
			story.Timeline = append(story.Timeline, view)
		}
	}
	for _, point := range siteStoryHeatSeries(data.Heat, data.Signals) {
		story.Heat = append(story.Heat, SiteStoryHeat{Hour: formatV1Time(point.Hour), Heat: point.Heat, Participants: point.Participants})
	}
	for _, related := range data.Related {
		story.Related = append(story.Related, SiteStoryRelated{PublicID: related.PublicID, Title: related.Title, Relation: related.Relation})
	}
	topicCounts := make(map[string]int)
	for _, report := range reports {
		if report.Role == "mention" {
			continue
		}
		for _, slug := range report.TopicSlugs {
			topicCounts[slug]++
		}
	}
	orderedTopics := append([]topics.Definition(nil), definitions...)
	sort.SliceStable(orderedTopics, func(i, j int) bool {
		if topicCounts[orderedTopics[i].Slug] != topicCounts[orderedTopics[j].Slug] {
			return topicCounts[orderedTopics[i].Slug] > topicCounts[orderedTopics[j].Slug]
		}
		return orderedTopics[i].Position < orderedTopics[j].Position
	})
	for _, definition := range orderedTopics {
		if topicCounts[definition.Slug] == 0 {
			continue
		}
		story.Topics = append(story.Topics, SiteItemTopic{Slug: definition.Slug, Name: definition.Name})
		if len(story.Topics) == 6 {
			break
		}
	}
	return story, true
}

func siteStoryReportView(report SiteStoryReport) SiteStoryReportView {
	return SiteStoryReportView{ID: report.ID, Title: report.Title, Summary: report.Summary, Source: SiteStorySource{Name: PublicSourceName(report.SourceName), FirstParty: report.FirstParty}, PublishedAt: formatV1Time(report.At), Selected: report.Selected}
}

func siteStoryHeatSeries(hours []SiteStoryHeatPoint, signals []SiteStorySignal) []SiteStoryHeatPoint {
	if len(hours) == 0 {
		return []SiteStoryHeatPoint{}
	}
	since := hours[0].Hour.Add(-48 * time.Hour)
	late := make(map[string]struct{})
	for _, signal := range signals {
		if signal.SourceSince.After(since) {
			late[signal.ParticipantKey] = struct{}{}
		}
	}
	observations := make(map[string][]time.Time)
	for _, signal := range signals {
		if _, excluded := late[signal.ParticipantKey]; !excluded {
			observations[signal.ParticipantKey] = append(observations[signal.ParticipantKey], signal.ObservedAt)
		}
	}
	result := make([]SiteStoryHeatPoint, 0, len(hours))
	for _, hour := range hours {
		point := SiteStoryHeatPoint{Hour: hour.Hour}
		for _, observed := range observations {
			var latest *time.Time
			for index := range observed {
				value := observed[index]
				if value.After(hour.Hour) || !value.After(hour.Hour.Add(-48*time.Hour)) {
					continue
				}
				if latest == nil || value.After(*latest) {
					latest = &value
				}
			}
			if latest == nil {
				continue
			}
			point.Heat += math.Pow(0.5, hour.Hour.Sub(*latest).Hours()/24)
			point.Participants++
		}
		result = append(result, point)
	}
	return result
}
