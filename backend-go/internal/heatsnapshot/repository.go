package heatsnapshot

import (
	"context"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

type sourceClockRow struct {
	ID           string     `gorm:"column:id"`
	LastOKAt     *time.Time `gorm:"column:last_ok_at"`
	IntervalMins int        `gorm:"column:interval_minutes"`
}

func (r *GormRepository) SourceClocks(ctx context.Context) ([]SourceClock, error) {
	var rows []sourceClockRow
	err := r.db.WithContext(ctx).Raw(`
		SELECT id, last_ok_at, interval_minutes
		FROM sources
		WHERE enabled AND kind IN ('rss', 'web_list', 'json_list', 'x_search')`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	clocks := make([]SourceClock, len(rows))
	for i, row := range rows {
		clocks[i] = SourceClock{ID: row.ID, LastOKAt: row.LastOKAt, IntervalMins: row.IntervalMins}
	}
	return clocks, nil
}

func (r *GormRepository) HeatRows(ctx context.Context, at time.Time, behind []string, storyIDs []int64, feeds CommunityFeeds) ([]HeatRow, error) {
	previous := at.Add(-6 * time.Hour)
	params := []any{
		at, previous, at.Add(-time.Duration(windowHours) * time.Hour), previous.Add(-time.Duration(windowHours) * time.Hour),
		model.TextArray(behind), model.TextArray(feeds.Dev), model.TextArray(feeds.HN), len(storyIDs) > 0, model.Int64Array(storyIDs),
	}
	var rows []HeatRow
	err := r.db.WithContext(ctx).Raw(heatRowsQuery, params...).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) Participants(ctx context.Context, storyID int64, at time.Time, feeds CommunityFeeds) ([]Participant, error) {
	previous := at.Add(-6 * time.Hour)
	params := []any{
		at, previous, at.Add(-time.Duration(windowHours) * time.Hour), previous.Add(-time.Duration(windowHours) * time.Hour),
		model.TextArray{}, model.TextArray(feeds.Dev), model.TextArray(feeds.HN), true, model.Int64Array{storyID},
	}
	var rows []Participant
	query := `WITH ` + heatParametersCTE + `,
` + currentSignalsCTE + `
SELECT DISTINCT ON (cs.participant_key) s.name, cs.kind, s.tier, cs.observed_at AS at
FROM current_signals cs JOIN sources s ON s.id = cs.source_id CROSS JOIN params
WHERE cs.story_id = ANY(params.story_ids) AND cs.observed_at > params.now_from AND cs.observed_at <= params.at
ORDER BY cs.participant_key, (cs.kind = 'editorial') DESC, cs.observed_at DESC`
	err := r.db.WithContext(ctx).Raw(query, params...).Scan(&rows).Error
	return rows, err
}

type staleStoryRow struct {
	Hour    time.Time `gorm:"column:hour"`
	StoryID int64     `gorm:"column:story_id"`
}

func (r *GormRepository) StaleHours(ctx context.Context, after, before time.Time) ([]StaleHour, error) {
	var rows []staleStoryRow
	err := r.db.WithContext(ctx).Raw(`
		SELECT hour, story_id
		FROM story_heat_hourly
		WHERE NOT complete AND hour > ? AND hour < ?
		ORDER BY hour, story_id`, after, before).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	stale := make([]StaleHour, 0)
	for _, row := range rows {
		last := len(stale) - 1
		if last < 0 || !stale[last].Hour.Equal(row.Hour) {
			stale = append(stale, StaleHour{Hour: row.Hour, StoryIDs: []int64{row.StoryID}})
			continue
		}
		stale[last].StoryIDs = append(stale[last].StoryIDs, row.StoryID)
	}
	return stale, nil
}

type heatRecord struct {
	StoryID      int64     `gorm:"column:story_id"`
	Hour         time.Time `gorm:"column:hour"`
	Heat         float64   `gorm:"column:heat"`
	Participants int       `gorm:"column:participants"`
	Cohort       int       `gorm:"column:cohort"`
	Complete     bool      `gorm:"column:complete"`
}

func (heatRecord) TableName() string { return "story_heat_hourly" }

func (r *GormRepository) SaveHeat(ctx context.Context, hour time.Time, rows []HeatRow) error {
	for start := 0; start < len(rows); start += 1000 {
		end := start + 1000
		if end > len(rows) {
			end = len(rows)
		}
		records := make([]heatRecord, 0, end-start)
		for _, row := range rows[start:end] {
			records = append(records, heatRecord{
				StoryID: row.StoryID, Hour: hour, Heat: heatIndex(row.Heat),
				Participants: row.Participants, Cohort: row.EditorialParticipants,
				Complete: row.BehindParticipants == 0,
			})
		}
		if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "story_id"}, {Name: "hour"}},
			DoUpdates: clause.AssignmentColumns([]string{"heat", "participants", "cohort", "complete"}),
		}).Create(&records).Error; err != nil {
			return err
		}
	}
	return nil
}

const heatParametersCTE = `
params AS (
  SELECT ?::timestamptz AS at, ?::timestamptz AS previous,
         ?::timestamptz AS now_from, ?::timestamptz AS previous_from,
         ?::text[] AS behind_sources, ?::text[] AS dev_feeds, ?::text[] AS hn_feeds,
         ?::boolean AS filter_stories, ?::bigint[] AS story_ids
)
`

const currentSignalsCTE = `current_signals AS (
  SELECT ss.story_id, ss.article_id, s.id AS source_id, ss.observed_at, s.created_at AS source_since,
         CASE WHEN s.participation_mode = 'editorial' THEN 'editorial' ELSE 'signal' END AS kind,
         CASE
           WHEN s.id = ANY(params.dev_feeds) THEN coalesce('dev:account:' || lower(substring(a.url from '^https://dev\.to/([A-Za-z0-9_-]{1,64})/[^/?#]+/?$')), 'unresolved:' || s.id)
           WHEN s.id = ANY(params.hn_feeds) THEN coalesce('hn:account:' || substring(a.author from '^[A-Za-z0-9_-]{1,64}$'), 'unresolved:' || s.id)
           WHEN s.signal_group_id IS NOT NULL THEN 'group:' || s.signal_group_id
           WHEN s.owner_entity_id IS NOT NULL THEN 'owner:' || s.owner_entity_id
           ELSE 'source:' || s.id
         END AS participant_key
  FROM story_signals ss JOIN articles a ON a.id = ss.article_id JOIN sources s ON s.id = a.source_id
  LEFT JOIN publications p ON p.article_id = ss.article_id CROSS JOIN params
  WHERE s.participation_mode <> 'isolated' AND coalesce(p.visibility, 'public') <> 'withdrawn'
    AND NOT EXISTS (SELECT 1 FROM grouping_overrides go WHERE go.article_id = ss.article_id AND go.mode = 'standalone')
    AND (s.participation_mode <> 'editorial' OR EXISTS (
      SELECT 1 FROM fact_articles fa JOIN facts f ON f.id = fa.fact_id
      WHERE fa.article_id = ss.article_id AND f.story_id = ss.story_id AND fa.role <> 'mention'
        AND NOT EXISTS (SELECT 1 FROM analyses evidence_an
          WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
    ))
)`

const heatRowsQuery = `
WITH ` + heatParametersCTE + `,
` + currentSignalsCTE + `,
obs AS (
  SELECT cs.story_id, cs.participant_key,
         max(cs.observed_at) FILTER (WHERE cs.observed_at > params.now_from) AS last_at,
         min(cs.observed_at) FILTER (WHERE cs.observed_at > params.now_from) AS first_at,
         coalesce(bool_or(cs.kind = 'editorial') FILTER (WHERE cs.observed_at > params.now_from), false) AS editorial,
         max(cs.observed_at) FILTER (WHERE cs.observed_at <= params.previous) AS last_prev,
         bool_or(cs.source_id = ANY(params.behind_sources)) AS behind,
         bool_or(cs.source_since > params.previous_from) AS late
  FROM current_signals cs CROSS JOIN params
  WHERE cs.observed_at > params.previous_from AND cs.observed_at <= params.at
    AND (NOT params.filter_stories OR cs.story_id = ANY(params.story_ids))
  GROUP BY cs.story_id, cs.participant_key
), agg AS (
  SELECT obs.story_id,
    count(*) FILTER (WHERE obs.last_at IS NOT NULL) AS participants,
    coalesce(sum(power(0.5, extract(epoch FROM (params.at - obs.last_at)) / 3600.0 / 24.0)) FILTER (WHERE obs.last_at IS NOT NULL), 0) AS heat,
    coalesce(sum(power(0.5, extract(epoch FROM (params.previous - obs.last_prev)) / 3600.0 / 24.0)) FILTER (WHERE obs.last_prev IS NOT NULL), 0) AS heat_prev,
    coalesce(sum(power(0.5, extract(epoch FROM (params.at - obs.last_at)) / 3600.0 / 24.0)) FILTER (WHERE obs.last_at IS NOT NULL AND NOT obs.behind AND NOT obs.late), 0) AS heat_obs,
    coalesce(sum(power(0.5, extract(epoch FROM (params.previous - obs.last_prev)) / 3600.0 / 24.0)) FILTER (WHERE obs.last_prev IS NOT NULL AND NOT obs.behind AND NOT obs.late), 0) AS heat_prev_obs,
    count(*) FILTER (WHERE obs.last_at IS NOT NULL AND obs.behind) AS behind_participants,
    count(*) FILTER (WHERE (obs.last_at IS NOT NULL OR obs.last_prev IS NOT NULL) AND (obs.behind OR obs.late)) AS uncomparable,
    count(*) FILTER (WHERE obs.first_at > params.previous) AS recent6h,
    count(*) FILTER (WHERE obs.last_at IS NOT NULL AND obs.editorial) AS editorial_participants,
    count(*) FILTER (WHERE obs.last_at IS NOT NULL AND NOT obs.editorial) AS signal_participants
  FROM obs CROSS JOIN params
  GROUP BY obs.story_id
  HAVING count(*) FILTER (WHERE obs.last_at IS NOT NULL) > 0
)
SELECT agg.story_id, st.public_id::text AS public_id, st.title, st.first_report_at, st.latest_at,
       agg.heat, agg.heat_prev, agg.heat_obs, agg.heat_prev_obs, agg.participants,
       agg.behind_participants, agg.uncomparable, agg.recent6h, agg.editorial_participants, agg.signal_participants
FROM agg JOIN stories st ON st.id = agg.story_id
WHERE st.merged_into IS NULL`

var _ Repository = (*GormRepository)(nil)
