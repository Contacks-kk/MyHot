package publiccontent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"aihot/backend-go/internal/mediaurl"
)

type SiteReportConfig struct {
	SiteName         string
	SiteURL          string
	Subject          string
	Measure          string
	Noun             string
	ImageProxySecret string
}

type SiteReportCitation struct {
	ItemID           *string              `json:"itemId"`
	Title            string               `json:"title"`
	Summary          *string              `json:"summary"`
	SourceName       string               `json:"sourceName"`
	SourceURL        string               `json:"sourceUrl"`
	SourceIconURL    *string              `json:"sourceIconUrl"`
	SourceIconSrcSet *string              `json:"sourceIconSrcSet,omitempty"`
	FirstParty       bool                 `json:"firstParty"`
	PublishedAt      *string              `json:"publishedAt"`
	Available        bool                 `json:"available"`
	OtherSources     *int                 `json:"otherSources,omitempty"`
	Related          []SiteReportCitation `json:"related,omitempty"`
	FollowUp         *string              `json:"followUp,omitempty"`
}

type SiteReportSection struct {
	Label   string               `json:"label"`
	Summary *string              `json:"summary"`
	Items   []SiteReportCitation `json:"items"`
}

type SiteReportCover struct {
	URL     string   `json:"url"`
	SrcSet  *string  `json:"srcSet,omitempty"`
	Width   *float64 `json:"width"`
	Height  *float64 `json:"height"`
	Caption *string  `json:"caption"`
}

type SiteReportDetail struct {
	Kind           string               `json:"kind"`
	Key            string               `json:"key"`
	IssueNumber    int                  `json:"issueNumber"`
	Title          string               `json:"title"`
	GeneratedAt    string               `json:"generatedAt"`
	Lead           *V1DailyLead         `json:"lead"`
	LeadItemID     *string              `json:"leadItemId"`
	Overview       *string              `json:"overview"`
	Highlights     []SiteReportCitation `json:"highlights"`
	Sections       []SiteReportSection  `json:"sections"`
	Flashes        []SiteReportCitation `json:"flashes"`
	Cover          *SiteReportCover     `json:"cover"`
	Metrics        map[string]any       `json:"metrics"`
	ReadingMinutes int                  `json:"readingMinutes"`
	Previous       *string              `json:"prev"`
	Next           *string              `json:"next"`
}

type SiteReportLatestPageResponse struct {
	Index  []SiteReportNavigationEntry `json:"index"`
	Report *SiteReportDetail           `json:"report"`
}

func (s *Service) SiteReport(ctx context.Context, kind, key string, now time.Time, config SiteReportConfig) (*SiteReportDetail, error) {
	if kind != "daily" && kind != "weekly" && kind != "monthly" {
		return nil, fmt.Errorf("unsupported report kind %q", kind)
	}
	repository, ok := s.repository.(SiteReportDetailRepository)
	if !ok {
		return nil, fmt.Errorf("site report repository is unavailable")
	}
	report, err := repository.V1Report(ctx, kind, key, false)
	if err != nil || report == nil {
		return nil, err
	}
	issueNumber, err := repository.SiteReportIssueNumber(ctx, kind, key)
	if err != nil {
		return nil, err
	}
	previous, next, err := repository.SiteReportNeighbors(ctx, kind, key)
	if err != nil {
		return nil, err
	}
	allRaw := dailyItems(report.Content)
	if kind != "daily" {
		allRaw = periodicItems(report.Content)
	}
	allRaw = append(append([]map[string]any(nil), allRaw...), reportArray(report.Content["flashes"])...)
	availability, err := loadV1ReportAvailability(ctx, repository, reportItemIDs(allRaw, true), now)
	if err != nil {
		return nil, err
	}
	entryKind := kind
	if entryKind != "daily" {
		entryKind = "periodic"
	}
	sections := make([]SiteReportSection, 0)
	if kind == "daily" {
		for _, raw := range reportArray(report.Content["sections"]) {
			section := SiteReportSection{Label: reportString(raw["label"]), Items: make([]SiteReportCitation, 0)}
			for _, item := range reportArray(raw["items"]) {
				citation, err := siteReportCitation(item, availability, config, now, true)
				if err != nil {
					return nil, err
				}
				section.Items = append(section.Items, citation)
			}
			sections = append(sections, section)
		}
	} else {
		for _, raw := range reportArray(report.Content["themes"]) {
			section := SiteReportSection{Label: reportString(raw["heading"]), Summary: reportSectionSummary(raw, availability), Items: make([]SiteReportCitation, 0)}
			for _, item := range reportArray(raw["storyRefs"]) {
				citation, err := siteReportCitation(item, availability, config, now, true)
				if err != nil {
					return nil, err
				}
				section.Items = append(section.Items, citation)
			}
			sections = append(sections, section)
		}
	}
	all := make([]SiteReportCitation, 0)
	for _, section := range sections {
		all = append(all, section.Items...)
	}
	highlightIDs := reportStringArray(report.Content["highlights"])
	highlights := make([]SiteReportCitation, 0)
	if len(highlightIDs) > 0 {
		for _, id := range highlightIDs {
			for _, citation := range all {
				if citation.ItemID != nil && *citation.ItemID == id {
					highlights = append(highlights, citation)
					break
				}
			}
		}
	} else {
		highlights = append(highlights, all[:min(3, len(all))]...)
	}
	flashes := make([]SiteReportCitation, 0)
	for _, raw := range reportArray(report.Content["flashes"]) {
		citation, err := siteReportCitation(raw, availability, config, now, true)
		if err != nil {
			return nil, err
		}
		flashes = append(flashes, citation)
	}
	gone := unavailableReportIDs(availability)
	hasCitedLead := hasWrittenReportLead(report.Content, entryKind, config.SiteName)
	var named *reportLead
	if hasCitedLead {
		named = issueLead(report.Content, entryKind, gone, config.SiteName)
	}
	var overview *string
	if kind == "daily" {
		overview = reportOptionalString(report.Content["overview"])
	} else {
		overview = periodOverview(report.Content, kind, gone, config.SiteName, config.Measure, config.Noun)
	}
	leadItem := siteLeadItem(kind, report.Content, highlights, all, named, config.SiteName)
	var lead *V1DailyLead
	if hasCitedLead {
		if named != nil {
			paragraph := ""
			if kind == "daily" && named.LeadParagraph != nil {
				paragraph = *named.LeadParagraph
			} else if kind == "daily" && named.ItemID != "" {
				if replacement, ok := availability[named.ItemID]; ok && replacement.Available && replacement.Summary != nil {
					paragraph = *replacement.Summary
				}
			} else if kind != "daily" && overview != nil {
				paragraph = *overview
			}
			lead = &V1DailyLead{Title: named.Title, LeadParagraph: paragraph}
		}
	} else if kind == "daily" {
		if raw := reportObject(report.Content["lead"]); len(raw) > 0 {
			lead = &V1DailyLead{Title: reportString(raw["title"]), LeadParagraph: reportString(raw["leadParagraph"])}
		}
	} else if raw := reportObject(report.Content["lead"]); len(raw) > 0 {
		paragraph := ""
		if overview != nil {
			paragraph = *overview
		}
		lead = &V1DailyLead{Title: reportString(raw["title"]), LeadParagraph: paragraph}
	} else if headline := reportLeadTitle(report.Content, kind, config.SiteName); headline != "" {
		paragraph := ""
		if overview != nil {
			paragraph = *overview
		}
		lead = &V1DailyLead{Title: headline, LeadParagraph: paragraph}
	}
	var cover *SiteReportCover
	if leadItem != nil && leadItem.Available && leadItem.ItemID != nil {
		picture, err := repository.SiteReportCover(ctx, *leadItem.ItemID, now)
		if err != nil {
			return nil, err
		}
		if picture != nil {
			image, err := mediaurl.ProxyURL(stringPointer(picture.URL), "full", now, 48*time.Hour, config.ImageProxySecret)
			if err != nil {
				return nil, err
			}
			if image != nil {
				srcSet, err := mediaurl.ProxyURLSet(stringPointer(picture.URL), []string{"image-720", "image-1200", "image-1600"}, []int{720, 1200, 1600}, now, 48*time.Hour, config.ImageProxySecret)
				if err != nil {
					return nil, err
				}
				caption := (*string)(nil)
				if kind != "daily" && reportString(report.Content["leadItemId"]) == "" {
					caption = stringPointer(leadItem.Title)
				}
				cover = &SiteReportCover{URL: *image, SrcSet: srcSet, Width: picture.Width, Height: picture.Height, Caption: caption}
			}
		}
	}
	metrics := make(map[string]any)
	for key, value := range reportObject(report.Content["metrics"]) {
		metrics[key] = value
	}
	if _, exists := metrics["firstPartyEvents"]; exists {
		count := 0
		for _, citation := range all {
			if citation.FirstParty {
				count++
			}
		}
		metrics["firstPartyEvents"] = count
	}
	if _, exists := metrics["sourcesCount"]; exists {
		metrics["sourcesCount"] = reportSourceCount(report.Content, kind, availability)
	}
	readText := reportString(reportObject(report.Content["lead"])["leadParagraph"]) + reportString(report.Content["overview"])
	for _, citation := range all {
		readText += citation.Title + reportStringValue(citation.Summary)
		for _, related := range citation.Related {
			readText += related.Title
		}
	}
	minutes := max(1, int(math.Round(float64(utf8.RuneCountInString(readText))/450)))
	title := ""
	if kind == "daily" {
		title = reportWithSubject("日报", config.Subject) + " · " + report.Key
	} else {
		headline := reportLeadTitle(report.Content, kind, config.SiteName)
		var titleValue any = report.Content["title"]
		if value, ok := titleValue.(string); hasCitedLead && ok && value == headline {
			titleValue = nil
			if named != nil {
				titleValue = named.Title
			}
		}
		if titleValue == nil {
			label := "周报"
			if kind == "monthly" {
				label = "月报"
			}
			title = strings.TrimSpace(config.SiteName) + " " + label + " · " + report.Key
		} else {
			title = reportJSString(titleValue)
		}
	}
	var leadItemID *string
	if (kind == "daily" || reportString(report.Content["leadItemId"]) != "") && leadItem != nil && leadItem.Available {
		leadItemID = leadItem.ItemID
	}
	return &SiteReportDetail{
		Kind: kind, Key: report.Key, IssueNumber: issueNumber, Title: title,
		GeneratedAt: formatV1Time(report.GeneratedAt), Lead: lead, LeadItemID: leadItemID, Overview: overview,
		Highlights: highlights, Sections: sections, Flashes: flashes, Cover: cover, Metrics: metrics,
		ReadingMinutes: minutes, Previous: previous, Next: next,
	}, nil
}

func (s *Service) SiteReportLatestPage(ctx context.Context, kind string, now time.Time, config SiteReportConfig) (SiteReportLatestPageResponse, error) {
	index, err := s.SiteReportIndex(ctx, kind, now, config.SiteName)
	if err != nil {
		return SiteReportLatestPageResponse{}, err
	}
	var latest *SiteReportDetail
	if len(index.Items) > 0 {
		latest, err = s.SiteReport(ctx, kind, index.Items[0].Key, now, config)
		if err != nil {
			return SiteReportLatestPageResponse{}, err
		}
	}
	key := ""
	if latest != nil {
		key = latest.Key
	}
	navigation, err := s.SiteReportNavigation(ctx, kind, key, now, config.SiteName)
	if err != nil {
		return SiteReportLatestPageResponse{}, err
	}
	return SiteReportLatestPageResponse{Index: navigation.Items, Report: latest}, nil
}

func siteReportCitation(raw map[string]any, availability map[string]V1ReportAvailabilityRecord, config SiteReportConfig, now time.Time, includeExtras bool) (SiteReportCitation, error) {
	id := reportString(raw["itemId"])
	value, hasCurrent := availability[id]
	var itemID *string
	if id != "" {
		itemID = stringPointer(id)
	}
	citation := SiteReportCitation{ItemID: itemID, Title: reportString(raw["title"]), Available: reportAvailable(raw, availability)}
	if !citation.Available {
		return citation, nil
	}
	metadata := reportCitationMetadata(raw, availability)
	citation.Summary = reportOptionalString(metadata["summary"])
	citation.SourceName = PublicSourceName(reportStringValue(metadata["sourceName"]))
	citation.SourceURL = reportStringValue(metadata["sourceUrl"])
	citation.PublishedAt = reportOptionalString(metadata["publishedAt"])
	citation.FirstParty = hasCurrent && value.FirstParty
	if hasCurrent && value.SourceIcon != nil {
		image, err := mediaurl.ProxyURL(value.SourceIcon, "avatar", now, 48*time.Hour, config.ImageProxySecret)
		if err != nil {
			return SiteReportCitation{}, err
		}
		citation.SourceIconURL = image
		set, err := mediaurl.ProxyURLSet(value.SourceIcon, []string{"avatar-48", "avatar-96"}, []int{48, 96}, now, 48*time.Hour, config.ImageProxySecret)
		if err != nil {
			return SiteReportCitation{}, err
		}
		citation.SourceIconSrcSet = set
	}
	if includeExtras {
		if count := reportNumber(raw["sources"]); count > 1 {
			other := count - 1
			citation.OtherSources = &other
		}
		related := reportArray(raw["related"])
		if len(related) > 0 {
			citation.Related = make([]SiteReportCitation, 0, len(related))
			for _, item := range related {
				entry, err := siteReportCitation(item, availability, config, now, false)
				if err != nil {
					return SiteReportCitation{}, err
				}
				entry.Summary = nil
				citation.Related = append(citation.Related, entry)
			}
		}
		citation.FollowUp = reportOptionalString(raw["followUp"])
	}
	return citation, nil
}

func siteLeadItem(kind string, content map[string]any, highlights, all []SiteReportCitation, named *reportLead, siteName string) *SiteReportCitation {
	reportKind := "periodic"
	if kind == "daily" {
		reportKind = "daily"
	}
	hasCitedLead := hasWrittenReportLead(content, reportKind, siteName)
	if hasCitedLead && named != nil {
		for i := range all {
			if all[i].ItemID != nil && *all[i].ItemID == named.ItemID {
				return &all[i]
			}
		}
		return nil
	}
	if kind == "daily" {
		lead := reportString(reportObject(content["lead"])["title"])
		id := reportWrittenLeadID(content, "daily", siteName)
		if lead != "" && id != "" {
			for i := range all {
				if all[i].ItemID != nil && *all[i].ItemID == id {
					return &all[i]
				}
			}
			return nil
		}
		if lead != "" {
			return nil
		}
		for i := range highlights {
			if highlights[i].Available {
				return &highlights[i]
			}
		}
	} else {
		for i := range highlights {
			if highlights[i].Available {
				return &highlights[i]
			}
		}
	}
	for i := range all {
		if all[i].Available {
			return &all[i]
		}
	}
	return nil
}

func reportSourceCount(content map[string]any, kind string, availability map[string]V1ReportAvailabilityRecord) int {
	seen := make(map[string]struct{})
	shape := kind
	if shape != "daily" {
		shape = "periodic"
	}
	for _, item := range reportEntries(content, shape) {
		if !reportAvailable(item, availability) {
			continue
		}
		for _, member := range append([]map[string]any{item}, reportArray(item["related"])...) {
			if !reportAvailable(member, availability) {
				continue
			}
			id := reportString(member["itemId"])
			sourceID := reportString(member["sourceId"])
			if current, ok := availability[id]; id != "" && ok {
				sourceID = current.SourceID
			}
			if sourceID != "" {
				seen[sourceID] = struct{}{}
			}
		}
	}
	return len(seen)
}

func reportNumber(value any) int {
	switch current := value.(type) {
	case int:
		return current
	case int64:
		return int(current)
	case float64:
		return int(current)
	case json.Number:
		parsed, _ := current.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(current)
		return parsed
	default:
		return 0
	}
}

func reportStringArray(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func reportWithSubject(noun, subject string) string {
	if subject == "" {
		subject = "AI"
	}
	last := subject[len(subject)-1]
	if last >= 'A' && last <= 'Z' || last >= 'a' && last <= 'z' || last >= '0' && last <= '9' {
		return subject + " " + noun
	}
	return subject + noun
}
