package publiccontent

import (
	"context"
	"encoding/json"
	"time"

	"aihot/backend-go/internal/model"
)

func (r *GormRepository) SiteHotRanking(ctx context.Context, now time.Time) (*SiteHotRanking, error) {
	var head struct {
		ID         int64     `gorm:"column:id"`
		ComputedAt time.Time `gorm:"column:computed_at"`
	}
	result := r.db.WithContext(ctx).Table("hot_rankings").Select("id, computed_at").
		Where("published").Order("computed_at DESC").Limit(1).Scan(&head)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	var rows []SiteHotRankingRecord
	const query = `
WITH ranking AS (
  SELECT h.computed_at, entry.ordinality::int AS rank, entry.value AS entry,
         (entry.value->>'storyId')::bigint AS story_id,
         entry.value->>'representativeItemId' AS representative_id
  FROM hot_rankings h
  CROSS JOIN LATERAL jsonb_array_elements(h.entries) WITH ORDINALITY AS entry(value, ordinality)
  WHERE h.id = ?
), current_reports AS (
  SELECT DISTINCT ON (f.story_id, p.article_id)
         f.story_id, p.article_id, st.public_id::text AS story_public_id, st.title AS story_title,
         p.url AS representative_url, s.name AS representative_source, latest.latest_at,
         h.computed_at, h.rank, h.entry
  FROM ranking h
  JOIN stories st ON st.id = h.story_id
  JOIN facts f ON f.story_id = st.id
  JOIN fact_articles fa ON fa.fact_id = f.id
  JOIN publications p ON p.article_id = fa.article_id
  JOIN sources s ON s.id = p.source_id
  JOIN LATERAL (
    SELECT max(coalesce(p2.published_at, p2.discovered_at)) AS latest_at
    FROM facts f2
    JOIN fact_articles fa2 ON fa2.fact_id = f2.id
    JOIN publications p2 ON p2.article_id = fa2.article_id
    WHERE f2.story_id = st.id AND fa2.role <> 'mention'
      AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p2.analysis_id AND evidence_an.output->>'scope' = 'composite')
      AND p2.visibility = 'public' AND p2.eligible AND (NOT p2.selected OR p2.visible_after <= ?)
  ) latest ON latest.latest_at IS NOT NULL
  WHERE p.article_id = h.representative_id AND fa.role <> 'mention'
    AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
    AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
  ORDER BY f.story_id, p.article_id, (fa.role = 'primary') DESC, f.id
)
SELECT computed_at, rank, story_id, entry, story_public_id, story_title, representative_url, representative_source, latest_at
FROM current_reports ORDER BY rank`
	if err := r.db.WithContext(ctx).Raw(query, head.ID, now, now).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []SiteHotRankingRecord{}
	}
	for i := range rows {
		rows[i].ComputedAt = head.ComputedAt
	}
	return &SiteHotRanking{ComputedAt: head.ComputedAt, Entries: rows}, nil
}

func (r *GormRepository) SiteHotDetails(ctx context.Context, rankings []SiteHotRankingRecord, computedAt, now time.Time) (SiteHotDetails, error) {
	details := SiteHotDetails{
		Sparks: make(map[int64][]SiteHotSparkRecord), Faces: []SiteHotFaceRecord{},
		Texts: make(map[int64]SiteHotTexts), Covers: make(map[int64]SiteHotCoverRecord),
	}
	if len(rankings) == 0 {
		return details, nil
	}
	storyIDs := make([]int64, 0, len(rankings))
	representativeIDs := make([]string, 0, len(rankings))
	seen := make(map[int64]struct{}, len(rankings))
	for _, ranking := range rankings {
		if _, exists := seen[ranking.StoryID]; !exists {
			seen[ranking.StoryID] = struct{}{}
			storyIDs = append(storyIDs, ranking.StoryID)
		}
		var entry struct {
			RepresentativeItemID *string `json:"representativeItemId"`
		}
		if err := json.Unmarshal(ranking.Entry, &entry); err != nil {
			return SiteHotDetails{}, err
		}
		if entry.RepresentativeItemID != nil {
			representativeIDs = append(representativeIDs, *entry.RepresentativeItemID)
		}
	}
	if err := r.loadSiteHotSparks(ctx, &details, storyIDs, computedAt); err != nil {
		return SiteHotDetails{}, err
	}
	if err := r.loadSiteHotFaces(ctx, &details, storyIDs); err != nil {
		return SiteHotDetails{}, err
	}
	if err := r.loadSiteHotTexts(ctx, &details, storyIDs, now); err != nil {
		return SiteHotDetails{}, err
	}
	if err := r.loadSiteHotCovers(ctx, &details, storyIDs, representativeIDs, now); err != nil {
		return SiteHotDetails{}, err
	}
	return details, nil
}

func (r *GormRepository) loadSiteHotSparks(ctx context.Context, details *SiteHotDetails, storyIDs []int64, computedAt time.Time) error {
	end := time.Unix(computedAt.Unix()/3600*3600, 0).UTC()
	start := end.Add(-24 * time.Hour)
	var rows []SiteHotSparkRecord
	err := r.db.WithContext(ctx).Table("story_heat_hourly").
		Select("story_id, hour, heat").
		Where("story_id IN ? AND complete AND hour >= ? AND hour <= ?", storyIDs, start, end).
		Scan(&rows).Error
	if err != nil {
		return err
	}
	for _, row := range rows {
		details.Sparks[row.StoryID] = append(details.Sparks[row.StoryID], row)
	}
	return nil
}

func (r *GormRepository) loadSiteHotFaces(ctx context.Context, details *SiteHotDetails, storyIDs []int64) error {
	var rows []SiteHotFaceRecord
	err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT ON (s.id) s.name,
		CASE WHEN s.icon_url IS NOT NULL THEN s.icon_url ELSE a.x_post->>'avatarUrl' END AS url
		FROM story_signals ss JOIN sources s ON s.id = ss.source_id
		LEFT JOIN articles a ON a.id = CASE WHEN s.icon_url IS NULL THEN ss.article_id END AND jsonb_exists(a.x_post, 'avatarUrl')
		WHERE ss.story_id IN ?
		ORDER BY s.id, (a.id IS NULL), a.discovered_at DESC, a.id DESC`, storyIDs).Scan(&rows).Error
	if err != nil {
		return err
	}
	details.Faces = rows
	return nil
}

func (r *GormRepository) loadSiteHotTexts(ctx context.Context, details *SiteHotDetails, storyIDs []int64, now time.Time) error {
	var bases []siteHotTextBase
	err := r.db.WithContext(ctx).Raw(`SELECT st.id, st.digest, st.digest_updated_at,
		saved.digest AS saved_digest, saved.inputs_hash, to_json(saved.article_ids) AS article_ids,
		CASE WHEN EXISTS (SELECT 1 FROM publications p WHERE p.story_id = st.id AND p.summary = st.summary
			AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)) THEN st.summary END AS summary,
		latest.title AS latest_title
		FROM stories st
		LEFT JOIN LATERAL (SELECT article_ids, inputs_hash, digest FROM story_digests WHERE story_id = st.id ORDER BY version DESC LIMIT 1) saved ON true
		LEFT JOIN LATERAL (
			SELECT p.title FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id
			WHERE f.story_id = st.id AND fa.role <> 'mention'
			AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
			AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
			ORDER BY coalesce(p.published_at, p.discovered_at) DESC, p.article_id LIMIT 1
		) latest ON true WHERE st.id IN ?`, now, now, storyIDs).Scan(&bases).Error
	if err != nil {
		return err
	}
	var digestRows []v1StoryDigestRecord
	if err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT ON (f.story_id, p.article_id) f.story_id,
		p.article_id AS id, p.title, p.summary, s.name AS source_name, (s.tier = 'T1') AS first_party,
		coalesce(p.published_at, p.discovered_at) AS at, f.id AS fact_id,
		f.subject AS fact_subject, f.action AS fact_action, f.object AS fact_object, f.conditions AS fact_conditions,
		fa.evidence, an.output->'fact' AS structured_fact
		FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id JOIN publications p ON p.article_id = fa.article_id
		JOIN sources s ON s.id = p.source_id LEFT JOIN analyses an ON an.id = p.analysis_id
		WHERE f.story_id IN ? AND fa.role <> 'mention'
		AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite')
		AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
		ORDER BY f.story_id, p.article_id, (fa.role = 'primary') DESC, f.id`, storyIDs, now).Scan(&digestRows).Error; err != nil {
		return err
	}
	byStory := make(map[int64][]v1StoryDigestRecord, len(storyIDs))
	for _, row := range digestRows {
		byStory[row.StoryID] = append(byStory[row.StoryID], row)
	}
	for _, base := range bases {
		story := v1StoryDatabase{
			Digest: base.Digest, DigestUpdated: base.DigestUpdatedAt, Summary: base.Summary,
			SavedDigest: base.SavedDigest, SavedHash: base.InputsHash, SavedArticleIDs: base.ArticleIDs,
		}
		digest, _ := currentStoryDigest(story, byStory[base.ID])
		summary := digest
		if summary == nil {
			summary = base.Summary
		}
		var latest *string
		if base.LatestTitle != nil {
			latest = base.LatestTitle
		}
		details.Texts[base.ID] = SiteHotTexts{Summary: summary, Latest: latest}
	}
	return nil
}

type siteHotTextBase struct {
	ID              int64           `gorm:"column:id"`
	Digest          *string         `gorm:"column:digest"`
	DigestUpdatedAt *time.Time      `gorm:"column:digest_updated_at"`
	SavedDigest     *string         `gorm:"column:saved_digest"`
	InputsHash      *string         `gorm:"column:inputs_hash"`
	ArticleIDs      json.RawMessage `gorm:"column:article_ids"`
	Summary         *string         `gorm:"column:summary"`
	LatestTitle     *string         `gorm:"column:latest_title"`
}

func (r *GormRepository) loadSiteHotCovers(ctx context.Context, details *SiteHotDetails, storyIDs []int64, representatives []string, now time.Time) error {
	var rows []SiteHotCoverRecord
	err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT ON (p.story_id) p.story_id, img.url, img.width, img.height
		FROM publications p JOIN articles a ON a.id = p.article_id
		CROSS JOIN LATERAL (
			SELECT m->>'url' AS url, NULLIF(m->>'width', '')::double precision AS width, NULLIF(m->>'height', '')::double precision AS height
			FROM jsonb_array_elements(coalesce(a.media, '[]'::jsonb)) m
			WHERE m->>'kind' = 'image' AND coalesce(NULLIF(m->>'width', '')::numeric, 800) >= 480 LIMIT 1
		) img
		WHERE p.story_id IN ? AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
		AND p.body_mode <> 'summary'
		ORDER BY p.story_id, (p.article_id = ANY(?::text[])) DESC, p.first_party DESC, p.selected DESC, coalesce(p.score, 0) DESC, p.article_id`, storyIDs, now, model.TextArray(representatives)).Scan(&rows).Error
	if err != nil {
		return err
	}
	for _, row := range rows {
		details.Covers[row.StoryID] = row
	}
	return nil
}

var _ SiteHotRepository = (*GormRepository)(nil)
