package tagging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"aihot/backend-go/internal/provider"

	"gorm.io/gorm"
)

/** Service coordinates source auto-labeling, storage, and canonical promotion. */
type Service struct {
	db     *gorm.DB
	engine *Engine
	chat   provider.ChatFunc
	model  string
}

/** NewService creates a new tagging service instance. */
func NewService(db *gorm.DB, engine *Engine, chat provider.ChatFunc, model string) *Service {
	return &Service{
		db:     db,
		engine: engine,
		chat:   chat,
		model:  model,
	}
}

/** TagSource extracts and persists labels for a given source, refreshing sources.tags. */
func (s *Service) TagSource(ctx context.Context, sourceID string) error {
	var samples []ArticleSample

	// 读取该信源最新的最多 10 篇文章样本
	query := `SELECT a.title, COALESCE(a.excerpt, '') AS excerpt
		FROM articles a
		JOIN article_discoveries ad ON ad.article_id = a.id
		WHERE ad.source_id = ?
		ORDER BY ad.discovered_at DESC
		LIMIT 10`
	if err := s.db.WithContext(ctx).Raw(query, sourceID).Scan(&samples).Error; err != nil {
		return err
	}

	if len(samples) == 0 {
		return nil
	}

	// 运行 Stage 1 规则匹配 (0 Token)
	tags := s.engine.ExtractRules(samples)

	// 若规则命中少于 2 个且模型可用，运行 Stage 2 兜底提炼
	if len(tags) < 2 && s.chat != nil && s.model != "" {
		llmTags, err := s.extractWithLLM(ctx, samples, tags)
		if err == nil && len(llmTags) > 0 {
			tags = append(tags, llmTags...)
		}
	}

	if len(tags) == 0 {
		return nil
	}

	// 事务内持久化并更新 sources.tags
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, tag := range tags {
			slug := tag.ID
			if slug == "" {
				slug = strings.ToLower(tag.Name)
			}

			// 插入或更新 labels 表
			labelSQL := `INSERT INTO labels(id, name, category, is_canonical, use_count, created_at)
				VALUES(?, ?, ?, false, 1, now())
				ON CONFLICT(id) DO UPDATE SET use_count = labels.use_count + 1, updated_at = now()
				RETURNING use_count`
			var useCount int
			_ = tx.Raw(labelSQL, slug, tag.Name, tag.Category).Scan(&useCount).Error

			// 候选标签晋升机制：若该标签已在 >= 3 个信源中使用，自动转正为标准标签
			if useCount >= 3 {
				_ = tx.Exec(`UPDATE labels SET is_canonical = true WHERE id = ?`, slug).Error
			}

			// 关联信源与标签
			slSQL := `INSERT INTO source_labels(source_id, label_id, confidence, origin, created_at)
				VALUES(?, ?, ?, ?, now())
				ON CONFLICT(source_id, label_id) DO UPDATE SET confidence = EXCLUDED.confidence`
			if err := tx.Exec(slSQL, sourceID, slug, tag.Confidence, tag.Origin).Error; err != nil {
				return err
			}
		}

		// 同步刷新 sources 表中的 tags 文本数组
		updateTagsSQL := `UPDATE sources SET tags = ARRAY(
			SELECT l.name FROM source_labels sl
			JOIN labels l ON l.id = sl.label_id
			WHERE sl.source_id = ?
			ORDER BY sl.confidence DESC
		) WHERE id = ?`
		return tx.Exec(updateTagsSQL, sourceID, sourceID).Error
	})
}

/** extractWithLLM invokes a lightweight model with tight guardrails to supplement tags. */
func (s *Service) extractWithLLM(ctx context.Context, samples []ArticleSample, existing []TagResult) ([]TagResult, error) {
	var titles strings.Builder
	for i, s := range samples {
		if i >= 6 {
			break
		}
		titles.WriteString(fmt.Sprintf("%d. %s\n", i+1, s.Title))
	}

	prompt := fmt.Sprintf(`你是严谨的科技知识图谱专家。根据以下信息源最新文章标题列表，为该信息源提炼 1 到 2 个最具代表性的专业技术标签。
规则约束：
1. 必须是 2 到 6 个字的专业技术专有名词（如“具身智能”、“端侧部署”）。
2. 严禁出现形容词、评价词、时效词（严禁“最新”、“分析”、“干货”、“推荐”）。
3. 严禁出现太宽泛的大词（严禁“科技”、“互联网”、“AI”）。
4. 格式必须是 JSON 字符串数组，例如：["标签A", "标签B"]

文章标题列表：
%s`, titles.String())

	out, err := s.chat(ctx, provider.Config{Model: s.model}, prompt)
	if err != nil {
		return nil, err
	}

	cleanText := strings.TrimSpace(out.Text)
	if idx := strings.Index(cleanText, "["); idx != -1 {
		cleanText = cleanText[idx:]
	}
	if idx := strings.LastIndex(cleanText, "]"); idx != -1 {
		cleanText = cleanText[:idx+1]
	}

	var rawTags []string
	if err := json.Unmarshal([]byte(cleanText), &rawTags); err != nil {
		return nil, errors.New("invalid LLM tags JSON")
	}

	var results []TagResult
	existingNames := make(map[string]bool)
	for _, ext := range existing {
		existingNames[strings.ToLower(ext.Name)] = true
	}

	for _, raw := range rawTags {
		clean, ok := CleanTag(raw)
		if !ok || existingNames[strings.ToLower(clean)] {
			continue
		}
		existingNames[strings.ToLower(clean)] = true
		slug := strings.ToLower(clean)
		results = append(results, TagResult{
			ID:         slug,
			Name:       clean,
			Category:   "concept",
			Confidence: 0.80,
			Origin:     "llm",
		})
	}
	return results, nil
}
