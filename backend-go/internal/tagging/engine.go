package tagging

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/topics"
)

/** TagResult contains an extracted label candidate with origin and confidence. */
type TagResult struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Category   string  `json:"category"`
	Confidence float32 `json:"confidence"`
	Origin     string  `json:"origin"`
}

/** ArticleSample represents minimal article data needed for labeling. */
type ArticleSample struct {
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
}

var blacklistedWords = map[string]struct{}{
	"最新": {}, "更新": {}, "发布": {}, "分析": {}, "推荐": {}, "深度": {},
	"干货": {}, "必看": {}, "整理": {}, "总结": {}, "观点": {}, "快讯": {},
	"科技": {}, "互联网": {}, "资讯": {}, "行业": {}, "动态": {}, "人工智能": {},
	"ai": {}, "article": {}, "news": {}, "daily": {}, "hot": {},
}

var nonAlphanumericZh = regexp.MustCompile(`[^\p{Han}a-zA-Z0-9_\-\.\s]`)

/** CleanTag sanitizes and validates an open candidate tag against guardrails. */
func CleanTag(raw string) (string, bool) {
	tag := strings.TrimSpace(raw)
	tag = nonAlphanumericZh.ReplaceAllString(tag, "")
	tag = strings.TrimSpace(tag)

	length := utf8.RuneCountInString(tag)
	if length < 2 || length > 12 {
		return "", false
	}

	lower := strings.ToLower(tag)
	if _, blocked := blacklistedWords[lower]; blocked {
		return "", false
	}

	return tag, true
}

/** Engine provides two-stage tagging over article samples. */
type Engine struct {
	entities    map[string]interests.Entity
	definitions []topics.Definition
}

/** NewEngine initializes tagging engine with loaded industry knowledge. */
func NewEngine(entities map[string]interests.Entity, definitions []topics.Definition) *Engine {
	return &Engine{
		entities:    entities,
		definitions: definitions,
	}
}

/** ExtractRules performs fast 0-token keyword & pattern extraction over article samples. */
func (e *Engine) ExtractRules(samples []ArticleSample) []TagResult {
	if len(samples) == 0 {
		return nil
	}

	var combined strings.Builder
	for _, s := range samples {
		combined.WriteString(s.Title)
		combined.WriteString(" ")
		combined.WriteString(s.Excerpt)
		combined.WriteString(" ")
	}
	content := combined.String()
	contentLower := strings.ToLower(content)

	results := make([]TagResult, 0, 4)
	seen := make(map[string]bool)

	// 匹配公司与重要实体
	for id, ent := range e.entities {
		matched := false
		if strings.Contains(contentLower, strings.ToLower(ent.Name)) {
			matched = true
		} else {
			for _, alias := range ent.Aliases {
				if len(alias) >= 2 && strings.Contains(contentLower, strings.ToLower(alias)) {
					matched = true
					break
				}
			}
		}

		if matched && !seen[id] {
			seen[id] = true
			displayName := ent.Name
			if ent.DisplayTag != nil && *ent.DisplayTag != "" {
				displayName = *ent.DisplayTag
			}
			results = append(results, TagResult{
				ID:         id,
				Name:       displayName,
				Category:   "company",
				Confidence: 0.95,
				Origin:     "rule",
			})
		}
	}

	// 匹配行业主题方向
	for _, def := range e.definitions {
		if seen[def.Slug] {
			continue
		}

		matched := false
		if strings.Contains(contentLower, strings.ToLower(def.Name)) {
			matched = true
		} else {
			for _, alias := range def.Aliases {
				if len(alias) >= 2 && strings.Contains(contentLower, strings.ToLower(alias)) {
					matched = true
					break
				}
			}
		}

		if matched && !seen[def.Slug] {
			seen[def.Slug] = true
			results = append(results, TagResult{
				ID:         def.Slug,
				Name:       def.Name,
				Category:   def.Group,
				Confidence: 0.90,
				Origin:     "rule",
			})
		}
	}

	// 限制返回数量最多 4 个高置信度标签
	if len(results) > 4 {
		return results[:4]
	}
	return results
}
