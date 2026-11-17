package storydigest

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

var evaluationPublicID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

/** ParseEvaluationStoryIDs validates, normalizes and deduplicates public story IDs. */
func ParseEvaluationStoryIDs(list string) ([]string, error) {
	ids := make([]string, 0)
	invalid := make([]string, 0)
	seen := make(map[string]bool)
	for _, value := range strings.Split(list, ",") {
		id := strings.ToLower(strings.TrimSpace(value))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if !evaluationPublicID.MatchString(id) {
			invalid = append(invalid, id)
			continue
		}
		ids = append(ids, id)
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("not an event id: %s", strings.Join(invalid, ", "))
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("stories did not name any events")
	}
	return ids, nil
}

/** ExportEvaluationCases reads one consistent, read-only snapshot of the requested events. */
func (r *GormRepository) ExportEvaluationCases(ctx context.Context, ids []string, now time.Time) ([]EvaluationCase, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("stories did not name any events")
	}
	var cases []EvaluationCase
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		type storyRow struct {
			ID        int64  `gorm:"column:id"`
			PublicID  string `gorm:"column:public_id"`
			Title     string `gorm:"column:title"`
			HasDigest bool   `gorm:"column:has_digest"`
		}
		var stories []storyRow
		if err := tx.Raw(`SELECT s.id, s.public_id::text AS public_id, s.title,
			EXISTS (SELECT 1 FROM story_digests d WHERE d.story_id = s.id) AS has_digest
			FROM stories s WHERE s.public_id IN ? AND s.merged_into IS NULL`, ids).Scan(&stories).Error; err != nil {
			return err
		}
		found := make(map[string]storyRow, len(stories))
		for _, story := range stories {
			found[story.PublicID] = story
		}
		missing := make([]string, 0)
		for _, id := range ids {
			if _, ok := found[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("no current event (unknown or merged): %s", strings.Join(missing, ", "))
		}
		empty := make([]string, 0)
		for _, id := range ids {
			story := found[id]
			reports, err := digestReports(tx, story.ID, now)
			if err != nil {
				return err
			}
			if len(reports) == 0 {
				empty = append(empty, id)
				continue
			}
			cases = append(cases, evaluationCaseFromStory(id, story.Title, story.HasDigest, reports))
		}
		if len(empty) > 0 {
			return fmt.Errorf("no listed report to write a digest from: %s", strings.Join(empty, ", "))
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return cases, nil
}

/** evaluationCaseFromStory converts current public evidence into a portable case. */
func evaluationCaseFromStory(id, title string, hasDigest bool, reports []DigestReport) EvaluationCase {
	sort.SliceStable(reports, func(i, j int) bool { return reports[i].At.Before(reports[j].At) })
	mode := "incremental"
	if hasDigest {
		mode = "corrected"
	}
	result := EvaluationCase{
		CaseID:          id,
		Story:           &EvaluationStory{Title: title},
		InputMode:       &mode,
		KnownArticleIDs: []string{},
		Reports:         make([]EvaluationReport, 0, len(reports)),
	}
	for _, report := range reports {
		firstParty := report.FirstParty
		result.Reports = append(result.Reports, EvaluationReport{
			ID:          report.ID,
			PublishedAt: report.At.UTC().Format("2006-01-02T15:04:05.000Z"),
			Source:      report.SourceName,
			FirstParty:  &firstParty,
			Title:       report.Title,
			Summary:     report.Summary,
			Fact: &EvaluationFact{
				ID:         report.FactID,
				Subject:    report.FactSubject,
				Action:     report.FactAction,
				Object:     report.FactObject,
				Conditions: report.FactConditions,
				Evidence:   report.Evidence,
				Structured: report.StructuredFact,
			},
		})
	}
	return result
}
