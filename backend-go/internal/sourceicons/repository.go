package sourceicons

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) RefreshXAvatars(ctx context.Context) (int, error) {
	result := r.db.WithContext(ctx).Exec(`
		UPDATE sources s SET icon_url = x.avatar
		FROM (
			SELECT DISTINCT ON (a.source_id) a.source_id, a.x_post->>'avatarUrl' AS avatar
			FROM articles a JOIN sources xs ON xs.id = a.source_id AND xs.kind = 'x_search'
			WHERE a.x_post ? 'avatarUrl'
			  AND lower(xs.name) LIKE '%(@' || lower(a.x_post->>'handle') || ')'
			ORDER BY a.source_id, a.discovered_at DESC
		) x
		WHERE s.id = x.source_id AND s.icon_url IS DISTINCT FROM x.avatar`)
	return int(result.RowsAffected), result.Error
}

func (r *GormRepository) Missing(ctx context.Context, now time.Time, limit int) ([]Source, error) {
	var sources []Source
	result := r.db.WithContext(ctx).Raw(`
		SELECT s.id, s.kind, s.config, coalesce(a.urls, ARRAY[]::text[]) AS urls
		FROM sources s
		LEFT JOIN LATERAL (
			SELECT array_agg(url ORDER BY discovered_at DESC) AS urls FROM (
				SELECT url, discovered_at FROM articles WHERE source_id = s.id ORDER BY discovered_at DESC LIMIT 10
			) recent
		) a ON true
		WHERE s.icon_url IS NULL AND s.kind <> 'x_search'
		  AND ((s.kind IN ('rss', 'web_list') AND s.health <> 'failing')
		    OR EXISTS (SELECT 1 FROM user_source_subscriptions u WHERE u.source_id=s.id AND u.enabled))
		  AND (s.icon_checked_at IS NULL OR s.icon_checked_at < ?::timestamptz - make_interval(days => CASE WHEN s.kind = 'mp_account' THEN ?::int ELSE ?::int END))
		ORDER BY s.icon_checked_at NULLS FIRST, s.id
		LIMIT ?`, now, MPRetryDays, RetryDays, limit).Scan(&sources)
	return sources, result.Error
}

func (r *GormRepository) Save(ctx context.Context, id string, icon *string, checkedAt time.Time) error {
	return r.db.WithContext(ctx).Exec(`
		UPDATE sources SET icon_url = coalesce(?, icon_url), icon_checked_at = ? WHERE id = ?`, icon, checkedAt, id).Error
}

type dueRepository interface {
	RanSince(context.Context, time.Time, time.Time) (bool, error)
}

func (r *GormRepository) RanSince(ctx context.Context, start, end time.Time) (bool, error) {
	var ran bool
	err := r.db.WithContext(ctx).Raw(`SELECT EXISTS (
		SELECT 1 FROM job_runs WHERE job = 'sources.icons' AND started_at >= ? AND started_at < ?
	)`, start, end).Scan(&ran).Error
	return ran, err
}

func DueWindow(now time.Time) (time.Time, time.Time, bool, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	local := now.In(location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 4, 40, 0, 0, location)
	end := start.AddDate(0, 0, 1)
	return start, end, !now.Before(start), nil
}

func (s *Service) Due(ctx context.Context, now time.Time) (string, bool, error) {
	start, end, due, err := DueWindow(now)
	if err != nil || !due {
		return "", false, err
	}
	repository, ok := s.repository.(dueRepository)
	if !ok {
		return "", false, nil
	}
	ran, err := repository.RanSince(ctx, start, end)
	return start.Format("2006-01-02"), !ran, err
}

var _ Repository = (*GormRepository)(nil)
