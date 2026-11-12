package collector

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"aihot/backend-go/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

type XShard struct {
	Key       string
	Mode      string
	SourceIDs []string
}

type xShardRow struct {
	ID                string          `gorm:"column:id"`
	Kind              string          `gorm:"column:kind"`
	ParticipationMode string          `gorm:"column:participation_mode"`
	Config            json.RawMessage `gorm:"column:config;type:jsonb"`
	Cursor            json.RawMessage `gorm:"column:cursor;type:jsonb"`
	IntervalMinutes   int             `gorm:"column:interval_minutes"`
	FailCount         int             `gorm:"column:fail_count"`
	NextFetchAt       *time.Time      `gorm:"column:next_fetch_at"`
	LastOKAt          *time.Time      `gorm:"column:last_ok_at"`
	Due               bool            `gorm:"column:due"`
}

type xConfig struct {
	Query         string `json:"query"`
	SearchType    string `json:"searchType"`
	PublisherRole string `json:"publisherRole"`
}

type xCursor struct {
	LastTweetID string     `json:"lastTweetId"`
	LastOKAt    *time.Time `json:"lastOkAt"`
}

type xShardAccount struct {
	id, handle string
	own        bool
}

type xShardRun struct {
	conn    *pgxpool.Conn
	locked  []string
	Members []*sourceRun
}

func (r *Repository) DueXShards(ctx context.Context) ([]XShard, error) {
	var rows []xShardRow
	err := r.orm.WithContext(ctx).Raw(`SELECT id, kind, participation_mode, config, cursor, interval_minutes, fail_count,
		next_fetch_at, last_ok_at, (next_fetch_at IS NULL OR next_fetch_at <= now()) AS due
		FROM sources WHERE enabled AND kind = 'x_search' AND EXISTS (SELECT 1 FROM user_source_subscriptions u WHERE u.source_id=sources.id AND u.enabled)
		AND config->>'query' ~* '^from:[A-Za-z0-9_]{1,15} -filter:replies$'
		AND coalesce(config->>'searchType', 'Latest') = 'Latest' AND cursor->>'lastTweetId' IS NOT NULL
		ORDER BY participation_mode, id`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	all := planXShards(rows)
	due := map[string]bool{}
	for _, row := range rows {
		if row.Due {
			due[row.ID] = true
		}
	}
	shards := make([]XShard, 0, len(all))
	for _, shard := range all {
		for _, id := range shard.SourceIDs {
			if due[id] {
				shards = append(shards, shard)
				break
			}
		}
	}
	return shards, nil
}

func (r *Repository) DeferXSources(ctx context.Context, sourceIDs []string) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	return r.orm.WithContext(ctx).Model(&model.Source{}).Where("id IN ? AND kind = 'x_search'", sourceIDs).
		Update("next_fetch_at", gorm.Expr("now() + interval '10 minutes'")).Error
}

func (r *Repository) BeginXShardRun(ctx context.Context, sourceIDs []string) (*xShardRun, bool, error) {
	ids := append([]string(nil), sourceIDs...)
	sort.Strings(ids)
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return nil, false, nil
	}
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	run := &xShardRun{conn: conn}
	for _, id := range ids {
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1)::bigint)`, "myhot:source:"+id).Scan(&locked); err != nil {
			r.releaseXShard(run)
			return nil, false, err
		}
		if !locked {
			r.releaseXShard(run)
			return nil, false, nil
		}
		run.locked = append(run.locked, id)
	}

	var sources []model.Source
	if err := r.orm.WithContext(ctx).Where("id IN ? AND enabled AND kind = 'x_search' AND EXISTS (SELECT 1 FROM user_source_subscriptions u WHERE u.source_id=sources.id AND u.enabled)", ids).Order("id").Find(&sources).Error; err != nil {
		r.releaseXShard(run)
		return nil, false, err
	}
	byID := make(map[string]model.Source, len(sources))
	for _, src := range sources {
		byID[src.ID] = src
	}
	for _, id := range ids {
		src, ok := byID[id]
		if !ok {
			continue
		}
		cfg := settings{}
		if err := json.Unmarshal(src.Config, &cfg); err != nil {
			r.releaseXShard(run)
			return nil, false, errors.New("source configuration is invalid")
		}
		cursor := settings{}
		if len(src.Cursor) > 0 && string(src.Cursor) != "null" {
			if err := json.Unmarshal(src.Cursor, &cursor); err != nil {
				r.releaseXShard(run)
				return nil, false, errors.New("source cursor is invalid")
			}
		}
		member := source{ID: src.ID, Kind: src.Kind, Mode: src.ParticipationMode, Config: cfg, Cursor: cursor,
			Interval: src.IntervalMinutes, Failures: src.FailCount, First: src.LastOKAt == nil && strings.TrimSpace(cursor.text("initializedAt")) == ""}
		if shardHandleForSource(member) == "" {
			r.releaseXShard(run)
			return nil, false, nil
		}
		run.Members = append(run.Members, &sourceRun{Source: member})
	}
	if len(run.Members) == 0 {
		r.releaseXShard(run)
		return nil, false, nil
	}
	err = r.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, member := range run.Members {
			fetch := model.FetchRun{SourceID: member.Source.ID, Status: "running"}
			if err := tx.Create(&fetch).Error; err != nil {
				return err
			}
			member.RunID = fetch.ID
		}
		return nil
	})
	if err != nil {
		r.releaseXShard(run)
		return nil, false, err
	}
	return run, true, nil
}

func (r *Repository) ReleaseXShard(run *xShardRun) { r.releaseXShard(run) }

func (r *Repository) releaseXShard(run *xShardRun) {
	if run == nil || run.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := len(run.locked) - 1; i >= 0; i-- {
		if _, err := run.conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtext($1)::bigint)`, "myhot:source:"+run.locked[i]); err != nil {
			_ = run.conn.Conn().Close(context.Background())
			break
		}
	}
	run.conn.Release()
}

func (r *Repository) FinishXSuccess(ctx context.Context, runs []*sourceRun, found, created map[string]int, cursors map[string]json.RawMessage, detail json.RawMessage, receiptIDs []int64) error {
	return r.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, run := range runs {
			id := run.Source.ID
			if err := tx.Model(&model.Source{}).Where("id = ?", id).Updates(map[string]any{
				"last_fetch_at": gorm.Expr("now()"), "last_ok_at": gorm.Expr("now()"), "fail_count": 0,
				"last_error": nil, "health": "ok", "cursor": cursors[id],
				"next_fetch_at": gorm.Expr("now() + (? * interval '1 minute')", run.Source.Interval), "updated_at": gorm.Expr("now()"),
			}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.FetchRun{}).Where("id = ?", run.RunID).Updates(map[string]any{
				"status": "ok", "finished_at": gorm.Expr("now()"), "found_count": found[id], "new_count": created[id], "detail": detail,
			}).Error; err != nil {
				return err
			}
		}
		return completeXReceipts(tx, receiptIDs)
	})
}

func (r *Repository) FinishXFailure(ctx context.Context, runs []*sourceRun, found, created map[string]int, detail json.RawMessage, message string, budget bool) error {
	if len(message) > 1000 {
		message = message[:1000]
	}
	return r.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, run := range runs {
			next := gorm.Expr("now() + (LEAST(?, 360) * interval '1 minute')", run.Source.Interval*(run.Source.Failures+2))
			updates := map[string]any{
				"last_fetch_at": gorm.Expr("now()"), "last_error": message,
				"next_fetch_at": next, "updated_at": gorm.Expr("now()"),
				"fail_count": gorm.Expr("fail_count + 1"),
				"health":     gorm.Expr("CASE WHEN fail_count + 1 >= 5 THEN 'failing' ELSE 'degraded' END"),
			}
			if budget {
				updates["next_fetch_at"] = gorm.Expr("now() + interval '15 minutes'")
				updates["fail_count"] = gorm.Expr("fail_count")
				updates["health"] = gorm.Expr("health")
			}
			if err := tx.Model(&model.Source{}).Where("id = ?", run.Source.ID).Updates(updates).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.FetchRun{}).Where("id = ?", run.RunID).Updates(map[string]any{
				"status": "failed", "finished_at": gorm.Expr("now()"), "found_count": found[run.Source.ID],
				"new_count": created[run.Source.ID], "error": message, "detail": detail,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func completeXReceipts(tx *gorm.DB, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return tx.Model(&model.PaidReceipt{}).Where("id IN ? AND status = 'received'", ids).Updates(map[string]any{
		"status": "completed", "completed_at": gorm.Expr("coalesce(completed_at, now())"), "updated_at": gorm.Expr("now()"),
	}).Error
}

func planXShards(rows []xShardRow) []XShard {
	byMode := map[string][]xShardAccount{}
	for _, row := range rows {
		var cfg xConfig
		var cursor xCursor
		if json.Unmarshal(row.Config, &cfg) != nil || json.Unmarshal(row.Cursor, &cursor) != nil {
			continue
		}
		handle := shardHandleFromConfig(cfg, cursor)
		if handle == "" {
			continue
		}
		byMode[row.ParticipationMode] = append(byMode[row.ParticipationMode], xShardAccount{id: row.ID, handle: handle, own: (cfg.PublisherRole == "organization" || cfg.PublisherRole == "person")})
	}
	modes := make([]string, 0, len(byMode))
	for mode := range byMode {
		modes = append(modes, mode)
	}
	sort.Strings(modes)
	shards := []XShard{}
	for _, mode := range modes {
		accounts := byMode[mode]
		sort.Slice(accounts, func(i, j int) bool { return accounts[i].id < accounts[j].id })
		current := []xShardAccount{}
		closeShard := func() {
			if len(current) == 0 {
				return
			}
			ids := make([]string, len(current))
			for i, a := range current {
				ids[i] = a.id
			}
			hash := xHashIDs(ids)
			shards = append(shards, XShard{Key: mode + ":" + hash[:12], Mode: mode, SourceIDs: ids})
			current = nil
		}
		for _, a := range accounts {
			candidate := append(append([]xShardAccount(nil), current...), a)
			if len(current) >= 24 || len(shardQueryForAccounts(candidate)) > 470 {
				closeShard()
			}
			current = append(current, a)
		}
		closeShard()
	}
	return shards
}

func shardQueryForAccounts(accounts []xShardAccount) string {
	handles := make([]string, 0, len(accounts))
	own := make([]string, 0, len(accounts))
	for _, account := range accounts {
		handles = append(handles, account.handle)
		if account.own {
			own = append(own, account.handle)
		}
	}
	posts := "(" + prefixedHandles(handles) + ") -filter:replies"
	if len(own) == 0 {
		return posts
	}
	return "(" + posts + " OR (" + prefixedHandles(own) + ") filter:self_threads)"
}

func prefixedHandles(handles []string) string {
	for i := range handles {
		handles[i] = "from:" + handles[i]
	}
	return strings.Join(handles, " OR ")
}

func shardHandleForSource(src source) string {
	cfg := xConfig{Query: src.Config.text("query"), SearchType: src.Config.text("searchType"), PublisherRole: src.Config.text("publisherRole")}
	return shardHandleFromConfig(cfg, xCursor{LastTweetID: cursorString(src.Cursor["lastTweetId"])})
}

func shardHandleFromConfig(cfg xConfig, cursor xCursor) string {
	if cfg.SearchType != "" && cfg.SearchType != "Latest" || cursor.LastTweetID == "" {
		return ""
	}
	match := intervalShardQuery.FindStringSubmatch(cfg.Query)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
