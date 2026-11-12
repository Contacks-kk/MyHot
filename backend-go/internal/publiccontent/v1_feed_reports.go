package publiccontent

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrUnsupportedFeedReportKind = errors.New("unsupported report kind")
var ErrFeedReportRepositoryUnavailable = errors.New("feed report repository is unavailable")

type V1FeedReportRepository interface {
	V1FeedReports(context.Context, string, int) ([]V1ReportRecord, error)
}

type V1FeedIssue struct {
	Key           string
	GeneratedAt   time.Time
	Headline      *string
	LeadParagraph *string
	Sections      []V1FeedIssueSection
}

type V1FeedIssueSection struct {
	Label string
	Items []V1FeedIssueItem
}

type V1FeedIssueItem struct {
	Title string
	Link  string
}

func (r *GormRepository) V1FeedReports(ctx context.Context, kind string, limit int) ([]V1ReportRecord, error) {
	var rows []ReportModel
	err := r.db.WithContext(ctx).Model(&ReportModel{}).
		Select("kind, key, window_start, window_end, content, generated_at").
		Where("kind = ?", kind).Order("key DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	reports := make([]V1ReportRecord, 0, len(rows))
	for _, row := range rows {
		content, err := decodeReportContent(row.Content)
		if err != nil {
			return nil, err
		}
		reports = append(reports, V1ReportRecord{Kind: row.Kind, Key: row.Key, WindowStart: row.WindowStart, WindowEnd: row.WindowEnd, Content: content, GeneratedAt: row.GeneratedAt})
	}
	return reports, nil
}

// V1FeedIssues projects report issues for RSS without exposing citations that are no longer public.
func (s *Service) V1FeedIssues(ctx context.Context, kind string, limit int, now time.Time, siteName, measure, noun, siteURL string) ([]V1FeedIssue, error) {
	if kind != "daily" && kind != "weekly" && kind != "monthly" {
		return nil, ErrUnsupportedFeedReportKind
	}
	repository, ok := s.repository.(V1ReportRepository)
	if !ok {
		return nil, ErrFeedReportRepositoryUnavailable
	}
	feedRepository, ok := s.repository.(V1FeedReportRepository)
	if !ok {
		return nil, ErrFeedReportRepositoryUnavailable
	}
	reports, err := feedRepository.V1FeedReports(ctx, kind, limit)
	if err != nil {
		return nil, err
	}
	shape := kind
	if kind == "weekly" || kind == "monthly" {
		shape = "periodic"
	}
	ids := make([]string, 0)
	for _, report := range reports {
		ids = append(ids, reportItemIDs(reportEntries(report.Content, shape), false)...)
	}
	availability, err := loadV1ReportAvailability(ctx, repository, ids, now)
	if err != nil {
		return nil, err
	}
	gone := unavailableReportIDs(availability)
	leads := make([]*reportLead, len(reports))
	replacements := make([]V1DailyReplacement, 0)
	for i, report := range reports {
		leads[i] = issueLead(report.Content, kind, gone, siteName)
		if kind == "daily" && leads[i] != nil && leads[i].ItemID != "" && leads[i].LeadParagraph == nil && hasWrittenReportLead(report.Content, kind, siteName) {
			replacements = append(replacements, V1DailyReplacement{Key: report.Key, ItemID: leads[i].ItemID})
		}
	}
	if len(replacements) > 0 {
		summaries, err := repository.V1DailyReplacementSummaries(ctx, replacements)
		if err != nil {
			return nil, err
		}
		for i, report := range reports {
			if leads[i] == nil || leads[i].ItemID == "" || leads[i].LeadParagraph != nil || !hasWrittenReportLead(report.Content, kind, siteName) {
				continue
			}
			leads[i].LeadParagraph = summaries[report.Key+"\x00"+leads[i].ItemID]
		}
	}
	baseURL := strings.TrimRight(siteURL, "/")
	issues := make([]V1FeedIssue, 0, len(reports))
	for i, report := range reports {
		issue := V1FeedIssue{Key: report.Key, GeneratedAt: report.GeneratedAt, Sections: make([]V1FeedIssueSection, 0)}
		if leads[i] != nil {
			issue.Headline = stringPointer(leads[i].Title)
			issue.LeadParagraph = leads[i].LeadParagraph
		}
		if kind != "daily" {
			issue.LeadParagraph = periodOverview(report.Content, kind, gone, siteName, measure, noun)
		}
		groups := make([]map[string]any, 0)
		if kind == "daily" {
			groups = reportArray(report.Content["sections"])
		} else {
			groups = reportArray(report.Content["themes"])
		}
		for _, group := range groups {
			label := reportString(group["label"])
			entries := reportArray(group["items"])
			if kind != "daily" {
				label = reportString(group["heading"])
				entries = reportArray(group["storyRefs"])
			}
			section := V1FeedIssueSection{Label: label, Items: make([]V1FeedIssueItem, 0, len(entries))}
			for _, entry := range entries {
				id := reportString(entry["itemId"])
				if id == "" {
					continue
				}
				if status, found := availability[id]; found && !status.Available {
					continue
				}
				section.Items = append(section.Items, V1FeedIssueItem{Title: reportString(entry["title"]), Link: baseURL + "/items/" + id})
			}
			if len(section.Items) > 0 {
				issue.Sections = append(issue.Sections, section)
			}
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

var _ V1FeedReportRepository = (*GormRepository)(nil)
