package reports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ReportRow struct {
	ID            string          `gorm:"column:id"`
	Title         string          `gorm:"column:title"`
	Summary       *string         `gorm:"column:summary"`
	URL           string          `gorm:"column:url"`
	Category      *string         `gorm:"column:category"`
	Tags          model.TextArray `gorm:"column:tags;type:text[]"`
	Score         *int            `gorm:"column:score"`
	FirstParty    bool            `gorm:"column:first_party"`
	BodyMode      string          `gorm:"column:body_mode"`
	Selected      bool            `gorm:"column:selected"`
	TimelineAt    time.Time       `gorm:"column:timeline_at"`
	SourceID      string          `gorm:"column:source_id"`
	SourceName    string          `gorm:"column:source_name"`
	SourceKind    string          `gorm:"column:source_kind"`
	SourceTier    string          `gorm:"column:source_tier"`
	PublisherRole *string         `gorm:"column:publisher_role"`
	OwnerEntityID *string         `gorm:"column:owner_entity_id"`
	FactSubject   *string         `gorm:"column:fact_subject"`
	FactID        *int64          `gorm:"column:fact_id"`
	FactPublicID  *string         `gorm:"column:fact_public_id"`
	StoryID       *int64          `gorm:"column:story_id"`
	StoryPublicID *string         `gorm:"column:story_public_id"`
}

type Issue struct {
	Key     string          `gorm:"column:key"`
	Content json.RawMessage `gorm:"column:content"`
}

type ArticleFact struct {
	ID           string  `gorm:"column:id"`
	FactPublicID *string `gorm:"column:fact_public_id"`
	StoryID      *int64  `gorm:"column:story_id"`
}

type StoryParticipants struct {
	StoryID      int64 `gorm:"column:story_id"`
	Participants int   `gorm:"column:participants"`
}

type Mention struct {
	ArticleID string `gorm:"column:article_id"`
	StoryID   int64  `gorm:"column:story_id"`
}

type DailyMemoryIssue struct {
	Key     string          `gorm:"column:key"`
	Content json.RawMessage `gorm:"column:content"`
}

type SavedReport struct {
	Entries int `gorm:"column:entries"`
}

type ReportRepository interface {
	PeriodReports(context.Context, time.Time, time.Time) ([]ReportRow, error)
	DailyIssues(context.Context, string, string) ([]Issue, error)
	CurrentReportsByIDs(context.Context, []string, bool) ([]ReportRow, error)
	DailyMemoryIssues(context.Context, string, string) ([]DailyMemoryIssue, error)
	CurrentFactsByArticles(context.Context, []string) ([]ArticleFact, error)
	MissedFacts(context.Context, time.Time, time.Time, interests.PublicationSettings) ([]int64, error)
	ReportsForFacts(context.Context, []int64, time.Time, interests.PublicationSettings) ([]ReportRow, error)
	FactSources(context.Context, []int64, time.Time) (map[int64][]string, error)
	StoryParticipants(context.Context, []int64, time.Time, time.Time, interests.PublicationSettings) (map[int64]int, error)
	MentionedStories(context.Context, []string) ([]Mention, error)
	ReportKeys(context.Context, string) ([]string, error)
	SavedReport(context.Context, string, string) (*SavedReport, error)
	SaveReport(context.Context, string, string, time.Time, time.Time, map[string]any, *string, *string, []int64) (bool, error)
}

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

const reportFieldsSQL = `p.article_id AS id, p.title, p.summary, p.url, p.category, p.tags, p.score,
 (s.tier = 'T1') AS first_party, p.body_mode, p.selected, p.timeline_at,
 s.id AS source_id, s.name AS source_name, s.kind AS source_kind, s.tier AS source_tier,
 s.config->>'publisherRole' AS publisher_role, s.owner_entity_id, f.subject AS fact_subject,
 f.id AS fact_id, f.public_id AS fact_public_id, st.id AS story_id, st.public_id::text AS story_public_id`

const ownEvidenceSQL = `EXISTS (SELECT 1 FROM fact_articles own_fa
 JOIN publications own_p ON own_p.article_id = own_fa.article_id
 WHERE own_fa.fact_id = p.fact_id AND own_fa.article_id = p.article_id
   AND own_fa.role <> 'mention'
   AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = own_p.analysis_id AND evidence_an.output->>'scope' = 'composite'))`

// currentSignalsSQL avoids literal '?' characters outside the two GORM bind markers.
// The Dev.to path regex builds a question mark with chr(63), so GORM cannot mistake it for a bind marker.
const currentSignalsSQL = `SELECT ss.story_id, ss.article_id, s.id AS source_id, ss.observed_at,
 CASE
   WHEN s.id = ANY(?::text[]) THEN coalesce('dev:account:' || lower(substring(a.url from ('^https://dev\.to/([A-Za-z0-9_-]{1,64})/[^/' || chr(63) || '#]+(/|$)'))), 'unresolved:' || s.id)
   WHEN s.id = ANY(?::text[]) THEN coalesce('hn:account:' || substring(a.author from '^[A-Za-z0-9_-]{1,64}$'), 'unresolved:' || s.id)
   WHEN s.signal_group_id IS NOT NULL THEN 'group:' || s.signal_group_id
   WHEN s.owner_entity_id IS NOT NULL THEN 'owner:' || s.owner_entity_id
   ELSE 'source:' || s.id
 END AS participant_key
 FROM story_signals ss JOIN articles a ON a.id = ss.article_id JOIN sources s ON s.id = a.source_id
 LEFT JOIN publications p ON p.article_id = ss.article_id
 WHERE s.participation_mode <> 'isolated' AND coalesce(p.visibility, 'public') <> 'withdrawn'
   AND NOT EXISTS (SELECT 1 FROM grouping_overrides go WHERE go.article_id = ss.article_id AND go.mode = 'standalone')
   AND (s.participation_mode <> 'editorial' OR EXISTS (
     SELECT 1 FROM fact_articles fa JOIN facts f ON f.id = fa.fact_id
     WHERE fa.article_id = ss.article_id AND f.story_id = ss.story_id AND fa.role <> 'mention'
       AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')))`

func (r *GormRepository) PeriodReports(ctx context.Context, start, end time.Time) ([]ReportRow, error) {
	var rows []ReportRow
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "report_candidates").Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT `+reportFieldsSQL+`
 FROM publications p JOIN sources s ON s.id = p.source_id
 LEFT JOIN facts f ON f.id = p.fact_id AND `+ownEvidenceSQL+`
 LEFT JOIN stories st ON st.id = f.story_id
 WHERE p.visibility = 'public' AND p.selected AND NOT p.backfill
   AND ((p.visible_after <= p.timeline_at AND p.timeline_at >= ? AND p.timeline_at < ?)
     OR (p.visible_after > p.timeline_at AND p.visible_after >= ? AND p.visible_after < ?))
   AND p.timeline_at >= ?::timestamptz - interval '24 hours'`, start, end, start, end, start).Scan(&rows).Error
	})
	return rows, err
}

func (r *GormRepository) DailyIssues(ctx context.Context, startDate, endDate string) ([]Issue, error) {
	var rows []Issue
	err := r.db.WithContext(ctx).Raw("SELECT key, content FROM reports WHERE kind = 'daily' AND key >= ? AND key <= ? ORDER BY key", startDate, endDate).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) CurrentReportsByIDs(ctx context.Context, ids []string, selectedOnly bool) ([]ReportRow, error) {
	if len(ids) == 0 {
		return []ReportRow{}, nil
	}
	query := `SELECT ` + reportFieldsSQL + `
 FROM publications p JOIN sources s ON s.id = p.source_id
 LEFT JOIN facts f ON f.id = p.fact_id AND ` + ownEvidenceSQL + `
 LEFT JOIN stories st ON st.id = f.story_id
 WHERE p.article_id = ANY(?::text[]) AND p.visibility = 'public' AND p.eligible`
	if selectedOnly {
		query += " AND p.selected"
	}
	var rows []ReportRow
	err := r.db.WithContext(ctx).Raw(query, ids).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) DailyMemoryIssues(ctx context.Context, date, fromDate string) ([]DailyMemoryIssue, error) {
	var rows []DailyMemoryIssue
	err := r.db.WithContext(ctx).Raw(`SELECT key, content FROM reports
 WHERE kind = 'daily' AND key < ? AND key >= ? ORDER BY key DESC`, date, fromDate).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) CurrentFactsByArticles(ctx context.Context, ids []string) ([]ArticleFact, error) {
	if len(ids) == 0 {
		return []ArticleFact{}, nil
	}
	var rows []ArticleFact
	err := r.db.WithContext(ctx).Raw(`SELECT p.article_id AS id, f.public_id AS fact_public_id, f.story_id
 FROM publications p LEFT JOIN facts f ON f.id = p.fact_id AND `+ownEvidenceSQL+`
 WHERE p.article_id = ANY(?::text[])`, ids).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) MissedFacts(ctx context.Context, start, end time.Time, feeds interests.PublicationSettings) ([]int64, error) {
	query := `WITH current_signals AS (` + currentSignalsSQL + `), active AS (
 SELECT DISTINCT fa.fact_id FROM fact_articles fa JOIN publications p ON p.article_id = fa.article_id JOIN sources s ON s.id = p.source_id
 WHERE fa.role <> 'mention'
   AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
   AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
   AND s.participation_mode = 'editorial' AND NOT p.backfill AND p.timeline_at >= ? AND p.timeline_at < ?
 )
 SELECT fa.fact_id FROM active JOIN fact_articles fa ON fa.fact_id = active.fact_id
 JOIN publications p ON p.article_id = fa.article_id JOIN current_signals cs ON cs.article_id = fa.article_id
 WHERE fa.role <> 'mention'
   AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
   AND cs.observed_at < ?
   AND NOT EXISTS (SELECT 1 FROM fact_articles sfa JOIN publications sp ON sp.article_id = sfa.article_id
     WHERE sfa.fact_id = fa.fact_id AND sfa.role <> 'mention' AND sp.selected AND sp.visibility = 'public')
 GROUP BY fa.fact_id HAVING count(DISTINCT cs.participant_key) >= 3 AND min(cs.observed_at) >= ?::timestamptz - interval '24 hours'`
	var rows []struct {
		FactID int64 `gorm:"column:fact_id"`
	}
	err := r.db.WithContext(ctx).Raw(query, feeds.CommunityFeeds.Dev, feeds.CommunityFeeds.HN, end, start, end, end, start).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.FactID
	}
	return ids, nil
}

func (r *GormRepository) ReportsForFacts(ctx context.Context, factIDs []int64, end time.Time, feeds interests.PublicationSettings) ([]ReportRow, error) {
	if len(factIDs) == 0 {
		return []ReportRow{}, nil
	}
	var rows []ReportRow
	err := r.db.WithContext(ctx).Raw(`SELECT `+reportFieldsSQL+`
 FROM fact_articles fa JOIN publications p ON p.article_id = fa.article_id JOIN sources s ON s.id = p.source_id
 JOIN facts f ON f.id = fa.fact_id LEFT JOIN stories st ON st.id = f.story_id
 WHERE fa.fact_id = ANY(?::bigint[]) AND fa.role <> 'mention'
   AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
   AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
   AND s.participation_mode = 'editorial' AND s.tier <> 'EXCLUDE_MP' AND NOT p.backfill AND p.timeline_at < ?`, factIDs, end, end).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) FactSources(ctx context.Context, factIDs []int64, end time.Time) (map[int64][]string, error) {
	result := make(map[int64][]string, len(factIDs))
	if len(factIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		FactID  int64    `gorm:"column:fact_id"`
		Sources []string `gorm:"column:sources"`
	}
	err := r.db.WithContext(ctx).Raw(`SELECT fa.fact_id, array_agg(DISTINCT p.source_id) AS sources
 FROM fact_articles fa JOIN publications p ON p.article_id = fa.article_id JOIN sources s ON s.id = p.source_id
 WHERE fa.fact_id = ANY(?::bigint[]) AND fa.role <> 'mention'
   AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
   AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
   AND s.participation_mode = 'editorial' AND p.timeline_at < ?
 GROUP BY fa.fact_id`, factIDs, end, end).Scan(&rows).Error
	for _, row := range rows {
		result[row.FactID] = row.Sources
	}
	return result, err
}

func (r *GormRepository) StoryParticipants(ctx context.Context, storyIDs []int64, start, end time.Time, feeds interests.PublicationSettings) (map[int64]int, error) {
	result := make(map[int64]int, len(storyIDs))
	if len(storyIDs) == 0 {
		return result, nil
	}
	var rows []StoryParticipants
	query := `WITH current_signals AS (` + currentSignalsSQL + `)
 SELECT cs.story_id, count(DISTINCT cs.participant_key)::int AS participants FROM current_signals cs
 WHERE cs.story_id = ANY(?::bigint[]) AND cs.observed_at >= ? AND cs.observed_at < ? GROUP BY cs.story_id`
	err := r.db.WithContext(ctx).Raw(query, feeds.CommunityFeeds.Dev, feeds.CommunityFeeds.HN, storyIDs, start, end).Scan(&rows).Error
	for _, row := range rows {
		result[row.StoryID] = row.Participants
	}
	return result, err
}

func (r *GormRepository) MentionedStories(ctx context.Context, articleIDs []string) ([]Mention, error) {
	if len(articleIDs) == 0 {
		return []Mention{}, nil
	}
	var rows []Mention
	err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT fa.article_id, f.story_id FROM fact_articles fa JOIN facts f ON f.id = fa.fact_id
 WHERE fa.article_id = ANY(?::text[]) AND f.story_id IS NOT NULL`, articleIDs).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) ReportKeys(ctx context.Context, kind string) ([]string, error) {
	var keys []string
	err := r.db.WithContext(ctx).Model(&model.Report{}).Where("kind = ?", kind).Order("key").Pluck("key", &keys).Error
	return keys, err
}

func (r *GormRepository) SavedReport(ctx context.Context, kind, key string) (*SavedReport, error) {
	var row SavedReport
	result := r.db.WithContext(ctx).Raw(`SELECT coalesce(CASE WHEN kind = 'daily' THEN (content->'metrics'->>'totalEvents')::int
 ELSE jsonb_array_length(content->'storyOrder') END, 0) AS entries FROM reports WHERE kind = ? AND key = ?`, kind, key).Scan(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &row, nil
}

func (r *GormRepository) SaveReport(ctx context.Context, kind, key string, start, end time.Time, content map[string]any, reason *string, modelName *string, receiptIDs []int64) (bool, error) {
	encoded, err := json.Marshal(content)
	if err != nil {
		return false, err
	}
	written := false
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		insert := func() error {
			result := tx.Exec(`INSERT INTO reports(kind,key,window_start,window_end,content,generated_at,model,origin)
 VALUES(?,?,?,?,?::jsonb,now(),?,'model') ON CONFLICT(kind,key) DO NOTHING`, kind, key, start, end, string(encoded), modelName)
			written = result.RowsAffected > 0
			return result.Error
		}
		if reason == nil {
			if err := insert(); err != nil {
				return err
			}
		} else {
			var existing model.Report
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("kind = ? AND key = ?", kind, key).Take(&existing)
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				if err := insert(); err != nil {
					return err
				}
			} else if result.Error != nil {
				return result.Error
			} else {
				if err := tx.Exec(`INSERT INTO report_revisions(report_id,revision,content,generated_at,reason)
 VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, existing.ID, existing.Revision, existing.Content, existing.GeneratedAt, reason).Error; err != nil {
					return err
				}
				if err := tx.Model(&model.Report{}).Where("id = ?", existing.ID).Updates(map[string]any{
					"content": gorm.Expr("?::jsonb", string(encoded)), "window_start": start, "window_end": end,
					"generated_at": gorm.Expr("now()"), "model": modelName, "revision": gorm.Expr("revision + 1"),
					"origin": "model", "updated_at": gorm.Expr("now()"),
				}).Error; err != nil {
					return err
				}
				written = true
			}
		}
		for _, receiptID := range receiptIDs {
			if err := tx.Model(&model.PaidReceipt{}).Where("id = ?", receiptID).Updates(map[string]any{
				"status": "completed", "completed_at": gorm.Expr("coalesce(completed_at, now())"), "updated_at": gorm.Expr("now()"),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return written, err
}

var _ ReportRepository = (*GormRepository)(nil)
