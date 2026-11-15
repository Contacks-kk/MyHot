package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"aihot/backend-go/internal/model"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

var (
	errRecordNotFound    = errors.New("record not found")
	errDailyQuotaReached = errors.New("daily user model call quota reached")
)

// Repository owns persistence for River-driven personal processing.
// The pgx pool is used only where River must share a transaction with a scan
// marker; all other application reads and writes use GORM.
type Repository struct {
	orm  *gorm.DB
	pool *pgxpool.Pool
}

func NewRepository(orm *gorm.DB, pool *pgxpool.Pool) *Repository {
	return &Repository{orm: orm, pool: pool}
}

type scanArticle struct {
	ID        string
	Revision  int
	Title     string
	Body      string
	Excerpt   string
	CreatedAt time.Time
	SourceIDs []string
}

type scanUserArticle struct {
	UserID            int64
	SourceID          string
	ArticleID         string
	Revision          int
	Title             string
	Body              string
	Excerpt           string
	ModelVersion      int
	PreferenceVersion int
	Keywords          []string
	EntityIDs         []string
	ColdStart         bool
	FilterMode        string
}

type coldStartSubscription struct {
	SubscriptionID    int64
	UserID            int64
	SourceID          string
	ModelVersion      int
	PreferenceVersion int
	Keywords          []string
	EntityIDs         []string
	FilterMode        string
}

type triageUserState struct {
	EncryptedKey      string          `gorm:"column:api_key_ciphertext"`
	BaseURL           string          `gorm:"column:base_url"`
	Model             string          `gorm:"column:model"`
	ModelVersion      int             `gorm:"column:config_version"`
	PreferenceVersion int             `gorm:"column:preference_version"`
	Keywords          model.TextArray `gorm:"column:keywords;type:text[]"`
	EntityIDs         model.TextArray `gorm:"column:entity_ids;type:text[]"`
}

type articleInput struct {
	EncryptedKey      string          `gorm:"column:api_key_ciphertext"`
	BaseURL           string          `gorm:"column:base_url"`
	Model             string          `gorm:"column:model"`
	Title             string          `gorm:"column:title"`
	Body              string          `gorm:"column:body"`
	Excerpt           string          `gorm:"column:excerpt"`
	URL               string          `gorm:"column:url"`
	Revision          int             `gorm:"column:revision"`
	ModelVersion      int             `gorm:"column:config_version"`
	PreferenceVersion int             `gorm:"column:preference_version"`
	Keywords          model.TextArray `gorm:"column:keywords;type:text[]"`
	EntityIDs         model.TextArray `gorm:"column:entity_ids;type:text[]"`
}

type dailyModel struct {
	EncryptedKey string `gorm:"column:api_key_ciphertext"`
	BaseURL      string `gorm:"column:base_url"`
	Model        string `gorm:"column:model"`
}

type dailyEntry struct {
	ArticleID string    `json:"articleId" gorm:"column:article_id"`
	Score     int       `json:"score" gorm:"column:relevance_score"`
	Title     string    `json:"title" gorm:"column:title"`
	Summary   string    `json:"summary" gorm:"column:summary"`
	Reason    *string   `json:"reason" gorm:"column:reason"`
	URL       string    `json:"url" gorm:"column:url"`
	Source    string    `json:"source" gorm:"column:source"`
	CreatedAt time.Time `json:"createdAt" gorm:"column:created_at"`
}

func (r *Repository) EnsureRollout(ctx context.Context) error {
	return r.orm.WithContext(ctx).Exec(`INSERT INTO personalization_rollout(id) VALUES(true) ON CONFLICT(id) DO NOTHING`).Error
}

/** WithPersonalScanLockTx runs one personal scan round under an advisory transaction lock. */
func (r *Repository) WithPersonalScanLockTx(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('myhot:personal:scan')::bigint)`).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

/** UnscannedArticlesTx lists unscanned non-backfill articles eligible for incremental matching. */
func (r *Repository) UnscannedArticlesTx(ctx context.Context, tx pgx.Tx) ([]scanArticle, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id,
		       a.revision,
		       a.title,
		       left(coalesce(a.body_text,''),64000) AS body,
		       left(coalesce(a.excerpt,''),12000) AS excerpt,
		       a.created_at,
		       array_agg(DISTINCT ad.source_id ORDER BY ad.source_id) AS source_ids
		FROM articles a
		JOIN article_discoveries ad ON ad.article_id=a.id
		JOIN user_source_subscriptions sub ON sub.source_id=ad.source_id AND sub.enabled
		LEFT JOIN personalization_scans x ON x.article_id=a.id AND x.input_revision=a.revision
		CROSS JOIN personalization_rollout p
		WHERE p.id=true
		  AND a.created_at>=p.activated_at
		  AND NOT a.backfill
		  AND (a.published_at IS NULL OR a.published_at>=a.discovered_at-interval '48 hours')
		  AND ad.discovered_at>=sub.activated_at
		  AND (
		    (x.article_id IS NULL)
		    OR (x.processed_source_ids IS NULL AND ad.discovered_at>x.scanned_at)
		    OR (x.processed_source_ids IS NOT NULL AND NOT ad.source_id=ANY(x.processed_source_ids))
		  )
		GROUP BY a.id
		ORDER BY a.created_at, a.id
		LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []scanArticle
	for rows.Next() {
		var item scanArticle
		if err := rows.Scan(&item.ID, &item.Revision, &item.Title, &item.Body, &item.Excerpt, &item.CreatedAt, &item.SourceIDs); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

/** IncrementalCandidatesTx loads eligible verified subscribers for one newly discovered article. */
func (r *Repository) IncrementalCandidatesTx(ctx context.Context, tx pgx.Tx, article scanArticle) ([]scanUserArticle, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (u.id)
		       ad.source_id,
		       u.id,
		       m.config_version,
		       coalesce(ui.preference_version, 1) AS preference_version,
		       coalesce(ui.keywords, '{}'::text[]) AS keywords,
		       coalesce(ui.entity_ids, '{}'::text[]) AS entity_ids,
		       sub.filter_mode
		FROM user_accounts u
		JOIN user_model_configs m ON m.user_id = u.id
		LEFT JOIN user_interests ui ON ui.user_id = u.id
		JOIN user_source_subscriptions sub
		  ON sub.user_id = u.id
		 AND sub.enabled
		 AND sub.source_id = ANY($2::text[])
		JOIN article_discoveries ad
		  ON ad.article_id = $1
		 AND ad.source_id = sub.source_id
		 AND ad.discovered_at >= sub.activated_at
		WHERE u.verified_at IS NOT NULL
		  AND ad.discovered_at >= greatest(
		    u.created_at,
		    u.verified_at,
		    m.updated_at,
		    coalesce(ui.updated_at, u.created_at),
		    sub.activated_at
		  )
		  AND NOT EXISTS (
		    SELECT 1
		    FROM user_article_results x
		    WHERE x.user_id = u.id
		      AND x.article_id = $1
		  )
		ORDER BY u.id, ad.source_id`, article.ID, article.SourceIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scanUserArticle
	for rows.Next() {
		candidate := scanUserArticle{
			ArticleID: article.ID,
			Revision:  article.Revision,
			Title:     article.Title,
			Body:      article.Body,
			Excerpt:   article.Excerpt,
		}
		if err := rows.Scan(&candidate.SourceID, &candidate.UserID, &candidate.ModelVersion, &candidate.PreferenceVersion, &candidate.Keywords, &candidate.EntityIDs, &candidate.FilterMode); err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

/** MarkArticleScannedTx records processed source IDs for an article revision. */
func (r *Repository) MarkArticleScannedTx(ctx context.Context, tx pgx.Tx, article scanArticle) error {
	_, err := tx.Exec(ctx, `INSERT INTO personalization_scans(article_id,input_revision,processed_source_ids,scanned_at) VALUES($1,$2,$3,now()) ON CONFLICT(article_id,input_revision) DO UPDATE SET processed_source_ids=CASE WHEN personalization_scans.processed_source_ids IS NULL THEN ARRAY(SELECT DISTINCT source_id FROM article_discoveries WHERE article_id=$1 AND discovered_at<=personalization_scans.scanned_at) || EXCLUDED.processed_source_ids ELSE ARRAY(SELECT DISTINCT unnest(personalization_scans.processed_source_ids || EXCLUDED.processed_source_ids)) END,scanned_at=now()`, article.ID, article.Revision, article.SourceIDs)
	return err
}

/** ColdStartSubscriptionsTx locks enabled cold-start subscriptions whose source already completed a fetch. */
func (r *Repository) ColdStartSubscriptionsTx(ctx context.Context, tx pgx.Tx) ([]coldStartSubscription, error) {
	rows, err := tx.Query(ctx, `
		SELECT sub.id,
		       sub.user_id,
		       sub.source_id,
		       m.config_version,
		       coalesce(ui.preference_version, 1) AS preference_version,
		       coalesce(ui.keywords, '{}'::text[]) AS keywords,
		       coalesce(ui.entity_ids, '{}'::text[]) AS entity_ids,
		       sub.filter_mode
		FROM user_source_subscriptions sub
		JOIN sources src ON src.id = sub.source_id
		JOIN user_accounts u ON u.id = sub.user_id
		JOIN user_model_configs m ON m.user_id = u.id
		LEFT JOIN user_interests ui ON ui.user_id = u.id
		WHERE sub.enabled
		  AND sub.cold_start_pending
		  AND src.last_ok_at IS NOT NULL
		  AND u.verified_at IS NOT NULL
		ORDER BY sub.id
		FOR UPDATE OF sub SKIP LOCKED`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []coldStartSubscription
	for rows.Next() {
		var sub coldStartSubscription
		if err := rows.Scan(&sub.SubscriptionID, &sub.UserID, &sub.SourceID, &sub.ModelVersion, &sub.PreferenceVersion, &sub.Keywords, &sub.EntityIDs, &sub.FilterMode); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return subs, nil
}

/** ColdStartArticlesTx selects up to limit newest non-backfill articles from the past 48h for a late subscriber. */
func (r *Repository) ColdStartArticlesTx(ctx context.Context, tx pgx.Tx, sub coldStartSubscription, limit int) ([]scanUserArticle, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id,
		       a.revision,
		       a.title,
		       left(coalesce(a.body_text,''),64000) AS body,
		       left(coalesce(a.excerpt,''),12000) AS excerpt
		FROM article_discoveries ad
		JOIN articles a ON a.id = ad.article_id
		CROSS JOIN personalization_rollout p
		WHERE ad.source_id = $1
		  AND p.id = true
		  AND a.created_at >= p.activated_at
		  AND NOT a.backfill
		  AND ad.discovered_at >= now() - interval '48 hours'
		  AND (a.published_at IS NULL OR a.published_at >= a.discovered_at - interval '48 hours')
		  AND NOT EXISTS (
		    SELECT 1
		    FROM user_article_results x
		    WHERE x.user_id = $2
		      AND x.article_id = a.id
		  )
		ORDER BY coalesce(a.published_at, a.discovered_at) DESC, a.discovered_at DESC, a.id DESC
		LIMIT $3`, sub.SourceID, sub.UserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scanUserArticle
	for rows.Next() {
		item := scanUserArticle{
			UserID:            sub.UserID,
			SourceID:          sub.SourceID,
			ModelVersion:      sub.ModelVersion,
			PreferenceVersion: sub.PreferenceVersion,
			Keywords:          sub.Keywords,
			EntityIDs:         sub.EntityIDs,
			ColdStart:         true,
			FilterMode:        sub.FilterMode,
		}
		if err := rows.Scan(&item.ArticleID, &item.Revision, &item.Title, &item.Body, &item.Excerpt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

/** ClearColdStartPendingTx clears the one-time cold-start flag after a subscription slice is handled. */
func (r *Repository) ClearColdStartPendingTx(ctx context.Context, tx pgx.Tx, subIDs []int64) error {
	if len(subIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE user_source_subscriptions SET cold_start_pending=false, updated_at=now() WHERE id=ANY($1::bigint[])`, subIDs)
	return err
}

/** DailyModelCallCountTx counts user model call receipts in the current daily window inside a transaction. */
func (r *Repository) DailyModelCallCountTx(ctx context.Context, tx pgx.Tx, userID int64, dayStartUTC time.Time) (int, error) {
	var used int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM user_model_call_receipts WHERE user_id=$1 AND purpose IN ('article','daily') AND created_at>=$2`, userID, dayStartUTC).Scan(&used)
	return used, err
}

/** DailyModelCallCount counts user model call receipts in the current daily window. */
func (r *Repository) DailyModelCallCount(ctx context.Context, userID int64, dayStartUTC time.Time) (int, error) {
	var used int
	err := r.orm.WithContext(ctx).Raw(`SELECT count(*) FROM user_model_call_receipts WHERE user_id=? AND purpose IN ('article','daily') AND created_at>=?`, userID, dayStartUTC).Row().Scan(&used)
	return used, err
}

/** TriageUserState loads verified user model configuration and interests matching queued versions. */
func (r *Repository) TriageUserState(ctx context.Context, userID int64, modelVersion, preferenceVersion int) (triageUserState, error) {
	var value triageUserState
	result := r.orm.WithContext(ctx).Raw(`
		SELECT m.api_key_ciphertext,
		       m.base_url,
		       m.model,
		       m.config_version,
		       coalesce(ui.preference_version, 1) AS preference_version,
		       coalesce(ui.keywords, '{}'::text[]) AS keywords,
		       coalesce(ui.entity_ids, '{}'::text[]) AS entity_ids
		FROM user_accounts u
		JOIN user_model_configs m ON m.user_id = u.id
		LEFT JOIN user_interests ui ON ui.user_id = u.id
		WHERE u.id = ?
		  AND u.verified_at IS NOT NULL
		  AND m.config_version = ?
		  AND coalesce(ui.preference_version, 1) = ?`,
		userID, modelVersion, preferenceVersion,
	).Scan(&value)
	if result.Error != nil {
		return triageUserState{}, result.Error
	}
	if result.RowsAffected == 0 {
		return triageUserState{}, errRecordNotFound
	}
	return value, nil
}

/** ExistingArticleResultIDs returns the subset of article IDs already scored for userID. */
func (r *Repository) ExistingArticleResultIDs(ctx context.Context, userID int64, articleIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(articleIDs))
	if len(articleIDs) == 0 {
		return out, nil
	}
	var existing []string
	if err := r.orm.WithContext(ctx).Model(&model.UserArticleResult{}).Where("user_id = ? AND article_id IN ?", userID, articleIDs).Pluck("article_id", &existing).Error; err != nil {
		return nil, err
	}
	for _, id := range existing {
		out[id] = true
	}
	return out, nil
}

func (r *Repository) ArticleResultExists(ctx context.Context, userID int64, articleID string) (bool, error) {
	var count int64
	err := r.orm.WithContext(ctx).Model(&model.UserArticleResult{}).Where("user_id = ? AND article_id = ?", userID, articleID).Count(&count).Error
	return count > 0, err
}

func (r *Repository) ArticleInput(
	ctx context.Context,
	args ArticleArgs,
) (articleInput, error) {
	var value articleInput

	result := r.orm.WithContext(ctx).Raw(`
		SELECT
			m.api_key_ciphertext,
			m.base_url,
			m.model,
			a.title,
			coalesce(a.body_text, '') AS body,
			coalesce(a.excerpt, '') AS excerpt,
			a.url,
			a.revision,
			m.config_version,
			coalesce(ui.preference_version, 1) AS preference_version,
			coalesce(ui.keywords, '{}'::text[]) AS keywords,
			coalesce(ui.entity_ids, '{}'::text[]) AS entity_ids
		FROM user_accounts u
		JOIN user_model_configs m
			ON m.user_id = u.id
		LEFT JOIN user_interests ui
			ON ui.user_id = u.id
		JOIN articles a
			ON a.id = ?
		WHERE u.id = ?
			AND u.verified_at IS NOT NULL
			AND m.config_version = ?
			AND coalesce(ui.preference_version, 1) = ?
			AND NOT a.backfill
			AND (
				a.published_at IS NULL
				OR a.published_at >= a.discovered_at - interval '48 hours'
			)
			AND (
				NOT ?::boolean
				OR EXISTS (
					SELECT 1
					FROM personalization_rollout p
					WHERE p.id = true
						AND a.created_at >= p.activated_at
				)
			)
			AND EXISTS (
				SELECT 1
				FROM article_discoveries ad
				JOIN user_source_subscriptions sub
					ON sub.source_id = ad.source_id
					AND sub.user_id = u.id
					AND sub.enabled
				WHERE ad.article_id = a.id
					AND (
						(?::boolean AND ad.discovered_at >= now() - interval '48 hours')
						OR (
							NOT ?::boolean AND ad.discovered_at >= greatest(
								u.created_at,
								u.verified_at,
								m.updated_at,
								coalesce(ui.updated_at, u.created_at),
								sub.activated_at
							)
						)
					)
			)
	`, args.ArticleID, args.UserID, args.ModelVersion, args.PreferenceVersion, args.ColdStart, args.ColdStart, args.ColdStart).Scan(&value)

	if result.Error != nil {
		return articleInput{}, result.Error
	}
	if result.RowsAffected == 0 {
		return articleInput{}, errRecordNotFound
	}
	return value, nil
}

/** StartReceiptWithQuota atomically checks the user's daily model call quota and starts a receipt row. */
func (r *Repository) StartReceiptWithQuota(ctx context.Context, userID int64, purpose, articleID, modelName, logicalKey, requestHash string, dayStartUTC time.Time, dailyLimit int) (int64, error) {
	var id int64
	err := r.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lockKey := fmt.Sprintf("myhot:quota:%d", userID)
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?::text)::bigint)`, lockKey).Error; err != nil {
			return err
		}
		var used int64
		if err := tx.Raw(`SELECT count(*) FROM user_model_call_receipts WHERE user_id=? AND purpose IN ('article','daily') AND created_at>=? AND logical_key<>?`, userID, dayStartUTC, logicalKey).Row().Scan(&used); err != nil {
			return err
		}
		if int(used) >= dailyLimit {
			return errDailyQuotaReached
		}
		return tx.Raw(`INSERT INTO user_model_call_receipts(user_id,logical_key,purpose,article_id,model,status,request_hash) VALUES(?,?,?,NULLIF(?,''),?,'pending',?) ON CONFLICT(logical_key) DO UPDATE SET status='pending',error=NULL,completed_at=NULL,usage=NULL,model=excluded.model,request_hash=excluded.request_hash RETURNING id`, userID, logicalKey, purpose, articleID, modelName, requestHash).Row().Scan(&id)
	})
	return id, err
}

func (r *Repository) StartReceipt(ctx context.Context, userID int64, purpose, articleID, modelName, logicalKey, requestHash string) (int64, error) {
	var id int64
	err := r.orm.WithContext(ctx).Raw(`INSERT INTO user_model_call_receipts(user_id,logical_key,purpose,article_id,model,status,request_hash) VALUES(?,?,?,NULLIF(?,''),?,'pending',?) ON CONFLICT(logical_key) DO UPDATE SET status='pending',error=NULL,completed_at=NULL,usage=NULL,model=excluded.model,request_hash=excluded.request_hash RETURNING id`, userID, logicalKey, purpose, articleID, modelName, requestHash).Row().Scan(&id)
	return id, err
}

func (r *Repository) FinishReceipt(ctx context.Context, id int64, usage json.RawMessage, callErr error) error {
	status := "completed"
	var message *string
	if callErr != nil {
		status = "failed"
		value := callErr.Error()
		message = &value
	}
	return r.orm.WithContext(ctx).Model(&model.UserModelCallReceipt{}).Where("id = ?", id).Updates(map[string]any{
		"status":       status,
		"usage":        usage,
		"error":        message,
		"completed_at": gorm.Expr("now()"),
	}).Error
}

func (r *Repository) SaveArticleResultAndReceipt(ctx context.Context, result model.UserArticleResult, receiptID int64, usage json.RawMessage) error {
	return r.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO user_article_results(user_id,article_id,input_revision,model,relevance_score,title,summary,reason) VALUES(?,?,?,?,?,?,?,NULLIF(?,'')) ON CONFLICT(user_id,article_id) DO NOTHING`, result.UserID, result.ArticleID, result.InputRevision, result.Model, result.RelevanceScore, result.Title, result.Summary, result.Reason).Error; err != nil {
			return err
		}
		return tx.Model(&model.UserModelCallReceipt{}).Where("id = ?", receiptID).Updates(map[string]any{
			"status":       "completed",
			"usage":        usage,
			"error":        nil,
			"completed_at": gorm.Expr("now()"),
		}).Error
	})
}

func (r *Repository) DailyUsers(ctx context.Context, date string, start, end time.Time) ([]int64, error) {
	var ids []int64
	err := r.orm.WithContext(ctx).Raw(`SELECT DISTINCT r.user_id FROM user_article_results r JOIN user_accounts u ON u.id=r.user_id JOIN user_model_configs m ON m.user_id=u.id WHERE u.verified_at IS NOT NULL AND r.created_at>=? AND r.created_at<? AND NOT EXISTS(SELECT 1 FROM user_daily_reports d WHERE d.user_id=r.user_id AND d.report_date=? AND d.is_scheduled)`, start, end, date).Scan(&ids).Error
	if ids == nil {
		ids = []int64{}
	}
	return ids, err
}

func (r *Repository) DailyReportExists(ctx context.Context, userID int64, date string) (bool, error) {
	var count int64
	err := r.orm.WithContext(ctx).Model(&model.UserDailyReport{}).Where("user_id = ? AND report_date = ? AND is_scheduled", userID, date).Count(&count).Error
	return count > 0, err
}

func (r *Repository) DailyModel(ctx context.Context, userID int64) (dailyModel, error) {
	var value dailyModel
	result := r.orm.WithContext(ctx).Raw(`SELECT m.api_key_ciphertext,m.base_url,m.model FROM user_model_configs m JOIN user_accounts u ON u.id=m.user_id WHERE m.user_id=? AND u.verified_at IS NOT NULL`, userID).Scan(&value)
	if result.Error != nil {
		return dailyModel{}, result.Error
	}
	if result.RowsAffected == 0 {
		return dailyModel{}, errRecordNotFound
	}
	return value, nil
}

func (r *Repository) DailyEntries(ctx context.Context, userID int64, start, end time.Time) ([]dailyEntry, error) {
	var entries []dailyEntry
	err := r.orm.WithContext(ctx).Raw(`SELECT r.article_id,r.relevance_score,r.title,r.summary,r.reason,a.url,s.name AS source,r.created_at FROM user_article_results r JOIN articles a ON a.id=r.article_id JOIN sources s ON s.id=a.source_id WHERE r.user_id=? AND r.created_at>=? AND r.created_at<? ORDER BY r.relevance_score DESC,r.created_at DESC,r.article_id`, userID, start, end).Scan(&entries).Error
	if entries == nil {
		entries = []dailyEntry{}
	}
	return entries, err
}

func (r *Repository) SaveDailyReportAndReceipt(ctx context.Context, report model.UserDailyReport, receiptID int64, usage json.RawMessage) error {
	return r.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO user_daily_reports(user_id,report_date,model,title,summary,entries) VALUES(?,?,?,?,?,?) ON CONFLICT(user_id,report_date) DO UPDATE SET model=excluded.model,title=excluded.title,summary=excluded.summary,entries=excluded.entries,created_at=now(),is_scheduled=true WHERE NOT user_daily_reports.is_scheduled`, report.UserID, report.ReportDate, report.Model, report.Title, report.Summary, report.Entries).Error; err != nil {
			return err
		}
		return tx.Model(&model.UserModelCallReceipt{}).Where("id = ?", receiptID).Updates(map[string]any{
			"status":       "completed",
			"usage":        usage,
			"error":        nil,
			"completed_at": gorm.Expr("now()"),
		}).Error
	})
}

/** ManualDailyStart finds the end of the latest issue existing at request time. */
func (r *Repository) ManualDailyStart(ctx context.Context, userID int64, end time.Time) (time.Time, error) {
	var start *time.Time
	err := r.orm.WithContext(ctx).Raw(`SELECT max(ended_at) FROM (
		SELECT (report_date::timestamp + interval '8 hours') AT TIME ZONE 'Asia/Shanghai' AS ended_at FROM user_daily_reports WHERE user_id=? AND is_scheduled AND created_at<=?
		UNION ALL
		SELECT (item->>'windowEnd')::timestamptz FROM user_daily_reports CROSS JOIN LATERAL jsonb_array_elements(manual_reports) item WHERE user_id=? AND (item->>'createdAt')::timestamptz<=?
	) reports WHERE ended_at<=?`, userID, end, userID, end, end).Row().Scan(&start)
	if err != nil {
		return time.Time{}, err
	}
	if start == nil {
		return time.Time{}, nil
	}
	return *start, nil
}

/** SaveManualDailyReportAndReceipt appends an issue and completes its model receipt atomically. */
func (r *Repository) SaveManualDailyReportAndReceipt(ctx context.Context, report model.UserDailyReport, issue json.RawMessage, receiptID int64, usage json.RawMessage) error {
	return r.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO user_daily_reports(user_id,report_date,model,title,summary,entries,is_scheduled,manual_reports) VALUES(?,?,?,?,?,'[]'::jsonb,false,jsonb_build_array(?::jsonb)) ON CONFLICT(user_id,report_date) DO UPDATE SET manual_reports=user_daily_reports.manual_reports || excluded.manual_reports`, report.UserID, report.ReportDate, report.Model, report.Title, report.Summary, string(issue)).Error; err != nil {
			return err
		}
		return tx.Model(&model.UserModelCallReceipt{}).Where("id = ?", receiptID).Updates(map[string]any{"status": "completed", "usage": usage, "error": nil, "completed_at": gorm.Expr("now()")}).Error
	})
}

func (r *Repository) ConnectionReceiptStatus(ctx context.Context, userID, receiptID int64) (string, error) {
	var status string
	result := r.orm.WithContext(ctx).Raw(`SELECT r.status FROM user_model_call_receipts r JOIN user_accounts u ON u.id=r.user_id WHERE r.id=? AND r.user_id=? AND r.purpose='connection_test' AND u.verified_at IS NOT NULL`, receiptID, userID).Scan(&status)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", errRecordNotFound
	}
	return status, nil
}
