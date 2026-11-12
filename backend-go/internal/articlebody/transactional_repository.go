package articlebody

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/publication"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// saveBodyAndProjection composes the body revision with the public projection withdrawal/update.
// A published article is never left serving a projection derived from a body revision that has
// already been superseded. The publication repository also writes its River effects in this tx.
func (r *Repository) saveBodyAndProjection(ctx context.Context, candidate Candidate, body *Extracted) (bool, error) {
	if body == nil {
		return r.MarkUnconfirmed(ctx, candidate.ID, candidate.Revision)
	}
	if candidate.HasPublication && !candidate.AllowHotSignal {
		return false, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var current struct {
		Title          string
		Excerpt        pgtype.Text
		ContentHash    pgtype.Text
		BodyText       pgtype.Text
		Revision       int
		PublishedAt    pgtype.Timestamptz
		DiscoveredAt   time.Time
		Backfill       bool
		BackfillReason pgtype.Text
		Media          []byte
	}
	err = tx.QueryRow(ctx, `SELECT title,excerpt,content_hash,body_text,revision,published_at,discovered_at,
		backfill,backfill_reason,media FROM articles
		WHERE id=$1 AND revision=$2 AND body_status='pending' FOR UPDATE`, candidate.ID, candidate.Revision).Scan(
		&current.Title, &current.Excerpt, &current.ContentHash, &current.BodyText, &current.Revision,
		&current.PublishedAt, &current.DiscoveredAt, &current.Backfill, &current.BackfillReason, &current.Media)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var hasPublication bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM publications WHERE article_id=$1)`, candidate.ID).Scan(&hasPublication); err != nil {
		return false, err
	}
	if hasPublication && !candidate.AllowHotSignal {
		return false, nil
	}

	oldExcerpt := ""
	if current.Excerpt.Valid {
		oldExcerpt = current.Excerpt.String
	}
	title := current.Title
	if body.Title != "" {
		title = body.Title
	}
	hash := materials.ContentHash(title, body.Text, oldExcerpt)
	var timeline *materials.TimelineDecision
	if !current.PublishedAt.Valid && body.PublishedAt != nil && !candidate.PublishedTimeAuthoritative {
		explicit := ""
		if current.BackfillReason.Valid && current.BackfillReason.String != "unknown-publication-time" {
			explicit = current.BackfillReason.String
		}
		decision := materials.DecideTimeline(body.PublishedAt, current.DiscoveredAt, explicit)
		if decision.PublishedAt != nil {
			timeline = &decision
		}
	}

	if timeline == nil && current.ContentHash.Valid && current.ContentHash.String == hash {
		if err := r.markBodyUnchanged(ctx, tx, candidate, body); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, nil
	}
	oldBody := ""
	if current.BodyText.Valid {
		oldBody = current.BodyText.String
	}
	if len(body.XArticle) == 0 && timeline == nil && current.BodyText.Valid && len(body.Text) < len(oldBody) {
		if err := r.markBodyUnchanged(ctx, tx, candidate, body); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, nil
	}
	if len(body.XArticle) == 0 && timeline == nil && current.BodyText.Valid && materials.SameBarringLoss(oldBody, body.Text) {
		if err := r.markBodyUnchanged(ctx, tx, candidate, body); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, nil
	}

	images, err := json.Marshal(body.Images)
	if err != nil {
		return false, err
	}
	if len(images) == 0 {
		images = []byte("[]")
	}
	var xArticle any
	if len(body.XArticle) > 0 {
		xArticle = string(body.XArticle)
	}
	var publishedAt, publishedClaim, timelineAt, backfillReason any
	var backfill any
	fillTimeline := timeline != nil
	if timeline != nil {
		publishedAt = timeline.PublishedAt
		publishedClaim = body.PublishedAt
		timelineAt = timeline.TimelineAt
		backfill = timeline.Backfill
		backfillReason = nullableString(timeline.BackfillReason)
	}
	_, err = tx.Exec(ctx, `UPDATE articles SET body_text=$1,body_status='ok',content_hash=$2,revision=revision+1,
		processing_state='new',processing_attempts=0,processing_retry_at=NULL,processing_error=NULL,processing_queued_at=NULL,
		grouping_status='pending',grouped_at=NULL,grouping_receipt_id=NULL,grouping_error=NULL,
		selection_adds_value=NULL,selection_value_reason=NULL,
		media=CASE WHEN coalesce(jsonb_array_length(media),0)=0 THEN $3::jsonb ELSE media END,
		body_html=CASE WHEN $4<>'' THEN $4 ELSE body_html END,
		title=CASE WHEN $5<>'' THEN $5 ELSE title END,
		x_article=CASE WHEN $6::jsonb IS NOT NULL THEN $6::jsonb ELSE x_article END,
		published_at=CASE WHEN $7 THEN $8 ELSE published_at END,
		published_at_claim=CASE WHEN $7 THEN $9 ELSE published_at_claim END,
		timeline_at=CASE WHEN $7 THEN $10 ELSE timeline_at END,
		backfill=CASE WHEN $7 THEN $11 ELSE backfill END,
		backfill_reason=CASE WHEN $7 THEN $12 ELSE backfill_reason END,
		updated_at=now() WHERE id=$13 AND revision=$14`, body.Text, hash, string(images), body.HTML, body.Title,
		xArticle, fillTimeline, publishedAt, publishedClaim, timelineAt, backfill, backfillReason, candidate.ID, current.Revision)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO article_revisions(article_id,revision,content_hash,title,body_text)
		VALUES($1,$2,$3,$4,$5)`, candidate.ID, current.Revision+1, hash, title, body.Text); err != nil {
		return false, err
	}
	if hasPublication {
		if _, err := r.publisher.PublishTx(ctx, tx, candidate.ID, publication.PublishOptions{}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) markBodyUnchanged(ctx context.Context, tx pgx.Tx, candidate Candidate, body *Extracted) error {
	var xArticle any
	if len(body.XArticle) > 0 {
		xArticle = string(body.XArticle)
	}
	_, err := tx.Exec(ctx, `UPDATE articles SET body_status='ok',processing_attempts=0,processing_retry_at=NULL,
		processing_queued_at=NULL,updated_at=now(),
		x_article=CASE WHEN $1::jsonb IS NOT NULL THEN $1::jsonb ELSE x_article END
		WHERE id=$2 AND revision=$3 AND body_status='pending'`, xArticle, candidate.ID, candidate.Revision)
	return err
}
