package grouping

import (
	"context"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/publication"
)

// CandidateViews adds each recalled fact's stable identity representative and selected coverage.
// The recall score decides which facts are considered; it never selects a report representative.
func (r *GormRepository) CandidateViews(ctx context.Context, recalled []Recalled, entities map[string]interests.Entity, now time.Time) ([]Candidate, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	if len(recalled) == 0 {
		return []Candidate{}, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	ids := make([]int64, 0, len(recalled))
	for _, row := range recalled {
		ids = append(ids, row.FactID)
	}
	var representatives []candidateRepresentativeRow
	if err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT ON (fa.fact_id)
		fa.fact_id, f.story_id, f.title AS fact_title, f.subject, f.action, f.object, f.occurred_at,
		p.title, p.summary, s.name AS source, (s.tier = 'T1') AS first_party,
		coalesce(p.published_at, p.discovered_at) AS at,
		(SELECT count(*) FROM fact_articles x WHERE x.fact_id = fa.fact_id AND x.role IN ('primary', 'report')
			AND (SELECT y.output->>'scope' = 'composite' FROM analyses y WHERE y.article_id = x.article_id
				ORDER BY y.input_revision DESC, y.id DESC LIMIT 1) IS NOT TRUE) AS members,
		coalesce((SELECT y.id FROM facts y
			JOIN fact_articles z ON z.fact_id = y.id AND z.role IN ('primary', 'report')
				AND (SELECT x.output->>'scope' = 'composite' FROM analyses x WHERE x.article_id = z.article_id
					ORDER BY x.input_revision DESC, x.id DESC LIMIT 1) IS NOT TRUE
			JOIN publications q ON q.article_id = z.article_id
			WHERE y.story_id = f.story_id
			ORDER BY coalesce(q.published_at, q.discovered_at), y.id LIMIT 1), 0) AS root_fact_id
		FROM fact_articles fa
		JOIN facts f ON f.id = fa.fact_id
		JOIN publications p ON p.article_id = fa.article_id
		JOIN sources s ON s.id = p.source_id
		WHERE fa.fact_id IN ? AND fa.role IN ('primary', 'report')
		AND (SELECT x.output->>'scope' = 'composite' FROM analyses x WHERE x.article_id = fa.article_id
			ORDER BY x.input_revision DESC, x.id DESC LIMIT 1) IS NOT TRUE
		ORDER BY fa.fact_id, (fa.role = 'primary') DESC, p.timeline_at ASC`, ids).Scan(&representatives).Error; err != nil {
		return nil, err
	}
	var selectedRows []candidateSelectedRow
	if err := r.db.WithContext(ctx).Raw(`SELECT p.fact_id, p.article_id, p.title, p.summary, p.body_mode, p.score, p.timeline_at,
		s.name AS source, (s.tier = 'T1') AS first_party, coalesce(p.published_at, p.discovered_at) AS at,
		s.tier AS source_tier, s.config->>'publisherRole' AS publisher_role, s.owner_entity_id, f.subject AS fact_subject
		FROM publications p JOIN sources s ON s.id = p.source_id JOIN facts f ON f.id = p.fact_id
		WHERE p.fact_id IN ? AND p.visibility = 'public' AND p.selected AND p.visible_after <= ?
		AND EXISTS (SELECT 1 FROM fact_articles fa WHERE fa.fact_id = p.fact_id AND fa.article_id = p.article_id
			AND fa.role <> 'mention'
			AND NOT EXISTS (SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id
				AND evidence_an.output->>'scope' = 'composite'))`, ids, now).Scan(&selectedRows).Error; err != nil {
		return nil, err
	}
	byFact := make(map[int64]candidateRepresentativeRow, len(representatives))
	for _, row := range representatives {
		byFact[row.FactID] = row
	}
	selectedByFact := make(map[int64][]candidateSelectedRow)
	for _, row := range selectedRows {
		selectedByFact[row.FactID] = append(selectedByFact[row.FactID], row)
	}
	result := make([]Candidate, 0, len(recalled))
	for _, recalledRow := range recalled {
		row, ok := byFact[recalledRow.FactID]
		if !ok {
			continue
		}
		candidate := Candidate{
			FactID: recalledRow.FactID, StoryID: row.StoryID, FactTitle: row.FactTitle, Members: row.Members,
			StoryRoot: row.RootFactID == recalledRow.FactID, Score: recalledRow.Score,
			Report: Report{Title: row.Title, Source: row.Source, FirstParty: row.FirstParty, At: &row.At,
				Summary: row.Summary, Frame: &Frame{Subject: row.Subject, Action: row.Action, Object: row.Object,
					OccurredAt: beijingDatePointer(row.OccurredAt)}},
		}
		rows := selectedByFact[recalledRow.FactID]
		if len(rows) > 0 {
			publicationRows := make([]publication.Representative, 0, len(rows))
			byArticle := make(map[string]candidateSelectedRow, len(rows))
			for _, selected := range rows {
				byArticle[selected.ArticleID] = selected
				publicationRows = append(publicationRows, publication.Representative{
					ArticleID: selected.ArticleID,
					Identity: publication.RepresentativeIdentity{SourceTier: selected.SourceTier, PublisherRole: stringValue(selected.PublisherRole),
						OwnerEntityID: stringValue(selected.OwnerEntityID), FactSubject: stringValue(selected.FactSubject)},
					BodyMode: selected.BodyMode, Score: selected.Score, TimelineAt: selected.TimelineAt,
				})
			}
			if selected, ok := publication.PickRepresentative(publicationRows, entities); ok {
				row := byArticle[selected.ArticleID]
				candidate.Selected = true
				candidate.SelectedReport = &Report{Title: row.Title, Summary: row.Summary, Source: row.Source,
					FirstParty: row.FirstParty, At: &row.At}
			}
		}
		result = append(result, candidate)
	}
	return result, nil
}

type candidateRepresentativeRow struct {
	FactID     int64      `gorm:"column:fact_id"`
	StoryID    int64      `gorm:"column:story_id"`
	FactTitle  string     `gorm:"column:fact_title"`
	Subject    *string    `gorm:"column:subject"`
	Action     *string    `gorm:"column:action"`
	Object     *string    `gorm:"column:object"`
	OccurredAt *time.Time `gorm:"column:occurred_at"`
	Title      string     `gorm:"column:title"`
	Summary    *string    `gorm:"column:summary"`
	Source     string     `gorm:"column:source"`
	FirstParty bool       `gorm:"column:first_party"`
	At         time.Time  `gorm:"column:at"`
	Members    int        `gorm:"column:members"`
	RootFactID int64      `gorm:"column:root_fact_id"`
}

type candidateSelectedRow struct {
	FactID        int64     `gorm:"column:fact_id"`
	ArticleID     string    `gorm:"column:article_id"`
	Title         string    `gorm:"column:title"`
	Summary       *string   `gorm:"column:summary"`
	BodyMode      string    `gorm:"column:body_mode"`
	Score         *float64  `gorm:"column:score"`
	TimelineAt    time.Time `gorm:"column:timeline_at"`
	Source        string    `gorm:"column:source"`
	FirstParty    bool      `gorm:"column:first_party"`
	At            time.Time `gorm:"column:at"`
	SourceTier    string    `gorm:"column:source_tier"`
	PublisherRole *string   `gorm:"column:publisher_role"`
	OwnerEntityID *string   `gorm:"column:owner_entity_id"`
	FactSubject   *string   `gorm:"column:fact_subject"`
}

func beijingDatePointer(value *time.Time) *string {
	if value == nil {
		return nil
	}
	date := value.UTC().Add(8 * time.Hour).Format("2006-01-02")
	return &date
}
