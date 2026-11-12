package publiccontent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

var publicStoryIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type V1StoryLookup struct {
	Kind   string
	ID     int64
	Target string
}

type V1StorySource struct {
	Name       string `json:"name"`
	FirstParty bool   `json:"firstParty"`
}

type V1StoryItemLinks struct {
	AIHot    string `json:"aihot"`
	Original string `json:"original"`
}

type V1StoryReport struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	Summary     *string          `json:"summary"`
	Source      V1StorySource    `json:"source"`
	PublishedAt string           `json:"publishedAt"`
	Links       V1StoryItemLinks `json:"links"`
}

type V1RelatedStoryLinks struct {
	AIHot string `json:"aihot"`
	API   string `json:"api"`
}

type V1RelatedStory struct {
	PublicID string              `json:"publicId"`
	Title    string              `json:"title"`
	Relation string              `json:"relation"`
	Links    V1RelatedStoryLinks `json:"links"`
}

type V1StoryBody struct {
	PublicID        string  `json:"publicId"`
	Title           string  `json:"title"`
	Status          string  `json:"status"`
	SourceCount     int     `json:"sourceCount"`
	ReportCount     int     `json:"reportCount"`
	FirstReportAt   string  `json:"firstReportAt"`
	LatestAt        string  `json:"latestAt"`
	Latest          string  `json:"latest"`
	Digest          *string `json:"digest"`
	DigestUpdatedAt *string `json:"digestUpdatedAt"`
	Links           struct {
		AIHot string `json:"aihot"`
	} `json:"links"`
	Reports   []V1StoryReport  `json:"reports"`
	Storyline []V1RelatedStory `json:"storyline"`
	Related   []V1RelatedStory `json:"related"`
}

type V1StoryResponse struct {
	SchemaVersion int         `json:"schemaVersion"`
	Story         V1StoryBody `json:"story"`
}

type v1StoryDatabase struct {
	PublicID        string          `gorm:"column:public_id"`
	Title           string          `gorm:"column:title"`
	Digest          *string         `gorm:"column:digest"`
	DigestUpdated   *time.Time      `gorm:"column:digest_updated_at"`
	Summary         *string         `gorm:"column:summary"`
	SavedDigest     *string         `gorm:"column:saved_digest"`
	SavedHash       *string         `gorm:"column:inputs_hash"`
	SavedArticleIDs json.RawMessage `gorm:"column:article_ids"`
}

type v1StoryReportRecord struct {
	ID         string    `gorm:"column:id"`
	Title      string    `gorm:"column:title"`
	Summary    *string   `gorm:"column:summary"`
	URL        string    `gorm:"column:url"`
	Selected   bool      `gorm:"column:selected"`
	At         time.Time `gorm:"column:at"`
	SourceID   string    `gorm:"column:source_id"`
	SourceName string    `gorm:"column:source_name"`
	FirstParty bool      `gorm:"column:first_party"`
	Role       string    `gorm:"column:role"`
	FactID     int64     `gorm:"column:fact_id"`
}

type v1StoryDigestRecord struct {
	StoryID        int64           `gorm:"column:story_id"`
	ID             string          `gorm:"column:id"`
	Title          string          `gorm:"column:title"`
	Summary        *string         `gorm:"column:summary"`
	SourceName     string          `gorm:"column:source_name"`
	FirstParty     bool            `gorm:"column:first_party"`
	At             time.Time       `gorm:"column:at"`
	FactID         int64           `gorm:"column:fact_id"`
	FactSubject    *string         `gorm:"column:fact_subject"`
	FactAction     *string         `gorm:"column:fact_action"`
	FactObject     *string         `gorm:"column:fact_object"`
	FactConditions *string         `gorm:"column:fact_conditions"`
	Evidence       *string         `gorm:"column:evidence"`
	StructuredFact json.RawMessage `gorm:"column:structured_fact"`
}

type v1StoryRelatedRecord struct {
	PublicID string `gorm:"column:public_id"`
	Title    string `gorm:"column:title"`
	Relation string `gorm:"column:relation"`
}

type V1StoryRepository interface {
	ResolveV1Story(context.Context, string) (V1StoryLookup, error)
	LoadV1Story(context.Context, int64, time.Time) (V1StoryResponse, bool, error)
}

func (r *GormRepository) ResolveV1Story(ctx context.Context, publicID string) (V1StoryLookup, error) {
	if !publicStoryIDPattern.MatchString(publicID) {
		return V1StoryLookup{Kind: "not_found"}, nil
	}
	var story struct {
		ID         int64  `gorm:"column:id"`
		MergedInto *int64 `gorm:"column:merged_into"`
		PublicID   string `gorm:"column:public_id"`
	}
	result := r.db.WithContext(ctx).Raw("SELECT id, merged_into, public_id::text FROM stories WHERE public_id = ?::uuid", publicID).Scan(&story)
	if result.Error != nil {
		return V1StoryLookup{}, result.Error
	}
	if result.RowsAffected == 0 {
		var alias struct {
			StoryID int64 `gorm:"column:story_id"`
		}
		aliasResult := r.db.WithContext(ctx).Raw("SELECT story_id FROM story_aliases WHERE public_id = ?::uuid", publicID).Scan(&alias)
		if aliasResult.Error != nil {
			return V1StoryLookup{}, aliasResult.Error
		}
		if aliasResult.RowsAffected == 0 {
			return V1StoryLookup{Kind: "not_found"}, nil
		}
		story.ID = alias.StoryID
		story.MergedInto = &alias.StoryID
	}
	if story.MergedInto == nil {
		return V1StoryLookup{Kind: "found", ID: story.ID, Target: publicID}, nil
	}
	targetID := *story.MergedInto
	for range 10 {
		var target struct {
			ID         int64  `gorm:"column:id"`
			PublicID   string `gorm:"column:public_id"`
			MergedInto *int64 `gorm:"column:merged_into"`
		}
		targetResult := r.db.WithContext(ctx).Raw("SELECT id, public_id::text, merged_into FROM stories WHERE id = ?", targetID).Scan(&target)
		if targetResult.Error != nil {
			return V1StoryLookup{}, targetResult.Error
		}
		if targetResult.RowsAffected == 0 {
			return V1StoryLookup{Kind: "not_found"}, nil
		}
		if target.MergedInto == nil {
			return V1StoryLookup{Kind: "merged", Target: target.PublicID}, nil
		}
		targetID = *target.MergedInto
	}
	return V1StoryLookup{Kind: "not_found"}, nil
}

func (r *GormRepository) LoadV1Story(ctx context.Context, storyID int64, now time.Time) (V1StoryResponse, bool, error) {
	var story v1StoryDatabase
	storyResult := r.db.WithContext(ctx).Raw(`SELECT st.public_id::text, st.title, st.digest, st.digest_updated_at, st.summary,
		saved.digest AS saved_digest, saved.inputs_hash, to_json(saved.article_ids) AS article_ids
		FROM stories st LEFT JOIN LATERAL (SELECT digest, inputs_hash, article_ids FROM story_digests WHERE story_id = st.id ORDER BY version DESC LIMIT 1) saved ON true
		WHERE st.id = ? AND st.merged_into IS NULL`, storyID).Scan(&story)
	if storyResult.Error != nil {
		return V1StoryResponse{}, false, storyResult.Error
	}
	if storyResult.RowsAffected == 0 {
		return V1StoryResponse{}, false, nil
	}
	var reports []v1StoryReportRecord
	if err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT ON (p.article_id) p.article_id AS id, p.title, p.summary, p.url, p.selected,
		coalesce(p.published_at, p.discovered_at) AS at, s.id AS source_id, s.name AS source_name,
		(s.tier = 'T1') AS first_party, f.id AS fact_id,
		CASE WHEN EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite') THEN 'mention' ELSE fa.role END AS role
		FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id JOIN sources s ON s.id = p.source_id
		WHERE f.story_id = ? AND p.visibility = 'public' AND s.participation_mode = 'editorial' AND (NOT p.selected OR p.visible_after <= ?)
		ORDER BY p.article_id, (fa.role = 'primary') DESC, (fa.role <> 'mention') DESC, f.id`, storyID, now).Scan(&reports).Error; err != nil {
		return V1StoryResponse{}, false, err
	}
	if len(reports) == 0 {
		return V1StoryResponse{}, false, nil
	}
	var digestReports []v1StoryDigestRecord
	if err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT ON (p.article_id) f.story_id, p.article_id AS id, p.title, p.summary, s.name AS source_name,
		(s.tier = 'T1') AS first_party, coalesce(p.published_at, p.discovered_at) AS at, f.id AS fact_id,
		f.subject AS fact_subject, f.action AS fact_action, f.object AS fact_object, f.conditions AS fact_conditions,
		fa.evidence, an.output->'fact' AS structured_fact
		FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id
		JOIN sources s ON s.id = p.source_id LEFT JOIN analyses an ON an.id = p.analysis_id
		WHERE f.story_id = ? AND fa.role <> 'mention'
		AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
		AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
		ORDER BY p.article_id, (fa.role = 'primary') DESC, f.id`, storyID, now).Scan(&digestReports).Error; err != nil {
		return V1StoryResponse{}, false, err
	}
	var related []v1StoryRelatedRecord
	if err := r.db.WithContext(ctx).Raw(`SELECT st.public_id::text, st.title, l.relation FROM story_links l JOIN stories st ON st.id = l.other_id
		WHERE l.story_id = ? AND st.merged_into IS NULL AND EXISTS (
			SELECT 1 FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id JOIN sources s ON s.id = p.source_id
			WHERE f.story_id = st.id AND fa.role <> 'mention'
			AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
			AND p.visibility = 'public' AND s.participation_mode = 'editorial' AND (NOT p.selected OR p.visible_after <= ?))
		ORDER BY st.latest_at DESC NULLS LAST, st.id DESC LIMIT 8`, storyID, now).Scan(&related).Error; err != nil {
		return V1StoryResponse{}, false, err
	}
	return buildV1Story(story, reports, digestReports, related, now), true, nil
}

func (s *Service) ResolveV1Story(ctx context.Context, publicID string) (V1StoryLookup, error) {
	repository, ok := s.repository.(V1StoryRepository)
	if !ok {
		return V1StoryLookup{}, errors.New("v1 stories repository is unavailable")
	}
	if !publicStoryIDPattern.MatchString(publicID) {
		return V1StoryLookup{Kind: "not_found"}, nil
	}
	return repository.ResolveV1Story(ctx, publicID)
}

func (s *Service) V1Story(ctx context.Context, storyID int64, now time.Time, siteURL string) (V1StoryResponse, bool, error) {
	repository, ok := s.repository.(V1StoryRepository)
	if !ok {
		return V1StoryResponse{}, false, errors.New("v1 stories repository is unavailable")
	}
	response, found, err := repository.LoadV1Story(ctx, storyID, now.UTC().Truncate(time.Millisecond))
	if err != nil || !found {
		return response, found, err
	}
	baseURL := strings.TrimRight(siteURL, "/")
	response.Story.Links.AIHot = baseURL + "/story/" + response.Story.PublicID
	for index := range response.Story.Reports {
		response.Story.Reports[index].Links.AIHot = baseURL + "/items/" + response.Story.Reports[index].ID
	}
	for index := range response.Story.Storyline {
		response.Story.Storyline[index].Links = V1RelatedStoryLinks{AIHot: baseURL + "/story/" + response.Story.Storyline[index].PublicID, API: baseURL + "/api/v1/stories/" + response.Story.Storyline[index].PublicID}
	}
	for index := range response.Story.Related {
		response.Story.Related[index].Links = V1RelatedStoryLinks{AIHot: baseURL + "/story/" + response.Story.Related[index].PublicID, API: baseURL + "/api/v1/stories/" + response.Story.Related[index].PublicID}
	}
	return response, found, nil
}

func buildV1Story(story v1StoryDatabase, reports []v1StoryReportRecord, digestRows []v1StoryDigestRecord, related []v1StoryRelatedRecord, now time.Time) V1StoryResponse {
	sort.Slice(reports, func(i, j int) bool {
		if !reports[i].At.Equal(reports[j].At) {
			return reports[i].At.After(reports[j].At)
		}
		return reports[i].ID < reports[j].ID
	})
	primary := make([]v1StoryReportRecord, 0, len(reports))
	sources := make(map[string]struct{}, len(reports))
	for _, report := range reports {
		sources[report.SourceID] = struct{}{}
		if report.Role != "mention" {
			primary = append(primary, report)
		}
	}
	if len(primary) == 0 {
		return V1StoryResponse{}
	}
	latestReport := primary[0]
	firstReport := primary[len(primary)-1]
	latestEvidence := latestDigestReport(digestRows)
	latestAt := latestReport.At
	latestTitle := latestReport.Title
	if latestEvidence != nil {
		latestAt, latestTitle = latestEvidence.At, latestEvidence.Title
	}
	age := now.Sub(latestAt)
	status := "active"
	if age >= 72*time.Hour {
		status = "settled"
	}
	digest, digestUpdatedAt := currentStoryDigest(story, digestRows)
	body := V1StoryBody{
		PublicID: story.PublicID, Title: story.Title, Status: status, SourceCount: len(sources), ReportCount: len(reports),
		FirstReportAt: formatV1Time(firstReport.At), LatestAt: formatV1Time(latestAt), Latest: latestTitle,
		Digest: digest, DigestUpdatedAt: digestUpdatedAt, Reports: make([]V1StoryReport, 0, min(len(reports), 50)),
		Storyline: make([]V1RelatedStory, 0), Related: make([]V1RelatedStory, 0),
	}
	body.Links.AIHot = "" // set from SITE_URL by the service wrapper
	for _, report := range reports[:min(len(reports), 50)] {
		body.Reports = append(body.Reports, V1StoryReport{
			ID: report.ID, Title: report.Title, Summary: report.Summary,
			Source:      V1StorySource{Name: report.SourceName, FirstParty: report.FirstParty},
			PublishedAt: formatV1Time(report.At), Links: V1StoryItemLinks{Original: report.URL},
		})
	}
	for _, row := range related {
		entry := V1RelatedStory{PublicID: row.PublicID, Title: row.Title, Relation: row.Relation}
		if row.Relation == "storyline" {
			body.Storyline = append(body.Storyline, entry)
		} else {
			body.Related = append(body.Related, entry)
		}
	}
	return V1StoryResponse{SchemaVersion: 1, Story: body}
}

func latestDigestReport(rows []v1StoryDigestRecord) *v1StoryDigestRecord {
	if len(rows) == 0 {
		return nil
	}
	latest := rows[0]
	for _, row := range rows[1:] {
		if row.At.After(latest.At) || (row.At.Equal(latest.At) && row.ID < latest.ID) {
			latest = row
		}
	}
	return &latest
}

func currentStoryDigest(story v1StoryDatabase, reports []v1StoryDigestRecord) (*string, *string) {
	if story.Digest == nil || story.SavedDigest == nil || *story.Digest != *story.SavedDigest || story.SavedHash == nil {
		return nil, nil
	}
	var articleIDs []string
	if err := json.Unmarshal(story.SavedArticleIDs, &articleIDs); err != nil || len(articleIDs) == 0 {
		return nil, nil
	}
	wanted := make(map[string]struct{}, len(articleIDs))
	for _, id := range articleIDs {
		wanted[id] = struct{}{}
	}
	for _, row := range reports {
		delete(wanted, row.ID)
	}
	if len(wanted) != 0 {
		return nil, nil
	}
	filtered := make([]v1StoryDigestRecord, 0, len(wanted))
	articleSet := make(map[string]struct{}, len(articleIDs))
	for _, id := range articleIDs {
		articleSet[id] = struct{}{}
	}
	for _, row := range reports {
		if _, ok := articleSet[row.ID]; ok {
			filtered = append(filtered, row)
		}
	}
	if storyDigestHash(filtered) != *story.SavedHash {
		return nil, nil
	}
	var updated *string
	if story.DigestUpdated != nil {
		value := formatV1Time(*story.DigestUpdated)
		updated = &value
	}
	return story.Digest, updated
}

func storyDigestHash(rows []v1StoryDigestRecord) string {
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	values := make([]any, 0, len(rows))
	for _, row := range rows {
		var fact map[string]any
		_ = json.Unmarshal(row.StructuredFact, &fact)
		conditions := make([]map[string]string, 0)
		if raw, ok := fact["conditions"].([]any); ok {
			for _, candidate := range raw {
				value, ok := candidate.(map[string]any)
				if !ok {
					continue
				}
				text, textOK := value["text"].(string)
				quote, quoteOK := value["quote"].(string)
				if textOK && quoteOK {
					conditions = append(conditions, map[string]string{"text": text, "quote": quote})
				}
			}
		}
		var extractedEvidence any
		if value, ok := fact["evidence"].(string); ok {
			extractedEvidence = value
		}
		evidence := map[string]any{
			"subject": row.FactSubject, "action": row.FactAction, "object": row.FactObject,
			"conditions": row.FactConditions, "evidence": row.Evidence,
			"extractedConditions": conditions, "extractedEvidence": extractedEvidence,
		}
		summary := ""
		if row.Summary != nil {
			summary = *row.Summary
		}
		values = append(values, []any{row.ID, row.FactID, row.Title, summary, row.SourceName, row.FirstParty, formatV1Time(row.At), evidence})
	}
	encoded, _ := marshalStoryJSON(values)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func marshalStoryJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := strings.TrimSuffix(output.String(), "\n")
	encoded = strings.ReplaceAll(encoded, `\u2028`, "\u2028")
	encoded = strings.ReplaceAll(encoded, `\u2029`, "\u2029")
	return []byte(encoded), nil
}
