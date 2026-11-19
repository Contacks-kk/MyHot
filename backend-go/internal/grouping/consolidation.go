package grouping

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
)

type StoryRoot struct {
	StoryID int64
	At      time.Time
	Roundup bool
	Report  Report
}

type StoryRootRepository interface {
	StoryRoots(context.Context, []int64) ([]StoryRoot, error)
}

type LiveStoryResolver interface {
	LiveStoryID(context.Context, int64) (int64, bool, error)
}

type EmptyStoryRepository interface {
	EmptyStories(context.Context, string, int64) ([]int64, error)
}

type StoryMerger interface {
	MergeStories(context.Context, int64, int64, string, string) (map[string]int, bool, error)
}

type Consolidation struct {
	From       int64     `json:"from"`
	Into       int64     `json:"into"`
	Merge      bool      `json:"merge"`
	First      Relation  `json:"first"`
	Second     *Relation `json:"second"`
	FromTitle  string    `json:"fromTitle"`
	IntoTitle  string    `json:"intoTitle"`
	Difference string    `json:"difference"`
}

/** StoryRoots returns live, model-managed stories with their earliest non-composite root report. */
func (r *GormRepository) StoryRoots(ctx context.Context, ids []int64) ([]StoryRoot, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	roots := make([]StoryRoot, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		liveID, found, err := r.LiveStoryID(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if _, exists := seen[liveID]; exists {
			continue
		}
		seen[liveID] = struct{}{}
		root, found, err := rootOfStory(ctx, r.db, liveID)
		if err != nil {
			return nil, err
		}
		if found {
			roots = append(roots, root)
		}
	}
	return roots, nil
}

type storyMergeState struct {
	MergedInto *int64 `gorm:"column:merged_into"`
}

/** LiveStoryID resolves a redirect chain without following more than the legacy twenty hops. */
func (r *GormRepository) LiveStoryID(ctx context.Context, id int64) (int64, bool, error) {
	if err := validateRepository(r); err != nil {
		return 0, false, err
	}
	current := id
	for range 20 {
		var rows []storyMergeState
		if err := r.db.WithContext(ctx).Raw(`SELECT merged_into FROM stories WHERE id=?`, current).Scan(&rows).Error; err != nil {
			return 0, false, err
		}
		if len(rows) == 0 {
			return 0, false, nil
		}
		if rows[0].MergedInto == nil {
			return current, true, nil
		}
		current = *rows[0].MergedInto
	}
	return 0, false, nil
}

/** EmptyStories finds prior live stories that lost every report when an article moved. */
func (r *GormRepository) EmptyStories(ctx context.Context, articleID string, targetStoryID int64) ([]int64, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	var rows []struct {
		ID int64 `gorm:"column:id"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT st.id
		FROM grouping_decisions d JOIN stories st ON st.id=d.story_id AND st.merged_into IS NULL
		WHERE d.article_id=? AND st.id<>?
		AND NOT EXISTS (
			SELECT 1 FROM facts f JOIN fact_articles fa ON fa.fact_id=f.id
			WHERE f.story_id=st.id AND fa.role IN ('primary','report')
		)
		ORDER BY st.id`, articleID, targetStoryID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

type storyRootRow struct {
	StoryID    int64      `gorm:"column:story_id"`
	StartedAt  time.Time  `gorm:"column:started_at"`
	Roundup    bool       `gorm:"column:roundup"`
	Title      string     `gorm:"column:title"`
	Summary    *string    `gorm:"column:summary"`
	Source     string     `gorm:"column:source"`
	FirstParty bool       `gorm:"column:first_party"`
	At         time.Time  `gorm:"column:at"`
	Subject    *string    `gorm:"column:subject"`
	Action     *string    `gorm:"column:action"`
	Object     *string    `gorm:"column:object"`
	OccurredAt *time.Time `gorm:"column:occurred_at"`
}

/** rootOfStory selects the oldest fact report and the legacy representative report for comparison. */
func rootOfStory(ctx context.Context, db *gorm.DB, storyID int64) (StoryRoot, bool, error) {
	var rows []storyRootRow
	query := `WITH root_fact AS (
		SELECT f.id, f.story_id, min(coalesce(p.published_at,p.discovered_at)) AS started_at
		FROM facts f
		JOIN fact_articles fa ON fa.fact_id=f.id AND fa.role IN ('primary','report')
		JOIN publications p ON p.article_id=fa.article_id
		WHERE f.story_id=?
		AND (SELECT a.output->>'scope'='composite' FROM analyses a WHERE a.article_id=fa.article_id
			ORDER BY a.input_revision DESC,a.id DESC LIMIT 1) IS NOT TRUE
		GROUP BY f.id,f.story_id ORDER BY started_at,f.id LIMIT 1
	)
	SELECT root_fact.story_id,root_fact.started_at,
		EXISTS(SELECT 1 FROM grouping_decisions d WHERE d.article_id=fa.article_id AND d.verdict='roundup') AS roundup,
		p.title,p.summary,s.name AS source,(s.tier='T1') AS first_party,
		coalesce(p.published_at,p.discovered_at) AS at,f.subject,f.action,f.object,f.occurred_at
	FROM root_fact
	JOIN stories st ON st.id=root_fact.story_id AND st.merged_into IS NULL AND st.origin<>'manual'
	JOIN facts f ON f.id=root_fact.id
	JOIN fact_articles fa ON fa.fact_id=f.id AND fa.role IN ('primary','report')
	JOIN publications p ON p.article_id=fa.article_id
	JOIN sources s ON s.id=p.source_id
	WHERE (SELECT a.output->>'scope'='composite' FROM analyses a WHERE a.article_id=fa.article_id
		ORDER BY a.input_revision DESC,a.id DESC LIMIT 1) IS NOT TRUE
	ORDER BY (fa.role='primary') DESC,p.timeline_at ASC LIMIT 1`
	if err := db.WithContext(ctx).Raw(query, storyID).Scan(&rows).Error; err != nil {
		return StoryRoot{}, false, err
	}
	if len(rows) == 0 {
		return StoryRoot{}, false, nil
	}
	row := rows[0]
	return StoryRoot{StoryID: row.StoryID, At: row.StartedAt, Roundup: row.Roundup, Report: Report{
		Title: row.Title, Summary: row.Summary, Source: row.Source, FirstParty: row.FirstParty, At: &row.At,
		Frame: &Frame{Subject: row.Subject, Action: row.Action, Object: row.Object, OccurredAt: beijingDatePointer(row.OccurredAt)},
	}}, true, nil
}

/** consolidateStoryRoots double-checks tied roots and merges only when both configured models agree. */
func (s *Service) consolidateStoryRoots(ctx context.Context, ownStoryID int64, storyIDs []int64) ([]Consolidation, int64, error) {
	if len(storyIDs) < 2 {
		return nil, ownStoryID, nil
	}
	rootRepository, ok := s.repository.(StoryRootRepository)
	if !ok || s.storyMerger == nil || s.judge == nil {
		return nil, ownStoryID, errors.New("story consolidation is unavailable")
	}
	roots, err := rootRepository.StoryRoots(ctx, storyIDs)
	if err != nil {
		return nil, ownStoryID, err
	}
	roots = nonRoundupRoots(roots)
	if len(roots) < 2 {
		return nil, ownStoryID, nil
	}
	results := make([]Consolidation, 0, len(roots)-1)
	anchor := roots[0]
	resultStoryID := ownStoryID
	for _, other := range roots[1:] {
		item, merged, err := s.compareStoryRoots(ctx, anchor, other)
		results = append(results, item)
		if err != nil {
			return results, resultStoryID, err
		}
		if merged && other.StoryID == ownStoryID {
			resultStoryID = anchor.StoryID
		}
	}
	if resolver, ok := s.repository.(LiveStoryResolver); ok {
		if liveStoryID, found, err := resolver.LiveStoryID(ctx, ownStoryID); err != nil {
			return results, resultStoryID, err
		} else if found {
			resultStoryID = liveStoryID
		}
	}
	return results, resultStoryID, nil
}

/** nonRoundupRoots sorts eligible roots by the time the story began, then by stable ID. */
func nonRoundupRoots(roots []StoryRoot) []StoryRoot {
	result := make([]StoryRoot, 0, len(roots))
	for _, root := range roots {
		if !root.Roundup {
			result = append(result, root)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].At.Equal(result[j].At) {
			return result[i].StoryID < result[j].StoryID
		}
		return result[i].At.Before(result[j].At)
	})
	return result
}

/** compareStoryRoots records the first judgement and merges only after a reversed review agrees. */
func (s *Service) compareStoryRoots(ctx context.Context, anchor, other StoryRoot) (Consolidation, bool, error) {
	item := Consolidation{From: other.StoryID, Into: anchor.StoryID, FromTitle: other.Report.Title, IntoTitle: anchor.Report.Title}
	first, err := s.judge.CompareStoryRoots(ctx, anchor, other, false)
	if err != nil {
		return item, false, err
	}
	item.First, item.Difference = first.Relation, first.Difference
	if !FirmlyTied(first.Relation, first.Confidence) {
		return item, false, nil
	}
	second, err := s.judge.CompareStoryRoots(ctx, other, anchor, true)
	if err != nil {
		return item, false, err
	}
	item.Second = &second.Relation
	if item.Difference == "" {
		item.Difference = second.Difference
	}
	if !FirmlyTied(second.Relation, second.Confidence, StoryReviewMinConfidence) {
		return item, false, nil
	}
	reason := fmt.Sprintf("同一事件（%s，复核 %s）：%s｜%s", first.Relation, second.Relation, other.Report.Title, anchor.Report.Title)
	_, merged, err := s.storyMerger.MergeStories(ctx, other.StoryID, anchor.StoryID, reason, "grouping")
	item.Merge = merged
	return item, merged, err
}

/** redirectEmptyStories preserves old public addresses after an article's last report moves away. */
func (s *Service) redirectEmptyStories(ctx context.Context, articleID string, targetStoryID int64) ([]int64, error) {
	emptyRepository, ok := s.repository.(EmptyStoryRepository)
	if !ok || s.storyMerger == nil || targetStoryID <= 0 {
		return nil, nil
	}
	emptied, err := emptyRepository.EmptyStories(ctx, articleID, targetStoryID)
	if err != nil {
		return nil, err
	}
	redirected := make([]int64, 0, len(emptied))
	for _, storyID := range emptied {
		reason := fmt.Sprintf("报道已全部移走，旧地址跳到报道所在事件（最后一篇 %s）", articleID)
		_, merged, err := s.storyMerger.MergeStories(ctx, storyID, targetStoryID, reason, "grouping")
		if err != nil {
			return redirected, err
		}
		if merged {
			redirected = append(redirected, storyID)
		}
	}
	return redirected, nil
}
