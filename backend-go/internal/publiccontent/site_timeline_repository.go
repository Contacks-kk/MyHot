package publiccontent

import (
	"context"
	"time"

	"aihot/backend-go/internal/topics"
	"gorm.io/gorm"
)

const timelineOwnFactEvidence = `EXISTS (
	SELECT 1 FROM fact_articles fa
	WHERE fa.fact_id = p.fact_id AND fa.article_id = p.article_id AND fa.role <> 'mention'
	AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
)`

func (r *GormRepository) SiteTimelineAnchors(ctx context.Context, query SiteTimelineQuery, now time.Time, after *siteTimelineCursor, limit int) ([]SiteTimelineAnchor, error) {
	grouped := r.siteTimelineGroupedQuery(query, now)
	anchorsQuery := r.db.WithContext(ctx).Table("(?) AS grouped", grouped)
	if after != nil {
		cursorTime := time.UnixMilli(after.Anchor).UTC()
		anchorsQuery = anchorsQuery.Where("anchor_at < ? OR (anchor_at = ? AND group_key COLLATE \"C\" < ?)", cursorTime, cursorTime, after.GroupKey)
	}
	var rows []struct {
		Key      string    `gorm:"column:group_key"`
		AnchorAt time.Time `gorm:"column:anchor_at"`
	}
	err := anchorsQuery.Select("group_key, anchor_at").Order("anchor_at DESC").Order("group_key COLLATE \"C\" DESC").Limit(limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	anchors := make([]SiteTimelineAnchor, 0, len(rows))
	for _, row := range rows {
		anchors = append(anchors, SiteTimelineAnchor{Key: row.Key, AnchorAt: row.AnchorAt})
	}
	return anchors, nil
}

func (r *GormRepository) SiteTimelineFacts(ctx context.Context, query SiteTimelineQuery, now time.Time, factIDs []int64) (SiteTimelineFactData, error) {
	data := SiteTimelineFactData{
		Members: []SiteTimelineMember{}, Pool: []SiteTimelinePoolReport{},
		Facts: []SiteTimelineFact{},
	}
	if len(factIDs) == 0 {
		return data, nil
	}
	var members []siteTimelineMemberRecord
	memberQuery := r.db.WithContext(ctx).Table("publications AS p").
		Select(`p.fact_id, p.article_id AS article_id, p.source_id AS source_id, s.tier AS source_tier,
			s.config->>'publisherRole' AS publisher_role, s.owner_entity_id, f.subject AS fact_subject,
			p.article_id AS id, p.title, p.summary, p.reason, p.category, p.tags, p.score, p.channel,
			p.published_at, p.timeline_at, p.body_mode, s.name AS source_name`).
		Joins("JOIN sources AS s ON s.id = p.source_id").
		Joins("LEFT JOIN facts AS f ON f.id = p.fact_id").
		Joins("JOIN articles AS a ON a.id = p.article_id").
		Where("p.fact_id IN ? AND p.visibility = 'public' AND p.selected AND p.visible_after <= ?", factIDs, now).
		Where(timelineOwnFactEvidence)
	memberQuery = applySiteTimelineFilters(memberQuery, query)
	if err := memberQuery.Scan(&members).Error; err != nil {
		return SiteTimelineFactData{}, err
	}
	data.Members = make([]SiteTimelineMember, 0, len(members))
	for _, row := range members {
		data.Members = append(data.Members, SiteTimelineMember{
			FactID: row.FactID, ArticleID: row.ArticleID, SourceID: row.SourceID, SourceTier: row.SourceTier,
			PublisherRole: row.PublisherRole, OwnerEntityID: row.OwnerEntityID, FactSubject: row.FactSubject,
			Item: row.PageItemRow,
		})
	}
	var pool []SiteTimelinePoolReport
	poolQuery := r.db.WithContext(ctx).Table("facts AS f").
		Select("DISTINCT f.id AS fact_id, p.article_id, p.source_id").
		Joins("JOIN fact_articles AS fa ON fa.fact_id = f.id").
		Joins("JOIN publications AS p ON p.article_id = fa.article_id").
		Where("f.id IN ? AND fa.role <> 'mention'", factIDs).
		Where("NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')").
		Where("p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)", now)
	poolQuery = applySiteTimelineFilters(poolQuery, query)
	if err := poolQuery.Scan(&pool).Error; err != nil {
		return SiteTimelineFactData{}, err
	}
	data.Pool = pool
	if err := r.db.WithContext(ctx).Table("facts").Select("id, public_id").Where("id IN ?", factIDs).Scan(&data.Facts).Error; err != nil {
		return SiteTimelineFactData{}, err
	}
	return data, nil
}

func (r *GormRepository) SiteTimelineItems(ctx context.Context, query SiteTimelineQuery, now time.Time, ids []string) ([]topics.PageItemRow, error) {
	if len(ids) == 0 {
		return []topics.PageItemRow{}, nil
	}
	var rows []topics.PageItemRow
	db := r.db.WithContext(ctx).Table("publications AS p").
		Select(`p.article_id AS id, p.title, p.summary, p.reason, p.category, p.tags, p.score, p.channel,
			p.published_at, p.timeline_at, p.body_mode, s.name AS source_name, a.x_post, qt.text_zh AS quoted_zh`).
		Joins("JOIN sources AS s ON s.id = p.source_id").
		Joins("JOIN articles AS a ON a.id = p.article_id").
		Joins("LEFT JOIN quote_translations AS qt ON p.channel = 'x' AND qt.tweet_id = substring(a.x_post->'quoted'->>'url' from '/status/([0-9]+)')").
		Where("p.article_id IN ? AND p.visibility = 'public' AND p.selected AND p.visible_after <= ?", ids, now)
	db = applySiteTimelineFilters(db, query)
	if err := db.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *GormRepository) SiteTimelineDayCounts(ctx context.Context, query SiteTimelineQuery, now time.Time, days []string) (map[string]int, error) {
	counts := make(map[string]int, len(days))
	if len(days) == 0 {
		return counts, nil
	}
	grouped := r.siteTimelineGroupedQuery(query, now)
	const dayExpression = `to_char(anchor_at AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD')`
	var rows []struct {
		Day   string `gorm:"column:day"`
		Count int    `gorm:"column:count"`
	}
	err := r.db.WithContext(ctx).Table("(?) AS grouped", grouped).
		Select(dayExpression+" AS day, count(*) AS count").
		Where(dayExpression+" IN ?", days).Group(dayExpression).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Count != 0 {
			counts[row.Day] = row.Count
		}
	}
	return counts, nil
}

func (r *GormRepository) siteTimelineGroupedQuery(query SiteTimelineQuery, now time.Time) *gorm.DB {
	base := r.db.Table("publications AS p").
		Select(`p.sort_at, CASE WHEN p.fact_id IS NOT NULL AND `+timelineOwnFactEvidence+`
			THEN 'f' || p.fact_id::text ELSE 'a' || p.article_id END AS group_key`).
		Where("p.visibility = 'public' AND p.selected AND p.visible_after <= ?", now)
	base = applySiteTimelineFilters(base, query)
	return r.db.Table("(?) AS base", base).
		Select("group_key, date_trunc('milliseconds', min(sort_at)) AS anchor_at").Group("group_key")
}

func applySiteTimelineFilters(db *gorm.DB, query SiteTimelineQuery) *gorm.DB {
	switch query.Channel {
	case "news", "x":
		db = db.Where("p.channel = ?", query.Channel)
	case "firstParty":
		db = db.Where("p.source_id IN (SELECT id FROM sources WHERE tier = 'T1')")
	}
	if query.Category != nil {
		db = db.Where("p.category = ?", *query.Category)
	}
	if query.Tag != nil {
		db = db.Where("p.tags @> ARRAY[?]::text[]", *query.Tag)
	}
	return db
}

type siteTimelineMemberRecord struct {
	FactID             int64   `gorm:"column:fact_id"`
	ArticleID          string  `gorm:"column:article_id"`
	SourceID           string  `gorm:"column:source_id"`
	SourceTier         string  `gorm:"column:source_tier"`
	PublisherRole      *string `gorm:"column:publisher_role"`
	OwnerEntityID      *string `gorm:"column:owner_entity_id"`
	FactSubject        *string `gorm:"column:fact_subject"`
	topics.PageItemRow `gorm:"embedded"`
}

var _ SiteTimelineRepository = (*GormRepository)(nil)
