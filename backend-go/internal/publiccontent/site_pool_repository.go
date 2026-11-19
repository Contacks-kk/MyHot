package publiccontent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"aihot/backend-go/internal/topics"
	"gorm.io/gorm"
)

const sitePoolCountCap = sitePoolPageSize * sitePoolMaxPages

var sitePoolTrigramTerm = regexp.MustCompile(`[^\pL\pN]*[\pL\pN]{3}`)

type sitePoolStats struct {
	TodayCount int        `gorm:"column:today_count"`
	UpdatedAt  *time.Time `gorm:"column:updated_at"`
}

type sitePoolScanRow struct {
	ID                 string `gorm:"column:id"`
	Seat               bool   `gorm:"column:seat"`
	FactID             *int64 `gorm:"column:fact_id"`
	topics.PageItemRow `gorm:"embedded"`
}

func (r *GormRepository) SitePoolPage(ctx context.Context, query SitePoolQuery, now time.Time, terms []string, entityTag string, offset, limit int) (SitePoolData, error) {
	data := SitePoolData{Rows: []SitePoolRow{}}
	var ids []string
	var err error
	switch {
	case len(terms) == 0:
		ids, data.Total, err = r.sitePoolTimePage(ctx, query, now, nil, entityTag, offset, limit)
	case query.Tab == "relevance":
		ids, data.Total, err = r.sitePoolRelevancePage(ctx, query, now, terms, entityTag, offset, limit)
	default:
		ids, data.Total, err = r.sitePoolTimePage(ctx, query, now, terms, entityTag, offset, limit)
	}
	if err != nil {
		return SitePoolData{}, err
	}
	if len(ids) > 0 {
		var records []sitePoolScanRow
		if err := r.db.WithContext(ctx).Table("publications AS p").
			Select(`p.article_id AS id, p.selected, p.seat, p.fact_id, p.title, p.summary, p.reason, p.category, p.tags, p.score,
				p.channel, p.published_at, p.timeline_at, p.body_mode, s.name AS source_name, a.x_post, qt.text_zh AS quoted_zh`).
			Joins("JOIN sources AS s ON s.id = p.source_id").
			Joins("JOIN articles AS a ON a.id = p.article_id").
			Joins("LEFT JOIN quote_translations AS qt ON p.channel = 'x' AND qt.tweet_id = substring(a.x_post->'quoted'->>'url' from '/status/([0-9]+)')").
			Where("p.article_id IN ?", ids).Scan(&records).Error; err != nil {
			return SitePoolData{}, err
		}
		byID := make(map[string]SitePoolRow, len(records))
		for _, record := range records {
			selected := record.PageItemRow.Selected != nil && *record.PageItemRow.Selected
			byID[record.ID] = SitePoolRow{Item: record.PageItemRow, Selected: selected, Seat: record.Seat, FactID: record.FactID}
		}
		for _, id := range ids {
			if row, ok := byID[id]; ok {
				row.Item.ID = id
				data.Rows = append(data.Rows, row)
			}
		}
	}
	stats, err := r.sitePoolStats(ctx, query, now)
	if err != nil {
		return SitePoolData{}, err
	}
	data.TodayCount = stats.TodayCount
	data.UpdatedAt = stats.UpdatedAt
	return data, nil
}

func (r *GormRepository) sitePoolTimePage(ctx context.Context, query SitePoolQuery, now time.Time, terms []string, entityTag string, offset, limit int) ([]string, int, error) {
	base := r.sitePoolListedQuery(ctx, query, now)
	if len(terms) > 0 {
		base = sitePoolDirectMatches(base, terms, entityTag)
	}
	var total int64
	if err := r.db.WithContext(ctx).Table("(?) AS capped", base.Session(&gorm.Session{}).Select("1").Limit(sitePoolCountCap)).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	err := r.sitePoolListedQuery(ctx, query, now).
		Scopes(func(db *gorm.DB) *gorm.DB {
			if len(terms) > 0 {
				return sitePoolDirectMatches(db, terms, entityTag)
			}
			return db
		}).
		Select("p.article_id AS id").Order("p.timeline_at DESC").Order("p.article_id DESC").Limit(limit).Offset(offset).Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, int(total), nil
}

func (r *GormRepository) sitePoolRelevancePage(ctx context.Context, query SitePoolQuery, now time.Time, terms []string, entityTag string, offset, limit int) ([]string, int, error) {
	splitFields := len(terms) == 1 && sitePoolTrigramTerm.MatchString(terms[0]) && query.Channel == "all" && query.Category == nil && query.Tag == nil
	var matches string
	matchArgs := make([]any, 0, len(terms)*4)
	if splitFields {
		pattern := "%" + terms[0] + "%"
		matches = `SELECT coalesce(d.article_id, b.article_id) AS article_id,
			(CASE WHEN d.article_id IS NOT NULL THEN 3 ELSE 0 END) + (CASE WHEN b.article_id IS NOT NULL THEN 1 ELSE 0 END) AS part
			FROM (SELECT article_id FROM pool_search WHERE direct LIKE ?) d
			FULL JOIN (SELECT article_id FROM pool_search WHERE body LIKE ?) b ON b.article_id = d.article_id`
		matchArgs = append(matchArgs, pattern, pattern)
	} else {
		part := "0"
		any := make([]string, 0, len(terms))
		for _, term := range terms {
			pattern := "%" + term + "%"
			part += " + (CASE WHEN ps.direct LIKE ? THEN 3 WHEN ps.body LIKE ? THEN 1 ELSE 0 END)"
			matchArgs = append(matchArgs, pattern, pattern)
			any = append(any, "(ps.direct LIKE ? OR ps.body LIKE ?)")
		}
		for _, term := range terms {
			pattern := "%" + term + "%"
			matchArgs = append(matchArgs, pattern, pattern)
		}
		matches = "SELECT ps.article_id, (" + part + ") AS part FROM pool_search ps WHERE " + joinSitePoolAnd(any)
	}
	var builder strings.Builder
	builder.WriteString("WITH matches AS ")
	if splitFields {
		builder.WriteString("MATERIALIZED (")
	} else {
		builder.WriteString("NOT MATERIALIZED (")
	}
	builder.WriteString(matches)
	builder.WriteString("), scored AS MATERIALIZED (SELECT p.article_id, p.timeline_at, max(matches.part)")
	args := append([]any(nil), matchArgs...)
	for _, term := range terms {
		builder.WriteString(" + (CASE WHEN lower(p.title) LIKE ? THEN 6 ELSE 0 END)")
		args = append(args, "%"+term+"%")
	}
	if entityTag != "" {
		builder.WriteString(" + (CASE WHEN p.tags @> ARRAY[?]::text[] THEN 10 ELSE 0 END)")
		args = append(args, entityTag)
	}
	builder.WriteString(" AS rel FROM (")
	if entityTag != "" {
		builder.WriteString("SELECT article_id, part FROM matches UNION ALL SELECT article_id, 0 FROM publications WHERE tags @> ARRAY[?]::text[]")
		args = append(args, entityTag)
	} else {
		builder.WriteString("SELECT article_id, part FROM matches")
	}
	builder.WriteString(") AS matches JOIN publications p ON p.article_id = matches.article_id WHERE p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)")
	args = append(args, now)
	filterQuery := timelineQueryFromPool(query)
	filterSQL, filterArgs := sitePoolFilterSQL(filterQuery)
	if filterSQL != "" {
		builder.WriteString(" AND ")
		builder.WriteString(filterSQL)
		args = append(args, filterArgs...)
	}
	builder.WriteString(" GROUP BY p.article_id, p.timeline_at, p.title, p.tags), page AS MATERIALIZED (SELECT article_id, timeline_at, rel FROM scored ORDER BY rel DESC, timeline_at DESC, article_id DESC LIMIT ? OFFSET ?), total AS (SELECT count(*) AS n FROM (SELECT 1 FROM scored LIMIT ?) capped) SELECT page.article_id, page.timeline_at, total.n AS total FROM total LEFT JOIN page ON true ORDER BY page.rel DESC NULLS LAST, page.timeline_at DESC NULLS LAST, page.article_id DESC NULLS LAST")
	args = append(args, limit, offset, sitePoolCountCap)
	var result []struct {
		ID         *string   `gorm:"column:article_id"`
		TimelineAt time.Time `gorm:"column:timeline_at"`
		Total      int       `gorm:"column:total"`
	}
	if err := r.db.WithContext(ctx).Raw(builder.String(), args...).Scan(&result).Error; err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(result))
	total := 0
	if len(result) > 0 {
		total = result[0].Total
	}
	for _, row := range result {
		if row.ID != nil {
			ids = append(ids, *row.ID)
		}
	}
	return ids, total, nil
}

func (r *GormRepository) sitePoolStats(ctx context.Context, query SitePoolQuery, now time.Time) (sitePoolStats, error) {
	var result sitePoolStats
	filters := timelineQueryFromPool(query)
	filterSQL, filterArgs := sitePoolFilterSQL(filters)
	today := beijingDate(now)
	start, err := time.ParseInLocation("2006-01-02", today, beijingLocation())
	if err != nil {
		return result, err
	}
	querySQL := `SELECT (SELECT count(*) FROM publications p WHERE p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?) AND p.timeline_at >= ?`
	args := []any{now, start}
	if filterSQL != "" {
		querySQL += " AND " + filterSQL
		args = append(args, filterArgs...)
	}
	querySQL += `) AS today_count, (SELECT max(updated_at) FROM publications WHERE eligible) AS updated_at`
	if err := r.db.WithContext(ctx).Raw(querySQL, args...).Scan(&result).Error; err != nil {
		return result, err
	}
	return result, nil
}

func (r *GormRepository) SitePoolSeatHolders(ctx context.Context, factIDs []int64, now time.Time) (map[int64]SitePoolHolder, error) {
	result := make(map[int64]SitePoolHolder, len(factIDs))
	if len(factIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		FactID int64  `gorm:"column:fact_id"`
		ID     string `gorm:"column:id"`
		Title  string `gorm:"column:title"`
	}
	err := r.db.WithContext(ctx).Table("publications AS p").
		Select("p.fact_id, p.article_id AS id, p.title").
		Where("p.fact_id IN ? AND p.visibility = 'public' AND p.selected AND p.seat AND p.visible_after <= ?", factIDs, now).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.FactID] = SitePoolHolder{ID: row.ID, Title: row.Title}
	}
	return result, nil
}

func (r *GormRepository) sitePoolListedQuery(ctx context.Context, query SitePoolQuery, now time.Time) *gorm.DB {
	db := r.db.WithContext(ctx).Table("publications AS p").
		Where("p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)", now)
	return applySiteTimelineFilters(db, timelineQueryFromPool(query))
}

func sitePoolDirectMatches(db *gorm.DB, terms []string, entityTag string) *gorm.DB {
	parts := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms)+1)
	for _, term := range terms {
		parts = append(parts, "p.search_text LIKE ?")
		args = append(args, "%"+term+"%")
	}
	textMatch := "(" + joinSitePoolAnd(parts) + ")"
	if entityTag == "" {
		return db.Where(textMatch, args...)
	}
	args = append([]any{entityTag}, args...)
	return db.Where("(p.tags @> ARRAY[?]::text[] OR "+textMatch+")", args...)
}

func sitePoolFilterSQL(query SiteTimelineQuery) (string, []any) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	switch query.Channel {
	case "news", "x":
		conditions = append(conditions, "p.channel = ?")
		args = append(args, query.Channel)
	case "firstParty":
		conditions = append(conditions, "p.source_id IN (SELECT id FROM sources WHERE tier = 'T1')")
	}
	if query.Category != nil {
		conditions = append(conditions, "p.category = ?")
		args = append(args, *query.Category)
	}
	if query.Tag != nil {
		conditions = append(conditions, "p.tags @> ARRAY[?]::text[]")
		args = append(args, *query.Tag)
	}
	return joinSitePoolAnd(conditions), args
}

func timelineQueryFromPool(query SitePoolQuery) SiteTimelineQuery {
	return SiteTimelineQuery{Channel: query.Channel, Category: query.Category, Tag: query.Tag}
}

func joinSitePoolAnd(parts []string) string {
	if len(parts) == 0 {
		return "TRUE"
	}
	return fmt.Sprintf("(%s)", strings.Join(parts, " AND "))
}

func beijingLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err == nil {
		return location
	}
	return time.FixedZone("Asia/Shanghai", 8*60*60)
}

var _ SitePoolRepository = (*GormRepository)(nil)
