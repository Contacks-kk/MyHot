package materials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"aihot/backend-go/internal/publication"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const attributionReason = "唯一 T1 原发信源与已验证 URL 范围一致（显式配置或已观察官网列表）"

type SourceAttributor interface {
	/** ReconcileMaterialSource repairs ownership and publication state after a discovery is persisted. */
	ReconcileMaterialSource(context.Context, string, string, string) (bool, error)
}

type AttributionRepository struct {
	pool        *pgxpool.Pool
	publication *publication.Repository
}

type publisherCandidate struct {
	id                string
	kind              string
	participationMode string
	config            json.RawMessage
}

type materialAttribution struct {
	sourceID          string
	url               string
	author            *string
	participationMode string
	analyzed          bool
}

/** NewAttributionRepository coordinates ownership corrections with the public projection transaction. */
func NewAttributionRepository(pool *pgxpool.Pool, publisher *publication.Repository) *AttributionRepository {
	return &AttributionRepository{pool: pool, publication: publisher}
}

/** ReconcileMaterialSource corrects source ownership without changing the material revision. */
func (r *AttributionRepository) ReconcileMaterialSource(ctx context.Context, articleID, observedSourceID, observedAuthor string) (bool, error) {
	if r == nil || r.pool == nil || r.publication == nil {
		return false, errors.New("material source attribution is unavailable")
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	article, err := loadMaterialAttribution(ctx, tx, articleID)
	if err != nil || article == nil {
		return false, err
	}
	candidates, err := loadPublisherCandidates(ctx, tx, articleID)
	if err != nil {
		return false, err
	}
	owned := matchingPublishers(candidates, article.url)
	if len(owned) != 1 || owned[0].id == article.sourceID {
		return false, nil
	}

	publisher := owned[0]
	author := observedPublisherAuthor(publisher.id, observedSourceID, observedAuthor)
	needsProcessing := article.participationMode != "editorial" && publisher.participationMode == "editorial" && !article.analyzed
	if err := updateMaterialAttribution(ctx, tx, articleID, publisher, author, needsProcessing); err != nil {
		return false, err
	}
	if err := recordMaterialAttribution(ctx, tx, articleID, article, publisher, author); err != nil {
		return false, err
	}
	if _, err := r.publication.PublishTx(ctx, tx, articleID, publication.PublishOptions{}); err != nil {
		return false, fmt.Errorf("refresh attributed publication: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return needsProcessing, nil
}

/** loadMaterialAttribution locks the article while ownership and judgement state are checked. */
func loadMaterialAttribution(ctx context.Context, tx pgx.Tx, articleID string) (*materialAttribution, error) {
	var article materialAttribution
	err := tx.QueryRow(ctx, `SELECT a.source_id,a.url,a.author,s.participation_mode,
		EXISTS (SELECT 1 FROM analyses an WHERE an.article_id=a.id AND an.input_revision=a.revision AND an.relevance IS NOT NULL)
		FROM articles a JOIN sources s ON s.id=a.source_id WHERE a.id=$1 FOR UPDATE OF a`, articleID).
		Scan(&article.sourceID, &article.url, &article.author, &article.participationMode, &article.analyzed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &article, nil
}

/** loadPublisherCandidates reads explicit publisher scopes and observed web-list ownership. */
func loadPublisherCandidates(ctx context.Context, tx pgx.Tx, articleID string) ([]publisherCandidate, error) {
	rows, err := tx.Query(ctx, `SELECT s.id,s.kind,s.participation_mode,s.config FROM sources s
		WHERE s.tier='T1' AND (jsonb_typeof(s.config->'publisherUrlPrefixes')='array'
			OR (s.kind='web_list' AND NOT (s.config ? 'publisherUrlPrefixes')
				AND EXISTS (SELECT 1 FROM article_discoveries d WHERE d.article_id=$1 AND d.source_id=s.id)))`, articleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]publisherCandidate, 0)
	for rows.Next() {
		var candidate publisherCandidate
		if err := rows.Scan(&candidate.id, &candidate.kind, &candidate.participationMode, &candidate.config); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

/** matchingPublishers keeps only T1 sources whose configured ownership path matches exactly. */
func matchingPublishers(candidates []publisherCandidate, articleURL string) []publisherCandidate {
	owned := make([]publisherCandidate, 0, 1)
	for _, candidate := range candidates {
		if sourceOwnsMaterialURL(candidate, articleURL) {
			owned = append(owned, candidate)
		}
	}
	return owned
}

/** sourceOwnsMaterialURL checks explicit URL scopes or an observed web-list path. */
func sourceOwnsMaterialURL(candidate publisherCandidate, articleURL string) bool {
	var config map[string]json.RawMessage
	if json.Unmarshal(candidate.config, &config) != nil {
		return false
	}
	var prefixes []json.RawMessage
	if raw, exists := config["publisherUrlPrefixes"]; exists {
		if json.Unmarshal(raw, &prefixes) != nil {
			return false
		}
	} else if candidate.kind == "web_list" {
		var listing string
		if json.Unmarshal(config["url"], &listing) != nil {
			return false
		}
		encoded, _ := json.Marshal(listing)
		prefixes = []json.RawMessage{encoded}
	} else {
		return false
	}
	for _, rawPrefix := range prefixes {
		var prefix string
		if json.Unmarshal(rawPrefix, &prefix) != nil {
			continue
		}
		if ownershipPathMatches(prefix, articleURL) {
			return true
		}
	}
	return false
}

/** ownershipPathMatches compares origin and path boundaries without URL identity normalization. */
func ownershipPathMatches(rawScope, rawURL string) bool {
	scope, ok := parseOwnershipURL(rawScope)
	if !ok || scope.RawQuery != "" || scope.ForceQuery || scope.Fragment != "" {
		return false
	}
	target, ok := parseOwnershipURL(rawURL)
	if !ok || ownershipOrigin(scope) != ownershipOrigin(target) {
		return false
	}
	prefix := strings.TrimSuffix(scope.EscapedPath(), "/")
	path := target.EscapedPath()
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

/** parseOwnershipURL accepts public HTTP origins without credentials. */
func parseOwnershipURL(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return nil, false
	}
	return parsed, true
}

/** ownershipOrigin normalizes scheme, hostname, and default ports for ownership comparisons. */
func ownershipOrigin(value *url.URL) string {
	scheme := strings.ToLower(value.Scheme)
	host := strings.ToLower(value.Hostname())
	port := value.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host
}

/** observedPublisherAuthor retains an author only when the current observation came from that publisher. */
func observedPublisherAuthor(publisherID, observedSourceID, observedAuthor string) *string {
	if publisherID != observedSourceID {
		return nil
	}
	value := strings.TrimSpace(observedAuthor)
	if value == "" {
		return nil
	}
	return &value
}

/** updateMaterialAttribution changes ownership and reopens only an unjudged signal promoted to editorial. */
func updateMaterialAttribution(ctx context.Context, tx pgx.Tx, articleID string, publisher publisherCandidate, author *string, needsProcessing bool) error {
	if needsProcessing {
		_, err := tx.Exec(ctx, `UPDATE articles SET source_id=$2,author=$3,updated_at=now(),processing_state='new',processing_queued_at=NULL,
			grouping_status='pending',grouped_at=NULL,grouping_receipt_id=NULL,grouping_error=NULL,
			selection_adds_value=NULL,selection_value_reason=NULL WHERE id=$1`, articleID, publisher.id, author)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE articles SET source_id=$2,author=$3,updated_at=now() WHERE id=$1`, articleID, publisher.id, author)
	return err
}

/** recordMaterialAttribution writes the auditable before and after ownership values. */
func recordMaterialAttribution(ctx context.Context, tx pgx.Tx, articleID string, previous *materialAttribution, publisher publisherCandidate, author *string) error {
	before, err := json.Marshal(map[string]any{"sourceId": previous.sourceID, "author": previous.author})
	if err != nil {
		return err
	}
	after, err := json.Marshal(map[string]any{"sourceId": publisher.id, "author": author})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log(actor,action,subject,reason,before,"after")
		VALUES('system','article.attribution',$1,$2,$3::jsonb,$4::jsonb)`, "article:"+articleID, attributionReason, before, after)
	return err
}
