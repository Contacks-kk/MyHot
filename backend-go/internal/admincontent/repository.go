package admincontent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

type seoProjection struct {
	Visibility        string  `gorm:"column:visibility"`
	ParticipationMode string  `gorm:"column:participation_mode"`
	Summary           *string `gorm:"column:summary"`
	Selected          bool    `gorm:"column:selected"`
	Indexable         bool    `gorm:"column:indexable"`
}

// SetSEO changes only the editor-controlled index decision and its derived public bit. The article
// lock follows the same lock order as publication, and the audit shares the transaction.
func (r *GormRepository) SetSEO(ctx context.Context, articleID string, input SEOInput, actor string) (*PublishResult, error) {
	var result *PublishResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedID string
		article := tx.Raw("SELECT id FROM articles WHERE id = ? FOR UPDATE", articleID).Scan(&lockedID)
		if article.Error != nil {
			return article.Error
		}
		if article.RowsAffected == 0 {
			return nil
		}

		var projection seoProjection
		publication := tx.Raw(`SELECT p.visibility, s.participation_mode, p.summary, p.selected, p.indexable
			FROM publications p JOIN sources s ON s.id = p.source_id WHERE p.article_id = ? FOR UPDATE OF p`, articleID).Scan(&projection)
		if publication.Error != nil {
			return publication.Error
		}
		if publication.RowsAffected == 0 {
			return nil
		}

		indexable := input.Indexed && projection.Visibility == "public" && projection.ParticipationMode == "editorial" && projection.Summary != nil && *projection.Summary != ""
		updates := map[string]any{
			"seo_indexed_at":  gorm.Expr("CASE WHEN ? THEN COALESCE(seo_indexed_at, now()) ELSE NULL END", input.Indexed),
			"seo_excluded_at": gorm.Expr("CASE WHEN ? THEN NULL ELSE now() END", input.Indexed),
		}
		if projection.Indexable != indexable {
			updates["indexable"] = indexable
			updates["revision"] = gorm.Expr("revision + 1")
			updates["updated_at"] = gorm.Expr("now()")
		}
		if err := tx.Table("publications").Where("article_id = ?", articleID).Updates(updates).Error; err != nil {
			return err
		}

		before, err := json.Marshal(map[string]bool{"indexed": projection.Indexable})
		if err != nil {
			return err
		}
		after, err := json.Marshal(map[string]bool{"indexed": input.Indexed})
		if err != nil {
			return err
		}
		subject, reason := "content:"+articleID, input.Reason
		audit := model.AdminAuditLog{
			Actor: actor, Action: "content.seo", Subject: &subject, Reason: &reason, Before: before, After: after,
		}
		if err := tx.Create(&audit).Error; err != nil {
			return err
		}

		result = &PublishResult{
			ArticleID:  articleID,
			Changed:    projection.Indexable != indexable,
			ChangeKind: "content",
			Selected:   projection.Selected,
			Visibility: projection.Visibility,
			Reduced:    false,
		}
		return nil
	})
	return result, err
}

func (r *GormRepository) ByID(ctx context.Context, id string) ([]Row, error) {
	var rows []Row
	err := r.base(ctx).Where("a.id = ?", id).Order("a.discovered_at DESC, a.id").Limit(50).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) ByURL(ctx context.Context, urls, identities []string) ([]Row, error) {
	var rows []Row
	err := r.base(ctx).Where("a.url IN ? OR a.identity_key IN ?", urls, identities).Order("a.discovered_at DESC, a.id").Limit(50).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) ByTitle(ctx context.Context, term string) ([]Row, error) {
	var rows []Row
	pattern := "%" + term + "%"
	err := r.base(ctx).Where("(a.title ILIKE ? OR p.title ILIKE ?)", pattern, pattern).Order("a.discovered_at DESC, a.id").Limit(50).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) Chain(ctx context.Context, id string) (*Chain, error) {
	db := r.db.WithContext(ctx)
	chain := Chain{
		Discoveries: []Discovery{}, Revisions: []Revision{}, Analyses: []Analysis{}, Ledger: []LedgerEntry{},
		Membership: []Membership{}, Decisions: []Decision{}, Deliveries: []Delivery{}, History: []AuditEntry{},
	}
	article := db.Table("articles AS a").Select(`a.id, a.source_id, a.url, a.identity_key, a.title, a.author, a.language, a.published_at, a.published_at_claim,
		a.discovered_at, a.timeline_at, a.backfill, a.body_status, a.revision, a.processing_state, a.processing_error, a.grouped_at,
		length(a.body_text) AS body_chars, s.name AS source_name, s.kind AS source_kind, s.tier, s.participation_mode, s.site_fulltext, s.syndicate_fulltext`).
		Joins("JOIN sources s ON s.id = a.source_id").Where("a.id = ?", id).Take(&chain.Article)
	if errors.Is(article.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if article.Error != nil {
		return nil, article.Error
	}
	queries := []struct {
		dest any
		sql  string
	}{
		{&chain.Discoveries, `SELECT source_id, via, discovered_at FROM article_discoveries WHERE article_id = ? ORDER BY discovered_at`},
		{&chain.Revisions, `SELECT revision, title, content_hash, created_at FROM article_revisions WHERE article_id = ? ORDER BY revision DESC LIMIT 10`},
		{nil, `SELECT an.id, an.origin, an.model, an.prompt_version, an.input_revision, an.relevance, an.category, an.score, an.selected, an.title_zh, an.reason_zh,
			an.created_at, (SELECT coalesce(jsonb_agg(jsonb_build_object('id', r.id, 'status', r.status, 'service', r.service, 'model', r.model, 'cost', r.cost, 'at', r.created_at) ORDER BY r.id), '[]'::jsonb)
			FROM receipts r WHERE r.id = ANY(an.receipt_ids)) AS receipts FROM analyses an WHERE an.article_id = ? ORDER BY an.created_at DESC LIMIT 10`},
		{&chain.Ledger, `SELECT seq, op, visible_at, changed_at FROM selected_ledger WHERE article_id = ? ORDER BY seq DESC LIMIT 10`},
		{&chain.Membership, `SELECT fa.fact_id, fa.role, fa.manual, f.public_id AS fact_public_id, f.title AS fact_title,
			st.id AS story_id, st.public_id AS story_public_id, st.title AS story_title FROM fact_articles fa JOIN facts f ON f.id = fa.fact_id
			LEFT JOIN stories st ON st.id = f.story_id WHERE fa.article_id = ?`},
		{&chain.Decisions, `SELECT verdict, fact_id, story_id, receipt_id, candidates, created_at FROM grouping_decisions WHERE article_id = ? ORDER BY created_at DESC LIMIT 5`},
		{&chain.Deliveries, `SELECT target_key, dedupe_key, status, attempts, response, created_at, sent_at FROM deliveries WHERE subject_id = ? ORDER BY created_at DESC`},
		{&chain.History, `SELECT created_at, actor, action, reason, before, after FROM audit_log WHERE subject = ? ORDER BY created_at DESC LIMIT 20`},
	}
	for i := range queries {
		if i == 2 {
			var rows []analysisScan
			if err := db.Raw(queries[i].sql, id).Scan(&rows).Error; err != nil {
				return nil, err
			}
			chain.Analyses = make([]Analysis, 0, len(rows))
			for _, row := range rows {
				var receipts []AnalysisReceipt
				if len(row.ReceiptsJSON) > 0 {
					if err := json.Unmarshal(row.ReceiptsJSON, &receipts); err != nil {
						return nil, err
					}
				}
				chain.Analyses = append(chain.Analyses, Analysis{
					ID: row.ID, Origin: row.Origin, Model: row.Model, PromptVersion: row.PromptVersion, InputRevision: row.InputRevision,
					Relevance: row.Relevance, Category: row.Category, Score: row.Score, Selected: row.Selected, TitleZH: row.TitleZH,
					ReasonZH: row.ReasonZH, CreatedAt: row.CreatedAt, Receipts: receipts,
				})
			}
			continue
		}
		if err := db.Raw(queries[i].sql, id).Scan(queries[i].dest).Error; err != nil {
			return nil, err
		}
	}
	var publication Publication
	publicationResult := db.Table("publications").Where("article_id = ?", id).Take(&publication)
	if publicationResult.Error != nil && !errors.Is(publicationResult.Error, gorm.ErrRecordNotFound) {
		return nil, publicationResult.Error
	}
	if publicationResult.RowsAffected > 0 {
		chain.Publication = &publication
	}
	var override Override
	overrideResult := db.Table("editorial_overrides").Select("fields, visibility, reason, version, updated_by, updated_at").Where("article_id = ?", id).Take(&override)
	if overrideResult.Error != nil && !errors.Is(overrideResult.Error, gorm.ErrRecordNotFound) {
		return nil, overrideResult.Error
	}
	if overrideResult.RowsAffected > 0 {
		chain.Override = &override
	}
	if chain.Discoveries == nil {
		chain.Discoveries = []Discovery{}
	}
	if chain.Revisions == nil {
		chain.Revisions = []Revision{}
	}
	if chain.Ledger == nil {
		chain.Ledger = []LedgerEntry{}
	}
	if chain.Membership == nil {
		chain.Membership = []Membership{}
	}
	if chain.Decisions == nil {
		chain.Decisions = []Decision{}
	}
	if chain.Deliveries == nil {
		chain.Deliveries = []Delivery{}
	}
	if chain.History == nil {
		chain.History = []AuditEntry{}
	}
	return &chain, nil
}

type analysisScan struct {
	ID            int64           `gorm:"column:id"`
	Origin        string          `gorm:"column:origin"`
	Model         *string         `gorm:"column:model"`
	PromptVersion *string         `gorm:"column:prompt_version"`
	InputRevision int             `gorm:"column:input_revision"`
	Relevance     *string         `gorm:"column:relevance"`
	Category      *string         `gorm:"column:category"`
	Score         *float64        `gorm:"column:score"`
	Selected      *bool           `gorm:"column:selected"`
	TitleZH       *string         `gorm:"column:title_zh"`
	ReasonZH      *string         `gorm:"column:reason_zh"`
	CreatedAt     time.Time       `gorm:"column:created_at"`
	ReceiptsJSON  json.RawMessage `gorm:"column:receipts"`
}

func (r *GormRepository) base(ctx context.Context) *gorm.DB {
	return r.db.WithContext(ctx).Table("articles AS a").Select(`a.id, coalesce(p.title, a.title) AS title, a.url, s.name AS source,
		a.discovered_at, a.processing_state, p.visibility, p.selected, p.score`).
		Joins("JOIN sources s ON s.id = a.source_id").Joins("LEFT JOIN publications p ON p.article_id = a.id")
}
