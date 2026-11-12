package adminselectbench

import (
	"context"
	"encoding/json"
	"errors"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) List(ctx context.Context) ([]RunListEntry, error) {
	var runs []RunListEntry
	err := r.db.WithContext(ctx).Table("selectbench_runs AS r").Select(`
		r.id, r.label, r.split, r.sample_size, r.prompt_version, r.models,
		(SELECT coalesce(jsonb_object_agg(key, value - 'sweep'), '{}'::jsonb) FROM jsonb_each(r.summary)) AS summary,
		r.created_at, r.imported_by,
		(SELECT count(*)::int FROM selectbench_results x WHERE x.run_id = r.id) AS cases`).
		Order("r.created_at DESC").Limit(100).Scan(&runs).Error
	if err != nil {
		return nil, err
	}
	if runs == nil {
		runs = []RunListEntry{}
	}
	return runs, nil
}

func (r *GormRepository) Run(ctx context.Context, id string, filters Filters) (*RunCases, error) {
	var stored model.SelectBenchRun
	result := r.db.WithContext(ctx).First(&stored, "id = ?", id)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	run := RunSummary{ID: stored.ID, Label: stored.Label, Split: stored.Split, SampleSize: stored.SampleSize, Seed: stored.Seed, PromptVersion: stored.PromptVersion, Models: stored.Models, Summary: stored.Summary, CreatedAt: stored.CreatedAt, ImportedBy: stored.ImportedBy}
	modelName := filters.Model
	if modelName == "" && len(stored.Models) > 0 {
		modelName = stored.Models[0]
	}
	var rawRows []struct {
		CaseID  string          `gorm:"column:case_id"`
		Title   string          `gorm:"column:title"`
		Stratum *string         `gorm:"column:stratum"`
		Gold    string          `gorm:"column:gold"`
		ByModel json.RawMessage `gorm:"column:by_model"`
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT case_id, min(title) AS title, min(stratum) AS stratum, min(gold) AS gold,
		       jsonb_object_agg(model, jsonb_build_object('decision', decision, 'score', score, 'relevance', relevance,
		         'category', category, 'reason', reason, 'error', error, 'receiptId', receipt_id)) AS by_model
		FROM selectbench_results
		WHERE run_id = ? AND (?::text IS NULL OR stratum = ?::text)
		GROUP BY case_id
		HAVING (?::text IS NULL OR bool_or(model = ? AND CASE ?
		  WHEN 'fp' THEN decision = 'select' AND gold = 'reject'
		  WHEN 'fn' THEN decision = 'reject' AND gold = 'select'
		  WHEN 'tp' THEN decision = 'select' AND gold = 'select'
		  WHEN 'tn' THEN decision = 'reject' AND gold = 'reject'
		  WHEN 'either' THEN gold = 'either'
		  WHEN 'error' THEN decision IS NULL
		  ELSE true END))
		  AND (? = false OR count(DISTINCT decision) > 1)
		ORDER BY min(stratum), case_id LIMIT 400`,
		id, filters.Stratum, filters.Stratum, nullableString(filters.Outcome), modelName, filters.Outcome, filters.Disagree).Scan(&rawRows).Error
	if err != nil {
		return nil, err
	}
	rows := make([]CaseRow, 0, len(rawRows))
	for _, row := range rawRows {
		decisions := map[string]Decision{}
		if err := json.Unmarshal(row.ByModel, &decisions); err != nil {
			return nil, err
		}
		rows = append(rows, CaseRow{CaseID: row.CaseID, Title: row.Title, Stratum: row.Stratum, Gold: row.Gold, ByModel: decisions})
	}
	var strata []Stratum
	err = r.db.WithContext(ctx).Raw(`SELECT stratum, count(DISTINCT case_id)::int AS n FROM selectbench_results WHERE run_id = ? GROUP BY 1 ORDER BY 2 DESC`, id).Scan(&strata).Error
	if err != nil {
		return nil, err
	}
	if strata == nil {
		strata = []Stratum{}
	}
	return &RunCases{Run: run, Rows: rows, Strata: strata}, nil
}

func (r *GormRepository) Import(ctx context.Context, data ImportData, actor string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run := model.SelectBenchRun{ID: data.ID, Label: data.Label, Split: data.Split, SampleSize: data.SampleSize, Seed: data.Seed, PromptVersion: data.Prompt, Models: data.Models, Summary: data.Summary, ImportedBy: &actor}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		rows := make([]model.SelectBenchResult, 0, len(data.Results))
		for _, item := range data.Results {
			rows = append(rows, model.SelectBenchResult{RunID: data.ID, Model: item.Model, CaseID: item.CaseID, Title: item.Title, Stratum: item.Stratum, Gold: item.Gold, Decision: item.Decision, Score: item.Score, Relevance: item.Relevance, Category: item.Category, Reason: item.Reason, ReceiptID: item.ReceiptID, Error: item.Error})
		}
		for start := 0; start < len(rows); start += 500 {
			end := min(start+500, len(rows))
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows[start:end], 500).Error; err != nil {
				return err
			}
		}
		before, _ := json.Marshal(nil)
		after, _ := json.Marshal(map[string]any{"label": data.Label, "models": data.Models, "sampleSize": data.SampleSize})
		subject := "selectbench:" + data.ID
		return tx.Create(&model.AdminAuditLog{Actor: actor, Action: "selectbench.import", Subject: &subject, After: after, Before: before}).Error
	})
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
