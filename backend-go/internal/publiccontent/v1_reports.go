package publiccontent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"
)

const v1ReportIndexLimit = 400

// ReportModel maps the existing reports table. SQL migrations remain the schema source.
type ReportModel struct {
	ID          int64           `gorm:"column:id;primaryKey"`
	Kind        string          `gorm:"column:kind"`
	Key         string          `gorm:"column:key"`
	WindowStart time.Time       `gorm:"column:window_start"`
	WindowEnd   time.Time       `gorm:"column:window_end"`
	Content     json.RawMessage `gorm:"column:content;type:jsonb"`
	GeneratedAt time.Time       `gorm:"column:generated_at"`
}

func (ReportModel) TableName() string { return "reports" }

type V1ReportRepository interface {
	V1ReportIndex(context.Context, string, int) ([]V1ReportIndexRecord, error)
	V1Report(context.Context, string, string, bool) (*V1ReportRecord, error)
	V1ReportAvailability(context.Context, []string, time.Time) (map[string]V1ReportAvailabilityRecord, error)
	V1DailyReplacementSummaries(context.Context, []V1DailyReplacement) (map[string]*string, error)
}

type V1ReportIndexRecord struct {
	Key         string
	IssueNumber int
	GeneratedAt time.Time
	Content     map[string]any
}

type V1ReportRecord struct {
	Kind        string
	Key         string
	WindowStart time.Time
	WindowEnd   time.Time
	Content     map[string]any
	GeneratedAt time.Time
}

type V1ReportAvailabilityRecord struct {
	Available   bool
	Summary     *string
	FirstParty  bool
	SourceID    string
	SourceName  *string
	SourceIcon  *string
	PublishedAt *time.Time
}

type V1DailyReplacement struct {
	Key    string `json:"key"`
	ItemID string `json:"item_id"`
}

func (r *GormRepository) V1ReportIndex(ctx context.Context, kind string, limit int) ([]V1ReportIndexRecord, error) {
	var rows []struct {
		Key         string          `gorm:"column:key"`
		IssueNumber int             `gorm:"column:issue_number"`
		GeneratedAt time.Time       `gorm:"column:generated_at"`
		Content     json.RawMessage `gorm:"column:content"`
	}
	var query string
	switch kind {
	case "daily":
		query = `SELECT key, (row_number() OVER (ORDER BY key))::int AS issue_number, generated_at, jsonb_build_object(
			'lead', content->'lead', 'headline', content->'headline', 'title', content->'title',
			'leadItemId', content->'leadItemId', 'highlights', content->'highlights', 'sections',
			jsonb_build_array(jsonb_build_object('items',
				(SELECT coalesce(jsonb_agg(jsonb_build_object('itemId', item->'itemId', 'title', item->'title') ORDER BY ord), '[]'::jsonb)
				 FROM jsonb_array_elements(jsonb_path_query_array(content, '$.sections[*].items[*]')) WITH ORDINALITY AS cited(item, ord))))) AS content
			FROM reports WHERE kind = ? ORDER BY key DESC LIMIT ?`
	case "weekly", "monthly":
		query = `SELECT key, (row_number() OVER (ORDER BY key))::int AS issue_number, generated_at, jsonb_build_object(
			'headline', content->'headline', 'title', content->'title', 'leadItemId', content->'leadItemId',
			'highlights', content->'highlights', 'themes', jsonb_build_array(jsonb_build_object('storyRefs',
				(SELECT coalesce(jsonb_agg(jsonb_build_object('itemId', item->'itemId', 'title', item->'title') ORDER BY ord), '[]'::jsonb)
				 FROM jsonb_array_elements(jsonb_path_query_array(content, '$.themes[*].storyRefs[*]')) WITH ORDINALITY AS cited(item, ord))))) AS content
			FROM reports WHERE kind = ? ORDER BY key DESC LIMIT ?`
	default:
		return nil, fmt.Errorf("unsupported report kind %q", kind)
	}
	if err := r.db.WithContext(ctx).Raw(query, kind, limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]V1ReportIndexRecord, 0, len(rows))
	for _, row := range rows {
		content, err := decodeReportContent(row.Content)
		if err != nil {
			return nil, err
		}
		result = append(result, V1ReportIndexRecord{Key: row.Key, IssueNumber: row.IssueNumber, GeneratedAt: row.GeneratedAt, Content: content})
	}
	return result, nil
}

type SiteReportIndexEntry struct {
	Key         string  `json:"key"`
	IssueNumber int     `json:"issueNumber"`
	Title       *string `json:"title"`
	Count       int     `json:"count"`
}

type SiteReportIndexResponse struct {
	Kind  string                 `json:"kind"`
	Items []SiteReportIndexEntry `json:"items"`
}

type SiteReportNavigationEntry struct {
	Key           string
	IssueNumber   int
	Title         *string
	titleIncluded bool
}

func (entry SiteReportNavigationEntry) MarshalJSON() ([]byte, error) {
	if entry.titleIncluded {
		return json.Marshal(struct {
			Key         string  `json:"key"`
			IssueNumber int     `json:"issueNumber"`
			Title       *string `json:"title"`
		}{Key: entry.Key, IssueNumber: entry.IssueNumber, Title: entry.Title})
	}
	return json.Marshal(struct {
		Key         string `json:"key"`
		IssueNumber int    `json:"issueNumber"`
	}{Key: entry.Key, IssueNumber: entry.IssueNumber})
}

type SiteReportNavigationResponse struct {
	Items []SiteReportNavigationEntry `json:"items"`
}

func (s *Service) SiteReportIndex(ctx context.Context, kind string, now time.Time, siteName string) (SiteReportIndexResponse, error) {
	if kind != "daily" && kind != "weekly" && kind != "monthly" {
		return SiteReportIndexResponse{}, errors.New("unsupported report kind")
	}
	repository, ok := s.repository.(V1ReportRepository)
	if !ok {
		return SiteReportIndexResponse{}, errors.New("report index repository is unavailable")
	}
	rows, err := repository.V1ReportIndex(ctx, kind, v1ReportIndexLimit)
	if err != nil {
		return SiteReportIndexResponse{}, err
	}
	gone, err := unavailableReportHeadlines(ctx, repository, rows, kind, now, siteName)
	if err != nil {
		return SiteReportIndexResponse{}, err
	}
	shape := "periodic"
	if kind == "daily" {
		shape = "daily"
	}
	items := make([]SiteReportIndexEntry, 0, len(rows))
	for _, row := range rows {
		var title *string
		if lead := issueLead(row.Content, shape, gone, siteName); lead != nil {
			title = stringPointer(lead.Title)
		}
		items = append(items, SiteReportIndexEntry{Key: row.Key, IssueNumber: row.IssueNumber, Title: title, Count: len(reportEntries(row.Content, shape))})
	}
	return SiteReportIndexResponse{Kind: kind, Items: items}, nil
}

func (s *Service) SiteReportNavigation(ctx context.Context, kind, key string, now time.Time, siteName string) (SiteReportNavigationResponse, error) {
	index, err := s.SiteReportIndex(ctx, kind, now, siteName)
	if err != nil {
		return SiteReportNavigationResponse{}, err
	}
	at := -1
	for i, entry := range index.Items {
		if entry.Key == key {
			at = i
			break
		}
	}
	month := ""
	if len(key) >= 7 {
		month = key[:7]
	}
	items := make([]SiteReportNavigationEntry, 0, len(index.Items))
	for i, entry := range index.Items {
		includeTitle := kind != "daily" || strings.HasPrefix(entry.Key, month) || i < 3 || distance(i, at) <= 1
		items = append(items, SiteReportNavigationEntry{Key: entry.Key, IssueNumber: entry.IssueNumber, Title: entry.Title, titleIncluded: includeTitle})
	}
	return SiteReportNavigationResponse{Items: items}, nil
}

func (s *Service) SiteDailyReportMonth(ctx context.Context, month string, now time.Time, siteName string) (SiteReportNavigationResponse, error) {
	index, err := s.SiteReportIndex(ctx, "daily", now, siteName)
	if err != nil {
		return SiteReportNavigationResponse{}, err
	}
	items := make([]SiteReportNavigationEntry, 0)
	for _, entry := range index.Items {
		if strings.HasPrefix(entry.Key, month) {
			items = append(items, SiteReportNavigationEntry{Key: entry.Key, IssueNumber: entry.IssueNumber, Title: entry.Title, titleIncluded: true})
		}
	}
	return SiteReportNavigationResponse{Items: items}, nil
}

func distance(left, right int) int {
	difference := left - right
	if difference < 0 {
		return -difference
	}
	return difference
}

func (r *GormRepository) V1Report(ctx context.Context, kind, key string, latest bool) (*V1ReportRecord, error) {
	query := r.db.WithContext(ctx).Model(&ReportModel{}).
		Select("kind, key, window_start, window_end, content, generated_at").Where("kind = ?", kind)
	if !latest {
		query = query.Where("key = ?", key)
	}
	var row ReportModel
	query = query.Order("key DESC").Limit(1)
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	content, err := decodeReportContent(row.Content)
	if err != nil {
		return nil, err
	}
	return &V1ReportRecord{Kind: row.Kind, Key: row.Key, WindowStart: row.WindowStart, WindowEnd: row.WindowEnd, Content: content, GeneratedAt: row.GeneratedAt}, nil
}

func (r *GormRepository) V1ReportAvailability(ctx context.Context, ids []string, now time.Time) (map[string]V1ReportAvailabilityRecord, error) {
	result := make(map[string]V1ReportAvailabilityRecord, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		ID          string     `gorm:"column:id"`
		Available   bool       `gorm:"column:available"`
		Summary     *string    `gorm:"column:summary"`
		FirstParty  bool       `gorm:"column:first_party"`
		SourceID    string     `gorm:"column:source_id"`
		SourceName  *string    `gorm:"column:source_name"`
		SourceIcon  *string    `gorm:"column:source_icon"`
		PublishedAt *time.Time `gorm:"column:published_at"`
	}
	err := r.db.WithContext(ctx).Table("publications AS p").
		Select(`p.article_id AS id,
			(p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)) AS available,
			p.summary, (s.tier = 'T1') AS first_party, p.source_id, s.name AS source_name, s.icon_url AS source_icon,
			coalesce(p.published_at, p.discovered_at) AS published_at`, now).
		Joins("LEFT JOIN sources AS s ON s.id = p.source_id").
		Where("p.article_id IN ?", ids).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = V1ReportAvailabilityRecord{Available: row.Available, Summary: row.Summary, FirstParty: row.FirstParty, SourceID: row.SourceID, SourceName: row.SourceName, SourceIcon: row.SourceIcon, PublishedAt: row.PublishedAt}
	}
	return result, nil
}

func (r *GormRepository) V1DailyReplacementSummaries(ctx context.Context, replacements []V1DailyReplacement) (map[string]*string, error) {
	result := make(map[string]*string, len(replacements))
	if len(replacements) == 0 {
		return result, nil
	}
	payload, err := json.Marshal(replacements)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Key     string  `gorm:"column:key"`
		ItemID  string  `gorm:"column:item_id"`
		Summary *string `gorm:"column:summary"`
	}
	const query = `SELECT wanted.key, wanted.item_id,
		(SELECT item->>'summary' FROM jsonb_path_query(r.content, '$.sections[*].items[*]') item
		 WHERE item->>'itemId' = wanted.item_id LIMIT 1) AS summary
		FROM reports r
		JOIN jsonb_to_recordset(?::jsonb) AS wanted(key text, item_id text) ON true
		WHERE r.kind = 'daily' AND r.key = wanted.key`
	if err := r.db.WithContext(ctx).Raw(query, string(payload)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.Key+"\x00"+row.ItemID] = row.Summary
	}
	return result, nil
}

func decodeReportContent(raw json.RawMessage) (map[string]any, error) {
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		return nil, fmt.Errorf("decode report content: %w", err)
	}
	if content == nil {
		content = make(map[string]any)
	}
	return content, nil
}

type V1ReportIndexResponse struct {
	SchemaVersion int              `json:"schemaVersion"`
	Count         int              `json:"count"`
	Items         []map[string]any `json:"items"`
}

type V1ReportLink struct {
	AIHot string `json:"aihot"`
}

type V1ReportAttribution struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type V1ReportItemLink struct {
	AIHot    *string `json:"aihot"`
	Original string  `json:"original"`
}

type V1ReportSource struct {
	Name string `json:"name"`
}

type V1DailyItemCitation struct {
	Title       string              `json:"title"`
	Summary     string              `json:"summary"`
	Source      V1ReportSource      `json:"source"`
	Links       V1ReportItemLink    `json:"links"`
	Attribution V1ReportAttribution `json:"attribution"`
}

type V1DailyFlashCitation struct {
	Title       string              `json:"title"`
	Source      V1ReportSource      `json:"source"`
	Links       V1ReportItemLink    `json:"links"`
	PublishedAt string              `json:"publishedAt"`
	Attribution V1ReportAttribution `json:"attribution"`
}

type V1PeriodCitation struct {
	Title       string              `json:"title"`
	Summary     string              `json:"summary"`
	Source      V1ReportSource      `json:"source"`
	Links       V1ReportItemLink    `json:"links"`
	PublishedAt *string             `json:"publishedAt"`
	Attribution V1ReportAttribution `json:"attribution"`
}

type V1DailySection struct {
	Label string                `json:"label"`
	Items []V1DailyItemCitation `json:"items"`
}

type V1DailyLead struct {
	Title         string `json:"title"`
	LeadParagraph string `json:"leadParagraph"`
}

type V1DailyBody struct {
	Date        string                 `json:"date"`
	GeneratedAt string                 `json:"generatedAt"`
	WindowStart string                 `json:"windowStart"`
	WindowEnd   string                 `json:"windowEnd"`
	Links       V1ReportLink           `json:"links"`
	Attribution V1ReportAttribution    `json:"attribution"`
	Lead        *V1DailyLead           `json:"lead"`
	Sections    []V1DailySection       `json:"sections"`
	Flashes     []V1DailyFlashCitation `json:"flashes"`
}

type V1DailyResponse struct {
	SchemaVersion int         `json:"schemaVersion"`
	Report        V1DailyBody `json:"report"`
}

type V1DailyAgentNote struct {
	OtherSources int
	Related      []V1DailyAgentRelated
	FollowUp     *string
}

type V1DailyAgentRelated struct {
	Title string
	Link  string
}

type V1PeriodSection struct {
	Label   string             `json:"label"`
	Summary *string            `json:"summary"`
	Items   []V1PeriodCitation `json:"items"`
}

type V1PeriodBody struct {
	Week        *string             `json:"week,omitempty"`
	Month       *string             `json:"month,omitempty"`
	PeriodStart string              `json:"periodStart"`
	PeriodEnd   string              `json:"periodEnd"`
	GeneratedAt string              `json:"generatedAt"`
	WindowStart string              `json:"windowStart"`
	WindowEnd   string              `json:"windowEnd"`
	Links       V1ReportLink        `json:"links"`
	Attribution V1ReportAttribution `json:"attribution"`
	Headline    *string             `json:"headline"`
	Overview    *string             `json:"overview"`
	Sections    []V1PeriodSection   `json:"sections"`
}

type V1PeriodResponse struct {
	SchemaVersion int          `json:"schemaVersion"`
	Report        V1PeriodBody `json:"report"`
}

func (s *Service) V1Dailies(ctx context.Context, limit int, now time.Time, siteURL, siteName string) (V1ReportIndexResponse, error) {
	return s.v1ReportIndex(ctx, "daily", limit, now, siteURL, siteName)
}

func (s *Service) V1Periods(ctx context.Context, kind string, limit int, now time.Time, siteURL, siteName string) (V1ReportIndexResponse, error) {
	if kind != "weekly" && kind != "monthly" {
		return V1ReportIndexResponse{}, fmt.Errorf("unsupported report kind %q", kind)
	}
	return s.v1ReportIndex(ctx, kind, limit, now, siteURL, siteName)
}

func (s *Service) v1ReportIndex(ctx context.Context, kind string, limit int, now time.Time, siteURL, siteName string) (V1ReportIndexResponse, error) {
	repository, ok := s.repository.(V1ReportRepository)
	if !ok {
		return V1ReportIndexResponse{}, errors.New("v1 reports repository is unavailable")
	}
	rows, err := repository.V1ReportIndex(ctx, kind, v1ReportIndexLimit)
	if err != nil {
		return V1ReportIndexResponse{}, err
	}
	gone, err := unavailableReportHeadlines(ctx, repository, rows, kind, now, siteName)
	if err != nil {
		return V1ReportIndexResponse{}, err
	}
	leads := make([]*reportLead, len(rows))
	replacements := make([]V1DailyReplacement, 0)
	for i, row := range rows {
		leads[i] = issueLead(row.Content, kind, gone, siteName)
		if kind == "daily" && leads[i] != nil && leads[i].ItemID != "" && leads[i].LeadParagraph == nil && hasWrittenReportLead(row.Content, kind, siteName) {
			replacements = append(replacements, V1DailyReplacement{Key: row.Key, ItemID: leads[i].ItemID})
		}
	}
	if len(replacements) > 0 {
		summaries, err := repository.V1DailyReplacementSummaries(ctx, replacements)
		if err != nil {
			return V1ReportIndexResponse{}, err
		}
		for i, row := range rows {
			if leads[i] == nil || leads[i].ItemID == "" || leads[i].LeadParagraph != nil || !hasWrittenReportLead(row.Content, kind, siteName) {
				continue
			}
			leads[i].LeadParagraph = summaries[row.Key+"\x00"+leads[i].ItemID]
		}
	}
	if len(rows) > limit {
		rows, leads = rows[:limit], leads[:limit]
	}
	baseURL := strings.TrimRight(siteURL, "/")
	items := make([]map[string]any, 0, len(rows))
	for i, row := range rows {
		url := reportURL(baseURL, kind, row.Key)
		item := map[string]any{"generatedAt": formatV1Time(row.GeneratedAt), "links": V1ReportLink{AIHot: url}, "attribution": V1ReportAttribution{Name: siteName, URL: url}}
		if kind == "daily" {
			item["date"] = row.Key
			var title, paragraph *string
			if leads[i] != nil {
				title, paragraph = stringPointer(leads[i].Title), leads[i].LeadParagraph
			}
			item["leadTitle"], item["leadParagraph"] = title, paragraph
		} else {
			start, end, valid := reportPeriodRange(kind, row.Key)
			if !valid {
				item["periodStart"], item["periodEnd"] = nil, nil
			} else {
				item["periodStart"], item["periodEnd"] = start, end
			}
			if kind == "weekly" {
				item["week"] = row.Key
			} else {
				item["month"] = row.Key
			}
			var headline *string
			if leads[i] != nil {
				headline = stringPointer(leads[i].Title)
			}
			item["headline"] = headline
		}
		items = append(items, item)
	}
	return V1ReportIndexResponse{SchemaVersion: 1, Count: len(items), Items: items}, nil
}

func (s *Service) V1Daily(ctx context.Context, key string, latest bool, now time.Time, siteURL, siteName string) (*V1DailyResponse, error) {
	repository, ok := s.repository.(V1ReportRepository)
	if !ok {
		return nil, errors.New("v1 reports repository is unavailable")
	}
	report, err := repository.V1Report(ctx, "daily", key, latest)
	if err != nil || report == nil {
		return nil, err
	}
	items := dailyItems(report.Content)
	ids := reportItemIDs(items, true)
	availability, err := loadV1ReportAvailability(ctx, repository, ids, now)
	if err != nil {
		return nil, err
	}
	gone := unavailableReportIDs(availability)
	lead := (*V1DailyLead)(nil)
	if report.Content["lead"] != nil || reportString(report.Content["leadItemId"]) != "" {
		if current := issueLead(report.Content, "daily", gone, siteName); current != nil {
			paragraph := ""
			if current.LeadParagraph != nil {
				paragraph = *current.LeadParagraph
			}
			lead = &V1DailyLead{Title: current.Title, LeadParagraph: paragraph}
		}
	}
	baseURL := strings.TrimRight(siteURL, "/")
	url := reportURL(baseURL, "daily", report.Key)
	sections := make([]V1DailySection, 0)
	for _, rawSection := range reportArray(report.Content["sections"]) {
		section := V1DailySection{Label: reportString(rawSection["label"]), Items: make([]V1DailyItemCitation, 0)}
		for _, raw := range reportArray(rawSection["items"]) {
			if !reportAvailable(raw, availability) {
				continue
			}
			section.Items = append(section.Items, v1DailyCitation(raw, availability, baseURL, url, siteName))
		}
		sections = append(sections, section)
	}
	flashes := make([]V1DailyFlashCitation, 0)
	for _, raw := range reportArray(report.Content["flashes"]) {
		if !reportAvailable(raw, availability) {
			continue
		}
		flashes = append(flashes, v1DailyFlash(raw, availability, baseURL, url, report.GeneratedAt, siteName))
	}
	return &V1DailyResponse{SchemaVersion: 1, Report: V1DailyBody{
		Date: report.Key, GeneratedAt: formatV1Time(report.GeneratedAt), WindowStart: formatV1Time(report.WindowStart), WindowEnd: formatV1Time(report.WindowEnd),
		Links: V1ReportLink{AIHot: url}, Attribution: V1ReportAttribution{Name: siteName, URL: url}, Lead: lead, Sections: sections, Flashes: flashes,
	}}, nil
}

func (s *Service) V1DailyWithAgentNotes(ctx context.Context, key string, latest bool, now time.Time, siteURL, siteName string) (*V1DailyResponse, map[string]V1DailyAgentNote, error) {
	report, err := s.V1Daily(ctx, key, latest, now, siteURL, siteName)
	if err != nil || report == nil {
		return report, nil, err
	}
	repository, ok := s.repository.(V1ReportRepository)
	if !ok {
		return nil, nil, errors.New("v1 reports repository is unavailable")
	}
	row, err := repository.V1Report(ctx, "daily", report.Report.Date, false)
	if err != nil || row == nil {
		return report, nil, err
	}
	items := dailyItems(row.Content)
	availability, err := loadV1ReportAvailability(ctx, repository, reportItemIDs(items, true), now)
	if err != nil {
		return nil, nil, err
	}
	baseURL := strings.TrimRight(siteURL, "/")
	notes := make(map[string]V1DailyAgentNote)
	for _, item := range items {
		if !reportAvailable(item, availability) {
			continue
		}
		itemID := reportString(item["itemId"])
		link := reportString(reportCitationMetadata(item, availability)["sourceUrl"])
		if itemID != "" {
			link = baseURL + "/items/" + itemID
		}
		otherSources := 0
		if count, ok := item["sources"].(float64); ok && count > 1 {
			otherSources = int(count) - 1
		}
		related := make([]V1DailyAgentRelated, 0)
		for _, raw := range reportArray(item["related"]) {
			relatedID := reportString(raw["itemId"])
			if relatedID == "" || !reportAvailable(raw, availability) {
				continue
			}
			related = append(related, V1DailyAgentRelated{Title: reportString(raw["title"]), Link: baseURL + "/items/" + relatedID})
		}
		followUp := reportOptionalString(item["followUp"])
		if otherSources > 0 || len(related) > 0 || (followUp != nil && *followUp != "") {
			notes[link] = V1DailyAgentNote{OtherSources: otherSources, Related: related, FollowUp: followUp}
		}
	}
	return report, notes, nil
}

func (s *Service) V1Period(ctx context.Context, kind, key string, latest bool, now time.Time, siteURL, siteName, measure, noun string) (*V1PeriodResponse, error) {
	if kind != "weekly" && kind != "monthly" {
		return nil, fmt.Errorf("unsupported report kind %q", kind)
	}
	repository, ok := s.repository.(V1ReportRepository)
	if !ok {
		return nil, errors.New("v1 reports repository is unavailable")
	}
	report, err := repository.V1Report(ctx, kind, key, latest)
	if err != nil || report == nil {
		return nil, err
	}
	rawEntries := periodicItems(report.Content)
	availability, err := loadV1ReportAvailability(ctx, repository, reportItemIDs(rawEntries, false), now)
	if err != nil {
		return nil, err
	}
	gone := unavailableReportIDs(availability)
	lead := issueLead(report.Content, kind, gone, siteName)
	overview := periodOverview(report.Content, kind, gone, siteName, measure, noun)
	baseURL := strings.TrimRight(siteURL, "/")
	url := reportURL(baseURL, kind, report.Key)
	start, end, valid := reportPeriodRange(kind, report.Key)
	if !valid {
		return nil, errors.New("stored report has an invalid period key")
	}
	var week, month *string
	if kind == "weekly" {
		week = stringPointer(report.Key)
	} else {
		month = stringPointer(report.Key)
	}
	sections := make([]V1PeriodSection, 0)
	for _, rawTheme := range reportArray(report.Content["themes"]) {
		rawItems := reportArray(rawTheme["storyRefs"])
		sectionItems := make([]V1PeriodCitation, 0, len(rawItems))
		for _, raw := range rawItems {
			if reportAvailable(raw, availability) {
				sectionItems = append(sectionItems, v1PeriodCitation(raw, availability, baseURL, url, siteName))
			}
		}
		if len(sectionItems) == 0 {
			continue
		}
		sections = append(sections, V1PeriodSection{Label: reportString(rawTheme["heading"]), Summary: reportSectionSummary(rawTheme, availability), Items: sectionItems})
	}
	var headline *string
	if lead != nil {
		headline = stringPointer(lead.Title)
	}
	return &V1PeriodResponse{SchemaVersion: 1, Report: V1PeriodBody{
		Week: week, Month: month, PeriodStart: start, PeriodEnd: end,
		GeneratedAt: formatV1Time(report.GeneratedAt), WindowStart: formatV1Time(report.WindowStart), WindowEnd: formatV1Time(report.WindowEnd),
		Links: V1ReportLink{AIHot: url}, Attribution: V1ReportAttribution{Name: siteName, URL: url}, Headline: headline, Overview: overview, Sections: sections,
	}}, nil
}

type reportLead struct {
	ItemID        string
	Title         string
	LeadParagraph *string
}

func issueLead(content map[string]any, kind string, gone map[string]struct{}, siteName string) *reportLead {
	candidates := reportLeadCandidates(content, kind, siteName)
	if len(candidates) == 0 {
		title := reportLeadTitle(content, kind, siteName)
		if title == "" {
			return nil
		}
		lead := &reportLead{Title: title}
		if kind == "daily" {
			lead.LeadParagraph = reportOptionalString(reportObject(content["lead"])["leadParagraph"])
		}
		return lead
	}
	var first map[string]any
	for _, entry := range candidates {
		id := reportString(entry["itemId"])
		if id == "" {
			first = entry
			break
		}
		if _, withdrawn := gone[id]; !withdrawn {
			first = entry
			break
		}
	}
	if first == nil {
		return nil
	}
	id := reportString(first["itemId"])
	title := reportString(first["title"])
	writtenID := ""
	if reportString(content["leadItemId"]) == "" {
		writtenID = reportWrittenLeadID(content, kind, siteName)
	}
	if writtenID != "" && id == writtenID {
		title = reportLeadTitle(content, kind, siteName)
	}
	lead := &reportLead{ItemID: id, Title: title}
	if kind != "daily" {
		return lead
	}
	writtenParagraph := reportOptionalString(reportObject(content["lead"])["leadParagraph"])
	leadItemID := reportString(content["leadItemId"])
	if id != "" && id == leadItemID && writtenParagraph != nil {
		lead.LeadParagraph = writtenParagraph
	} else if leadItemID != "" || writtenID != "" {
		lead.LeadParagraph = reportOptionalString(first["summary"])
	}
	return lead
}

func reportLeadCandidates(content map[string]any, kind, siteName string) []map[string]any {
	entries := reportEntries(content, kind)
	leadID := reportString(content["leadItemId"])
	if leadID == "" {
		leadID = reportWrittenLeadID(content, kind, siteName)
	}
	if leadID == "" {
		if reportLeadTitle(content, kind, siteName) != "" {
			return nil
		}
		return entries
	}
	byID := make(map[string]map[string]any, len(entries))
	for _, entry := range entries {
		if id := reportString(entry["itemId"]); id != "" {
			byID[id] = entry
		}
	}
	order := make([]string, 0, len(entries)+4)
	order = append(order, leadID)
	for _, raw := range reportArray(content["highlights"]) {
		if id := reportString(raw); id != "" {
			order = append(order, id)
		}
	}
	for _, entry := range entries {
		if id := reportString(entry["itemId"]); id != "" {
			order = append(order, id)
		}
	}
	seen := make(map[string]struct{}, len(order))
	out := make([]map[string]any, 0, len(order))
	for _, id := range order {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		if entry := byID[id]; entry != nil {
			out = append(out, entry)
		}
	}
	return out
}

func reportWrittenLeadID(content map[string]any, kind, siteName string) string {
	title := reportLeadTitle(content, kind, siteName)
	if title == "" {
		return ""
	}
	entries := reportEntries(content, kind)
	want := reportBigrams(title)
	if len(want) == 0 {
		return ""
	}
	bestShare := -1.0
	bestID := ""
	for _, entry := range entries {
		id := reportString(entry["itemId"])
		if id == "" {
			continue
		}
		have := reportBigrams(reportString(entry["title"]))
		shared := 0
		for pair := range want {
			if _, ok := have[pair]; ok {
				shared++
			}
		}
		share := float64(shared) / float64(len(want))
		if share > bestShare {
			bestShare, bestID = share, id
		}
	}
	if bestShare < 0.5 {
		return ""
	}
	return bestID
}

func reportBigrams(value string) map[string]struct{} {
	chars := make([]rune, 0, len(value))
	for _, char := range strings.ToLower(value) {
		if unicode.IsSpace(char) || unicode.IsPunct(char) {
			continue
		}
		chars = append(chars, char)
	}
	out := make(map[string]struct{}, len(chars))
	for i := 1; i < len(chars); i++ {
		out[string(chars[i-1:i+1])] = struct{}{}
	}
	return out
}

func reportLeadTitle(content map[string]any, kind, siteName string) string {
	if kind == "daily" {
		return reportString(reportObject(content["lead"])["title"])
	}
	value, exists := content["headline"]
	if !exists || value == nil {
		value = content["title"]
	}
	text := reportJSString(value)
	for _, periodName := range []string{"周报", "月报"} {
		prefix := strings.TrimSpace(siteName) + " " + periodName + " · "
		if strings.HasPrefix(text, prefix) {
			return ""
		}
	}
	return text
}

func hasWrittenReportLead(content map[string]any, kind, siteName string) bool {
	return reportString(content["leadItemId"]) != "" || reportWrittenLeadID(content, kind, siteName) != ""
}

func periodOverview(content map[string]any, kind string, gone map[string]struct{}, siteName, measure, noun string) *string {
	changed := false
	for _, entry := range reportEntries(content, kind) {
		if id := reportString(entry["itemId"]); id != "" {
			if _, unavailable := gone[id]; unavailable {
				changed = true
				break
			}
		}
	}
	if !changed {
		if overview := reportString(content["overview"]); overview != "" {
			return stringPointer(overview)
		}
	}
	candidates := reportLeadCandidates(content, kind, siteName)
	shown := make([]map[string]any, 0, len(candidates))
	for _, entry := range candidates {
		id := reportString(entry["itemId"])
		if _, unavailable := gone[id]; !unavailable {
			shown = append(shown, entry)
		}
	}
	if len(shown) == 0 {
		return nil
	}
	periodName := "本周"
	if kind == "monthly" {
		periodName = "本月"
	}
	if measure == "" {
		measure = "件"
	}
	if noun == "" {
		noun = "大事"
	}
	titles := make([]string, 0, 3)
	for _, entry := range shown[:min(3, len(shown))] {
		titles = append(titles, reportString(entry["title"]))
	}
	return stringPointer(fmt.Sprintf("%s %d %s%s，最受关注的是：%s。", periodName, len(shown), measure, noun, strings.Join(titles, "；")))
}

func reportSectionSummary(section map[string]any, availability map[string]V1ReportAvailabilityRecord) *string {
	for _, entry := range reportArray(section["storyRefs"]) {
		id := reportString(entry["itemId"])
		if id == "" {
			continue
		}
		if available, ok := availability[id]; ok && !available.Available {
			return nil
		}
	}
	if value := reportOptionalString(section["summary"]); value != nil && *value != "" {
		return value
	}
	return nil
}

func v1DailyCitation(raw map[string]any, availability map[string]V1ReportAvailabilityRecord, siteURL, reportURL, siteName string) V1DailyItemCitation {
	metadata := reportCitationMetadata(raw, availability)
	id := reportString(raw["itemId"])
	var itemURL *string
	if id != "" {
		itemURL = stringPointer(siteURL + "/items/" + id)
	}
	original := reportString(raw["sourceUrl"])
	if original == "" {
		original = reportString(reportObject(raw["links"])["original"])
	}
	citation := V1DailyItemCitation{Title: reportString(raw["title"]), Summary: reportStringValue(metadata["summary"]), Source: V1ReportSource{Name: reportStringValue(metadata["sourceName"])}, Links: V1ReportItemLink{AIHot: itemURL, Original: original}}
	attrURL := reportURL
	if itemURL != nil {
		attrURL = *itemURL
	}
	citation.Attribution = V1ReportAttribution{Name: siteName, URL: attrURL}
	return citation
}

func v1DailyFlash(raw map[string]any, availability map[string]V1ReportAvailabilityRecord, siteURL, reportURL string, generatedAt time.Time, siteName string) V1DailyFlashCitation {
	metadata := reportCitationMetadata(raw, availability)
	id := reportString(raw["itemId"])
	var itemURL *string
	if id != "" {
		itemURL = stringPointer(siteURL + "/items/" + id)
	}
	original := reportString(raw["sourceUrl"])
	if original == "" {
		original = reportString(reportObject(raw["links"])["original"])
	}
	published := reportStringValue(metadata["publishedAt"])
	if published == "" {
		published = formatV1Time(generatedAt)
	}
	attrURL := reportURL
	if itemURL != nil {
		attrURL = *itemURL
	}
	return V1DailyFlashCitation{Title: reportString(raw["title"]), Source: V1ReportSource{Name: reportStringValue(metadata["sourceName"])},
		Links: V1ReportItemLink{AIHot: itemURL, Original: original}, PublishedAt: published, Attribution: V1ReportAttribution{Name: siteName, URL: attrURL}}
}

func v1PeriodCitation(raw map[string]any, availability map[string]V1ReportAvailabilityRecord, siteURL, reportURL, siteName string) V1PeriodCitation {
	metadata := reportCitationMetadata(raw, availability)
	id := reportString(raw["itemId"])
	var itemURL *string
	if id != "" {
		itemURL = stringPointer(siteURL + "/items/" + id)
	}
	original := reportString(raw["sourceUrl"])
	if original == "" {
		original = reportString(reportObject(raw["links"])["original"])
	}
	attrURL := reportURL
	if itemURL != nil {
		attrURL = *itemURL
	}
	return V1PeriodCitation{Title: reportString(raw["title"]), Summary: reportStringValue(metadata["summary"]),
		Source:      V1ReportSource{Name: reportStringValue(metadata["sourceName"])},
		Links:       V1ReportItemLink{AIHot: itemURL, Original: original},
		PublishedAt: reportOptionalString(metadata["publishedAt"]),
		Attribution: V1ReportAttribution{Name: siteName, URL: attrURL}}
}

func reportCitationMetadata(raw map[string]any, availability map[string]V1ReportAvailabilityRecord) map[string]any {
	id := reportString(raw["itemId"])
	var current *V1ReportAvailabilityRecord
	if value, ok := availability[id]; ok && id != "" {
		current = &value
	}
	summary := raw["summary"]
	if summary == nil && current != nil {
		summary = current.Summary
	}
	sourceName := any(nil)
	if current != nil && current.SourceName != nil {
		sourceName = *current.SourceName
	} else if value, ok := raw["sourceName"]; ok && value != nil {
		sourceName = value
	} else {
		sourceName = reportObject(raw["source"])["name"]
	}
	sourceURL := any(nil)
	if value, ok := raw["sourceUrl"]; ok && value != nil {
		sourceURL = value
	} else {
		sourceURL = reportObject(raw["links"])["original"]
	}
	var publishedAt any
	if current != nil && current.PublishedAt != nil {
		publishedAt = formatV1Time(*current.PublishedAt)
	} else if value := reportString(raw["publishedAt"]); value != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			publishedAt = formatV1Time(parsed)
		} else {
			publishedAt = value
		}
	}
	name := reportStringValue(sourceName)
	return map[string]any{"summary": summary, "sourceName": name, "sourceUrl": sourceURL, "publishedAt": publishedAt}
}

func reportAvailable(item map[string]any, availability map[string]V1ReportAvailabilityRecord) bool {
	id := reportString(item["itemId"])
	if id == "" {
		return true
	}
	value, exists := availability[id]
	return !exists || value.Available
}

func unavailableReportIDs(availability map[string]V1ReportAvailabilityRecord) map[string]struct{} {
	result := make(map[string]struct{})
	for id, record := range availability {
		if !record.Available {
			result[id] = struct{}{}
		}
	}
	return result
}

func unavailableReportHeadlines(ctx context.Context, repository V1ReportRepository, rows []V1ReportIndexRecord, kind string, now time.Time, siteName string) (map[string]struct{}, error) {
	reports := make([][]map[string]any, 0, len(rows))
	for _, row := range rows {
		reports = append(reports, reportLeadCandidates(row.Content, kind, siteName))
	}
	gone := make(map[string]struct{})
	checked := make(map[string]struct{})
	for {
		candidates := make([]string, 0, len(rows))
		for _, entries := range reports {
			var candidate string
			for _, entry := range entries {
				id := reportString(entry["itemId"])
				if id == "" {
					break
				}
				if _, withdrawn := gone[id]; !withdrawn {
					candidate = id
					break
				}
			}
			if candidate == "" {
				continue
			}
			if _, wasChecked := checked[candidate]; !wasChecked {
				candidates = append(candidates, candidate)
			}
		}
		candidates = uniqueStrings(candidates)
		if len(candidates) == 0 {
			return gone, nil
		}
		availability, err := repository.V1ReportAvailability(ctx, candidates, now)
		if err != nil {
			return nil, err
		}
		for _, id := range candidates {
			checked[id] = struct{}{}
			if item, exists := availability[id]; exists && !item.Available {
				gone[id] = struct{}{}
			}
		}
	}
}

func loadV1ReportAvailability(ctx context.Context, repository V1ReportRepository, ids []string, now time.Time) (map[string]V1ReportAvailabilityRecord, error) {
	ids = uniqueStrings(ids)
	result := make(map[string]V1ReportAvailabilityRecord, len(ids))
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		batch, err := repository.V1ReportAvailability(ctx, ids[start:end], now)
		if err != nil {
			return nil, err
		}
		for id, record := range batch {
			result[id] = record
		}
	}
	return result, nil
}

func dailyItems(content map[string]any) []map[string]any {
	items := make([]map[string]any, 0)
	for _, section := range reportArray(content["sections"]) {
		items = append(items, reportArray(section["items"])...)
	}
	items = append(items, reportArray(content["flashes"])...)
	return items
}

func periodicItems(content map[string]any) []map[string]any {
	items := make([]map[string]any, 0)
	for _, theme := range reportArray(content["themes"]) {
		items = append(items, reportArray(theme["storyRefs"])...)
	}
	return items
}

func reportEntries(content map[string]any, kind string) []map[string]any {
	if kind == "daily" {
		items := make([]map[string]any, 0)
		for _, section := range reportArray(content["sections"]) {
			items = append(items, reportArray(section["items"])...)
		}
		return items
	}
	return periodicItems(content)
}

func reportItemIDs(items []map[string]any, includeRelated bool) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := reportString(item["itemId"]); id != "" {
			ids = append(ids, id)
		}
		if includeRelated {
			for _, related := range reportArray(item["related"]) {
				if id := reportString(related["itemId"]); id != "" {
					ids = append(ids, id)
				}
			}
		}
	}
	return uniqueStrings(ids)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func reportArray(value any) []map[string]any {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func reportObject(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

func reportString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func reportJSString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func reportStringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if text, ok := value.(*string); ok && text != nil {
		return *text
	}
	return ""
}

func reportOptionalString(value any) *string {
	if text, ok := value.(string); ok {
		return &text
	}
	if text, ok := value.(*string); ok {
		return text
	}
	return nil
}

func stringPointer(value string) *string { return &value }

func reportURL(baseURL, kind, key string) string {
	switch kind {
	case "daily":
		return baseURL + "/daily/" + url.PathEscape(key)
	case "weekly", "monthly":
		return baseURL + "/" + kind + "/" + url.PathEscape(key)
	default:
		return baseURL
	}
}

func reportPeriodRange(kind, key string) (string, string, bool) {
	if kind == "monthly" {
		parsed, err := time.Parse("2006-01", key)
		if err != nil || parsed.Format("2006-01") != key {
			return "", "", false
		}
		start := time.Date(parsed.Year(), parsed.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 1, -1)
		return start.Format("2006-01-02"), end.Format("2006-01-02"), true
	}
	if kind != "weekly" || len(key) != 8 || key[4] != '-' || key[5] != 'W' {
		return "", "", false
	}
	var year, week int
	if _, err := fmt.Sscanf(key, "%4d-W%2d", &year, &week); err != nil || year < 1 || week < 1 || week > 53 || fmt.Sprintf("%04d-W%02d", year, week) != key {
		return "", "", false
	}
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday())+6)%7)).AddDate(0, 0, (week-1)*7)
	isoYear, isoWeek := monday.ISOWeek()
	if isoYear != year || isoWeek != week {
		return "", "", false
	}
	return monday.Format("2006-01-02"), monday.AddDate(0, 0, 6).Format("2006-01-02"), true
}

func ReportPeriodRange(kind, key string) (string, string, bool) {
	return reportPeriodRange(kind, key)
}
