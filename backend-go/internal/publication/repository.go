package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf16"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository is the PostgreSQL boundary for material publication. This path uses pgx because
// publication, selected-ledger changes, and River inserts must be able to share one transaction.
type Repository struct {
	pool        *pgxpool.Pool
	entities    map[string]interests.Entity
	categoryMap map[string]string
	siteName    string
	siteURL     string
	effects     Effects
}

var ErrEffectsUnavailable = errors.New("publication side-effect enqueuer is unavailable")

// Effects inserts River jobs and cache-change events through the publication transaction.
type Effects interface {
	EnqueueSelectedTx(context.Context, pgx.Tx, string, time.Time) error
	EnqueueMediaTx(context.Context, pgx.Tx, string) error
	ArticleChangedTx(context.Context, pgx.Tx, ArticleChanged) error
}

type ArticleChanged struct {
	ArticleID string
	Kind      string
	StoryIDs  []int64
}

func NewRepository(pool *pgxpool.Pool, entities map[string]interests.Entity, settings interests.PublicationSettings, siteName, siteURL string) *Repository {
	return &Repository{pool: pool, entities: entities, categoryMap: settings.PublicCategories.Merge, siteName: siteName, siteURL: strings.TrimRight(siteURL, "/")}
}

func (r *Repository) SetEffects(effects Effects) { r.effects = effects }

type PublishOptions struct {
	Now        time.Time
	ReleasedAt *time.Time
}

type Result struct {
	ArticleID  string
	Changed    bool
	ChangeKind string
	Selected   bool
	Visibility string
	Ledger     *string
	Reduced    bool
}

type articleRow struct {
	id, sourceID, url, title, bodyStatus, groupingStatus string
	language, bodyText                                   *string
	publishedAt                                          *time.Time
	discoveredAt, timelineAt                             time.Time
	backfill                                             bool
	xPost                                                json.RawMessage
	selectionAddsValue                                   *bool
}

type analysisRow struct {
	id                                                int64
	relevance, category, titleZH, summaryZH, reasonZH *string
	tags, subjects                                    []string
	score                                             *float64
	selected                                          *bool
}

type sourceRow struct {
	ID, Name, Kind, Tier, ParticipationMode string
	SiteFulltext, SyndicateFulltext         bool
	OwnerEntityID                           *string
	Config                                  json.RawMessage
}

type overrideRow struct {
	fields     json.RawMessage
	visibility *string
}

func (r *Repository) Publish(ctx context.Context, articleID string, options PublishOptions) (*Result, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("publication repository is unavailable")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result, err := r.publishTx(ctx, tx, articleID, options)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Repository) CountSourcePublications(ctx context.Context, sourceID string) (int, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("publication repository is unavailable")
	}
	var total int
	err := r.pool.QueryRow(ctx, `SELECT count(*)::int FROM publications WHERE source_id=$1`, sourceID).Scan(&total)
	return total, err
}

func (r *Repository) SourcePublicationBatch(ctx context.Context, sourceID, after string, limit int) ([]string, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("publication repository is unavailable")
	}
	if limit < 1 || limit > 500 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `SELECT article_id FROM publications WHERE source_id=$1 AND article_id>$2 ORDER BY article_id LIMIT $3`, sourceID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var articleID string
		if err := rows.Scan(&articleID); err != nil {
			return nil, err
		}
		ids = append(ids, articleID)
	}
	return ids, rows.Err()
}

func (r *Repository) SetSourceRepublishStatus(ctx context.Context, sourceID string, status json.RawMessage) error {
	if r == nil || r.pool == nil {
		return errors.New("publication repository is unavailable")
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO settings(key,value,updated_by) VALUES($1,$2::jsonb,'worker')
		ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_by=EXCLUDED.updated_by,updated_at=now()`, "republish.source:"+sourceID, string(status))
	return err
}

func (r *Repository) QueuedSourceRepublishes(ctx context.Context) ([]string, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("publication repository is unavailable")
	}
	rows, err := r.pool.Query(ctx, `SELECT key FROM settings WHERE key LIKE 'republish.source:%' AND value->>'status'='queued' ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		id := strings.TrimPrefix(key, "republish.source:")
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// PublishTx allows a caller such as an admin UseCase to compose publication with its own audit
// or queue changes. The transaction must use the same River-bound pgx pool as this repository.
func (r *Repository) PublishTx(ctx context.Context, tx pgx.Tx, articleID string, options PublishOptions) (*Result, error) {
	if r == nil || tx == nil {
		return nil, errors.New("publication transaction is unavailable")
	}
	return r.publishTx(ctx, tx, articleID, options)
}

func (r *Repository) publishTx(ctx context.Context, tx pgx.Tx, articleID string, options PublishOptions) (*Result, error) {
	var a articleRow
	err := tx.QueryRow(ctx, `SELECT id,source_id,url,title,language,published_at,discovered_at,timeline_at,backfill,
		body_status,body_text,x_post,grouping_status,selection_adds_value FROM articles WHERE id=$1 FOR UPDATE`, articleID).Scan(
		&a.id, &a.sourceID, &a.url, &a.title, &a.language, &a.publishedAt, &a.discoveredAt, &a.timelineAt, &a.backfill, &a.bodyStatus, &a.bodyText, &a.xPost, &a.groupingStatus, &a.selectionAddsValue)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if options.ReleasedAt != nil && a.groupingStatus == "pending" {
		if _, err := tx.Exec(ctx, `UPDATE articles SET grouping_status='complete',grouped_at=coalesce(grouped_at,$2) WHERE id=$1`, articleID, *options.ReleasedAt); err != nil {
			return nil, err
		}
		a.groupingStatus = "complete"
	}
	var source sourceRow
	err = tx.QueryRow(ctx, `SELECT id,name,kind,tier,participation_mode,site_fulltext,syndicate_fulltext,owner_entity_id,config
		FROM sources WHERE id=$1`, a.sourceID).Scan(&source.ID, &source.Name, &source.Kind, &source.Tier, &source.ParticipationMode, &source.SiteFulltext, &source.SyndicateFulltext, &source.OwnerEntityID, &source.Config)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sourceFacts := SourceFacts{ID: source.ID, Name: source.Name, Kind: source.Kind, Tier: source.Tier, ParticipationMode: source.ParticipationMode,
		SiteFulltext: source.SiteFulltext, SyndicateFulltext: source.SyndicateFulltext}
	var analysis *analysisRow
	var current analysisRow
	err = tx.QueryRow(ctx, `SELECT id,relevance,category,tags,subjects,title_zh,summary_zh,reason_zh,score,selected
		FROM analyses WHERE article_id=$1 ORDER BY input_revision DESC,id DESC LIMIT 1`, articleID).Scan(
		&current.id, &current.relevance, &current.category, &current.tags, &current.subjects, &current.titleZH, &current.summaryZH, &current.reasonZH, &current.score, &current.selected)
	if err == nil {
		analysis = &current
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var override overrideRow
	err = tx.QueryRow(ctx, `SELECT fields,visibility FROM editorial_overrides WHERE article_id=$1`, articleID).Scan(&override.fields, &override.visibility)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var membership Membership
	var membershipStoryID *int64
	err = tx.QueryRow(ctx, `SELECT fa.fact_id,f.story_id FROM fact_articles fa JOIN facts f ON f.id=fa.fact_id
		LEFT JOIN stories s ON s.id=f.story_id WHERE fa.article_id=$1 AND fa.role IN ('primary','report')
		AND (s.id IS NULL OR s.merged_into IS NULL)
		AND (SELECT x.output->>'scope'='composite' FROM analyses x WHERE x.article_id=$1 ORDER BY x.input_revision DESC,x.id DESC LIMIT 1) IS NOT TRUE
		ORDER BY (fa.role='primary') DESC,fa.created_at LIMIT 1`, articleID).Scan(&membership.FactID, &membershipStoryID)
	membership.StoryID = membershipStoryID
	if errors.Is(err, pgx.ErrNoRows) {
		membership = Membership{}
	} else if err != nil {
		return nil, err
	}
	previous, err := loadPrevious(ctx, tx, articleID)
	if err != nil {
		return nil, err
	}
	seatFacts := make([]int64, 0, 2)
	if previous != nil && previous.FactID != nil {
		seatFacts = append(seatFacts, *previous.FactID)
	}
	if membership.FactID != nil {
		seatFacts = append(seatFacts, *membership.FactID)
	}
	if len(seatFacts) == 2 && seatFacts[0] > seatFacts[1] {
		seatFacts[0], seatFacts[1] = seatFacts[1], seatFacts[0]
	}
	if len(seatFacts) == 2 && seatFacts[0] == seatFacts[1] {
		seatFacts = seatFacts[:1]
	}
	for _, factID := range seatFacts {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, fmt.Sprintf("seat:%d", factID)); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('report_candidates'))`); err != nil {
		return nil, err
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	inputArticle := Article{ID: a.id, SourceID: a.sourceID, URL: a.url, Title: a.title, Language: a.language, PublishedAt: a.publishedAt, DiscoveredAt: a.discoveredAt,
		TimelineAt: a.timelineAt, Backfill: a.backfill, BodyStatus: a.bodyStatus, BodyText: a.bodyText, XPost: a.xPost, GroupingStatus: a.groupingStatus, SelectionAddsValue: a.selectionAddsValue}
	var analysisInput *Analysis
	if analysis != nil {
		analysisInput = &Analysis{ID: analysis.id, Relevance: analysis.relevance, Category: analysis.category, Tags: analysis.tags, Subjects: analysis.subjects,
			TitleZH: analysis.titleZH, SummaryZH: analysis.summaryZH, ReasonZH: analysis.reasonZH, Score: analysis.score, Selected: analysis.selected}
	}
	var manual *Override
	if len(override.fields) > 0 || override.visibility != nil {
		manual = &Override{Visibility: override.visibility}
		fields := map[string]json.RawMessage{}
		if len(override.fields) > 0 {
			if err := json.Unmarshal(override.fields, &fields); err != nil {
				return nil, fmt.Errorf("decode editorial override: %w", err)
			}
		}
		manual.Fields = decodeOverride(fields)
	}
	var anchor *time.Time
	if membership.FactID != nil {
		var value *time.Time
		if err := tx.QueryRow(ctx, `SELECT min(timeline_at) FROM publications WHERE fact_id=$1 AND eligible AND visibility='public' AND article_id<>$2`, *membership.FactID, articleID).Scan(&value); err != nil {
			return nil, err
		}
		anchor = value
	}
	var hasState bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM selected_state WHERE article_id=$1)`, articleID).Scan(&hasState); err != nil {
		return nil, err
	}
	var released *time.Time
	if options.ReleasedAt != nil {
		v := options.ReleasedAt.UTC()
		released = &v
	}
	projection := DeriveProjection(ProjectionInput{Article: inputArticle, Source: sourceFacts, Analysis: analysisInput, Override: manual, Membership: membership, Previous: previous,
		HasSelectedState: hasState, FactAnchor: anchor, Now: now, ReleasedAt: released})
	if err := saveProjection(ctx, tx, projection.Projection, projection.Changed); err != nil {
		return nil, err
	}
	if err := syncPoolSearch(ctx, tx, inputArticle, projection.Projection); err != nil {
		return nil, err
	}
	if projection.Projection.Selected && (previous == nil || !previous.Selected) && options.ReleasedAt == nil && !inputArticle.Backfill && projection.Projection.Visibility == "public" {
		if r.effects == nil {
			return nil, ErrEffectsUnavailable
		}
		if err := r.effects.EnqueueSelectedTx(ctx, tx, articleID, now.Add(5*time.Second)); err != nil {
			return nil, err
		}
		if err := r.effects.EnqueueMediaTx(ctx, tx, articleID); err != nil {
			return nil, err
		}
	}
	var ledger *string
	if membership.FactID != nil {
		own, err := r.settleSeats(ctx, tx, *membership.FactID, articleID, now)
		if err != nil {
			return nil, err
		}
		if own != nil {
			ledger = own
		}
	}
	if membership.FactID == nil {
		if _, err := tx.Exec(ctx, `UPDATE publications SET seat=true WHERE article_id=$1 AND NOT seat`, articleID); err != nil {
			return nil, err
		}
	}
	if previous != nil && previous.FactID != nil && (membership.FactID == nil || *previous.FactID != *membership.FactID) {
		if _, err := r.settleSeats(ctx, tx, *previous.FactID, "", now); err != nil {
			return nil, err
		}
	}
	if op, err := r.syncLedger(ctx, tx, articleID, now); err != nil {
		return nil, err
	} else if op != nil {
		ledger = op
	}
	if previous != nil && projection.Changed {
		if r.effects == nil {
			return nil, ErrEffectsUnavailable
		}
		change := ArticleChanged{ArticleID: articleID, Kind: projection.ChangeKind, StoryIDs: []int64{}}
		if previous.StoryID != nil {
			change.StoryIDs = []int64{*previous.StoryID}
		}
		if err := r.effects.ArticleChangedTx(ctx, tx, change); err != nil {
			return nil, err
		}
	}
	var ledgerValue *string
	if ledger != nil {
		value := *ledger
		ledgerValue = &value
	}
	return &Result{ArticleID: articleID, Changed: projection.Changed, ChangeKind: projection.ChangeKind, Selected: projection.Projection.Selected,
		Visibility: projection.Projection.Visibility, Ledger: ledgerValue, Reduced: projection.Reduced}, nil
}

func loadPrevious(ctx context.Context, tx pgx.Tx, id string) (*Projection, error) {
	var p model.Publication
	err := tx.QueryRow(ctx, `SELECT article_id,analysis_id,revision,visibility,eligible,selected,selection_candidate,title,original_title,summary,reason,category,tags,score,
		source_id,channel,first_party,url,published_at,discovered_at,timeline_at,backfill,selected_ready_at,visible_after,body_mode,syndicate,indexable,
		story_id,fact_id,search_text,sort_at,seo_indexed_at,seo_excluded_at FROM publications WHERE article_id=$1`, id).Scan(
		&p.ArticleID, &p.AnalysisID, &p.Revision, &p.Visibility, &p.Eligible, &p.Selected, &p.SelectionCandidate, &p.Title, &p.OriginalTitle, &p.Summary, &p.Reason, &p.Category, &p.Tags, &p.Score,
		&p.SourceID, &p.Channel, &p.FirstParty, &p.URL, &p.PublishedAt, &p.DiscoveredAt, &p.TimelineAt, &p.Backfill, &p.SelectedReadyAt, &p.VisibleAfter, &p.BodyMode, &p.Syndicate, &p.Indexable,
		&p.StoryID, &p.FactID, &p.SearchText, &p.SortAt, &p.SEOIndexedAt, &p.SEOExcludedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Projection{ArticleID: p.ArticleID, AnalysisID: p.AnalysisID, Revision: p.Revision, Visibility: p.Visibility, Eligible: p.Eligible, Selected: p.Selected, SelectionCandidate: p.SelectionCandidate,
		Title: p.Title, OriginalTitle: p.OriginalTitle, Summary: p.Summary, Reason: p.Reason, Category: p.Category, Tags: p.Tags, Score: p.Score, SourceID: p.SourceID, Channel: p.Channel, FirstParty: p.FirstParty,
		URL: p.URL, PublishedAt: p.PublishedAt, DiscoveredAt: p.DiscoveredAt, TimelineAt: p.TimelineAt, Backfill: p.Backfill, SelectedReadyAt: p.SelectedReadyAt, VisibleAfter: p.VisibleAfter,
		BodyMode: p.BodyMode, Syndicate: p.Syndicate, Indexable: p.Indexable, StoryID: p.StoryID, FactID: p.FactID, SearchText: p.SearchText, SortAt: p.SortAt, SEOIndexedAt: p.SEOIndexedAt, SEOExcludedAt: p.SEOExcludedAt}, nil
}

func decodeOverride(fields map[string]json.RawMessage) OverrideFields {
	var result OverrideFields
	decodeString := func(key string, dst **string) {
		if raw, ok := fields[key]; ok {
			var value string
			if json.Unmarshal(raw, &value) == nil {
				*dst = &value
			}
		}
	}
	decodeString("title", &result.Title)
	decodeString("summary", &result.Summary)
	decodeString("category", &result.Category)
	decodeString("relevance", &result.Relevance)
	decodeString("reason", &result.Reason)
	if raw, ok := fields["tags"]; ok {
		var value []string
		if json.Unmarshal(raw, &value) == nil {
			result.Tags = &value
		}
	}
	if raw, ok := fields["score"]; ok {
		var value *float64
		if json.Unmarshal(raw, &value) == nil {
			result.Score = value
		}
	}
	if raw, ok := fields["selected"]; ok {
		var value *bool
		if json.Unmarshal(raw, &value) == nil {
			result.Selected = value
		}
	}
	return result
}

func saveProjection(ctx context.Context, tx pgx.Tx, result Projection, _ bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO publications(article_id,analysis_id,revision,visibility,eligible,selected,selection_candidate,title,original_title,summary,reason,category,tags,score,
		source_id,channel,first_party,url,published_at,discovered_at,timeline_at,backfill,selected_ready_at,visible_after,body_mode,syndicate,indexable,story_id,fact_id,search_text,sort_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,now())
		ON CONFLICT(article_id) DO UPDATE SET analysis_id=EXCLUDED.analysis_id,revision=EXCLUDED.revision,visibility=EXCLUDED.visibility,eligible=EXCLUDED.eligible,
		selected=EXCLUDED.selected,selection_candidate=EXCLUDED.selection_candidate,title=EXCLUDED.title,original_title=EXCLUDED.original_title,summary=EXCLUDED.summary,reason=EXCLUDED.reason,
		category=EXCLUDED.category,tags=EXCLUDED.tags,score=EXCLUDED.score,source_id=EXCLUDED.source_id,channel=EXCLUDED.channel,first_party=EXCLUDED.first_party,url=EXCLUDED.url,
		published_at=EXCLUDED.published_at,discovered_at=EXCLUDED.discovered_at,timeline_at=EXCLUDED.timeline_at,backfill=EXCLUDED.backfill,selected_ready_at=EXCLUDED.selected_ready_at,
		visible_after=EXCLUDED.visible_after,body_mode=EXCLUDED.body_mode,syndicate=EXCLUDED.syndicate,indexable=EXCLUDED.indexable,story_id=EXCLUDED.story_id,fact_id=EXCLUDED.fact_id,
		search_text=EXCLUDED.search_text,sort_at=EXCLUDED.sort_at,updated_at=now()
		WHERE (publications.analysis_id,publications.revision,publications.visibility,publications.eligible,publications.selected,publications.selection_candidate,publications.title,
		publications.original_title,publications.summary,publications.reason,publications.category,publications.tags,publications.score,publications.source_id,publications.channel,
		publications.first_party,publications.url,publications.published_at,publications.discovered_at,publications.timeline_at,publications.backfill,publications.selected_ready_at,
		publications.visible_after,publications.body_mode,publications.syndicate,publications.indexable,publications.story_id,publications.fact_id,publications.search_text,publications.sort_at)
		IS DISTINCT FROM (EXCLUDED.analysis_id,EXCLUDED.revision,EXCLUDED.visibility,EXCLUDED.eligible,EXCLUDED.selected,EXCLUDED.selection_candidate,EXCLUDED.title,EXCLUDED.original_title,
		EXCLUDED.summary,EXCLUDED.reason,EXCLUDED.category,EXCLUDED.tags,EXCLUDED.score,EXCLUDED.source_id,EXCLUDED.channel,EXCLUDED.first_party,EXCLUDED.url,EXCLUDED.published_at,
		EXCLUDED.discovered_at,EXCLUDED.timeline_at,EXCLUDED.backfill,EXCLUDED.selected_ready_at,EXCLUDED.visible_after,EXCLUDED.body_mode,EXCLUDED.syndicate,EXCLUDED.indexable,
		EXCLUDED.story_id,EXCLUDED.fact_id,EXCLUDED.search_text,EXCLUDED.sort_at)`,
		result.ArticleID, result.AnalysisID, result.Revision, result.Visibility, result.Eligible, result.Selected, result.SelectionCandidate, result.Title, result.OriginalTitle, result.Summary, result.Reason,
		result.Category, result.Tags, result.Score, result.SourceID, result.Channel, result.FirstParty, result.URL, result.PublishedAt, result.DiscoveredAt, result.TimelineAt, result.Backfill,
		result.SelectedReadyAt, result.VisibleAfter, result.BodyMode, result.Syndicate, result.Indexable, result.StoryID, result.FactID, result.SearchText, result.SortAt)
	return err
}

func syncPoolSearch(ctx context.Context, tx pgx.Tx, a Article, p Projection) error {
	if !p.Eligible {
		return deletePoolSearch(ctx, tx, a.ID)
	}
	body := ""
	if p.BodyMode == "full" && a.BodyText != nil {
		body = truncateJS(*a.BodyText, 12000)
		body = strings.ToLower(body)
	}
	_, err := tx.Exec(ctx, `INSERT INTO pool_search(article_id,direct,body) VALUES($1,$2,$3) ON CONFLICT(article_id) DO UPDATE SET direct=EXCLUDED.direct,body=EXCLUDED.body
		WHERE pool_search.direct IS DISTINCT FROM EXCLUDED.direct OR pool_search.body IS DISTINCT FROM EXCLUDED.body`, a.ID, p.SearchText, body)
	return err
}
func deletePoolSearch(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `DELETE FROM pool_search WHERE article_id=$1`, id)
	return err
}

func (r *Repository) settleSeats(ctx context.Context, tx pgx.Tx, factID int64, self string, now time.Time) (*string, error) {
	rows, err := tx.Query(ctx, `SELECT p.article_id,p.body_mode,p.score,p.timeline_at,p.seat,
		s.tier,s.owner_entity_id,coalesce(s.config->>'publisherRole',''),coalesce(f.subject,''),
		COALESCE((p.selected AND p.visibility='public' AND fa.role IN ('primary','report') AND
		(SELECT a.output->>'scope'='composite' FROM analyses a WHERE a.article_id=p.article_id ORDER BY a.input_revision DESC,a.id DESC LIMIT 1) IS NOT TRUE),false) AS holds
		FROM publications p JOIN sources s ON s.id=p.source_id JOIN facts f ON f.id=p.fact_id
		LEFT JOIN fact_articles fa ON fa.fact_id=p.fact_id AND fa.article_id=p.article_id WHERE p.fact_id=$1`, factID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type member struct {
		id, bodyMode, tier, role, subject string
		score                             *float64
		timeline                          time.Time
		seat, holds                       bool
		owner                             *string
	}
	var members []member
	var candidates []Representative
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.id, &m.bodyMode, &m.score, &m.timeline, &m.seat, &m.tier, &m.owner, &m.role, &m.subject, &m.holds); err != nil {
			return nil, err
		}
		members = append(members, m)
		if m.holds {
			owner := ""
			if m.owner != nil {
				owner = *m.owner
			}
			candidates = append(candidates, Representative{ArticleID: m.id, BodyMode: m.bodyMode, Score: m.score, TimelineAt: m.timeline, Identity: RepresentativeIdentity{SourceTier: m.tier, PublisherRole: m.role, OwnerEntityID: owner, FactSubject: m.subject}})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rep, ok := PickRepresentative(candidates, r.entities)
	var own *string
	for _, m := range members {
		seat := !m.holds || !ok || m.id == rep.ArticleID
		if seat == m.seat {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE publications SET seat=$2 WHERE article_id=$1`, m.id, seat); err != nil {
			return nil, err
		}
		op, err := r.syncLedger(ctx, tx, m.id, now)
		if err != nil {
			return nil, err
		}
		if m.id == self && op != nil {
			own = op
		}
	}
	return own, nil
}

type selectedPayload struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle *string `json:"originalTitle"`
	Summary       *string `json:"summary"`
	Source        struct {
		Name string `json:"name"`
	} `json:"source"`
	Links struct {
		AIHOT    string `json:"aihot"`
		Original string `json:"original"`
	} `json:"links"`
	PublishedAt  *string `json:"publishedAt"`
	DiscoveredAt string  `json:"discoveredAt"`
	Category     *string `json:"category"`
	Score        *int    `json:"score"`
	Selected     bool    `json:"selected"`
	Reason       *string `json:"reason"`
	Attribution  struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"attribution"`
}

func (r *Repository) syncLedger(ctx context.Context, tx pgx.Tx, id string, now time.Time) (*string, error) {
	var p model.Publication
	var sourceName string
	err := tx.QueryRow(ctx, `SELECT p.article_id,p.selected,p.visibility,p.seat,p.title,p.original_title,p.summary,p.url,p.published_at,p.discovered_at,p.category,p.score,p.reason,s.name
		FROM publications p JOIN sources s ON s.id=p.source_id WHERE p.article_id=$1`, id).Scan(&p.ArticleID, &p.Selected, &p.Visibility, &p.Seat, &p.Title, &p.OriginalTitle, &p.Summary, &p.URL, &p.PublishedAt, &p.DiscoveredAt, &p.Category, &p.Score, &p.Reason, &sourceName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var inSet bool
	var oldHash *string
	err = tx.QueryRow(ctx, `SELECT in_set,payload_hash FROM selected_state WHERE article_id=$1`, id).Scan(&inSet, &oldHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if p.Selected && p.Visibility == "public" && p.Seat {
		payload := r.ledgerPayload(p, sourceName)
		encoded, err := stableJSON(payload)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(encoded)
		hash := hex.EncodeToString(sum[:])
		if inSet && oldHash != nil && *oldHash == hash {
			return nil, nil
		}
		seq, err := r.appendLedger(ctx, tx, id, "upsert", encoded, now)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO selected_state(article_id,in_set,payload_hash,last_seq) VALUES($1,true,$2,$3)
			ON CONFLICT(article_id) DO UPDATE SET in_set=true,payload_hash=EXCLUDED.payload_hash,last_seq=EXCLUDED.last_seq`, id, hash, seq)
		if err != nil {
			return nil, err
		}
		op := "upsert"
		return &op, nil
	}
	if !inSet {
		return nil, nil
	}
	seq, err := r.appendLedger(ctx, tx, id, "remove", nil, now)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE selected_state SET in_set=false,payload_hash=NULL,last_seq=$2 WHERE article_id=$1`, id, seq); err != nil {
		return nil, err
	}
	op := "remove"
	return &op, nil
}

func (r *Repository) ledgerPayload(p model.Publication, sourceName string) selectedPayload {
	itemURL := r.siteURL + "/items/" + p.ArticleID
	value := selectedPayload{ID: p.ArticleID, Title: p.Title, OriginalTitle: p.OriginalTitle, Summary: p.Summary, DiscoveredAt: p.DiscoveredAt.UTC().Format("2006-01-02T15:04:05.000Z"), Selected: true, Reason: p.Reason}
	value.Source.Name = PublicSourceName(sourceName)
	value.Links.AIHOT = itemURL
	value.Links.Original = p.URL
	value.Attribution.Name = r.siteName
	value.Attribution.URL = itemURL
	if p.PublishedAt != nil {
		v := p.PublishedAt.UTC().Format("2006-01-02T15:04:05.000Z")
		value.PublishedAt = &v
	}
	if p.Category != nil {
		if mapped, ok := r.categoryMap[*p.Category]; ok {
			v := mapped
			value.Category = &v
		} else {
			value.Category = p.Category
		}
	}
	if p.Score != nil {
		v := int(math.Floor(*p.Score + 0.5))
		value.Score = &v
	}
	return value
}

func (r *Repository) appendLedger(ctx context.Context, tx pgx.Tx, id, op string, payload []byte, now time.Time) (int64, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('selected_ledger'))`); err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(seq),0)+1 FROM selected_ledger`).Scan(&seq); err != nil {
		return 0, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO selected_ledger(seq,article_id,op,changed_at,visible_at,payload) VALUES($1,$2,$3,$4,$4,$5)`, seq, id, op, now, payload)
	return seq, err
}

func stableJSON(value any) ([]byte, error) {
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(decoded); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	encoded = unescapeJSSep(encoded, []byte(`\u2028`), []byte("\u2028"))
	encoded = unescapeJSSep(encoded, []byte(`\u2029`), []byte("\u2029"))
	return encoded, nil
}

// JSON.stringify leaves U+2028 and U+2029 in strings. encoding/json escapes them even when HTML
// escaping is disabled, so undo only those escapes that are not themselves literal backslash text.
func unescapeJSSep(value, escaped, literal []byte) []byte {
	var result bytes.Buffer
	for index := 0; index < len(value); {
		if bytes.HasPrefix(value[index:], escaped) {
			backslashes := 0
			for previous := index - 1; previous >= 0 && value[previous] == '\\'; previous-- {
				backslashes++
			}
			if backslashes%2 == 0 {
				result.Write(literal)
				index += len(escaped)
				continue
			}
		}
		result.WriteByte(value[index])
		index++
	}
	return result.Bytes()
}

func truncateJS(value string, limit int) string {
	units := utf16.Encode([]rune(value))
	if len(units) > limit {
		units = units[:limit]
	}
	return string(utf16.Decode(units))
}
