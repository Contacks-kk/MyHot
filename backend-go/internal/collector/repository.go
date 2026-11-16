package collector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/model"
	"aihot/backend-go/internal/sourceschedule"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

// Repository owns source scheduling and fetch-run persistence. It retains a pgx
// connection only for the PostgreSQL advisory lock that spans HTTP fetches.
type Repository struct {
	orm      *gorm.DB
	pool     *pgxpool.Pool
	schedule config.SourceSchedule
}

/** NewRepository creates a collector repository with the platform's source policy. */
func NewRepository(orm *gorm.DB, pool *pgxpool.Pool, schedule config.SourceSchedule) *Repository {
	return &Repository{
		orm:      orm,
		pool:     pool,
		schedule: schedule,
	}
}

type sourceRun struct {
	conn   *pgxpool.Conn
	Source source
	RunID  int64
	locked []string
	Reused bool
	Force  bool
}

const sourceIsActive = `enabled AND EXISTS (
	SELECT 1 FROM user_source_subscriptions u WHERE u.source_id=sources.id AND u.enabled
)`

const automaticMinimumElapsed = `(last_ok_at IS NULL OR last_ok_at + (? * interval '1 minute') <= now())`

/** DueSourceIDs finds active sources eligible for automatic collection. */
func (r *Repository) DueSourceIDs(ctx context.Context) ([]string, error) {
	var ids []string
	query := r.orm.WithContext(ctx).Model(&model.Source{}).
		Where(sourceIsActive + " AND kind IN ('rss','web_list') AND (next_fetch_at IS NULL OR next_fetch_at <= now())")
	if r.schedule.AutoMinInterval > 0 {
		query = query.Where(automaticMinimumElapsed, r.schedule.AutoMinInterval)
	}
	err := query.Order("next_fetch_at NULLS FIRST, id").Limit(40).Pluck("id", &ids).Error
	if ids == nil {
		ids = []string{}
	}
	return ids, err
}

/** BeginSourceRun claims a source and records the start or reuse of a fetch. */
func (r *Repository) BeginSourceRun(ctx context.Context, id string, force bool) (*sourceRun, bool, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1)::bigint)`, "myhot:source:"+id).Scan(&locked); err != nil || !locked {
		conn.Release()
		return nil, false, err
	}
	run := &sourceRun{
		conn:   conn,
		locked: []string{id},
		Force:  force,
	}
	err = r.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.beginLockedRun(tx, id, run)
	})
	if err != nil || run.RunID == 0 {
		r.ReleaseSource(run)
		return nil, false, err
	}
	return run, true, nil
}

/** beginLockedRun checks the current plan and records a fetch while holding the source row lock. */
func (r *Repository) beginLockedRun(tx *gorm.DB, id string, run *sourceRun) error {
	if err := sourceschedule.Lock(tx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}

	var record struct {
		ID          string          `gorm:"column:id"`
		Kind        string          `gorm:"column:kind"`
		Mode        string          `gorm:"column:participation_mode"`
		Config      []byte          `gorm:"column:config"`
		Cursor      json.RawMessage `gorm:"column:cursor"`
		Interval    int             `gorm:"column:interval_minutes"`
		Failures    int             `gorm:"column:fail_count"`
		First       bool            `gorm:"column:first"`
		ReuseWindow int             `gorm:"column:reuse_window_minutes"`
		LastOK      *time.Time      `gorm:"column:last_ok_at"`
		CheckedAt   time.Time       `gorm:"column:checked_at"`
	}
	query := tx.Model(&model.Source{}).
		Select("id", "kind", "participation_mode", "config", "cursor", "interval_minutes", "reuse_window_minutes", "last_ok_at", "fail_count", "last_ok_at IS NULL AS first", "clock_timestamp() AS checked_at").
		Where("id = ?", id)
	if run.Force {
		query = query.Where("kind IN ('rss','web_list','x_search') AND EXISTS (SELECT 1 FROM user_source_subscriptions u WHERE u.source_id=sources.id)")
	} else {
		query = query.Where(sourceIsActive + " AND kind IN ('rss','web_list') AND (next_fetch_at IS NULL OR next_fetch_at <= now())")
		if r.schedule.AutoMinInterval > 0 {
			query = query.Where(automaticMinimumElapsed, r.schedule.AutoMinInterval)
		}
	}
	result := query.Scan(&record)
	if result.Error != nil || result.RowsAffected == 0 {
		return result.Error
	}
	run.Source = source{
		ID:       record.ID,
		Kind:     record.Kind,
		Mode:     record.Mode,
		Interval: record.Interval,
		LastOK:   record.LastOK,
		Failures: record.Failures,
		First:    record.First,
		Config:   settings{},
		Cursor:   settings{},
	}
	if err := json.Unmarshal(record.Config, &run.Source.Config); err != nil {
		return errors.New("source configuration is invalid")
	}
	if len(record.Cursor) > 0 && string(record.Cursor) != "null" {
		if err := json.Unmarshal(record.Cursor, &run.Source.Cursor); err != nil {
			return errors.New("source cursor is invalid")
		}
	}

	// Only successful upstream checks provide a timestamp eligible for reuse.
	window := record.ReuseWindow
	if record.Kind != "x_search" {
		limit := r.schedule.AutoReuseWindow
		if run.Force {
			limit = r.schedule.ManualReuseWindow
		}
		window = sourceschedule.ReuseWindow(window, limit)
	}
	fetch := model.FetchRun{
		SourceID: id,
		Status:   "running",
	}
	if record.LastOK != nil && record.CheckedAt.Sub(*record.LastOK) < time.Duration(window)*time.Minute {
		run.Reused = true
		fetch.Status = "skipped"
		fetch.Detail = json.RawMessage(`{"state":"reused"}`)
		fetch.FinishedAt = &record.CheckedAt
		if !run.Force {
			next := record.LastOK.Add(time.Duration(record.Interval) * time.Minute)
			if err := tx.Model(&model.Source{}).Where("id = ?", id).Update("next_fetch_at", next).Error; err != nil {
				return err
			}
		}
	}
	if err := tx.Create(&fetch).Error; err != nil {
		return err
	}
	run.RunID = fetch.ID
	return nil
}

func (r *Repository) ReleaseSource(run *sourceRun) {
	if run == nil || run.conn == nil {
		return
	}
	unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := len(run.locked) - 1; i >= 0; i-- {
		if _, err := run.conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext($1)::bigint)`, "myhot:source:"+run.locked[i]); err != nil {
			// A session lock must never survive when its connection goes back to the pool.
			_ = run.conn.Conn().Close(context.Background())
			break
		}
	}
	run.conn.Release()
}

/** FinishFailure records the failed fetch and backs off the current automatic schedule. */
func (r *Repository) FinishFailure(ctx context.Context, run *sourceRun, found, created int, message string) error {
	updates := map[string]any{
		"fail_count":    gorm.Expr("fail_count + 1"),
		"health":        gorm.Expr("CASE WHEN NOT (" + sourceIsActive + ") THEN 'paused' WHEN fail_count >= 2 THEN 'failing' ELSE 'degraded' END"),
		"last_fetch_at": gorm.Expr("now()"),
		"last_error":    message,
		"updated_at":    gorm.Expr("now()"),
		"next_fetch_at": gorm.Expr("CASE WHEN " + sourceIsActive + " THEN next_fetch_at END"),
	}
	if !run.Force {
		updates["next_fetch_at"] = gorm.Expr("CASE WHEN "+sourceIsActive+
			" THEN now() + greatest(?,least(interval_minutes * (fail_count + 2),360)) * interval '1 minute' END", r.schedule.AutoMinInterval)
	}
	return r.finishRun(ctx, run, updates, map[string]any{
		"status":      "failed",
		"finished_at": gorm.Expr("now()"),
		"found_count": found,
		"new_count":   created,
		"error":       message,
	})
}

/** FinishSuccess records a validated upstream response and advances the current schedule. */
func (r *Repository) FinishSuccess(ctx context.Context, run *sourceRun, found, created int, cursorUpdates map[string]string, notModified bool) error {
	cursor := gorm.Expr("COALESCE(cursor,'{}'::jsonb)||jsonb_build_object('goCollectedAt',now())")
	if notModified {
		cursor = gorm.Expr("COALESCE(cursor,'{}'::jsonb)||jsonb_build_object('rssETag',coalesce(?,cursor->>'rssETag'),'rssLastModified',coalesce(?,cursor->>'rssLastModified'))", nullable(cursorUpdates["rssETag"]), nullable(cursorUpdates["rssLastModified"]))
	} else if len(cursorUpdates) > 0 {
		cursor = gorm.Expr("COALESCE(cursor,'{}'::jsonb)||jsonb_build_object('goCollectedAt',now(),'rssETag',NULLIF(?,''),'rssLastModified',NULLIF(?,''),'rssURL',?::text)", cursorUpdates["rssETag"], cursorUpdates["rssLastModified"], cursorUpdates["rssURL"])
	}
	updates := map[string]any{
		"fail_count":    0,
		"health":        gorm.Expr("CASE WHEN " + sourceIsActive + " THEN 'ok' ELSE 'paused' END"),
		"last_fetch_at": gorm.Expr("now()"),
		"last_ok_at":    gorm.Expr("now()"),
		"last_error":    nil,
		"cursor":        cursor,
		"updated_at":    gorm.Expr("now()"),
		"next_fetch_at": gorm.Expr("CASE WHEN " + sourceIsActive + " THEN now() + interval_minutes * interval '1 minute' END"),
	}
	return r.finishRun(ctx, run, updates, map[string]any{
		"status":      "ok",
		"finished_at": gorm.Expr("now()"),
		"found_count": found,
		"new_count":   created,
	})
}

/** finishRun saves both records against the plan committed by subscription changes. */
func (r *Repository) finishRun(ctx context.Context, run *sourceRun, sourceUpdates, runUpdates map[string]any) error {
	return r.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := sourceschedule.Lock(tx, run.Source.ID); err != nil {
			return err
		}
		if err := tx.Model(&model.Source{}).Where("id = ?", run.Source.ID).Updates(sourceUpdates).Error; err != nil {
			return err
		}
		return tx.Model(&model.FetchRun{}).Where("id = ?", run.RunID).Updates(runUpdates).Error
	})
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
