package grouping

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const RecallDays = 14

type PoolRow struct {
	ArticleID string `json:"article_id" gorm:"column:article_id"`
	FactID    int64  `json:"fact_id" gorm:"column:fact_id"`
	StoryID   int64  `json:"story_id" gorm:"column:story_id"`
	FactTitle string `json:"fact_title" gorm:"column:fact_title"`
	Revision  int    `json:"revision" gorm:"column:revision"`
}

type ReadingRow struct {
	ArticleID  string     `gorm:"column:article_id"`
	Revision   int        `gorm:"column:revision"`
	Title      string     `gorm:"column:title"`
	Summary    *string    `gorm:"column:summary"`
	Source     string     `gorm:"column:source"`
	FirstParty bool       `gorm:"column:first_party"`
	At         *time.Time `gorm:"column:at"`
	Scope      *string    `gorm:"column:scope"`
}

type ReportTextRow struct {
	ArticleID string  `gorm:"column:article_id"`
	Title     string  `gorm:"column:title"`
	Summary   *string `gorm:"column:summary"`
}

// GormRepository owns the bounded candidate and reading-context queries used by grouping recall.
type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) RecallPool(ctx context.Context, now time.Time) ([]PoolRow, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	var rows []PoolRow
	err := r.db.WithContext(ctx).Raw(`SELECT fa.article_id, fa.fact_id, f.story_id, f.title AS fact_title,
		coalesce(p.revision, 0) AS revision
		FROM fact_articles fa
		JOIN facts f ON f.id = fa.fact_id
		JOIN stories st ON st.id = f.story_id AND st.merged_into IS NULL
		JOIN articles a ON a.id = fa.article_id
		LEFT JOIN publications p ON p.article_id = a.id
		WHERE fa.role IN ('primary', 'report')
		AND (SELECT x.output->>'scope' = 'composite' FROM analyses x
			WHERE x.article_id = a.id ORDER BY x.input_revision DESC, x.id DESC LIMIT 1) IS NOT TRUE
		AND a.discovered_at > ?`, now.Add(-RecallDays*24*time.Hour)).Scan(&rows).Error
	if rows == nil {
		rows = []PoolRow{}
	}
	return rows, err
}

func (r *GormRepository) SelectedReadingPool(ctx context.Context, now time.Time) ([]ReadingRow, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	var rows []ReadingRow
	err := r.db.WithContext(ctx).Raw(`SELECT p.article_id, p.revision, p.title, p.summary, s.name AS source,
		(s.tier = 'T1') AS first_party, coalesce(p.published_at, p.discovered_at) AS at,
		an.output->>'scope' AS scope
		FROM publications p
		JOIN sources s ON s.id = p.source_id
		LEFT JOIN analyses an ON an.id = p.analysis_id
		WHERE p.fact_id IS NULL AND p.visibility = 'public' AND p.selected AND p.visible_after <= ?
		AND p.discovered_at > ?`, now, now.Add(-RecallDays*24*time.Hour)).Scan(&rows).Error
	if rows == nil {
		rows = []ReadingRow{}
	}
	return rows, err
}

// ReportTexts returns the current public title and summary when available, then the newest analysis,
// then the original title. Embedding and lexical recall use the same text on both sides.
func (r *GormRepository) ReportTexts(ctx context.Context, articleIDs []string) (map[string]string, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(articleIDs))
	if len(articleIDs) == 0 {
		return result, nil
	}
	var rows []ReportTextRow
	err := r.db.WithContext(ctx).Raw(`SELECT a.id AS article_id,
		coalesce(p.title, an.title_zh, a.title) AS title, coalesce(p.summary, an.summary_zh) AS summary
		FROM articles a
		LEFT JOIN publications p ON p.article_id = a.id
		LEFT JOIN LATERAL (SELECT title_zh, summary_zh FROM analyses x WHERE x.article_id = a.id
			ORDER BY x.input_revision DESC, x.id DESC LIMIT 1) an ON true
		WHERE a.id IN ?`, articleIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ArticleID] = ReportText(row.Title, row.Summary)
	}
	return result, nil
}

func (r *GormRepository) ArticleBodies(ctx context.Context, articleIDs []string) (map[string]*string, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	result := make(map[string]*string, len(articleIDs))
	if len(articleIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		ID   string  `gorm:"column:id"`
		Text *string `gorm:"column:text"`
	}
	err := r.db.WithContext(ctx).Raw(`SELECT id, left(body_text, 12000) AS text FROM articles WHERE id IN ?`, articleIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = row.Text
	}
	return result, nil
}

func uniqueArticleIDs(pool []PoolRow) []string {
	seen := make(map[string]struct{}, len(pool))
	ids := make([]string, 0, len(pool))
	for _, row := range pool {
		if _, ok := seen[row.ArticleID]; ok {
			continue
		}
		seen[row.ArticleID] = struct{}{}
		ids = append(ids, row.ArticleID)
	}
	return ids
}

func (r *GormRepository) RelatedPosts(ctx context.Context, articleID, articleURL string, xIDs []string) (*PoolRow, []PoolRow, error) {
	if err := validateRepository(r); err != nil {
		return nil, nil, err
	}
	var sameURL []PoolRow
	err := r.db.WithContext(ctx).Raw(`SELECT fa.article_id, fa.fact_id, f.story_id, f.title AS fact_title, 0 AS revision
		FROM articles b
		JOIN fact_articles fa ON fa.article_id = b.id AND fa.role IN ('primary', 'report')
		JOIN facts f ON f.id = fa.fact_id
		JOIN stories st ON st.id = f.story_id AND st.merged_into IS NULL
		WHERE b.url = ? AND b.id <> ?
		AND (SELECT x.output->>'scope' = 'composite' FROM analyses x
			WHERE x.article_id = fa.article_id ORDER BY x.input_revision DESC, x.id DESC LIMIT 1) IS NOT TRUE
		ORDER BY fa.created_at LIMIT 1`, articleURL, articleID).Scan(&sameURL).Error
	if err != nil {
		return nil, nil, err
	}
	var referenced []PoolRow
	if len(xIDs) > 0 {
		identityKeys := make([]string, 0, len(xIDs))
		for _, id := range xIDs {
			identityKeys = append(identityKeys, "x:"+id)
		}
		err = r.db.WithContext(ctx).Raw(`SELECT fa.article_id, fa.fact_id, f.story_id, f.title AS fact_title, 0 AS revision
			FROM articles b
			JOIN fact_articles fa ON fa.article_id = b.id AND fa.role IN ('primary', 'report')
			JOIN facts f ON f.id = fa.fact_id
			JOIN stories st ON st.id = f.story_id AND st.merged_into IS NULL
			WHERE b.identity_key IN ? AND b.id <> ?
			AND (SELECT x.output->>'scope' = 'composite' FROM analyses x
				WHERE x.article_id = fa.article_id ORDER BY x.input_revision DESC, x.id DESC LIMIT 1) IS NOT TRUE`,
			identityKeys, articleID).Scan(&referenced).Error
	}
	if referenced == nil {
		referenced = []PoolRow{}
	}
	if len(sameURL) == 0 {
		return nil, referenced, err
	}
	return &sameURL[0], referenced, err
}

func (r *GormRepository) FindPoolRows(ctx context.Context, articleIDs []string) ([]PoolRow, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	if len(articleIDs) == 0 {
		return []PoolRow{}, nil
	}
	var rows []PoolRow
	err := r.db.WithContext(ctx).Raw(`SELECT fa.article_id, fa.fact_id, f.story_id, f.title AS fact_title,
		coalesce(p.revision, 0) AS revision
		FROM fact_articles fa JOIN facts f ON f.id = fa.fact_id JOIN stories st ON st.id = f.story_id AND st.merged_into IS NULL
		LEFT JOIN publications p ON p.article_id = fa.article_id
		WHERE fa.article_id IN ? AND fa.role IN ('primary', 'report')
		AND (SELECT x.output->>'scope' = 'composite' FROM analyses x WHERE x.article_id = fa.article_id
			ORDER BY x.input_revision DESC, x.id DESC LIMIT 1) IS NOT TRUE`, articleIDs).Scan(&rows).Error
	if rows == nil {
		rows = []PoolRow{}
	}
	return rows, err
}

func validateRepository(r *GormRepository) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("grouping repository is unavailable")
	}
	return nil
}
