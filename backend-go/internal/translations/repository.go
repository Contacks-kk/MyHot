package translations

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
)

type Article struct {
	ID         string          `gorm:"column:id"`
	Revision   int             `gorm:"column:revision"`
	Channel    string          `gorm:"column:channel"`
	Language   *string         `gorm:"column:language"`
	BodyHTML   *string         `gorm:"column:body_html"`
	BodyText   *string         `gorm:"column:body_text"`
	XPost      json.RawMessage `gorm:"column:x_post"`
	Title      string          `gorm:"column:title"`
	Selected   bool            `gorm:"column:selected"`
	BodyMode   string          `gorm:"column:body_mode"`
	Visibility string          `gorm:"column:visibility"`
}

type QuoteCandidate struct {
	TweetID  string  `gorm:"column:tweet_id"`
	Text     string  `gorm:"column:text"`
	TextHash *string `gorm:"column:text_hash"`
	OwnZH    *string `gorm:"column:own_zh"`
}

type Translation struct {
	ArticleID string
	Revision  int
	Title     string
	BodyHTML  string
	BodyText  string
	Complete  bool
}

type QuoteTranslation struct {
	TweetID  string
	TextHash string
	TextZH   string
	Origin   string
}

type Repository interface {
	DueArticles(context.Context, int) ([]string, error)
	Article(context.Context, string) (*Article, error)
	SaveTranslation(context.Context, Translation) (bool, error)
	RecordAttempt(context.Context, string, int, string, *string) error
	QuoteCandidates(context.Context, int) ([]QuoteCandidate, error)
	SaveQuoteTranslation(context.Context, QuoteTranslation) (bool, error)
}

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) DueArticles(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 30
	}
	var ids []string
	err := r.db.WithContext(ctx).Raw(`SELECT p.article_id
		FROM publications p JOIN articles a ON a.id = p.article_id
		LEFT JOIN translations tr ON tr.article_id = p.article_id AND tr.lang = 'zh'
		WHERE p.selected AND p.visibility = 'public' AND p.body_mode = 'full' AND coalesce(a.language, '') <> 'zh'
		  AND (p.discovered_at > now() - interval '3 days'
		       OR EXISTS (SELECT 1 FROM article_revisions r WHERE r.article_id = a.id AND r.revision = a.revision AND r.revision > 1
		                  AND r.created_at > now() - interval '3 days'))
		  AND (tr.article_id IS NULL OR (tr.origin <> 'source' AND tr.revision < a.revision))
		  AND NOT EXISTS (SELECT 1 FROM translation_attempts t WHERE t.article_id = p.article_id AND t.revision = a.revision
		                  AND (t.outcome IN ('skipped', 'translated', 'partial') OR t.attempts >= 3))
		ORDER BY p.discovered_at DESC LIMIT ?`, limit).Scan(&ids).Error
	return ids, err
}

func (r *GormRepository) Article(ctx context.Context, id string) (*Article, error) {
	var article Article
	result := r.db.WithContext(ctx).Raw(`SELECT a.id, a.revision, p.channel, a.language, a.body_html, a.body_text, a.x_post,
		p.title, p.selected, p.body_mode, p.visibility
		FROM publications p JOIN articles a ON a.id = p.article_id WHERE p.article_id = ?`, id).Scan(&article)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &article, nil
}

func (r *GormRepository) SaveTranslation(ctx context.Context, translation Translation) (bool, error) {
	result := r.db.WithContext(ctx).Exec(`INSERT INTO translations (article_id, lang, revision, title, body_html, body_text, complete, origin)
		VALUES (?, 'zh', ?, ?, ?, ?, ?, 'model')
		ON CONFLICT (article_id, lang) DO UPDATE SET revision = EXCLUDED.revision, title = EXCLUDED.title, body_html = EXCLUDED.body_html,
			body_text = EXCLUDED.body_text, complete = EXCLUDED.complete, origin = 'model', created_at = now()
		WHERE translations.origin <> 'source' AND translations.revision <= EXCLUDED.revision
		  AND (translations.revision, translations.title, translations.body_html, translations.body_text, translations.complete, translations.origin)
		      IS DISTINCT FROM (EXCLUDED.revision, EXCLUDED.title, EXCLUDED.body_html, EXCLUDED.body_text, EXCLUDED.complete, EXCLUDED.origin)`,
		translation.ArticleID, translation.Revision, translation.Title, translation.BodyHTML, translation.BodyText, translation.Complete)
	return result.RowsAffected > 0, result.Error
}

func (r *GormRepository) RecordAttempt(ctx context.Context, articleID string, revision int, outcome string, reason *string) error {
	return r.db.WithContext(ctx).Exec(`INSERT INTO translation_attempts (article_id, revision, attempts, outcome, reason)
		SELECT ?, coalesce(nullif(?, 0), a.revision), 1, ?, ? FROM articles a WHERE a.id = ?
		ON CONFLICT (article_id) DO UPDATE SET
		  attempts = CASE WHEN translation_attempts.revision = EXCLUDED.revision THEN translation_attempts.attempts + 1 ELSE 1 END,
		  revision = EXCLUDED.revision, outcome = EXCLUDED.outcome, reason = EXCLUDED.reason, updated_at = now()`,
		articleID, revision, outcome, reason, articleID).Error
}

func (r *GormRepository) QuoteCandidates(ctx context.Context, days int) ([]QuoteCandidate, error) {
	if days <= 0 {
		days = 3
	}
	var candidates []QuoteCandidate
	err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT ON (q.tweet_id) q.tweet_id, a.x_post->'quoted'->>'text' AS text, qt.text_hash,
		(SELECT tr.body_text FROM articles o JOIN translations tr ON tr.article_id = o.id AND tr.lang = 'zh' AND tr.revision >= o.revision
		 WHERE o.identity_key = 'x:' || q.tweet_id AND tr.complete AND coalesce(tr.body_text, '') <> '') AS own_zh
		FROM publications p JOIN articles a ON a.id = p.article_id
		CROSS JOIN LATERAL (SELECT substring(a.x_post->'quoted'->>'url' from '/status/([0-9]+)') AS tweet_id) q
		LEFT JOIN quote_translations qt ON qt.tweet_id = q.tweet_id
		WHERE p.channel = 'x' AND p.selected AND p.visibility = 'public' AND p.body_mode = 'full'
		  AND p.discovered_at > now() - make_interval(days => ?)
		  AND q.tweet_id IS NOT NULL AND coalesce(a.x_post->'quoted'->>'text', '') <> ''
		ORDER BY q.tweet_id, p.discovered_at DESC`, days).Scan(&candidates).Error
	return candidates, err
}

func (r *GormRepository) SaveQuoteTranslation(ctx context.Context, quote QuoteTranslation) (bool, error) {
	result := r.db.WithContext(ctx).Exec(`INSERT INTO quote_translations (tweet_id, text_hash, text_zh, origin)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (tweet_id) DO UPDATE SET text_hash = EXCLUDED.text_hash, text_zh = EXCLUDED.text_zh,
			origin = EXCLUDED.origin, created_at = now()
		WHERE (quote_translations.text_hash, quote_translations.text_zh, quote_translations.origin)
		  IS DISTINCT FROM (EXCLUDED.text_hash, EXCLUDED.text_zh, EXCLUDED.origin)`, quote.TweetID, quote.TextHash, quote.TextZH, quote.Origin)
	return result.RowsAffected > 0, result.Error
}

var _ Repository = (*GormRepository)(nil)
