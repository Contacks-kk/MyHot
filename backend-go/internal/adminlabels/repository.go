package adminlabels

import (
	"context"
	"sort"
	"strings"
	"time"

	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

// LabelType defines whether a label is a category, topic, or entity label.
type LabelType string

const (
	LabelTypeCategory LabelType = "category"
	LabelTypeTopic    LabelType = "topic"
	LabelTypeEntity   LabelType = "entity"
	LabelTypeCustom   LabelType = "custom"
)

// StableLabel represents an official vocabulary label with usage statistics.
type StableLabel struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Type          LabelType  `json:"type"`
	UseCount      int        `json:"use_count"`
	ArticleCount  int64      `json:"article_count"`
	SelectedCount int64      `json:"selected_count"`
	AvgScore      float64    `json:"avg_score"`
	MaxScore      float64    `json:"max_score"`
	LatestAt      *time.Time `json:"latest_at"`
}

// CandidateLabel represents a label being considered for promotion or synonym mapping.
type CandidateLabel struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	MappedTo     string     `json:"mapped_to"`
	Status       string     `json:"status"`
	UseCount     int        `json:"use_count"`
	ArticleCount int64      `json:"article_count"`
	MaxScore     float64    `json:"max_score"`
	AvgScore     float64    `json:"avg_score"`
	LatestAt     *time.Time `json:"latest_at"`
}

// LabelHotspot represents a publication associated with a label.
type LabelHotspot struct {
	ArticleID    string     `json:"article_id"`
	Title        string     `json:"title"`
	Summary      string     `json:"summary"`
	Source       string     `json:"source"`
	URL          string     `json:"url"`
	Category     string     `json:"category"`
	Tags         []string   `json:"tags"`
	Score        *float64   `json:"score"`
	Selected     bool       `json:"selected"`
	PublishedAt  *time.Time `json:"published_at"`
	DiscoveredAt time.Time  `json:"discovered_at"`
}

// Totals contains summary counts for the label management interface.
type Totals struct {
	StableCount    int `json:"stable_count"`
	CandidateCount int `json:"candidate_count"`
	TotalTagged    int `json:"total_tagged"`
}

// OverviewResult contains stable and candidate labels with metrics.
type OverviewResult struct {
	StableLabels    []StableLabel    `json:"stable_labels"`
	CandidateLabels []CandidateLabel `json:"candidate_labels"`
	Totals          Totals           `json:"totals"`
}

// HotspotsFilter specifies filtering and pagination for label hotspots.
type HotspotsFilter struct {
	Label    string
	Query    string
	SortBy   string
	Page     int
	PageSize int
}

// HotspotsResult contains paginated hotspot rows and total count.
type HotspotsResult struct {
	Label string         `json:"label"`
	Page  int            `json:"page"`
	Total int64          `json:"total"`
	Rows  []LabelHotspot `json:"rows"`
}

// Repository exposes label statistics and associated hotspot queries.
type Repository interface {
	Overview(ctx context.Context) (OverviewResult, error)
	Hotspots(ctx context.Context, filter HotspotsFilter) (HotspotsResult, error)
}

type GormRepository struct {
	db         *gorm.DB
	vocabulary editorial.Vocabulary
}

/** NewRepository initializes a label repository with database connection and industry vocabulary. */
func NewRepository(db *gorm.DB, vocabulary editorial.Vocabulary) *GormRepository {
	return &GormRepository{
		db:         db,
		vocabulary: vocabulary,
	}
}

type tagStatRow struct {
	Tag           string     `gorm:"column:tag"`
	ArticleCount  int64      `gorm:"column:article_count"`
	SelectedCount int64      `gorm:"column:selected_count"`
	AvgScore      float64    `gorm:"column:avg_score"`
	MaxScore      float64    `gorm:"column:max_score"`
	LatestAt      *time.Time `gorm:"column:latest_at"`
}

/** Overview computes metrics for stable official labels and discovers candidate labels under evaluation. */
func (r *GormRepository) Overview(ctx context.Context) (OverviewResult, error) {
	// 汇总 publications 标签使用统计
	var statRows []tagStatRow
	statsQuery := `WITH tag_stats AS (
		SELECT 
			unnest(p.tags) AS tag,
			count(*) AS article_count,
			count(*) FILTER (WHERE p.selected) AS selected_count,
			round(avg(p.score)::numeric, 1) AS avg_score,
			max(p.score) AS max_score,
			max(coalesce(p.published_at, p.discovered_at)) AS latest_at
		FROM publications p
		WHERE p.visibility = 'public'
		GROUP BY unnest(p.tags)
	)
	SELECT tag, article_count, selected_count, avg_score, max_score, latest_at 
	FROM tag_stats`

	if err := r.db.WithContext(ctx).Raw(statsQuery).Scan(&statRows).Error; err != nil {
		statRows = []tagStatRow{}
	}

	statsMap := make(map[string]tagStatRow, len(statRows))
	for _, row := range statRows {
		statsMap[row.Tag] = row
		statsMap[strings.ToLower(row.Tag)] = row
	}

	stableMap := make(map[string]StableLabel)
	candidateMap := make(map[string]CandidateLabel)

	// 1. 载入词表固定标准标签（分类标签、主题标签、重点实体）
	for _, name := range r.vocabulary.CategoryTags {
		stat := statsMap[name]
		stableMap[strings.ToLower(name)] = StableLabel{
			ID:            strings.ToLower(name),
			Name:          name,
			Type:          LabelTypeCategory,
			UseCount:      1,
			ArticleCount:  stat.ArticleCount,
			SelectedCount: stat.SelectedCount,
			AvgScore:      stat.AvgScore,
			MaxScore:      stat.MaxScore,
			LatestAt:      stat.LatestAt,
		}
	}

	for _, name := range r.vocabulary.TopicTags {
		stat := statsMap[name]
		stableMap[strings.ToLower(name)] = StableLabel{
			ID:            strings.ToLower(name),
			Name:          name,
			Type:          LabelTypeTopic,
			UseCount:      1,
			ArticleCount:  stat.ArticleCount,
			SelectedCount: stat.SelectedCount,
			AvgScore:      stat.AvgScore,
			MaxScore:      stat.MaxScore,
			LatestAt:      stat.LatestAt,
		}
	}

	for _, name := range r.vocabulary.EntityTags {
		stat := statsMap[name]
		stableMap[strings.ToLower(name)] = StableLabel{
			ID:            strings.ToLower(name),
			Name:          name,
			Type:          LabelTypeEntity,
			UseCount:      1,
			ArticleCount:  stat.ArticleCount,
			SelectedCount: stat.SelectedCount,
			AvgScore:      stat.AvgScore,
			MaxScore:      stat.MaxScore,
			LatestAt:      stat.LatestAt,
		}
	}

	// 2. 载入词表近义词映射作为候选待晋升标签
	for rawSynonym, targetCanonical := range r.vocabulary.TagSynonyms {
		stat := statsMap[rawSynonym]
		candidateMap[strings.ToLower(rawSynonym)] = CandidateLabel{
			ID:           strings.ToLower(rawSynonym),
			Name:         rawSynonym,
			MappedTo:     targetCanonical,
			Status:       "synonym",
			UseCount:     1,
			ArticleCount: stat.ArticleCount,
			MaxScore:     stat.MaxScore,
			AvgScore:     stat.AvgScore,
			LatestAt:     stat.LatestAt,
		}
	}

	// 将 publications 中出现但未入库的标签列入候选挖掘
	for tag, stat := range statsMap {
		key := strings.ToLower(tag)
		if _, isStable := stableMap[key]; isStable {
			continue
		}
		if existing, isCandidate := candidateMap[key]; isCandidate {
			existing.ArticleCount = stat.ArticleCount
			existing.MaxScore = stat.MaxScore
			existing.AvgScore = stat.AvgScore
			existing.LatestAt = stat.LatestAt
			candidateMap[key] = existing
			continue
		}

		candidateMap[key] = CandidateLabel{
			ID:           key,
			Name:         tag,
			MappedTo:     r.vocabulary.TagSynonyms[tag],
			Status:       "emerging",
			UseCount:     1,
			ArticleCount: stat.ArticleCount,
			MaxScore:     stat.MaxScore,
			AvgScore:     stat.AvgScore,
			LatestAt:     stat.LatestAt,
		}
	}

	var stableList []StableLabel
	for _, item := range stableMap {
		stableList = append(stableList, item)
	}

	// 稳定标签排序：热度高的排前面
	sort.Slice(stableList, func(i, j int) bool {
		if stableList[i].ArticleCount != stableList[j].ArticleCount {
			return stableList[i].ArticleCount > stableList[j].ArticleCount
		}
		if stableList[i].SelectedCount != stableList[j].SelectedCount {
			return stableList[i].SelectedCount > stableList[j].SelectedCount
		}
		return stableList[i].Name < stableList[j].Name
	})

	var candidateList []CandidateLabel
	for _, item := range candidateMap {
		candidateList = append(candidateList, item)
	}

	// 候选标签排序：引用频次与使用次数高的排前面
	sort.Slice(candidateList, func(i, j int) bool {
		if candidateList[i].UseCount != candidateList[j].UseCount {
			return candidateList[i].UseCount > candidateList[j].UseCount
		}
		if candidateList[i].ArticleCount != candidateList[j].ArticleCount {
			return candidateList[i].ArticleCount > candidateList[j].ArticleCount
		}
		return candidateList[i].Name < candidateList[j].Name
	})

	var totalTagged int
	for _, s := range statRows {
		totalTagged += int(s.ArticleCount)
	}

	return OverviewResult{
		StableLabels:    stableList,
		CandidateLabels: candidateList,
		Totals: Totals{
			StableCount:    len(stableList),
			CandidateCount: len(candidateList),
			TotalTagged:    totalTagged,
		},
	}, nil
}

type hotspotRow struct {
	ArticleID    string          `gorm:"column:article_id"`
	Title        string          `gorm:"column:title"`
	Summary      string          `gorm:"column:summary"`
	Source       string          `gorm:"column:source"`
	URL          string          `gorm:"column:url"`
	Category     string          `gorm:"column:category"`
	Tags         model.TextArray `gorm:"column:tags"`
	Score        *float64        `gorm:"column:score"`
	Selected     bool            `gorm:"column:selected"`
	PublishedAt  *time.Time      `gorm:"column:published_at"`
	DiscoveredAt time.Time       `gorm:"column:discovered_at"`
}

/** Hotspots queries publications linked to a specific label with fuzzy search and score ranking. */
func (r *GormRepository) Hotspots(ctx context.Context, filter HotspotsFilter) (HotspotsResult, error) {
	if filter.Page <= 0 {
		filter.Page = 1
	}

	if filter.PageSize <= 0 || filter.PageSize > 100 {
		filter.PageSize = 20
	}

	var whereClauses []string
	var args []any

	whereClauses = append(whereClauses, "p.visibility = 'public'")

	// 标签过滤条件：匹配文章自身标签、分类或模糊包含
	filter.Label = strings.TrimSpace(filter.Label)
	if filter.Label != "" {
		whereClauses = append(whereClauses, "(? = ANY(p.tags) OR p.category = ? OR array_to_string(p.tags, ',') ILIKE ?)")
		args = append(args, filter.Label, filter.Label, "%"+filter.Label+"%")
	}

	// 模糊检索词条件：匹配标题、摘要导读、信源名称或标签
	filter.Query = strings.TrimSpace(filter.Query)
	if filter.Query != "" {
		pattern := "%" + filter.Query + "%"
		whereClauses = append(whereClauses, "(p.title ILIKE ? OR p.summary ILIKE ? OR s.name ILIKE ? OR array_to_string(p.tags, ',') ILIKE ?)")
		args = append(args, pattern, pattern, pattern, pattern)
	}

	whereSQL := "WHERE " + strings.Join(whereClauses, " AND ")

	// 统计符合条件的总记录数
	var total int64
	countQuery := `SELECT count(*) FROM publications p JOIN sources s ON s.id = p.source_id ` + whereSQL
	if err := r.db.WithContext(ctx).Raw(countQuery, args...).Scan(&total).Error; err != nil {
		return HotspotsResult{}, err
	}

	// 排序逻辑：支持打分排名（高->低）、发布时间（新->旧）或低打分排查
	orderSQL := "ORDER BY p.score DESC NULLS LAST, p.timeline_at DESC"
	if filter.SortBy == "recent" {
		orderSQL = "ORDER BY coalesce(p.published_at, p.discovered_at) DESC, p.timeline_at DESC"
	} else if filter.SortBy == "score_asc" {
		orderSQL = "ORDER BY p.score ASC NULLS LAST, p.timeline_at DESC"
	}

	// 分页读取热点记录
	offset := (filter.Page - 1) * filter.PageSize
	dataQuery := `SELECT 
		p.article_id,
		p.title,
		coalesce(p.summary, '') AS summary,
		s.name AS source,
		p.url,
		coalesce(p.category, '') AS category,
		p.tags,
		p.score,
		p.selected,
		p.published_at,
		p.discovered_at
	FROM publications p
	JOIN sources s ON s.id = p.source_id ` + whereSQL + ` ` + orderSQL + ` LIMIT ? OFFSET ?`

	dataArgs := append(append([]any{}, args...), filter.PageSize, offset)

	var rows []hotspotRow
	if err := r.db.WithContext(ctx).Raw(dataQuery, dataArgs...).Scan(&rows).Error; err != nil {
		return HotspotsResult{}, err
	}

	hotspots := make([]LabelHotspot, 0, len(rows))
	for _, row := range rows {
		hotspots = append(hotspots, LabelHotspot{
			ArticleID:    row.ArticleID,
			Title:        row.Title,
			Summary:      row.Summary,
			Source:       row.Source,
			URL:          row.URL,
			Category:     row.Category,
			Tags:         []string(row.Tags),
			Score:        row.Score,
			Selected:     row.Selected,
			PublishedAt:  row.PublishedAt,
			DiscoveredAt: row.DiscoveredAt,
		})
	}

	return HotspotsResult{
		Label: filter.Label,
		Page:  filter.Page,
		Total: total,
		Rows:  hotspots,
	}, nil
}
