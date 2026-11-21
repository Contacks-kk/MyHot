package tagging

import (
	"context"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type Repository struct{ db *gorm.DB }

type Article struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Excerpt  string    `json:"excerpt"`
	URL      string    `json:"-"`
	Revision int       `json:"-"`
	SeenAt   time.Time `json:"-"`
}

type CatalogEntry struct {
	Label     `gorm:"embedded"`
	Count     *int64 `json:"count"`
	UserCount int64  `gorm:"column:user_count" json:"userCount"`
}

type Evidence struct {
	Articles   int        `json:"articles"`
	Publishers int        `json:"publishers"`
	Weeks      int        `json:"weeks"`
	Latest     *time.Time `json:"latest"`
}

type AdminEntry struct {
	Label
	Evidence
}

/** NewRepository provides the persistence boundary for the interest label catalog. */
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

/** catalog returns labels in stable ID order for matching and prompt construction. */
func (r *Repository) catalog(ctx context.Context) ([]Label, error) {
	labels := []Label{}
	err := r.db.WithContext(ctx).Table("interest_labels").Order("id").Find(&labels).Error
	return labels, err
}

/** seed synchronizes the configured built-in labels without overwriting curation decisions. */
func (r *Repository) seed(ctx context.Context, items []Label) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCatalog(tx); err != nil {
			return err
		}
		for _, l := range items {
			if err := tx.Exec(`INSERT INTO interest_labels(id,name,name_key,kind,definition,aliases,status,builtin,promoted_at)
				VALUES(?,?,?,?,?,?,'active',true,now()) ON CONFLICT(id) DO NOTHING`,
				l.ID, l.Name, l.NameKey, l.Kind, l.Definition, l.Aliases).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

/** lockCatalog serializes quota, merge and lifecycle decisions across workers. */
func lockCatalog(tx *gorm.DB) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtext('interest-label-catalog'))").Error
}

/** pendingArticles returns recent unindexed or stale articles regardless of subscription type. */
func (r *Repository) pendingArticles(ctx context.Context, version string) ([]Article, error) {
	var rows []Article
	err := r.db.WithContext(ctx).Raw(`SELECT a.id,a.title,coalesce(a.excerpt,'') excerpt,a.revision
		FROM articles a LEFT JOIN article_label_index_state i ON i.article_id=a.id
		WHERE NOT a.backfill AND a.discovered_at>=now()-interval '90 days'
		  AND (i.article_id IS NULL OR i.revision<>a.revision OR i.catalog_version<>?)
		ORDER BY (i.article_id IS NULL) DESC,a.discovered_at DESC,a.id LIMIT 200`, version).Scan(&rows).Error
	return rows, err
}

/** selectedArticles returns scanner candidates missing coverage for their revision. */
func (r *Repository) selectedArticles(ctx context.Context, ids []string, version string) ([]Article, error) {
	var rows []Article
	err := r.db.WithContext(ctx).Raw(`SELECT a.id, a.title, coalesce(a.excerpt,'') AS excerpt, a.revision
		FROM articles a LEFT JOIN article_label_index_state i ON i.article_id=a.id
		WHERE a.id=ANY(?::text[]) AND NOT a.backfill
		AND (i.article_id IS NULL OR i.revision<>a.revision OR i.catalog_version<>?)
		ORDER BY a.id`, model.TextArray(ids), version).Scan(&rows).Error
	return rows, err
}

/** index replaces each article's label associations and coverage marker atomically. */
func (r *Repository) index(ctx context.Context, rows []Article, rules []matcher, version string) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCatalog(tx); err != nil {
			return err
		}
		catalog, err := (&Repository{db: tx}).catalog(ctx)
		if err != nil {
			return err
		}
		if _, current := compileCatalog(indexable(catalog)); current != version {
			return nil
		}
		for _, a := range rows {
			var revision int
			result := tx.Raw("SELECT revision FROM articles WHERE id=? FOR SHARE", a.ID).Scan(&revision)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 || revision != a.Revision {
				continue
			}
			if err := tx.Exec("DELETE FROM article_interest_labels WHERE article_id=?", a.ID).Error; err != nil {
				return err
			}
			if ids := matchArticle(rules, a.Title, a.Excerpt); len(ids) > 0 {
				if err := tx.Exec(`INSERT INTO article_interest_labels(article_id,label_id)
					SELECT ?,id FROM unnest(?::text[]) AS id ON CONFLICT DO NOTHING`, a.ID, model.TextArray(ids)).Error; err != nil {
					return err
				}
			}
			if err := tx.Exec(`INSERT INTO article_label_index_state(article_id,revision,catalog_version) VALUES(?,?,?)
				ON CONFLICT(article_id) DO UPDATE SET revision=excluded.revision,catalog_version=excluded.catalog_version`, a.ID, a.Revision, version).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

/** userCatalog counts only articles discovered from the requester's enabled subscriptions. */
func (r *Repository) userCatalog(ctx context.Context, userID int64, version string) ([]CatalogEntry, error) {
	rows := []CatalogEntry{}
	err := r.db.WithContext(ctx).Raw(`WITH scoped AS (
		SELECT DISTINCT a.id,a.revision FROM user_source_subscriptions sub
		JOIN article_discoveries d ON d.source_id=sub.source_id
		JOIN articles a ON a.id=d.article_id
		WHERE sub.user_id=? AND sub.enabled AND NOT a.backfill
		AND d.discovered_at >= now()-interval '7 days' AND d.discovered_at<=now()
	), coverage AS (
		SELECT NOT EXISTS (SELECT 1 FROM scoped s LEFT JOIN article_label_index_state i ON i.article_id=s.id
		WHERE i.article_id IS NULL OR i.revision<>s.revision OR i.catalog_version<>?) complete
	), counts AS (
		SELECT l.label_id,count(*) amount FROM article_interest_labels l JOIN scoped s ON s.id=l.article_id GROUP BY l.label_id
	), followers AS (
		SELECT coalesce(m.merged_into, tag.id) AS label_id, count(DISTINCT u.user_id) AS user_count
		FROM user_interests u
		CROSS JOIN LATERAL unnest(coalesce(u.entity_ids, '{}'::text[]) || coalesce(u.label_ids, '{}'::text[])) AS tag(id)
		LEFT JOIN interest_labels m ON m.id=tag.id AND m.status='merged' AND m.merged_into IS NOT NULL
		GROUP BY 1
	)
	SELECT l.*,CASE WHEN coverage.complete THEN coalesce(c.amount,0) END count,
	       coalesce(f.user_count, 0) AS user_count
	FROM interest_labels l
	CROSS JOIN coverage LEFT JOIN counts c ON c.label_id=l.id
	LEFT JOIN followers f ON f.label_id=l.id
	WHERE l.status='active' OR (l.status='dormant' AND EXISTS (
		SELECT 1 FROM user_interests u WHERE u.user_id=? AND (l.id=ANY(u.label_ids || u.entity_ids) OR EXISTS (
			SELECT 1 FROM interest_labels m WHERE m.status='merged' AND m.merged_into=l.id AND m.id=ANY(u.label_ids || u.entity_ids)))))
	ORDER BY l.kind,l.name,l.id`, userID, version, userID).Scan(&rows).Error
	return rows, err
}

/** nextSource selects one shared feed eligible for a discovery call. */
func (r *Repository) nextSource(ctx context.Context) (string, error) {
	var rows []string
	err := r.db.WithContext(ctx).Raw(`SELECT s.id FROM sources s LEFT JOIN label_model_attempts m
		ON m.kind='discover' AND m.subject=s.id
		WHERE s.enabled AND s.participation_mode<>'isolated'
		AND (SELECT count(*) FROM interest_labels WHERE status='candidate')<100
		AND (m.attempted_at IS NULL OR m.attempted_at<=now()-interval '7 days')
		AND EXISTS (SELECT 1 FROM article_discoveries d JOIN articles a ON a.id=d.article_id
		JOIN sources origin ON origin.id=a.source_id AND origin.participation_mode<>'isolated'
		WHERE d.source_id=s.id AND NOT a.backfill AND d.discovered_at>=now()-interval '7 days')
		ORDER BY m.attempted_at NULLS FIRST,s.id LIMIT 1`).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return rows[0], nil
}

/** samples returns article evidence from a shared feed only. */
func (r *Repository) samples(ctx context.Context, sourceID string) ([]Article, error) {
	var rows []Article
	err := r.db.WithContext(ctx).Raw(`SELECT a.id,a.title,left(coalesce(a.excerpt,''),600) excerpt,max(d.discovered_at) seen_at
		FROM article_discoveries d JOIN articles a ON a.id=d.article_id JOIN sources s ON s.id=d.source_id
		JOIN sources origin ON origin.id=a.source_id AND origin.participation_mode<>'isolated'
		WHERE s.id=? AND s.enabled AND s.participation_mode<>'isolated' AND NOT a.backfill
		AND d.discovered_at BETWEEN now()-interval '7 days' AND now()
		GROUP BY a.id ORDER BY seen_at DESC,a.id LIMIT 10`, sourceID).Scan(&rows).Error
	return rows, err
}

/** claimCall prevents repeated provider calls for the same subject and bounds total daily calls. */
func (r *Repository) claimCall(ctx context.Context, kind, subject string, now time.Time) (bool, error) {
	var claimed bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCatalog(tx); err != nil {
			return err
		}
		var used int
		if err := tx.Raw("SELECT count(*) FROM label_model_attempts WHERE attempted_at>=?", now.Add(-24*time.Hour)).Scan(&used).Error; err != nil || used >= 20 {
			return err
		}
		result := tx.Exec(`INSERT INTO label_model_attempts(kind,subject,attempted_at) VALUES(?,?,?)
			ON CONFLICT(kind,subject) DO UPDATE SET attempted_at=excluded.attempted_at
			WHERE label_model_attempts.attempted_at<=excluded.attempted_at-interval '7 days'`, kind, subject, now)
		claimed = result.RowsAffected == 1
		return result.Error
	})
	return claimed, err
}

/** evidenceArticles restricts governance evidence to recent material from enabled shared sources. */
func (r *Repository) evidenceArticles(ctx context.Context, id string, days int) ([]Article, error) {
	var rows []Article
	err := r.db.WithContext(ctx).Raw(`SELECT a.id,a.title,left(coalesce(a.excerpt,''),600) excerpt,a.url,
		min(d.discovered_at) seen_at FROM article_interest_labels l JOIN articles a ON a.id=l.article_id
		JOIN article_discoveries d ON d.article_id=a.id JOIN sources s ON s.id=d.source_id
		JOIN sources origin ON origin.id=a.source_id AND origin.participation_mode<>'isolated'
		WHERE l.label_id=? AND NOT a.backfill AND s.enabled AND s.participation_mode<>'isolated'
		GROUP BY a.id HAVING min(d.discovered_at)>=now()-make_interval(days=>?)
		AND min(d.discovered_at)<=now() ORDER BY seen_at DESC,a.id`, id, days).Scan(&rows).Error
	return rows, err
}
