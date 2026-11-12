package admincontent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ReportCorrectionConfig struct {
	SectionByCategory map[string]string
	SectionOrder      []string
	ReleaseCategory   string
	ReleaseTag        string
}

type storedReport struct {
	ID          int64
	Kind        string
	Content     json.RawMessage
	Revision    int
	GeneratedAt time.Time
}

type reportGroup map[string]any
type reportEntry map[string]any

// correctReportClassification moves a frozen report entry without composing new report text or
// making a model call. Its revision and the editor's override share the caller's transaction.
func (r *CommandsRepository) correctReportClassification(ctx context.Context, tx pgx.Tx, articleID, reason string) error {
	var category *string
	if err := tx.QueryRow(ctx, `SELECT category FROM publications WHERE article_id=$1`, articleID).Scan(&category); err != nil {
		return err
	}
	if category == nil {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id,kind,content,revision,generated_at FROM reports
		WHERE content @> jsonb_build_object('sections',jsonb_build_array(jsonb_build_object('items',jsonb_build_array(jsonb_build_object('itemId',$1)))))
		   OR content @> jsonb_build_object('themes',jsonb_build_array(jsonb_build_object('storyRefs',jsonb_build_array(jsonb_build_object('itemId',$1)))))
		ORDER BY id FOR UPDATE`, articleID)
	if err != nil {
		return err
	}
	reports := make([]storedReport, 0)
	for rows.Next() {
		var report storedReport
		if err := rows.Scan(&report.ID, &report.Kind, &report.Content, &report.Revision, &report.GeneratedAt); err != nil {
			rows.Close()
			return err
		}
		reports = append(reports, report)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, report := range reports {
		content := map[string]any{}
		decoder := json.NewDecoder(bytes.NewReader(report.Content))
		decoder.UseNumber()
		if err := decoder.Decode(&content); err != nil {
			return err
		}
		daily := report.Kind == "daily"
		groupKey, entriesKey := "themes", "storyRefs"
		labelKey := "heading"
		if daily {
			groupKey, entriesKey, labelKey = "sections", "items", "label"
		}
		groups := reportGroups(content[groupKey])
		var source reportGroup
		for _, group := range groups {
			if reportEntries(group[entriesKey], articleID) > 0 {
				source = group
				break
			}
		}
		if source == nil {
			continue
		}
		label, _ := source[labelKey].(string)
		if !containsString(r.reportCorrection.SectionOrder, label) {
			// Older hand-written theme headings are editorial prose, not taxonomy sections.
			continue
		}
		targetLabel := r.reportCorrection.SectionByCategory[*category]
		if targetLabel == "" {
			targetLabel = r.reportCorrection.SectionByCategory["industry"]
		}
		if targetLabel == "" && len(r.reportCorrection.SectionOrder) > 0 {
			targetLabel = r.reportCorrection.SectionOrder[len(r.reportCorrection.SectionOrder)-1]
		}
		if label != targetLabel {
			moving := takeReportEntries(source, entriesKey, articleID)
			target := findReportGroup(groups, labelKey, targetLabel)
			if target == nil {
				target = reportGroup{labelKey: targetLabel, entriesKey: []any{}}
				groups = append(groups, target)
			}
			target[entriesKey] = append(reportEntrySlice(target[entriesKey]), moving...)
			if !daily {
				source["summary"], target["summary"] = nil, nil
			}
		}
		groups = arrangedReportGroups(groups, labelKey, entriesKey, r.reportCorrection.SectionOrder)
		content[groupKey] = groups
		if daily && r.reportCorrection.ReleaseCategory != "" && r.reportCorrection.ReleaseTag != "" {
			if err := r.recountReleaseMetric(ctx, tx, content, groups); err != nil {
				return err
			}
		}
		before, err := canonicalJSON(report.Content)
		if err != nil {
			return err
		}
		after, err := json.Marshal(content)
		if err != nil {
			return err
		}
		if bytes.Equal(before, after) {
			continue
		}
		revisionReason := fmt.Sprintf("classification %s: %s", articleID, reason)
		if _, err := tx.Exec(ctx, `INSERT INTO report_revisions(report_id,revision,content,generated_at,reason) VALUES($1,$2,$3::jsonb,$4,$5)`, report.ID, report.Revision, report.Content, report.GeneratedAt, revisionReason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reports SET content=$2::jsonb,revision=revision+1,updated_at=now() WHERE id=$1`, report.ID, after); err != nil {
			return err
		}
	}
	return nil
}

func (r *CommandsRepository) recountReleaseMetric(ctx context.Context, tx pgx.Tx, content map[string]any, groups []reportGroup) error {
	itemIDs := make([]string, 0)
	for _, group := range groups {
		for _, value := range reportEntrySlice(group["items"]) {
			if entry, ok := value.(map[string]any); ok {
				if id, ok := entry["itemId"].(string); ok && id != "" {
					itemIDs = append(itemIDs, id)
				}
			}
		}
	}
	type publicationInfo struct {
		Category *string  `db:"category"`
		Tags     []string `db:"tags"`
	}
	info := make(map[string]publicationInfo, len(itemIDs))
	if len(itemIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT article_id,category,tags FROM publications WHERE article_id=ANY($1)`, itemIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var value publicationInfo
			if err := rows.Scan(&id, &value.Category, &value.Tags); err != nil {
				rows.Close()
				return err
			}
			info[id] = value
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	released := 0
	for _, group := range groups {
		for _, value := range reportEntrySlice(group["items"]) {
			entry, ok := value.(map[string]any)
			if !ok {
				continue
			}
			id, _ := entry["itemId"].(string)
			row, ok := info[id]
			if !ok || row.Category == nil || *row.Category != r.reportCorrection.ReleaseCategory || !containsString(row.Tags, r.reportCorrection.ReleaseTag) || truthy(entry["followUp"]) {
				continue
			}
			role, _ := entry["role"].(string)
			if truthy(entry["firstParty"]) || role == "官方" || role == "X·官方" {
				released++
			}
		}
	}
	metrics, _ := content["metrics"].(map[string]any)
	if metrics == nil {
		metrics = map[string]any{}
		content["metrics"] = metrics
	}
	metrics["modelsReleased"] = released
	return nil
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func reportGroups(value any) []reportGroup {
	items, _ := value.([]any)
	result := make([]reportGroup, 0, len(items))
	for _, item := range items {
		if group, ok := item.(map[string]any); ok {
			result = append(result, group)
		}
	}
	return result
}

func reportEntrySlice(value any) []any {
	items, _ := value.([]any)
	if items == nil {
		return []any{}
	}
	return items
}

func reportEntries(value any, itemID string) int {
	count := 0
	for _, value := range reportEntrySlice(value) {
		if entry, ok := value.(map[string]any); ok && entry["itemId"] == itemID {
			count++
		}
	}
	return count
}

func takeReportEntries(group reportGroup, key, itemID string) []any {
	entries := reportEntrySlice(group[key])
	moving := make([]any, 0, 1)
	remaining := make([]any, 0, len(entries))
	for _, value := range entries {
		entry, ok := value.(map[string]any)
		if ok && entry["itemId"] == itemID {
			moving = append(moving, value)
		} else {
			remaining = append(remaining, value)
		}
	}
	group[key] = remaining
	return moving
}

func findReportGroup(groups []reportGroup, key, label string) reportGroup {
	for _, group := range groups {
		if group[key] == label {
			return group
		}
	}
	return nil
}

func arrangedReportGroups(groups []reportGroup, labelKey, entriesKey string, order []string) []reportGroup {
	index := make(map[string]int, len(order))
	for i, label := range order {
		index[label] = i
	}
	result := make([]reportGroup, 0, len(groups))
	for _, group := range groups {
		if len(reportEntrySlice(group[entriesKey])) > 0 {
			result = append(result, group)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, _ := result[i][labelKey].(string)
		right, _ := result[j][labelKey].(string)
		return index[left] < index[right]
	})
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func truthy(value any) bool {
	switch current := value.(type) {
	case bool:
		return current
	case string:
		return strings.TrimSpace(current) != ""
	default:
		return value != nil
	}
}
