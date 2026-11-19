package editorial

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type AnalyzeResult struct {
	AnalysisID *int64          `json:"analysisId"`
	Stale      bool            `json:"stale"`
	NeedsBody  bool            `json:"needsBody,omitempty"`
	Output     *AnalysisOutput `json:"output"`
	ReceiptIDs []int64         `json:"receiptIds"`
	Reused     bool            `json:"reused"`
}

type Repository interface {
	LoadInput(context.Context, string) (*Input, error)
	SaveAnalysis(context.Context, Input, AnalysisRun, AnalysisOutput, string) (AnalyzeResult, error)
}

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

type analysisInputRow struct {
	ID            string          `gorm:"column:id"`
	Revision      int             `gorm:"column:revision"`
	Title         string          `gorm:"column:title"`
	URL           string          `gorm:"column:url"`
	Author        *string         `gorm:"column:author"`
	PublishedAt   *time.Time      `gorm:"column:published_at"`
	DiscoveredAt  *time.Time      `gorm:"column:discovered_at"`
	BodyText      *string         `gorm:"column:body_text"`
	Excerpt       *string         `gorm:"column:excerpt"`
	BodyStatus    string          `gorm:"column:body_status"`
	XPost         json.RawMessage `gorm:"column:x_post;type:jsonb"`
	XArticle      json.RawMessage `gorm:"column:x_article;type:jsonb"`
	Media         json.RawMessage `gorm:"column:media;type:jsonb"`
	SourceName    string          `gorm:"column:source_name"`
	SourceKind    string          `gorm:"column:source_kind"`
	Tier          string          `gorm:"column:tier"`
	SourceTags    []string        `gorm:"column:source_tags;type:text[]"`
	OwnerEntityID *string         `gorm:"column:owner_entity_id"`
	SourceConfig  json.RawMessage `gorm:"column:source_config;type:jsonb"`
	TranslationZH *string         `gorm:"column:translation_zh"`
}

func (r *GormRepository) LoadInput(ctx context.Context, articleID string) (*Input, error) {
	var row analysisInputRow
	query := `SELECT a.id,a.revision,a.title,a.url,a.author,a.published_at,a.discovered_at,a.body_text,a.excerpt,a.body_status,
		a.x_post,a.x_article,a.media,s.name AS source_name,s.kind AS source_kind,s.tier,s.tags AS source_tags,s.owner_entity_id,
		s.config AS source_config,tr.body_text AS translation_zh
		FROM articles a JOIN sources s ON s.id=a.source_id
		LEFT JOIN translations tr ON tr.article_id=a.id AND tr.lang='zh' AND tr.revision>=a.revision
		WHERE a.id=?`
	result := r.db.WithContext(ctx).Raw(query, articleID).Scan(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	var post *XPost
	if len(row.XPost) > 0 && string(row.XPost) != "null" {
		var value XPost
		if err := json.Unmarshal(row.XPost, &value); err != nil {
			return nil, fmt.Errorf("decode article X post: %w", err)
		}
		post = &value
	}
	var xArticle *XArticle
	if len(row.XArticle) > 0 && string(row.XArticle) != "null" {
		var value XArticle
		if err := json.Unmarshal(row.XArticle, &value); err != nil {
			return nil, fmt.Errorf("decode article X Article: %w", err)
		}
		xArticle = &value
	}
	media := []Media{}
	if len(row.Media) > 0 {
		if err := json.Unmarshal(row.Media, &media); err != nil {
			return nil, fmt.Errorf("decode article media: %w", err)
		}
	}
	config := map[string]json.RawMessage{}
	if len(row.SourceConfig) > 0 {
		if err := json.Unmarshal(row.SourceConfig, &config); err != nil {
			return nil, fmt.Errorf("decode source config: %w", err)
		}
	}
	return &Input{
		ID: row.ID, Revision: row.Revision, Title: row.Title, URL: row.URL, Author: stringValue(row.Author),
		PublishedAt: row.PublishedAt, DiscoveredAt: row.DiscoveredAt, BodyText: row.BodyText, Excerpt: row.Excerpt,
		BodyStatus: row.BodyStatus, XPost: WithXArticle(post, xArticle), Media: media,
		Source: Source{Name: row.SourceName, Kind: row.SourceKind, Tier: row.Tier, FirstParty: row.Tier == "T1",
			Tags: row.SourceTags, OwnerEntityID: row.OwnerEntityID,
			FetchesBody: jsonTruthy(config["fetchPublicContent"]) || jsonTruthy(config["detail"]) || row.SourceKind == "web_list"},
		TranslationZH: row.TranslationZH,
	}, nil
}

func (r *GormRepository) SaveAnalysis(ctx context.Context, input Input, run AnalysisRun, output AnalysisOutput, promptVersion string) (AnalyzeResult, error) {
	receiptIDs := make([]int64, 0, 1+2+1+1)
	if run.Prefilter.ReceiptID > 0 {
		receiptIDs = append(receiptIDs, run.Prefilter.ReceiptID)
	}
	if run.Scores != nil {
		receiptIDs = append(receiptIDs, run.Scores.ReceiptIDs...)
	}
	if run.Writing != nil {
		receiptIDs = append(receiptIDs, run.Writing.ReceiptIDs...)
	}
	if run.Structure != nil && run.Structure.ReceiptID > 0 {
		receiptIDs = append(receiptIDs, run.Structure.ReceiptID)
	}
	modelName := run.Prefilter.Model
	if run.Writing != nil && run.Writing.Model != nil {
		modelName = *run.Writing.Model
	}
	detail, err := marshalAnalysisDetail(run, output)
	if err != nil {
		return AnalyzeResult{}, err
	}
	row := model.ArticleAnalysis{
		ArticleID: input.ID, InputRevision: input.Revision, Origin: "model", Model: stringPointer(modelName), PromptVersion: promptVersion,
		ReceiptIDs: model.Int64Array(receiptIDs), Relevance: output.Relevance, Category: output.Category,
		Tags: model.TextArray(output.Tags), Subjects: model.TextArray(output.Subjects),
		TitleZh: output.TitleZh, SummaryZh: output.SummaryZh, ReasonZh: output.ReasonZh, Score: output.Score, Selected: output.Selected, Output: detail,
	}
	var id int64
	var stale bool
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current struct {
			Revision int `gorm:"column:revision"`
		}
		result := tx.Raw("SELECT revision FROM articles WHERE id = ? FOR UPDATE", input.ID).Scan(&current)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			stale = true
			return nil
		}
		stale = current.Revision != input.Revision
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		id = row.ID
		if len(receiptIDs) > 0 {
			if err := tx.Table("receipts").Where("id IN ?", receiptIDs).Updates(map[string]any{
				"status": "completed", "completed_at": gorm.Expr("coalesce(completed_at, now())"), "updated_at": gorm.Expr("now()"),
			}).Error; err != nil {
				return err
			}
		}
		if !stale {
			state := "analyzed"
			if output.Relevance == "block" {
				state = "blocked"
			}
			if err := tx.Model(&model.ArticleProcessing{}).Where("id = ?", input.ID).Updates(map[string]any{
				"processing_state": state, "processing_error": nil,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return AnalyzeResult{}, err
	}
	if id == 0 {
		return AnalyzeResult{Stale: true, Output: &output, ReceiptIDs: []int64{}, Reused: analysisReused(run)}, nil
	}
	return AnalyzeResult{AnalysisID: &id, Stale: stale, Output: &output, ReceiptIDs: receiptIDs, Reused: analysisReused(run)}, nil
}

func marshalAnalysisDetail(run AnalysisRun, output AnalysisOutput) (json.RawMessage, error) {
	var scores any
	if output.Scores != nil {
		scores = output.Scores
	}
	detail := map[string]any{
		"prefilter": map[string]any{"label": run.Prefilter.Label, "reason": run.Prefilter.Reason},
		"scores":    scores, "scoreModel": output.ScoreModel, "threshold": output.Threshold,
		"scope": output.Scope, "fact": output.Fact,
	}
	if output.ScoreRefused {
		detail["scoreRefused"] = true
	}
	if run.Writing != nil {
		detail["writer"] = run.Writing.Kind
		detail["writerModel"] = run.Writing.Model
		if run.Writing.ItemType == nil {
			detail["itemType"] = nil
		} else {
			detail["itemType"] = *run.Writing.ItemType
		}
		if run.Writing.AuthorRole == nil {
			detail["authorRole"] = nil
		} else {
			detail["authorRole"] = *run.Writing.AuthorRole
		}
		if run.Writing.IdentityGuard != nil && run.Writing.IdentityGuard.Outcome == "fallback" {
			detail["identityGuard"] = run.Writing.IdentityGuard
		}
	}
	encoded, err := json.Marshal(detail)
	return encoded, err
}

func analysisReused(run AnalysisRun) bool {
	reused := run.Prefilter.Reused
	if run.Scores != nil {
		reused = reused && run.Scores.Reused
	}
	if run.Writing != nil {
		reused = reused && run.Writing.Reused
	}
	if run.Structure != nil {
		reused = reused && run.Structure.Reused
	}
	return reused
}

func jsonTruthy(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "false" || string(raw) == "0" || string(raw) == `""` {
		return false
	}
	return true
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
