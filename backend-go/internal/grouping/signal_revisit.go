package grouping

import (
	"context"
	"time"
)

const (
	signalRevisitWindow = 6 * time.Hour
	signalWaitWindow    = 48 * time.Hour
)

// SignalRevisitRow contains the text needed to recall an unattached discussion post.
type SignalRevisitRow struct {
	ArticleID string  `gorm:"column:article_id"`
	Revision  int     `gorm:"column:revision"`
	Title     string  `gorm:"column:title"`
	BodyText  *string `gorm:"column:body_text"`
}

// SignalRevisitRepository finds discussion posts that remain eligible for automatic grouping.
type SignalRevisitRepository interface {
	/** WaitingSignals returns eligible replies or quotes for a post identity. */
	WaitingSignals(context.Context, string, time.Time) ([]SignalRevisitRow, error)
	/** UnattachedSignals returns semantically matchable posts after the supplied cutoff. */
	UnattachedSignals(context.Context, time.Time) ([]SignalRevisitRow, error)
}

// SignalTaskEnqueuer schedules signal-only grouping for newly eligible posts.
type SignalTaskEnqueuer interface {
	/** EnqueueSignalGroups schedules the given articles for signal-only grouping. */
	EnqueueSignalGroups(context.Context, []string) error
}

/** WaitingSignals returns recent replies or quotes of an arriving X post that remain unattached. */
func (r *GormRepository) WaitingSignals(ctx context.Context, tweetID string, since time.Time) ([]SignalRevisitRow, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	var rows []SignalRevisitRow
	err := r.db.WithContext(ctx).Raw(`SELECT a.id AS article_id, a.revision, a.title, left(a.body_text, 300) AS body_text
		FROM articles a JOIN sources s ON s.id=a.source_id
		WHERE a.discovered_at > ? AND `+unattachedSignalCondition+`
		AND (a.x_post->>'replyTo' = ? OR substring(a.x_post->'quoted'->>'url' from '/status/([0-9]+)') = ?)
		ORDER BY a.discovered_at`, since, tweetID, tweetID).Scan(&rows).Error
	if rows == nil {
		rows = []SignalRevisitRow{}
	}
	return rows, err
}

/** UnattachedSignals returns recent unattached discussion posts for semantic rematching. */
func (r *GormRepository) UnattachedSignals(ctx context.Context, since time.Time) ([]SignalRevisitRow, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	var rows []SignalRevisitRow
	err := r.db.WithContext(ctx).Raw(`SELECT a.id AS article_id, a.revision, a.title, left(a.body_text, 300) AS body_text
		FROM articles a JOIN sources s ON s.id=a.source_id
		WHERE a.discovered_at > ? AND `+unattachedSignalCondition+`
		ORDER BY a.discovered_at`, since).Scan(&rows).Error
	if rows == nil {
		rows = []SignalRevisitRow{}
	}
	return rows, err
}

const unattachedSignalCondition = `s.participation_mode='hot_signal' AND a.processing_state='skipped'
	AND (NOT a.backfill OR (a.published_at IS NOT NULL AND a.discovered_at-a.published_at <= interval '48 hours'))
	AND NOT EXISTS (SELECT 1 FROM story_signals ss WHERE ss.article_id=a.id)
	AND NOT EXISTS (SELECT 1 FROM grouping_decisions d WHERE d.article_id=a.id AND d.verdict <> 'signal-unmatched')
	AND NOT EXISTS (SELECT 1 FROM grouping_overrides o WHERE o.article_id=a.id)`

/** revisitSignals requeues replies to a collected post and semantically close recent discussion posts. */
func (s *Service) revisitSignals(ctx context.Context, article ArticleInput, queryText, verdict string) (int, int, error, error) {
	repository, ok := s.repository.(SignalRevisitRepository)
	if !ok || s.signalTasks == nil {
		return 0, 0, nil, nil
	}
	var reclaimed, rematched int
	var reclaimErr, rematchErr error
	if article.XPost.TweetID != "" {
		rows, err := repository.WaitingSignals(ctx, article.XPost.TweetID, s.config.Now().Add(-signalWaitWindow))
		if err != nil {
			reclaimErr = err
		} else {
			reclaimed, reclaimErr = s.enqueueSignalRows(ctx, rows)
		}
	}
	if verdict == "new-story" || verdict == "new-fact-in-story" {
		rematched, rematchErr = s.rematchSignals(ctx, repository, article, queryText)
	}
	return reclaimed, rematched, reclaimErr, rematchErr
}

/** rematchSignals queues unattached posts whose embeddings meet the established similarity threshold. */
func (s *Service) rematchSignals(ctx context.Context, repository SignalRevisitRepository, article ArticleInput, queryText string) (int, error) {
	if s.embeddings == nil || !s.embeddings.Available(ctx) {
		return 0, nil
	}
	rows, err := repository.UnattachedSignals(ctx, s.config.Now().Add(-signalRevisitWindow))
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	inputs := make([]VectorInput, 0, len(rows)+1)
	inputs = append(inputs, VectorInput{ID: article.ID, Text: queryText})
	for _, row := range rows {
		inputs = append(inputs, VectorInput{ID: row.ArticleID, Text: ReportText(row.Title, row.BodyText), Revision: &row.Revision})
	}
	vectors, err := s.embeddings.Ensure(ctx, inputs)
	if err != nil {
		return 0, err
	}
	query := vectors[article.ID]
	if len(query) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if Cosine32(query, vectors[row.ArticleID]) >= signalMinCosine {
			ids = append(ids, row.ArticleID)
		}
	}
	return s.enqueueSignalIDs(ctx, ids)
}

/** enqueueSignalRows schedules the eligible signal rows returned by a repository query. */
func (s *Service) enqueueSignalRows(ctx context.Context, rows []SignalRevisitRow) (int, error) {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ArticleID)
	}
	return s.enqueueSignalIDs(ctx, ids)
}

/** enqueueSignalIDs schedules each unique article for signal-only grouping. */
func (s *Service) enqueueSignalIDs(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if s.signalTasks == nil {
		return 0, nil
	}
	if err := s.signalTasks.EnqueueSignalGroups(ctx, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
}

var _ SignalRevisitRepository = (*GormRepository)(nil)
