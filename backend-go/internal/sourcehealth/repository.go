package sourcehealth

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type Source struct {
	ID             string    `gorm:"column:id"`
	Name           string    `gorm:"column:name"`
	Mode           string    `gorm:"column:participation_mode"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	Health         string    `gorm:"column:health"`
	FailCount      int       `gorm:"column:fail_count"`
	LastError      *string   `gorm:"column:last_error"`
	Runs           int       `gorm:"column:runs"`
	Failed         int       `gorm:"column:failed"`
	Discoveries    int       `gorm:"column:discoveries"`
	Items          int       `gorm:"column:items"`
	Undated        int       `gorm:"column:undated"`
	Repeated       int       `gorm:"column:repeated"`
	DetailFailures int       `gorm:"column:detail_failures"`
}

type Snapshot struct {
	Enabled  int
	Failing  int
	Degraded int
	Added    int
	ThisWeek int
	LastWeek int
	Selected int
	Sources  []Source `gorm:"-"`
}

type Repository interface {
	Weekly(context.Context, time.Time, time.Time) (Snapshot, error)
	HasRun(context.Context, time.Time, time.Time) (bool, error)
}

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) Weekly(ctx context.Context, since, before time.Time) (Snapshot, error) {
	var snapshot Snapshot
	if err := r.db.WithContext(ctx).Raw(`
		SELECT count(*) FILTER (WHERE enabled)::int AS enabled,
		       count(*) FILTER (WHERE enabled AND health = 'failing')::int AS failing,
		       count(*) FILTER (WHERE enabled AND health = 'degraded')::int AS degraded,
	       count(*) FILTER (WHERE created_at >= ?)::int AS added
		FROM sources`, since).Scan(&snapshot).Error; err != nil {
		return Snapshot{}, err
	}
	var totals struct {
		ThisWeek int `gorm:"column:this_week"`
		LastWeek int `gorm:"column:last_week"`
		Selected int `gorm:"column:selected"`
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT count(*) FILTER (WHERE discovered_at >= ?)::int AS this_week,
		       count(*) FILTER (WHERE discovered_at >= ? AND discovered_at < ?)::int AS last_week,
		       (SELECT count(*)::int FROM publications p WHERE p.selected AND p.visibility <> 'withdrawn' AND p.discovered_at >= ?) AS selected
		FROM articles WHERE discovered_at >= ?`, since, before, since, since, before).Scan(&totals).Error; err != nil {
		return Snapshot{}, err
	}
	snapshot.ThisWeek, snapshot.LastWeek, snapshot.Selected = totals.ThisWeek, totals.LastWeek, totals.Selected
	if err := r.db.WithContext(ctx).Raw(`
		WITH runs AS (
			SELECT source_id,
				count(*) FILTER (WHERE status IN ('ok','failed') AND coalesce(error, '') NOT LIKE 'Budget for % exhausted (%)')::int AS runs,
				count(*) FILTER (WHERE status = 'failed' AND coalesce(error, '') NOT LIKE 'Budget for % exhausted (%)')::int AS failed,
				coalesce(sum((detail->>'detailFailures')::int), 0)::int AS detail_failures
			FROM fetch_runs WHERE started_at >= ? GROUP BY source_id
		), seen AS (
			SELECT article_id, source_id FROM article_discoveries WHERE discovered_at >= ?
			UNION SELECT id AS article_id, source_id FROM articles WHERE discovered_at >= ?
		), discoveries AS (
			SELECT source_id, count(*)::int AS n FROM seen GROUP BY source_id
		), items AS (
			SELECT source_id, count(*)::int AS n, count(*) FILTER (WHERE published_at IS NULL)::int AS undated
			FROM articles WHERE discovered_at >= ? GROUP BY source_id
		), revisions AS (
			SELECT article_id FROM article_revisions WHERE created_at >= ? AND revision > 1
			GROUP BY article_id HAVING count(*) >= 5
		), repeated AS (
			SELECT a.source_id, count(*)::int AS n FROM revisions r JOIN articles a ON a.id = r.article_id GROUP BY a.source_id
		)
		SELECT s.id,s.name,s.participation_mode,s.created_at,s.health,s.fail_count,left(s.last_error,200) AS last_error,
			coalesce(r.runs,0) AS runs, coalesce(r.failed,0) AS failed, coalesce(r.detail_failures,0) AS detail_failures,
			coalesce(d.n,0) AS discoveries, coalesce(a.n,0) AS items, coalesce(a.undated,0) AS undated, coalesce(e.n,0) AS repeated
		FROM sources s LEFT JOIN runs r ON r.source_id = s.id LEFT JOIN discoveries d ON d.source_id = s.id
			LEFT JOIN items a ON a.source_id = s.id LEFT JOIN repeated e ON e.source_id = s.id
		WHERE s.enabled AND s.kind NOT IN ('external','mp_account') AND s.participation_mode IN ('editorial','hot_signal')
		ORDER BY s.name,s.id`, since, since, since, since, since).Scan(&snapshot.Sources).Error; err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (r *GormRepository) HasRun(ctx context.Context, since, before time.Time) (bool, error) {
	var found bool
	err := r.db.WithContext(ctx).Raw(`
		SELECT EXISTS (SELECT 1 FROM job_runs WHERE job = 'reports.source-health' AND started_at >= ? AND started_at < ?)`, since, before).Scan(&found).Error
	return found, err
}

var _ Repository = (*GormRepository)(nil)
