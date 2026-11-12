package storylinks

import (
	"context"

	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) LinkRelatedStories(ctx context.Context, recallDays int, confidence float64, reportCount int) (int, error) {
	var result struct {
		Added int `gorm:"column:added"`
	}
	const query = `
		WITH latest AS (
			SELECT DISTINCT ON (article_id) article_id, verdict, candidates
			FROM grouping_decisions
			WHERE created_at > now() - make_interval(days => ?)
			ORDER BY article_id, id DESC
		), ties AS (
			SELECT DISTINCT latest.article_id, own.story_id AS a, other.story_id AS b
			FROM latest
			JOIN fact_articles fa ON fa.article_id = latest.article_id AND fa.role IN ('primary', 'report')
				AND NOT ((SELECT analysis.output->>'scope' = 'composite'
					FROM analyses analysis WHERE analysis.article_id = fa.article_id
					ORDER BY analysis.input_revision DESC, analysis.id DESC LIMIT 1) IS TRUE)
			JOIN facts own ON own.id = fa.fact_id
			CROSS JOIN LATERAL jsonb_array_elements(latest.candidates) candidate
			JOIN facts other ON other.id = (candidate->>'id')::bigint
			WHERE latest.verdict IN ('same-fact', 'same-url', 'new-fact-in-story', 'new-story')
				AND candidate->>'relation' IN ('SAME_OCCURRENCE', 'SAME_STORY')
				AND (candidate->>'confidence')::numeric >= ?
				AND other.story_id <> own.story_id
		), pairs AS (
			SELECT least(a, b) AS x, greatest(a, b) AS y
			FROM ties GROUP BY 1, 2 HAVING count(DISTINCT article_id) >= ?
		), linked AS (
			SELECT pairs.x, pairs.y
			FROM pairs
			JOIN stories sx ON sx.id = pairs.x AND sx.merged_into IS NULL
			JOIN stories sy ON sy.id = pairs.y AND sy.merged_into IS NULL
			WHERE NOT EXISTS (
				SELECT 1 FROM fact_articles root_report
				JOIN grouping_decisions root_decision ON root_decision.article_id = root_report.article_id AND root_decision.verdict = 'roundup'
				WHERE root_report.fact_id = (
					SELECT root.id FROM facts root
					JOIN fact_articles root_member ON root_member.fact_id = root.id
						AND root_member.role IN ('primary', 'report')
						AND NOT ((SELECT analysis.output->>'scope' = 'composite'
							FROM analyses analysis WHERE analysis.article_id = root_member.article_id
							ORDER BY analysis.input_revision DESC, analysis.id DESC LIMIT 1) IS TRUE)
					JOIN publications root_publication ON root_publication.article_id = root_member.article_id
					WHERE root.story_id = pairs.x
					ORDER BY coalesce(root_publication.published_at, root_publication.discovered_at), root.id
					LIMIT 1
				) AND root_report.role <> 'mention'
			) AND NOT EXISTS (
				SELECT 1 FROM fact_articles root_report
				JOIN grouping_decisions root_decision ON root_decision.article_id = root_report.article_id AND root_decision.verdict = 'roundup'
				WHERE root_report.fact_id = (
					SELECT root.id FROM facts root
					JOIN fact_articles root_member ON root_member.fact_id = root.id
						AND root_member.role IN ('primary', 'report')
						AND NOT ((SELECT analysis.output->>'scope' = 'composite'
							FROM analyses analysis WHERE analysis.article_id = root_member.article_id
							ORDER BY analysis.input_revision DESC, analysis.id DESC LIMIT 1) IS TRUE)
					JOIN publications root_publication ON root_publication.article_id = root_member.article_id
					WHERE root.story_id = pairs.y
					ORDER BY coalesce(root_publication.published_at, root_publication.discovered_at), root.id
					LIMIT 1
				) AND root_report.role <> 'mention'
			)
		), added AS (
			INSERT INTO story_links (story_id, other_id, relation)
			SELECT x, y, 'related' FROM linked
			UNION ALL
			SELECT y, x, 'related' FROM linked
			ON CONFLICT (story_id, other_id) DO NOTHING
			RETURNING 1
		)
		SELECT count(*)::int AS added FROM added`
	err := r.db.WithContext(ctx).Raw(query, recallDays, confidence, reportCount).Scan(&result).Error
	return result.Added, err
}
