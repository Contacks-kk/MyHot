package collector

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

// Repository owns source scheduling and fetch-run persistence. It retains a pgx
// connection only for the PostgreSQL advisory lock that spans HTTP fetches.
type Repository struct {
	orm  *gorm.DB
	pool *pgxpool.Pool
}

func NewRepository(orm *gorm.DB, pool *pgxpool.Pool) *Repository {
	return &Repository{orm: orm, pool: pool}
}

type sourceRun struct {
	conn   *pgxpool.Conn
	Source source
	RunID  int64
	locked []string
	Reused bool
	Force  bool
}

func (r *Repository) DueSourceIDs(ctx context.Context) ([]string, error) {
	var ids []string
	err := r.orm.WithContext(ctx).Model(&model.Source{}).Where("enabled AND kind = 'rss' AND EXISTS (SELECT 1 FROM user_source_subscriptions u WHERE u.source_id=sources.id AND u.enabled) AND (next_fetch_at IS NULL OR next_fetch_at <= now())").Order("next_fetch_at NULLS FIRST, id").Limit(40).Pluck("id", &ids).Error
	if ids == nil {
		ids = []string{}
	}
	return ids, err
}

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
	run := &sourceRun{conn: conn, locked: []string{id}, Force: force}
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
	}
	result := r.orm.WithContext(ctx).Model(&model.Source{}).Select("id", "kind", "participation_mode", "config", "cursor", "interval_minutes", "reuse_window_minutes", "last_ok_at", "fail_count", "last_ok_at IS NULL AS first").Where("id = ? AND kind IN ('rss','x_search') AND (? OR kind='rss') AND (? OR enabled) AND EXISTS (SELECT 1 FROM user_source_subscriptions u WHERE u.source_id=sources.id AND (? OR u.enabled)) AND (? OR next_fetch_at IS NULL OR next_fetch_at <= now())", id, force, force, force, force).Scan(&record)
	if result.Error != nil {
		r.ReleaseSource(run)
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		r.ReleaseSource(run)
		return nil, false, nil
	}
	run.Source = source{ID: record.ID, Kind: record.Kind, Mode: record.Mode, Interval: record.Interval, ReuseWindow: record.ReuseWindow, LastOK: record.LastOK, Failures: record.Failures, First: record.First}
	run.Source.Config = settings{}
	if err := json.Unmarshal(record.Config, &run.Source.Config); err != nil {
		r.ReleaseSource(run)
		return nil, false, errors.New("source configuration is invalid")
	}
	run.Source.Cursor = settings{}
	if len(record.Cursor) > 0 && string(record.Cursor) != "null" {
		if err := json.Unmarshal(record.Cursor, &run.Source.Cursor); err != nil {
			r.ReleaseSource(run)
			return nil, false, errors.New("source cursor is invalid")
		}
	}
	if record.LastOK != nil && time.Since(*record.LastOK) < time.Duration(record.ReuseWindow)*time.Minute {
		run.Reused = true
		detail, _ := json.Marshal(map[string]string{"state": "reused"})
		finished := time.Now().UTC()
		fetch := model.FetchRun{SourceID: id, Status: "skipped", Detail: detail, FinishedAt: &finished}
		if err := r.orm.WithContext(ctx).Create(&fetch).Error; err != nil {
			r.ReleaseSource(run)
			return nil, false, err
		}
		run.RunID = fetch.ID
		if !force {
			if err := r.orm.WithContext(ctx).Model(&model.Source{}).Where("id = ?", id).Update("next_fetch_at", gorm.Expr("now() + (? * interval '1 minute')", record.Interval)).Error; err != nil {
				r.ReleaseSource(run)
				return nil, false, err
			}
		}
		return run, true, nil
	}
	fetch := model.FetchRun{SourceID: id, Status: "running"}
	if err := r.orm.WithContext(ctx).Create(&fetch).Error; err != nil {
		r.ReleaseSource(run)
		return nil, false, err
	}
	run.RunID = fetch.ID
	return run, true, nil
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

func (r *Repository) FinishFailure(ctx context.Context, run *sourceRun, found, created int, interval int, message string) error {
	updates := map[string]any{
		"fail_count":    gorm.Expr("fail_count + 1"),
		"health":        gorm.Expr("CASE WHEN fail_count >= 2 THEN 'failing' ELSE 'degraded' END"),
		"last_fetch_at": gorm.Expr("now()"),
		"last_error":    message,
		"updated_at":    gorm.Expr("now()"),
	}
	if !run.Force {
		updates["next_fetch_at"] = gorm.Expr("now() + (? * interval '1 minute')", interval)
	}
	statusErr := r.orm.WithContext(ctx).Model(&model.Source{}).Where("id = ?", run.Source.ID).Updates(updates).Error
	runErr := r.orm.WithContext(ctx).Model(&model.FetchRun{}).Where("id = ?", run.RunID).Updates(map[string]any{
		"status": "failed", "finished_at": gorm.Expr("now()"), "found_count": found, "new_count": created, "error": message,
	}).Error
	if statusErr != nil {
		return statusErr
	}
	return runErr
}

func (r *Repository) FinishSuccess(ctx context.Context, run *sourceRun, found, created int, cursorUpdates map[string]string, notModified bool) error {
	cursor := gorm.Expr("COALESCE(cursor,'{}'::jsonb)||jsonb_build_object('goCollectedAt',now())")
	if notModified {
		cursor = gorm.Expr("COALESCE(cursor,'{}'::jsonb)||jsonb_build_object('rssETag',coalesce(?,cursor->>'rssETag'),'rssLastModified',coalesce(?,cursor->>'rssLastModified'))", nullable(cursorUpdates["rssETag"]), nullable(cursorUpdates["rssLastModified"]))
	} else if len(cursorUpdates) > 0 {
		cursor = gorm.Expr("COALESCE(cursor,'{}'::jsonb)||jsonb_build_object('goCollectedAt',now(),'rssETag',NULLIF(?,''),'rssLastModified',NULLIF(?,''),'rssURL',?::text)", cursorUpdates["rssETag"], cursorUpdates["rssLastModified"], cursorUpdates["rssURL"])
	}
	updates := map[string]any{
		"fail_count":    0,
		"health":        "ok",
		"last_fetch_at": gorm.Expr("now()"),
		"last_ok_at":    gorm.Expr("now()"),
		"last_error":    nil,
		"cursor":        cursor,
		"updated_at":    gorm.Expr("now()"),
	}
	if !run.Force {
		updates["next_fetch_at"] = gorm.Expr("now() + (? * interval '1 minute')", run.Source.Interval)
	}
	if err := r.orm.WithContext(ctx).Model(&model.Source{}).Where("id = ?", run.Source.ID).Updates(updates).Error; err != nil {
		return err
	}
	return r.orm.WithContext(ctx).Model(&model.FetchRun{}).Where("id = ?", run.RunID).Updates(map[string]any{
		"status": "ok", "finished_at": gorm.Expr("now()"), "found_count": found, "new_count": created,
	}).Error
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
