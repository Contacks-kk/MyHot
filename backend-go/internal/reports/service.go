package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/modelcall"
)

type ModelClient interface {
	ResolveKey(context.Context, string) (string, error)
	Call(context.Context, modelcall.Request) (modelcall.Result, error)
	Reject(context.Context, int64, string) error
}

type Config struct {
	SiteName       string
	DailyTime      string
	WeeklyTime     string
	MonthlyTime    string
	EntryMeasure   string
	EntryNoun      string
	Categories     []Category
	Entities       map[string]interests.Entity
	Reporting      ReportingConfig
	Prompts        PromptFiles
	CommunityFeeds interests.PublicationSettings
}

type Category struct {
	Key        string
	Section    string
	Commentary bool
}

type Service struct {
	repository   ReportRepository
	models       ModelClient
	config       Config
	sections     map[string]string
	sectionOrder []string
	companyNames [][]string
	plainTerms   map[string]struct{}
	location     *time.Location
	now          func() time.Time
}

func NewService(repository ReportRepository, models ModelClient, config Config) (*Service, error) {
	byCategory, order, err := validateSections(config.Categories)
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	companyNames := make([][]string, 0, len(config.Entities))
	for _, entity := range config.Entities {
		names := []string{entity.Name}
		names = append(names, entity.Aliases...)
		names = append(names, entity.OtherNames...)
		for i := range names {
			names[i] = strings.ToLower(names[i])
		}
		companyNames = append(companyNames, names)
	}
	plain := make(map[string]struct{}, len(config.Reporting.PlainTerms)+1)
	for _, term := range config.Reporting.PlainTerms {
		plain[strings.ToLower(term)] = struct{}{}
	}
	plain[strings.ToLower(config.SiteName)] = struct{}{}
	return &Service{repository: repository, models: models, config: config, sections: byCategory, sectionOrder: order,
		companyNames: companyNames, plainTerms: plain, location: location, now: time.Now}, nil
}

func (s *Service) ComposeDaily(ctx context.Context, date string, reason *string) (map[string]any, error) {
	previous, err := s.repository.SavedReport(ctx, "daily", date)
	if err != nil {
		return nil, err
	}
	if previous != nil && reason == nil {
		return map[string]any{"key": date, "entries": previous.Entries}, nil
	}
	end, err := beijingAt(date, s.config.DailyTime, s.location)
	if err != nil {
		return nil, err
	}
	start := end.Add(-24 * time.Hour)
	edition, err := s.dailyEdition(ctx, date, start, end)
	if err != nil {
		return nil, err
	}
	if len(edition.Entries) == 0 {
		return nil, fmt.Errorf("daily %s: no selected items in its window", date)
	}
	arranged := arrangeDaily(edition.Entries, s.sections, s.categories())
	if len(arranged.Main) == 0 {
		return nil, fmt.Errorf("daily %s: no main entries in its window", date)
	}
	lead := arranged.Main[0].Entry
	sections := make([]map[string]any, 0, len(s.sectionOrder))
	for _, label := range s.sectionOrder {
		items := make([]Entry, 0)
		for _, entry := range arranged.Main {
			if sectionOf(entry.Category, s.sections, s.sectionOrder) == label {
				items = append(items, entry.Entry)
			}
		}
		if len(items) > 0 {
			sections = append(sections, map[string]any{"label": label, "items": items})
		}
	}
	highlights := make([]string, 0, min(3, max(0, len(arranged.Main)-1)))
	for _, entry := range arranged.Main[1:min(4, len(arranged.Main))] {
		highlights = append(highlights, entry.Entry.ItemID)
	}
	stats := map[string]int{}
	for key, value := range edition.Stats {
		stats[key] = value
	}
	for key, value := range arranged.Stats {
		stats[key] = value
	}
	content := map[string]any{
		"date": date, "lead": map[string]string{"title": lead.Title, "leadParagraph": lead.Summary}, "leadItemId": lead.ItemID,
		"highlights": highlights, "sections": sections, "flashes": entryValues(arranged.Flashes), "metrics": s.dailyMetrics(arranged.Main),
		"windowStart": start.UTC().Format("2006-01-02T15:04:05.000Z"), "windowEnd": end.UTC().Format("2006-01-02T15:04:05.000Z"),
		"generator": map[string]any{"version": s.config.Prompts.Version, "selectedReports": stats["selectedReports"], "facts": stats["facts"],
			"repeatsSuppressed": stats["repeatsSuppressed"], "fillIns": stats["fillIns"], "events": stats["events"],
			"roundupsFolded": stats["roundupsFolded"], "followUpsAsFlashes": stats["followUpsAsFlashes"], "left": stats["left"]},
	}
	_, err = s.repository.SaveReport(ctx, "daily", date, start, end, content, reason, nil, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"key": date, "entries": len(arranged.Main)}, nil
}

func (s *Service) ComposeWeekly(ctx context.Context, label string, reason *string) (map[string]any, error) {
	start, end, ok := isoWeekRange(label)
	if !ok {
		return nil, fmt.Errorf("bad week label %s", label)
	}
	return s.composePeriod(ctx, "weekly", label, start, end, reason)
}

func (s *Service) ComposeMonthly(ctx context.Context, label string, reason *string) (map[string]any, error) {
	start, end, ok := monthRange(label)
	if !ok {
		return nil, fmt.Errorf("bad month label %s", label)
	}
	return s.composePeriod(ctx, "monthly", label, start, end, reason)
}

// ComposeDue fills all missing issues through the newest Beijing edition that is due.
// It limits each scan so a long outage can continue on the next scheduled run.
func (s *Service) ComposeDue(ctx context.Context, now time.Time, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 8
	}
	daily, err := dueDaily(now, s.config.DailyTime, s.location)
	if err != nil {
		return nil, err
	}
	weekly, err := dueWeekly(now, s.config.WeeklyTime, s.location)
	if err != nil {
		return nil, err
	}
	monthly, err := dueMonthly(now, s.config.MonthlyTime, s.location)
	if err != nil {
		return nil, err
	}
	generated, failed := make([]string, 0), make([]string, 0)
	types := []struct {
		kind string
		due  string
		next func(string) (string, error)
		run  func(context.Context, string, *string) (map[string]any, error)
	}{
		{kind: "daily", due: daily, next: func(key string) (string, error) { return addDate(key, 1) }, run: s.ComposeDaily},
		{kind: "weekly", due: weekly, next: nextWeek, run: s.ComposeWeekly},
		{kind: "monthly", due: monthly, next: nextMonth, run: s.ComposeMonthly},
	}
kinds:
	for _, reportType := range types {
		keys, err := s.repository.ReportKeys(ctx, reportType.kind)
		if err != nil {
			return nil, err
		}
		first := reportType.due
		if len(keys) > 0 {
			first = keys[0]
		}
		for key := first; key <= reportType.due; {
			if ctx.Err() != nil || len(generated) >= limit {
				break kinds
			}
			if !contains(keys, key) {
				if _, err := reportType.run(ctx, key, nil); err != nil {
					failed = append(failed, reportType.kind+":"+key)
					log.Printf(`{"level":"error","msg":"report failed","report":%q,"error":%q}`, reportType.kind+":"+key, safeError(err))
				} else {
					generated = append(generated, reportType.kind+":"+key)
				}
			}
			next, err := reportType.next(key)
			if err != nil {
				return map[string]any{"generated": generated, "failed": failed}, err
			}
			if next <= key {
				return map[string]any{"generated": generated, "failed": failed}, fmt.Errorf("report schedule did not advance from %s", key)
			}
			key = next
		}
	}
	result := map[string]any{"generated": generated, "failed": failed}
	if len(failed) > 0 {
		message := "reports: " + strings.Join(failed, ", ") + " failed"
		if len(generated) > 0 {
			message += "; " + strings.Join(generated, ", ") + " written"
		}
		return result, errors.New(message)
	}
	return result, nil
}

func (s *Service) composePeriod(ctx context.Context, kind, key, startDate, endDate string, reason *string) (map[string]any, error) {
	previous, err := s.repository.SavedReport(ctx, kind, key)
	if err != nil {
		return nil, err
	}
	if previous != nil && reason == nil {
		return map[string]any{"key": key, "entries": previous.Entries}, nil
	}
	prevDay, err := addDate(startDate, -1)
	if err != nil {
		return nil, err
	}
	start, err := beijingAt(prevDay, s.config.DailyTime, s.location)
	if err != nil {
		return nil, err
	}
	end, err := beijingAt(endDate, s.config.DailyTime, s.location)
	if err != nil {
		return nil, err
	}
	period, err := s.periodEntries(ctx, startDate, endDate)
	if err != nil {
		return nil, err
	}
	topLimit := weeklyEventLimit
	if kind == "monthly" {
		topLimit = monthlyEventLimit
	}
	top := period.Entries[:min(topLimit, len(period.Entries))]
	if len(top) == 0 {
		return nil, fmt.Errorf("%s %s: no daily entries in the period", kind, key)
	}
	selectedRows, err := s.repository.PeriodReports(ctx, start, end)
	if err != nil {
		return nil, err
	}
	selected := s.candidates(selectedRows)
	groups := make([]promptGroup, 0, len(s.sectionOrder))
	corpusParts := []string{startDate + " " + endDate}
	for _, candidate := range top {
		corpusParts = append(corpusParts, candidate.Title+" "+candidate.Summary)
	}
	for _, section := range s.sectionOrder {
		items := make([]Candidate, 0)
		for _, candidate := range top {
			if sectionOf(candidate.Category, s.sections, s.sectionOrder) == section {
				items = append(items, candidate)
			}
		}
		if len(items) > 0 {
			groups = append(groups, promptGroup{Label: section, Items: items})
		}
	}
	corpus := strings.Join(corpusParts, "\n")
	modelName := ""
	written := periodWritten{Overview: "", Sections: map[string]string{}}
	var receiptID *int64
	if s.models != nil {
		modelName, err = s.models.ResolveKey(ctx, "report")
		if err != nil {
			return nil, err
		}
		modelResult, modelErr := s.writePeriod(ctx, kind, key, startDate, endDate, groups)
		if modelErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			log.Printf(`{"level":"warn","msg":"period writer failed; the issue goes out with its plain overview","report":%q,"error":%q}`, kind+":"+key, safeError(modelErr))
		} else {
			modelName = modelResult.Model
			written = modelResult.Output
			receiptID = &modelResult.ReceiptID
		}
	}
	overview := s.usable(written.Overview, corpus, periodOverviewChars(kind))
	sections := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		intro := (*string)(nil)
		if len(group.Items) >= introEvents {
			intro = s.usable(written.Sections[group.Label], corpus, 90)
		}
		refs := make([]map[string]any, 0, len(group.Items))
		for _, item := range group.Items {
			refs = append(refs, candidateJSON(item))
		}
		sections = append(sections, map[string]any{"heading": group.Label, "summary": intro, "storyRefs": refs})
	}
	lead := top[0]
	highlights := make([]string, 0, min(3, max(0, len(top)-1)))
	for _, entry := range top[1:min(4, len(top))] {
		highlights = append(highlights, entry.ItemID)
	}
	storyOrder := make([]string, 0, len(top))
	for _, entry := range top {
		storyOrder = append(storyOrder, entry.ItemID)
	}
	selectedCount := len(selected)
	titleNoun := "周报"
	if kind == "monthly" {
		titleNoun = "月报"
	}
	content := map[string]any{
		"kind": kind, "title": s.config.SiteName + " " + titleNoun + " · " + key,
		"periodStart": startDate, "periodEnd": endDate, "headline": lead.Title, "leadItemId": lead.ItemID,
		"highlights": highlights, "overview": overview, "themes": sections, "storyOrder": storyOrder,
		"metrics":   map[string]int{"totalStories": len(top), "selectedCount": selectedCount, "reportsCovered": period.Issues},
		"generator": map[string]any{"version": s.config.Prompts.Version, "model": nilIfEmpty(modelName), "written": overview != nil},
	}
	if kind == "weekly" {
		content["isoLabel"] = key
	} else {
		content["monthLabel"] = key
	}
	receipts := []int64{}
	if receiptID != nil {
		receipts = append(receipts, *receiptID)
	}
	_, err = s.repository.SaveReport(ctx, kind, key, start, end, content, reason, stringPointerOrNil(modelName), receipts)
	if err != nil {
		return nil, err
	}
	return map[string]any{"key": key, "entries": len(top)}, nil
}

func (s *Service) writePeriod(ctx context.Context, kind, key, startDate, endDate string, groups []promptGroup) (periodModelResult, error) {
	kindName, span, sentences, chars := "周报", "一周", "三句话", "160"
	if kind == "monthly" {
		kindName, span, sentences, chars = "月报", "个月", "三到四句话", "240"
	}
	sections := make([]string, 0, len(groups))
	lines := make([]string, 0)
	for _, group := range groups {
		lines = append(lines, "【"+group.Label+"】")
		for _, entry := range group.Items {
			lines = append(lines, "- "+entry.Title+"｜"+truncateUTF16(entry.Summary, 140))
		}
		if len(group.Items) >= introEvents {
			sections = append(sections, group.Label)
		}
	}
	system, err := s.config.Prompts.PeriodSystem(s.config.SiteName, kindName, span, sentences, chars, sections)
	if err != nil {
		return periodModelResult{}, err
	}
	temperature := 0.3
	result, err := s.models.Call(ctx, modelcall.Request{
		Capability: "report", Purpose: "report_" + kind, Subject: "report:" + kind + ":" + key,
		PromptVersion: s.config.Prompts.Version, System: system,
		User:        "本期：" + startDate + " 至 " + endDate + "\n" + strings.Join(lines, "\n"),
		Temperature: &temperature, MaxTokens: 2500,
	})
	if err != nil {
		return periodModelResult{}, err
	}
	output, parseErr := parsePeriodOutput(result.Completion.Text)
	if parseErr != nil {
		_ = s.models.Reject(context.WithoutCancel(ctx), result.ReceiptID, "unusable output: "+parseErr.Error())
		return periodModelResult{}, parseErr
	}
	return periodModelResult{Model: result.Model, ReceiptID: result.ReceiptID, Output: output}, nil
}

func parsePeriodOutput(text string) (periodWritten, error) {
	text = escapeControlChars(text)
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return periodWritten{}, errors.New("model report response is not JSON")
	}
	var raw struct {
		Overview any `json:"overview"`
		Sections any `json:"sections"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err != nil {
		return periodWritten{}, err
	}
	output := periodWritten{Overview: "", Sections: map[string]string{}}
	if value, ok := raw.Overview.(string); ok && utf16Length(value) <= 1500 {
		output.Overview = value
	}
	if values, ok := raw.Sections.(map[string]any); ok {
		for key, rawValue := range values {
			if value, ok := rawValue.(string); ok {
				if utf16Length(value) > 600 {
					value = ""
				}
				output.Sections[key] = value
			}
		}
	}
	return output, nil
}

func escapeControlChars(value string) string {
	var output strings.Builder
	inside, escaped := false, false
	for _, char := range value {
		if inside {
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inside = false
			} else if char < ' ' {
				switch char {
				case '\n':
					output.WriteString(`\n`)
				case '\r':
					output.WriteString(`\r`)
				case '\t':
					output.WriteString(`\t`)
				default:
					fmt.Fprintf(&output, `\u%04x`, char)
				}
				continue
			}
		} else if char == '"' {
			inside = true
		}
		output.WriteRune(char)
	}
	return output.String()
}

func (s *Service) usable(value, corpus string, limit int) *string {
	fit := fitted(value, limit)
	if fit == "" || !s.grounded(fit, corpus) {
		return nil
	}
	return &fit
}

func (s *Service) grounded(text, corpus string) bool {
	known := strings.ToLower(corpus)
	companyPresent := make([]bool, len(s.companyNames))
	for i, names := range s.companyNames {
		for _, name := range names {
			if name != "" && strings.Contains(known, name) {
				companyPresent[i] = true
				break
			}
		}
	}
	named := func(word string) bool {
		if _, ok := s.plainTerms[word]; ok || strings.Contains(known, word) {
			return true
		}
		for _, names := range s.companyNames {
			for _, name := range names {
				if name == word {
					return true
				}
			}
		}
		return false
	}
	for _, word := range latinWords.FindAllString(text, -1) {
		if !containsUpperOrDigit(word) {
			continue
		}
		word = strings.TrimRight(word, ".+-")
		if !named(strings.ToLower(word)) {
			return false
		}
	}
	for _, figure := range numbers.FindAllString(text, -1) {
		if len([]rune(figure)) >= 3 || strings.ContainsAny(figure, ".%") {
			if !strings.Contains(known, figure) {
				return false
			}
		}
	}
	lower := strings.ToLower(text)
	for i, names := range s.companyNames {
		for _, name := range names {
			if containsHan(name) && strings.Contains(lower, name) && !companyPresent[i] {
				return false
			}
		}
	}
	return true
}

func containsUpperOrDigit(value string) bool {
	for _, char := range value {
		if unicode.IsUpper(char) || unicode.IsDigit(char) {
			return true
		}
	}
	return false
}

func containsHan(value string) bool {
	for _, char := range value {
		if unicode.In(char, unicode.Han) {
			return true
		}
	}
	return false
}

var latinWords = regexp.MustCompile(`[A-Za-z][A-Za-z0-9.+-]*`)
var numbers = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?%?`)

func fitted(text string, maxChars int) string {
	runes := []rune(strings.TrimSpace(text))
	var output []rune
	for index := 0; index < len(runes); {
		end := index
		for end < len(runes) && runes[end] != '。' && runes[end] != '！' && runes[end] != '？' {
			end++
		}
		if end < len(runes) {
			end++
			for end < len(runes) && (runes[end] == '。' || runes[end] == '！' || runes[end] == '？') {
				end++
			}
			for end < len(runes) && strings.ContainsRune("」”’）", runes[end]) {
				end++
			}
		}
		if len(output)+end-index > maxChars {
			break
		}
		output = append(output, runes[index:end]...)
		index = end
	}
	return strings.TrimSpace(string(output))
}

func (s *Service) dailyEdition(ctx context.Context, date string, start, end time.Time) (Edition, error) {
	from, err := addDate(date, -7)
	if err != nil {
		return Edition{}, err
	}
	var memory DailyMemory
	var selectedRows []ReportRow
	var missedIDs []int64
	var failures []error
	var memoryErr, selectedErr, missedErr error
	var wait sync.WaitGroup
	wait.Add(3)
	go func() { defer wait.Done(); memory, memoryErr = s.dailyMemory(ctx, date, from) }()
	go func() { defer wait.Done(); selectedRows, selectedErr = s.repository.PeriodReports(ctx, start, end) }()
	go func() {
		defer wait.Done()
		missedIDs, missedErr = s.repository.MissedFacts(ctx, start, end, s.config.CommunityFeeds)
	}()
	wait.Wait()
	for _, failure := range []error{memoryErr, selectedErr, missedErr} {
		if failure != nil {
			failures = append(failures, failure)
		}
	}
	if len(failures) > 0 {
		return Edition{}, errors.Join(failures...)
	}
	selectedGroups := groupRows(selectedRows, factKey)
	raw := make([]rawFact, 0, len(selectedGroups)+len(missedIDs))
	for key, rows := range selectedGroups {
		raw = append(raw, rawFact{key: key, rows: rows, selected: true})
	}
	missedRows, err := s.repository.ReportsForFacts(ctx, missedIDs, end, s.config.CommunityFeeds)
	if err != nil {
		return Edition{}, err
	}
	for key, rows := range groupRows(missedRows, factKey) {
		official := false
		for _, row := range rows {
			official = official || priority(row, s.config.Entities) < 3
		}
		if official {
			raw = append(raw, rawFact{key: key, rows: rows})
		}
	}
	kept := make([]rawFact, 0, len(raw))
	for _, fact := range raw {
		if _, exists := memory.Keys[fact.key]; exists {
			continue
		}
		repeat := false
		for _, row := range fact.rows {
			if _, exists := memory.Keys["a:"+row.ID]; exists {
				repeat = true
				break
			}
		}
		if !repeat {
			kept = append(kept, fact)
		}
	}
	factIDs := make([]int64, 0)
	seenFact := make(map[int64]struct{})
	for _, fact := range kept {
		for _, row := range fact.rows {
			if row.FactID != nil {
				if _, exists := seenFact[*row.FactID]; !exists {
					seenFact[*row.FactID] = struct{}{}
					factIDs = append(factIDs, *row.FactID)
				}
			}
		}
	}
	sourcesByFact, err := s.repository.FactSources(ctx, factIDs, end)
	if err != nil {
		return Edition{}, err
	}
	facts := make([]rawFact, 0, len(kept))
	for _, fact := range kept {
		sources := make(map[string]struct{})
		var at time.Time
		for _, row := range fact.rows {
			if row.FactID != nil {
				for _, source := range sourcesByFact[*row.FactID] {
					sources[source] = struct{}{}
				}
			}
			sources[row.SourceID] = struct{}{}
			if at.IsZero() || row.TimelineAt.Before(at) {
				at = row.TimelineAt
			}
		}
		fact.sources = keysOf(sources)
		fact.at = at
		facts = append(facts, fact)
	}
	events := groupFacts(facts)
	storyIDs := make([]int64, 0)
	looseIDs := make([]string, 0)
	for _, event := range events {
		story := event[0].rows[0].StoryID
		if story == nil {
			for _, fact := range event {
				for _, row := range fact.rows {
					looseIDs = append(looseIDs, row.ID)
				}
			}
		} else {
			storyIDs = append(storyIDs, *story)
		}
	}
	participants, err := s.repository.StoryParticipants(ctx, uniqueInt64(storyIDs), start, end, s.config.CommunityFeeds)
	if err != nil {
		return Edition{}, err
	}
	mentions, err := s.repository.MentionedStories(ctx, uniqueStrings(looseIDs))
	if err != nil {
		return Edition{}, err
	}
	entries := make([]EditionEntry, 0, len(events))
	for _, event := range events {
		entry, err := s.editionEntry(event, memory, participants, mentions)
		if err != nil {
			return Edition{}, err
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Importance != entries[j].Importance {
			return entries[i].Importance > entries[j].Importance
		}
		return entries[i].Entry.ItemID < entries[j].Entry.ItemID
	})
	fillIns := 0
	for _, fact := range kept {
		if !fact.selected {
			fillIns++
		}
	}
	return Edition{Entries: entries, Stats: map[string]int{"selectedReports": len(selectedRows), "facts": len(raw), "repeatsSuppressed": len(raw) - len(kept), "fillIns": fillIns, "events": len(entries)}}, nil
}

func (s *Service) dailyMemory(ctx context.Context, date, fromDate string) (DailyMemory, error) {
	issues, err := s.repository.DailyMemoryIssues(ctx, date, fromDate)
	if err != nil {
		return DailyMemory{}, err
	}
	memory := DailyMemory{Keys: make(map[string]struct{}), Stories: make(map[int64]PreviousStory)}
	cited := make(map[string]PreviousStory)
	for _, issue := range issues {
		var content dailyIssueContent
		if err := json.Unmarshal(issue.Content, &content); err != nil {
			return DailyMemory{}, err
		}
		entries := content.Entries
		entries = append(entries, content.Flashes...)
		for _, section := range content.Sections {
			entries = append(entries, section.Items...)
		}
		for _, entry := range entries {
			if entry.FactID != nil && *entry.FactID != "" {
				memory.Keys[*entry.FactID] = struct{}{}
			}
			if entry.ItemID == "" {
				continue
			}
			memory.Keys["a:"+entry.ItemID] = struct{}{}
			if _, exists := cited[entry.ItemID]; !exists {
				cited[entry.ItemID] = PreviousStory{Key: issue.Key, Title: entry.Title}
			}
			for _, related := range entry.Related {
				if related.FactID != nil && *related.FactID != "" {
					memory.Keys[*related.FactID] = struct{}{}
				}
				if related.ItemID != "" {
					memory.Keys["a:"+related.ItemID] = struct{}{}
					if _, exists := cited[related.ItemID]; !exists {
						cited[related.ItemID] = PreviousStory{Key: issue.Key, Title: related.Title}
					}
				}
			}
		}
	}
	if len(cited) == 0 {
		return memory, nil
	}
	ids := make([]string, 0, len(cited))
	for id := range cited {
		ids = append(ids, id)
	}
	current, err := s.repository.CurrentFactsByArticles(ctx, ids)
	if err != nil {
		return DailyMemory{}, err
	}
	for _, article := range current {
		if article.FactPublicID != nil {
			memory.Keys[*article.FactPublicID] = struct{}{}
		}
		if article.StoryID == nil {
			continue
		}
		at := cited[article.ID]
		previous, exists := memory.Stories[*article.StoryID]
		if !exists || previous.Key < at.Key {
			memory.Stories[*article.StoryID] = at
		}
	}
	return memory, nil
}

func (s *Service) editionEntry(facts []rawFact, memory DailyMemory, participants map[int64]int, mentions []Mention) (EditionEntry, error) {
	ordered := append([]rawFact(nil), facts...)
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i].sources) != len(ordered[j].sources) {
			return len(ordered[i].sources) > len(ordered[j].sources)
		}
		if !ordered[i].at.Equal(ordered[j].at) {
			return ordered[i].at.Before(ordered[j].at)
		}
		return ordered[i].key < ordered[j].key
	})
	representatives := make([]ReportRow, 0, len(ordered))
	for _, fact := range ordered {
		representatives = append(representatives, pickRepresentative(fact.rows, s.config.Entities))
	}
	if len(representatives) == 0 {
		return EditionEntry{}, errors.New("report event has no reports")
	}
	representative := representatives[0]
	sourceIDs := make(map[string]struct{})
	scoreMax := score(representative.Score)
	official := false
	allSelected := true
	for _, fact := range facts {
		for _, source := range fact.sources {
			sourceIDs[source] = struct{}{}
		}
		allSelected = allSelected && fact.selected
		if fact.selected {
			for _, row := range fact.rows {
				scoreMax = max(scoreMax, score(row.Score))
			}
		}
		for _, row := range fact.rows {
			official = official || priority(row, s.config.Entities) < 3
		}
	}
	ids := make(map[string]struct{})
	for _, fact := range facts {
		for _, row := range fact.rows {
			ids[row.ID] = struct{}{}
		}
	}
	var sourceStory = representative.StoryID
	var previous *PreviousStory
	if sourceStory != nil {
		if current, ok := memory.Stories[*sourceStory]; ok {
			previous = &current
		}
	}
	entry := reportEntry(representative, s.config.Entities)
	entry.Sources = len(sourceIDs)
	if len(representatives) > 1 {
		entry.Related = make([]RelatedEntry, 0, len(representatives)-1)
		for _, related := range representatives[1:] {
			entry.Related = append(entry.Related, relatedEntry(reportEntry(related, s.config.Entities)))
		}
	}
	if previous != nil {
		entry.FollowUp = previous.Key
	}
	if !allSelected {
		entry.FillIn = true
	}
	mentionedStoryIDs := make(map[int64]struct{})
	if sourceStory == nil {
		for _, mention := range mentions {
			if _, exists := ids[mention.ArticleID]; exists {
				mentionedStoryIDs[mention.StoryID] = struct{}{}
			}
		}
	}
	participantCount := len(sourceIDs)
	if sourceStory != nil && participants[*sourceStory] > 0 {
		participantCount = participants[*sourceStory]
	}
	return EditionEntry{Entry: entry, Category: representative.Category, Tags: representative.Tags, StoryID: sourceStory,
		Mentions: mentionedStoryIDs, SourceIDs: sourceIDs, Authority: priority(representative, s.config.Entities),
		Importance: importance(scoreMax, participantCount, official, previous != nil), Previous: previous}, nil
}

func (s *Service) periodEntries(ctx context.Context, startDate, endDate string) (periodResult, error) {
	issues, err := s.repository.DailyIssues(ctx, startDate, endDate)
	if err != nil {
		return periodResult{}, err
	}
	carried := make([]carriedEntry, 0)
	for _, issue := range issues {
		var content dailyIssueContent
		if err := json.Unmarshal(issue.Content, &content); err != nil {
			return periodResult{}, err
		}
		for _, section := range content.Sections {
			for _, item := range section.Items {
				if item.ItemID != "" {
					carried = append(carried, carriedEntry{ID: item.ItemID, Key: issue.Key, Sources: max(1, item.Sources), Lead: content.LeadItemID == item.ItemID, Highlight: contains(content.Highlights, item.ItemID)})
				}
			}
		}
	}
	if len(carried) == 0 {
		return periodResult{Entries: []Candidate{}, Issues: len(issues)}, nil
	}
	idSet := make(map[string]struct{})
	for _, entry := range carried {
		idSet[entry.ID] = struct{}{}
	}
	rows, err := s.repository.CurrentReportsByIDs(ctx, sortedStrings(idSet), false)
	if err != nil {
		return periodResult{}, err
	}
	byID := make(map[string]ReportRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	events := make(map[string][]carriedEntry)
	for _, entry := range carried {
		row, exists := byID[entry.ID]
		if !exists {
			continue
		}
		key := "a:" + row.ID
		if row.StoryID != nil {
			key = "s:" + strconv.FormatInt(*row.StoryID, 10)
		}
		events[key] = append(events[key], entry)
	}
	ranked := make([]rankedCandidate, 0, len(events))
	for _, group := range events {
		sort.Slice(group, func(i, j int) bool {
			if group[i].Sources != group[j].Sources {
				return group[i].Sources > group[j].Sources
			}
			if group[i].Key != group[j].Key {
				return group[i].Key < group[j].Key
			}
			return priority(byID[group[i].ID], s.config.Entities) < priority(byID[group[j].ID], s.config.Entities)
		})
		main := byID[group[0].ID]
		maxSources, lead, highlight := 0, false, false
		keys := make(map[string]struct{})
		for _, item := range group {
			maxSources = max(maxSources, item.Sources)
			lead = lead || item.Lead
			highlight = highlight || item.Highlight
			keys[item.Key] = struct{}{}
		}
		rank := float64(score(main.Score)) + 5*math.Log2(1+float64(maxSources))
		if lead {
			rank += 6
		}
		if highlight {
			rank += 3
		}
		rank += 4 * float64(max(0, len(keys)-1))
		ranked = append(ranked, rankedCandidate{Candidate: Candidate{Entry: reportEntry(main, s.config.Entities), Category: main.Category, FactKey: factKey(main)}, Rank: rank})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Rank != ranked[j].Rank {
			return ranked[i].Rank > ranked[j].Rank
		}
		return ranked[i].Candidate.ItemID < ranked[j].Candidate.ItemID
	})
	entries := make([]Candidate, len(ranked))
	for i := range ranked {
		entries[i] = ranked[i].Candidate
	}
	return periodResult{Entries: entries, Issues: len(issues)}, nil
}

func (s *Service) candidates(rows []ReportRow) []Candidate {
	groups := groupRows(rows, factKey)
	entries := make([]Candidate, 0, len(groups))
	for key, members := range groups {
		representative := pickRepresentative(members, s.config.Entities)
		entries = append(entries, Candidate{Entry: reportEntry(representative, s.config.Entities), Category: representative.Category, FactKey: key})
	}
	sort.SliceStable(entries, func(i, j int) bool { return score(entries[i].Score) > score(entries[j].Score) })
	return entries
}

func (s *Service) dailyMetrics(entries []EditionEntry) map[string]int {
	sources := make(map[string]struct{})
	metrics := map[string]int{"totalEvents": len(entries), "firstPartyEvents": 0}
	for _, item := range entries {
		sources[item.Entry.SourceID] = struct{}{}
		for _, related := range item.Entry.Related {
			sources[related.SourceID] = struct{}{}
		}
		if item.Entry.FirstParty {
			metrics["firstPartyEvents"]++
		}
		if release := s.config.Reporting.Release; release != nil && stringValue(item.Category) == release.Category && contains(item.Tags, release.Tag) && item.Previous == nil && item.Authority < 3 {
			metrics["modelsReleased"]++
		}
	}
	metrics["sourcesCount"] = len(sources)
	return metrics
}

func (s *Service) categories() []Category { return s.config.Categories }

func groupRows(rows []ReportRow, key func(ReportRow) string) map[string][]ReportRow {
	groups := make(map[string][]ReportRow)
	for _, row := range rows {
		groups[key(row)] = append(groups[key(row)], row)
	}
	return groups
}

func groupFacts(facts []rawFact) [][]rawFact {
	groups := make(map[string][]rawFact)
	for _, fact := range facts {
		row := fact.rows[0]
		key := "f:" + fact.key
		if row.StoryID != nil {
			key = "s:" + strconv.FormatInt(*row.StoryID, 10)
		}
		groups[key] = append(groups[key], fact)
	}
	keys := sortedMapKeys(groups)
	result := make([][]rawFact, 0, len(groups))
	for _, key := range keys {
		result = append(result, groups[key])
	}
	return result
}

func keysOf(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	return keys
}

func uniqueInt64(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func sortedStrings(values map[string]struct{}) []string {
	result := keysOf(values)
	sort.Strings(result)
	return result
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func entryValues(entries []EditionEntry) []Entry {
	values := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		values = append(values, entry.Entry)
	}
	return values
}

func candidateJSON(candidate Candidate) map[string]any {
	data, _ := json.Marshal(candidate.Entry)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

type promptGroup struct {
	Label string
	Items []Candidate
}

type periodWritten struct {
	Overview string
	Sections map[string]string
}

type periodModelResult struct {
	Model     string
	ReceiptID int64
	Output    periodWritten
}

type periodResult struct {
	Entries []Candidate
	Issues  int
}

type rankedCandidate struct {
	Candidate Candidate
	Rank      float64
}

type carriedEntry struct {
	ID        string
	Key       string
	Sources   int
	Lead      bool
	Highlight bool
}

type dailyIssueContent struct {
	LeadItemID string   `json:"leadItemId"`
	Highlights []string `json:"highlights"`
	Entries    []Entry  `json:"entries"`
	Flashes    []Entry  `json:"flashes"`
	Sections   []struct {
		Items []Entry `json:"items"`
	} `json:"sections"`
}

func periodOverviewChars(kind string) int {
	if kind == "monthly" {
		return 340
	}
	return 240
}

func truncateUTF16(value string, limit int) string {
	encoded := utf16.Encode([]rune(value))
	if len(encoded) > limit {
		encoded = encoded[:limit]
	}
	return string(utf16.Decode(encoded))
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func safeError(err error) string {
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func stringPointerOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
