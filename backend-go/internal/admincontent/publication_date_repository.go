package admincontent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/publication"
	"github.com/jackc/pgx/v5"
)

const publicationDateCorrectionAction = "content.correct-publication-date"

/** PreviewPublicationDateCorrection reads the article and returns a proof-bound historical date plan. */
func (r *CommandsRepository) PreviewPublicationDateCorrection(ctx context.Context, id, publishedAt string) (*PublicationDateCorrectionPlan, error) {
	if r == nil || r.pool == nil {
		return nil, ErrPublicationDateCorrectionUnavailable
	}
	material, found, err := readPublicationDateMaterial(ctx, r.pool, id, false)
	if err != nil || !found {
		return nil, err
	}
	plan, err := PlanPublicationDateCorrection(material, publishedAt, time.Now())
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

/** CorrectPublicationDate applies a reviewed date change, projection check, and audit atomically. */
func (r *CommandsRepository) CorrectPublicationDate(ctx context.Context, id string, input PublicationDateCorrectionInput, actor string) (*PublicationDateCorrectionResult, bool, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	articleFound, err := r.lockArticle(ctx, tx, id)
	if err != nil || !articleFound {
		return nil, articleFound, err
	}
	material, found, err := readPublicationDateMaterial(ctx, tx, id, false)
	if err != nil || !found {
		return nil, found, err
	}
	prior, priorFound, err := existingPublicationDateCorrection(ctx, tx, id, actor, input.RequestID)
	if err != nil {
		return nil, true, err
	}
	if priorFound {
		if prior.PublishedAt != input.PublishedAt {
			return nil, true, ErrCommandConflict
		}
		return &prior, true, nil
	}
	plan, err := PlanPublicationDateCorrection(material, input.PublishedAt, time.Now())
	if err != nil {
		return nil, true, err
	}
	if material.Revision != input.Version || plan.Hash != input.Hash {
		return nil, true, ErrCommandConflict
	}
	if !plan.Changed {
		return &PublicationDateCorrectionResult{
			ArticleID:   id,
			Revision:    material.Revision,
			Status:      "unchanged",
			PublishedAt: input.PublishedAt,
		}, true, nil
	}
	beforeProjection, hasProjection, err := readPublicationDateProjection(ctx, tx, id)
	if err != nil {
		return nil, true, err
	}
	requested, err := parsePublicationDate(input.PublishedAt)
	if err != nil {
		return nil, true, err
	}
	updatedTimeline, err := parsePublicationDate(plan.After.TimelineAt)
	if err != nil {
		return nil, true, err
	}
	if _, err := tx.Exec(ctx, `UPDATE articles SET published_at=$2,published_at_claim=$2,timeline_at=$3,updated_at=now() WHERE id=$1`,
		id, requested, updatedTimeline); err != nil {
		return nil, true, err
	}
	if hasProjection {
		if _, err := r.publication.PublishTx(ctx, tx, id, publication.PublishOptions{}); err != nil {
			return nil, true, err
		}
		afterProjection, _, err := readPublicationDateProjection(ctx, tx, id)
		if err != nil {
			return nil, true, err
		}
		beforeJSON, err := canonicalJSON(beforeProjection)
		if err != nil {
			return nil, true, err
		}
		afterJSON, err := canonicalJSON(afterProjection)
		if err != nil {
			return nil, true, err
		}
		if !bytes.Equal(beforeJSON, afterJSON) {
			return nil, true, ErrCommandConflict
		}
	}
	result := PublicationDateCorrectionResult{
		ArticleID:   id,
		Revision:    material.Revision,
		Status:      "corrected",
		PublishedAt: input.PublishedAt,
	}
	after := map[string]any{
		"publishedAt":      plan.After.PublishedAt,
		"publishedAtClaim": plan.After.PublishedAtClaim,
		"timelineAt":       plan.After.TimelineAt,
		"result":           result,
	}
	if err := insertAudit(ctx, tx, actor, publicationDateCorrectionAction, "content:"+id, input.Reason, plan.Before, after, input.RequestID); err != nil {
		return nil, true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, true, err
	}
	return &result, true, nil
}

/** readPublicationDateMaterial loads all mutable identity fields bound to an approval hash. */
func readPublicationDateMaterial(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string, lock bool) (PublicationDateCorrectionMaterial, bool, error) {
	query := `SELECT id,identity_key,source_id,url,revision,content_hash,published_at,published_at_claim,discovered_at,timeline_at,backfill,backfill_reason,updated_at
		FROM articles WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var material PublicationDateCorrectionMaterial
	err := queryer.QueryRow(ctx, query, id).Scan(&material.ID, &material.Identity, &material.SourceID, &material.URL, &material.Revision,
		&material.ContentHash, &material.PublishedAt, &material.PublishedAtClaim, &material.DiscoveredAt, &material.TimelineAt,
		&material.Backfill, &material.BackfillReason, &material.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicationDateCorrectionMaterial{}, false, nil
	}
	return material, err == nil, err
}

/** readPublicationDateProjection snapshots every public decision except the corrected date and ordering metadata. */
func readPublicationDateProjection(ctx context.Context, tx pgx.Tx, id string) (json.RawMessage, bool, error) {
	var projection json.RawMessage
	err := tx.QueryRow(ctx, `SELECT to_jsonb(p)-ARRAY['published_at','timeline_at','sort_at','revision','updated_at']
		FROM publications p WHERE p.article_id=$1`, id).Scan(&projection)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return projection, err == nil, err
}

/** existingPublicationDateCorrection loads a prior result for actor-scoped request idempotency. */
func existingPublicationDateCorrection(ctx context.Context, tx pgx.Tx, id, actor, requestID string) (PublicationDateCorrectionResult, bool, error) {
	var after json.RawMessage
	err := tx.QueryRow(ctx, `SELECT "after" FROM audit_log WHERE subject=$1 AND action=$2 AND actor=$3 AND request_id=$4 ORDER BY id LIMIT 1`,
		"content:"+id, publicationDateCorrectionAction, actor, requestID).Scan(&after)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicationDateCorrectionResult{}, false, nil
	}
	if err != nil {
		return PublicationDateCorrectionResult{}, false, err
	}
	var previous struct {
		PublishedAt string                          `json:"publishedAt"`
		Result      PublicationDateCorrectionResult `json:"result"`
	}
	if err := json.Unmarshal(after, &previous); err != nil {
		return PublicationDateCorrectionResult{}, false, err
	}
	return previous.Result, true, nil
}

var _ PublicationDateCorrectionRepository = (*CommandsRepository)(nil)
