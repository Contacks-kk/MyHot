package admincontent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"aihot/backend-go/internal/publication"
	"github.com/jackc/pgx/v5"
)

const xEncodingRepairAction = "content.normalize-x-encoding"

/** PreviewXEncodingRepair loads the exact current material used by operator review. */
func (r *CommandsRepository) PreviewXEncodingRepair(ctx context.Context, id string) (*XEncodingPlan, error) {
	if r == nil || r.pool == nil {
		return nil, ErrXEncodingRepairUnavailable
	}
	material, found, err := readXEncodingMaterial(ctx, r.pool, id)
	if err != nil || !found {
		return nil, err
	}
	plan, err := PlanXEncodingRepair(material)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

/** NormalizeXEncoding applies an approved repair with projection and audit in one transaction. */
func (r *CommandsRepository) NormalizeXEncoding(ctx context.Context, id string, input XEncodingRepairInput, actor string) (*XEncodingRepairResult, bool, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	material, found, err := readXEncodingMaterialTx(ctx, tx, id, true)
	if err != nil || !found {
		return nil, found, err
	}
	prior, found, err := existingXEncodingRepair(ctx, tx, id)
	if err != nil {
		return nil, true, err
	}
	if found {
		if prior.RequestID != nil && *prior.RequestID == input.RequestID {
			return &prior.Result, true, nil
		}
		return &XEncodingRepairResult{
			ArticleID: id,
			Revision:  material.Revision,
			Status:    "already-normalized",
		}, true, nil
	}
	currentHash, err := xEncodingMaterialHash(material)
	if err != nil {
		return nil, true, err
	}
	if material.Revision != input.Version || currentHash != input.Hash {
		return nil, true, ErrCommandConflict
	}
	plan, err := PlanXEncodingRepair(material)
	if err != nil {
		return nil, true, err
	}
	if !plan.Changed {
		return &XEncodingRepairResult{
			ArticleID: id,
			Revision:  material.Revision,
			Status:    "unchanged",
		}, true, nil
	}
	beforeProjection, hasProjection, err := readXEncodingProjection(ctx, tx, id)
	if err != nil {
		return nil, true, err
	}
	if err := updateXEncodingMaterial(ctx, tx, material, plan); err != nil {
		return nil, true, err
	}
	if hasProjection {
		if _, err := r.publication.PublishTx(ctx, tx, id, publication.PublishOptions{}); err != nil {
			return nil, true, err
		}
		afterProjection, _, err := readXEncodingProjection(ctx, tx, id)
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
	result := XEncodingRepairResult{
		ArticleID: id,
		Revision:  material.Revision,
		Status:    "repaired",
	}
	after := map[string]any{
		"title":        plan.After.Title,
		"body_text":    plan.After.BodyText,
		"x_post":       plan.After.XPost,
		"content_hash": plan.After.ContentHash,
		"result":       result,
	}
	if err := insertAudit(ctx, tx, actor, xEncodingRepairAction, "content:"+id, input.Reason, plan.Before, after, input.RequestID); err != nil {
		return nil, true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, true, err
	}
	return &result, true, nil
}

/** readXEncodingMaterial reads one article and its provider kind from PostgreSQL. */
func readXEncodingMaterial(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) (XEncodingMaterial, bool, error) {
	return readXEncodingMaterialTx(ctx, pool, id, false)
}

/** readXEncodingMaterialTx supports both preview reads and the transaction's locked recheck. */
func readXEncodingMaterialTx(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string, lock bool) (XEncodingMaterial, bool, error) {
	statement := `SELECT a.id,s.kind,a.identity_key,a.url,a.revision,a.content_hash,a.title,a.body_text,a.excerpt,a.x_post,a.x_article
		FROM articles a JOIN sources s ON s.id=a.source_id WHERE a.id=$1`
	if lock {
		statement += ` FOR UPDATE OF a`
	}
	var material XEncodingMaterial
	err := queryer.QueryRow(ctx, statement, id).Scan(&material.ID, &material.Kind, &material.Identity, &material.URL, &material.Revision,
		&material.ContentHash, &material.Title, &material.BodyText, &material.Excerpt, &material.XPost, &material.XArticle)
	if errors.Is(err, pgx.ErrNoRows) {
		return XEncodingMaterial{}, false, nil
	}
	return material, err == nil, err
}

/** existingXEncodingRepair reads the audit marker that makes the repair one-time and idempotent. */
func existingXEncodingRepair(ctx context.Context, tx pgx.Tx, id string) (struct {
	RequestID *string
	Result    XEncodingRepairResult
}, bool, error) {
	var prior struct {
		RequestID *string
		After     json.RawMessage
		Result    XEncodingRepairResult
	}
	err := tx.QueryRow(ctx, `SELECT request_id,"after" FROM audit_log WHERE subject=$1 AND action=$2 ORDER BY id LIMIT 1`,
		"content:"+id, xEncodingRepairAction).Scan(&prior.RequestID, &prior.After)
	if errors.Is(err, pgx.ErrNoRows) {
		return struct {
			RequestID *string
			Result    XEncodingRepairResult
		}{}, false, nil
	}
	if err != nil {
		return struct {
			RequestID *string
			Result    XEncodingRepairResult
		}{}, false, err
	}
	var after struct {
		Result XEncodingRepairResult `json:"result"`
	}
	if err := json.Unmarshal(prior.After, &after); err != nil {
		return struct {
			RequestID *string
			Result    XEncodingRepairResult
		}{}, false, err
	}
	return struct {
		RequestID *string
		Result    XEncodingRepairResult
	}{RequestID: prior.RequestID, Result: after.Result}, true, nil
}

/** readXEncodingProjection snapshots editorial and grouping decisions that this repair must retain. */
func readXEncodingProjection(ctx context.Context, tx pgx.Tx, id string) (json.RawMessage, bool, error) {
	var projection json.RawMessage
	err := tx.QueryRow(ctx, `SELECT jsonb_build_object('selected',selected,'eligible',eligible,'visibility',visibility,'fact_id',fact_id,
		'story_id',story_id,'body_mode',body_mode,'selected_ready_at',selected_ready_at,'visible_after',visible_after)
		FROM publications WHERE article_id=$1`, id).Scan(&projection)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return projection, err == nil, err
}

/** updateXEncodingMaterial revises only the same article revision and its stored content hash. */
func updateXEncodingMaterial(ctx context.Context, tx pgx.Tx, material XEncodingMaterial, plan XEncodingPlan) error {
	if _, err := tx.Exec(ctx, `UPDATE articles SET title=$2,body_text=$3,x_post=$4::jsonb,content_hash=$5,updated_at=now() WHERE id=$1`,
		material.ID, plan.After.Title, plan.After.BodyText, string(plan.After.XPost), plan.After.ContentHash); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO article_revisions(article_id,revision,title,body_text,content_hash)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(article_id,revision) DO UPDATE SET title=EXCLUDED.title,body_text=EXCLUDED.body_text,content_hash=EXCLUDED.content_hash`,
		material.ID, material.Revision, plan.After.Title, plan.After.BodyText, plan.After.ContentHash)
	return err
}

var _ XEncodingRepairRepository = (*CommandsRepository)(nil)
