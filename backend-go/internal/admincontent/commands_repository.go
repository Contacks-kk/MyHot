package admincontent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"aihot/backend-go/internal/jobargs"
	"aihot/backend-go/internal/publication"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// CommandsRepository owns the transactional part of administrator content changes. Publication
// projection and River inserts share one PostgreSQL transaction so a committed command cannot
// leave public reads or follow-up work behind.
type CommandsRepository struct {
	pool               *pgxpool.Pool
	publication        *publication.Repository
	river              *river.Client[pgx.Tx]
	storyDigestEnabled bool
	reportCorrection   ReportCorrectionConfig
}

func NewCommandsRepository(pool *pgxpool.Pool, publisher *publication.Repository, riverClient *river.Client[pgx.Tx], storyDigestEnabled bool, reportCorrection ReportCorrectionConfig) *CommandsRepository {
	return &CommandsRepository{pool: pool, publication: publisher, river: riverClient, storyDigestEnabled: storyDigestEnabled, reportCorrection: reportCorrection}
}

func (r *CommandsRepository) SetVisibility(ctx context.Context, id string, input VisibilityInput, actor string) (*PublishResult, bool, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	articleFound, err := r.lockArticle(ctx, tx, id)
	if err != nil || !articleFound {
		return nil, articleFound, err
	}
	before, found, err := r.override(ctx, tx, id)
	if err != nil {
		return nil, false, err
	}
	if !found {
		before = overrideSnapshot{Fields: json.RawMessage(`{}`)}
	}
	if before.Version != input.Version {
		return nil, true, ErrCommandConflict
	}
	if _, err := tx.Exec(ctx, `INSERT INTO editorial_overrides(article_id,visibility,reason,version,updated_by)
		VALUES($1,$2,$3,1,$4) ON CONFLICT(article_id) DO UPDATE SET visibility=EXCLUDED.visibility,reason=EXCLUDED.reason,
		version=editorial_overrides.version+1,updated_by=EXCLUDED.updated_by,updated_at=now()`, id, input.Visibility, input.Reason, actor); err != nil {
		return nil, true, err
	}
	published, err := r.publication.PublishTx(ctx, tx, id, publication.PublishOptions{})
	if err != nil {
		return nil, true, err
	}
	changed := published != nil && (published.Reduced || valueOr(before.Visibility, "public") != input.Visibility)
	if changed {
		storyIDs, err := articleStoryIDs(ctx, tx, id)
		if err != nil {
			return nil, true, err
		}
		if err := r.enqueueStoryDigests(ctx, tx, storyIDs); err != nil {
			return nil, true, err
		}
		if err := r.enqueueHotRanking(ctx, tx); err != nil {
			return nil, true, err
		}
	}
	oldVisibility := map[string]any{}
	if before.Visibility != nil {
		oldVisibility["visibility"] = *before.Visibility
	}
	if err := insertAudit(ctx, tx, actor, "content.visibility", "content:"+id, input.Reason,
		oldVisibility, map[string]any{"visibility": input.Visibility}, ""); err != nil {
		return nil, true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, true, err
	}
	return publishResult(published), true, nil
}

// OverrideFields applies an editorial correction and its report/audit effects atomically.
func (r *CommandsRepository) OverrideFields(ctx context.Context, id string, input OverrideInput, actor string) (*PublishResult, bool, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	articleFound, err := r.lockArticle(ctx, tx, id)
	if err != nil || !articleFound {
		return nil, articleFound, err
	}
	before, found, err := r.override(ctx, tx, id)
	if err != nil {
		return nil, false, err
	}
	if !found {
		before = overrideSnapshot{Fields: json.RawMessage(`{}`)}
	}
	if before.Version != input.Version {
		return nil, true, ErrCommandConflict
	}
	fields := map[string]json.RawMessage{}
	if len(before.Fields) > 0 && string(before.Fields) != "null" {
		if err := json.Unmarshal(before.Fields, &fields); err != nil {
			return nil, true, err
		}
	}
	for key, value := range input.Fields {
		fields[key] = value
	}
	for _, key := range input.Clear {
		delete(fields, key)
	}
	next, err := json.Marshal(fields)
	if err != nil {
		return nil, true, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO editorial_overrides(article_id,fields,reason,version,updated_by)
		VALUES($1,$2::jsonb,$3,1,$4) ON CONFLICT(article_id) DO UPDATE SET fields=EXCLUDED.fields,reason=EXCLUDED.reason,
		version=editorial_overrides.version+1,updated_by=EXCLUDED.updated_by,updated_at=now()`, id, next, input.Reason, actor); err != nil {
		return nil, true, err
	}
	published, err := r.publication.PublishTx(ctx, tx, id, publication.PublishOptions{})
	if err != nil {
		return nil, true, err
	}
	if published != nil && published.Changed {
		changedClassification := hasField(input.Fields, "category") || hasField(input.Fields, "tags") || containsString(input.Clear, "category") || containsString(input.Clear, "tags")
		if changedClassification {
			if err := r.correctReportClassification(ctx, tx, id, input.Reason); err != nil {
				return nil, true, err
			}
		}
		if !changedClassification || hasNonClassificationOverride(input.Fields, input.Clear) {
			storyIDs, err := articleStoryIDs(ctx, tx, id)
			if err != nil {
				return nil, true, err
			}
			if err := r.enqueueStoryDigests(ctx, tx, storyIDs); err != nil {
				return nil, true, err
			}
		}
	}
	if err := insertAudit(ctx, tx, actor, "content.override", "content:"+id, input.Reason, rawObject(before.Fields), fields, ""); err != nil {
		return nil, true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, true, err
	}
	return publishResult(published), true, nil
}

func hasField(fields map[string]json.RawMessage, key string) bool { _, ok := fields[key]; return ok }

// hasNonClassificationOverride reports whether an edit also changes content beyond category or tags.
func hasNonClassificationOverride(fields map[string]json.RawMessage, clear []string) bool {
	for key := range fields {
		if key != "category" && key != "tags" {
			return true
		}
	}
	for _, key := range clear {
		if key != "category" && key != "tags" {
			return true
		}
	}
	return false
}

func (r *CommandsRepository) Rerun(ctx context.Context, id, step, requestID, actor string) (*RerunResult, bool, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	articleFound, err := r.lockArticle(ctx, tx, id)
	if err != nil || !articleFound {
		return nil, articleFound, err
	}
	action := "content.rerun." + step
	var prior *string
	err = tx.QueryRow(ctx, `SELECT after->>'jobId' FROM audit_log WHERE subject=$1 AND action=$2 AND actor=$3 AND request_id=$4 ORDER BY id DESC LIMIT 1`,
		"content:"+id, action, actor, requestID).Scan(&prior)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, true, err
		}
		return &RerunResult{JobID: prior}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, true, err
	}
	var result *rivertype.JobInsertResult
	switch step {
	case "extract":
		if _, err := tx.Exec(ctx, `UPDATE articles SET processing_state='new',processing_error=NULL,processing_attempts=0,processing_retry_at=NULL,body_status='pending' WHERE id=$1`, id); err != nil {
			return nil, true, err
		}
		result, err = r.river.InsertTx(ctx, tx, jobargs.SharedBodyArgs{ArticleID: id}, &river.InsertOpts{Queue: "shared_content", Priority: 2, MaxAttempts: 3, UniqueOpts: activeArgsUniqueness()})
	case "analyze":
		attemptTag := "admin:" + requestID

		// Keep the paid-call identity on the article so a later sweep can rebuild a lost job.
		if _, err := tx.Exec(ctx, `UPDATE articles SET processing_state='new',processing_error=NULL,processing_attempts=0,
			processing_retry_at=NULL,processing_queued_at=now(),processing_attempt_tag=$2 WHERE id=$1`, id, attemptTag); err != nil {
			return nil, true, err
		}
		result, err = r.river.InsertTx(ctx, tx, jobargs.ContentAnalyzeArgs{ArticleID: id, AttemptTag: attemptTag}, &river.InsertOpts{Queue: "shared_analysis", Priority: 1, MaxAttempts: 5, UniqueOpts: activeArgsUniqueness()})
	case "group":
		if err = r.resetGrouping(ctx, tx, id); err != nil {
			return nil, true, err
		}
		if _, err = r.publication.PublishTx(ctx, tx, id, publication.PublishOptions{}); err != nil {
			return nil, true, err
		}
		result, err = r.river.InsertTx(ctx, tx, jobargs.EventGroupArgs{ArticleID: id}, &river.InsertOpts{Queue: "shared_grouping", Priority: 1, MaxAttempts: 5, UniqueOpts: activeArgsUniqueness()})
	}
	if err != nil {
		return nil, true, err
	}
	var jobID *string
	if result != nil && result.Job != nil {
		value := fmt.Sprint(result.Job.ID)
		jobID = &value
	}
	if err := insertAudit(ctx, tx, actor, action, "content:"+id, "", nil, map[string]any{"jobId": jobID, "requestId": requestID}, requestID); err != nil {
		return nil, true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, true, err
	}
	return &RerunResult{JobID: jobID}, true, nil
}

func (r *CommandsRepository) Detach(ctx context.Context, id, reason, actor string) (map[string]int, bool, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	articleFound, err := r.lockArticle(ctx, tx, id)
	if err != nil || !articleFound {
		return nil, articleFound, err
	}
	var facts []int64
	rows, err := tx.Query(ctx, `DELETE FROM fact_articles WHERE article_id=$1 RETURNING fact_id`, id)
	if err != nil {
		return nil, true, err
	}
	for rows.Next() {
		var fact int64
		if err := rows.Scan(&fact); err != nil {
			rows.Close()
			return nil, true, err
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, true, err
	}
	rows.Close()
	storyIDs := make(map[int64]struct{})
	if len(facts) > 0 {
		var stories []int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT story_id), ARRAY[]::bigint[]) FROM facts WHERE id=ANY($1) AND story_id IS NOT NULL`, facts).Scan(&stories); err != nil {
			return nil, true, err
		}
		for _, story := range stories {
			storyIDs[story] = struct{}{}
		}
	}
	rows, err = tx.Query(ctx, `DELETE FROM story_signals WHERE article_id=$1 RETURNING story_id`, id)
	if err != nil {
		return nil, true, err
	}
	for rows.Next() {
		var story int64
		if err := rows.Scan(&story); err != nil {
			rows.Close()
			return nil, true, err
		}
		storyIDs[story] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, true, err
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `INSERT INTO grouping_overrides(article_id,reason,actor) VALUES($1,$2,$3)
		ON CONFLICT(article_id) DO UPDATE SET reason=EXCLUDED.reason,actor=EXCLUDED.actor,created_at=now()`, id, reason, actor); err != nil {
		return nil, true, err
	}
	if _, err := tx.Exec(ctx, `UPDATE articles SET grouped_at=now(),grouping_status='complete',grouping_receipt_id=NULL,grouping_error=NULL,selection_adds_value=true,selection_value_reason=NULL WHERE id=$1`, id); err != nil {
		return nil, true, err
	}
	if _, err := r.publication.PublishTx(ctx, tx, id, publication.PublishOptions{}); err != nil {
		return nil, true, err
	}
	if len(facts) > 0 {
		var others []string
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT article_id), ARRAY[]::text[]) FROM fact_articles WHERE fact_id=ANY($1)`, facts).Scan(&others); err != nil {
			return nil, true, err
		}
		for _, other := range others {
			if _, err := r.publication.PublishTx(ctx, tx, other, publication.PublishOptions{}); err != nil {
				return nil, true, err
			}
		}
	}
	if err := r.enqueueStoryDigests(ctx, tx, mapKeys(storyIDs)); err != nil {
		return nil, true, err
	}
	if err := insertAudit(ctx, tx, actor, "content.detach", "content:"+id, reason, map[string]any{"facts": facts, "stories": mapKeys(storyIDs)}, nil, ""); err != nil {
		return nil, true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, true, err
	}
	return map[string]int{"detached": len(facts)}, true, nil
}

func (r *CommandsRepository) MergeStories(ctx context.Context, fromID, intoID int64, reason, actor string) (map[string]int, bool, error) {
	if fromID == intoID {
		return nil, false, ErrInvalidCommand
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var stories []struct {
		ID         int64
		PublicID   string
		MergedInto *int64
		Origin     string
	}
	rows, err := tx.Query(ctx, `SELECT id,public_id,merged_into,origin FROM stories WHERE id=ANY($1) ORDER BY id FOR UPDATE`, []int64{fromID, intoID})
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		var row struct {
			ID         int64
			PublicID   string
			MergedInto *int64
			Origin     string
		}
		if err := rows.Scan(&row.ID, &row.PublicID, &row.MergedInto, &row.Origin); err != nil {
			rows.Close()
			return nil, false, err
		}
		stories = append(stories, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	if len(stories) != 2 {
		return nil, false, nil
	}
	var from, into *struct {
		ID         int64
		PublicID   string
		MergedInto *int64
		Origin     string
	}
	for i := range stories {
		if stories[i].ID == fromID {
			from = &stories[i]
		}
		if stories[i].ID == intoID {
			into = &stories[i]
		}
	}
	if from == nil || into == nil || from.MergedInto != nil || into.MergedInto != nil {
		return nil, false, nil
	}
	if !groupingMergeAllowed(actor, from.Origin, into.Origin) {
		return nil, false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE facts SET story_id=$2,updated_at=now() WHERE story_id=$1`, fromID, intoID); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO story_signals(story_id,article_id,participant_key,source_id,kind,observed_at)
		SELECT $2,article_id,participant_key,source_id,kind,observed_at FROM story_signals WHERE story_id=$1 ON CONFLICT(story_id,article_id) DO NOTHING`, fromID, intoID); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM story_signals WHERE story_id=$1`, fromID); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE stories SET merged_into=$2,version=version+1,updated_at=now() WHERE id=$1`, fromID, intoID); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE stories SET version=version+1,updated_at=now(),latest_at=greatest(latest_at,(SELECT latest_at FROM stories WHERE id=$1)),
		first_report_at=least(first_report_at,(SELECT first_report_at FROM stories WHERE id=$1)) WHERE id=$2`, fromID, intoID); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO story_aliases(public_id,story_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, from.PublicID, intoID); err != nil {
		return nil, false, err
	}
	var articles []string
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT fa.article_id),ARRAY[]::text[]) FROM fact_articles fa JOIN facts f ON f.id=fa.fact_id WHERE f.story_id=$1`, intoID).Scan(&articles); err != nil {
		return nil, false, err
	}
	for _, articleID := range articles {
		if _, err := r.publication.PublishTx(ctx, tx, articleID, publication.PublishOptions{}); err != nil {
			return nil, false, err
		}
	}
	if err := r.enqueueStoryDigests(ctx, tx, []int64{intoID}); err != nil {
		return nil, false, err
	}
	if err := insertAudit(ctx, tx, actor, "story.merge", fmt.Sprintf("story:%d", fromID), reason, nil, map[string]any{"into": intoID}, ""); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return map[string]int{"moved": len(articles)}, true, nil
}

/** groupingMergeAllowed prevents automatic merges from overriding either editor-confirmed identity. */
func groupingMergeAllowed(actor, fromOrigin, intoOrigin string) bool {
	return actor != "grouping" || (fromOrigin != "manual" && intoOrigin != "manual")
}

type overrideSnapshot struct {
	Fields     json.RawMessage
	Visibility *string
	Version    int
}

func (r *CommandsRepository) begin(ctx context.Context) (pgx.Tx, error) {
	if r.pool == nil || r.publication == nil || r.river == nil {
		return nil, ErrCommandsUnavailable
	}
	return r.pool.Begin(ctx)
}

func (r *CommandsRepository) lockArticle(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var locked string
	err := tx.QueryRow(ctx, `SELECT id FROM articles WHERE id=$1 FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *CommandsRepository) override(ctx context.Context, tx pgx.Tx, id string) (overrideSnapshot, bool, error) {
	var out overrideSnapshot
	err := tx.QueryRow(ctx, `SELECT fields,visibility,version FROM editorial_overrides WHERE article_id=$1`, id).Scan(&out.Fields, &out.Visibility, &out.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return overrideSnapshot{}, false, nil
	}
	return out, err == nil, err
}

func (r *CommandsRepository) resetGrouping(ctx context.Context, tx pgx.Tx, id string) error {
	var stories []int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT story_id),ARRAY[]::bigint[]) FROM (
		SELECT p.story_id FROM publications p WHERE p.article_id=$1 AND p.story_id IS NOT NULL
		UNION SELECT f.story_id FROM facts f JOIN fact_articles fa ON fa.fact_id=f.id WHERE fa.article_id=$1 AND f.story_id IS NOT NULL) x`, id).Scan(&stories); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM grouping_overrides WHERE article_id=$1`, id); err != nil {
		return err
	}
	var hasManual bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fact_articles WHERE article_id=$1 AND manual)`, id).Scan(&hasManual); err != nil {
		return err
	}
	if !hasManual {
		if _, err := tx.Exec(ctx, `DELETE FROM fact_articles WHERE article_id=$1 AND NOT manual`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM story_signals WHERE article_id=$1`, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE articles SET grouping_status='pending',grouped_at=NULL,grouping_receipt_id=NULL,grouping_error=NULL,
		selection_adds_value=NULL,selection_value_reason=NULL WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err := r.publication.PublishTx(ctx, tx, id, publication.PublishOptions{}); err != nil {
		return err
	}
	if err := r.enqueueStoryDigests(ctx, tx, stories); err != nil {
		return err
	}
	return nil
}

func (r *CommandsRepository) enqueueStoryDigests(ctx context.Context, tx pgx.Tx, ids []int64) error {
	if !r.storyDigestEnabled {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if _, err := r.river.InsertTx(ctx, tx, jobargs.StoryDigestArgs{StoryID: id}, &river.InsertOpts{Queue: "story_digests", Priority: 2, MaxAttempts: 4, UniqueOpts: activeArgsUniqueness()}); err != nil {
			return err
		}
	}
	return nil
}

func (r *CommandsRepository) enqueueHotRanking(ctx context.Context, tx pgx.Tx) error {
	_, err := r.river.InsertTx(ctx, tx, jobargs.HotRankingArgs{}, &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: activeArgsUniqueness()})
	return err
}

func articleStoryIDs(ctx context.Context, tx pgx.Tx, id string) ([]int64, error) {
	var ids []int64
	err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT story_id),ARRAY[]::bigint[]) FROM (
		SELECT f.story_id FROM facts f JOIN fact_articles fa ON fa.fact_id=f.id WHERE fa.article_id=$1 AND f.story_id IS NOT NULL) x`, id).Scan(&ids)
	return ids, err
}

func insertAudit(ctx context.Context, tx pgx.Tx, actor, action, subject, reason string, before, after any, requestID string) error {
	var beforeJSON, afterJSON []byte
	var err error
	if before != nil {
		beforeJSON, err = json.Marshal(before)
		if err != nil {
			return err
		}
	}
	if after != nil {
		afterJSON, err = json.Marshal(after)
		if err != nil {
			return err
		}
	}
	var reasonValue, requestValue any
	if reason != "" {
		reasonValue = reason
	}
	if requestID != "" {
		requestValue = requestID
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log(actor,action,subject,reason,before,"after",request_id) VALUES($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7)`, actor, action, subject, reasonValue, nullJSON(beforeJSON), nullJSON(afterJSON), requestValue)
	return err
}

func nullJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
func rawObject(value json.RawMessage) any {
	if len(value) == 0 || string(value) == "null" {
		return map[string]any{}
	}
	return json.RawMessage(value)
}
func publishResult(value *publication.Result) *PublishResult {
	if value == nil {
		return nil
	}
	return &PublishResult{ArticleID: value.ArticleID, Changed: value.Changed, ChangeKind: value.ChangeKind, Selected: value.Selected, Visibility: value.Visibility, Ledger: value.Ledger, Reduced: value.Reduced}
}
func valueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}
func mapKeys(values map[int64]struct{}) []int64 {
	result := make([]int64, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
func activeArgsUniqueness() river.UniqueOpts {
	return river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}
}

var _ CommandRepository = (*CommandsRepository)(nil)
