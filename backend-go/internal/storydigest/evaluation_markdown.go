package storydigest

import (
	"fmt"
	"sort"
	"strings"
)

/** EvaluationMarkdown formats the stored comparison for human review. */
func EvaluationMarkdown(report EvaluationComparisonReport) string {
	lines := []string{
		"# 事件综述提示词对比",
		"",
		fmt.Sprintf("- 生成时间：%s", report.Meta.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")),
		fmt.Sprintf("- 案例：%s", report.Meta.Cases),
		fmt.Sprintf("- 线上提示词：%s", report.Meta.Prompts["live"].Version),
	}
	if candidate, ok := report.Meta.Prompts["candidate"]; ok {
		lines = append(lines, fmt.Sprintf("- 候选提示词：%s（%s）", candidate.File, candidate.Version))
	}
	lines = append(lines, fmt.Sprintf("- 模型调用：%d 次", report.Meta.Calls))
	models := make([]string, 0, len(report.Models))
	for model := range report.Models {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		summary := report.Models[model].Summary
		lines = append(lines, evaluationSummaryLine(model, "线上提示词", summary.Live))
		if summary.Candidate != nil {
			lines = append(lines, evaluationSummaryLine(model, "候选提示词", *summary.Candidate))
		}
	}
	if len(models) == 0 {
		return strings.Join(lines, "\n") + "\n"
	}
	for index, item := range report.Models[models[0]].Cases {
		lines = append(lines, "", fmt.Sprintf("## %d. %s", index+1, item.StoryTitle), "", fmt.Sprintf("案例 `%s` · 报道 %d 篇 · %s", item.CaseID, item.Reports, item.Input))
		for _, model := range models {
			cases := report.Models[model].Cases
			if index >= len(cases) {
				continue
			}
			lines = appendEvaluationMarkdownCall(lines, model, "线上提示词", cases[index].Live)
			if cases[index].Candidate != nil {
				lines = appendEvaluationMarkdownCall(lines, model, "候选提示词", *cases[index].Candidate)
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

/** evaluationSummaryLine formats one prompt's outcome and token totals. */
func evaluationSummaryLine(model, label string, summary EvaluationPromptSummary) string {
	return fmt.Sprintf("- %s · %s：成功 %d/%d，输入 %d tokens，输出 %d tokens，%d 次复用已收到的结果", model, label, summary.Succeeded, summary.Calls, summary.TokensIn, summary.TokensOut, summary.Reused)
}

/** appendEvaluationMarkdownCall adds a single answer, error and receipt usage. */
func appendEvaluationMarkdownCall(lines []string, model, label string, call EvaluationReportCall) []string {
	lines = append(lines, "", fmt.Sprintf("### %s · %s", label, model), "")
	if call.Error != nil || call.Digest == nil {
		message := "null"
		if call.Error != nil {
			message = *call.Error
		}
		lines = append(lines, "生成失败："+message)
	} else {
		title := "（沿用事件标题）"
		if call.Title != nil && *call.Title != "" {
			title = *call.Title
		}
		lines = append(lines, "**"+title+"**", "")
		for _, line := range strings.Split(*call.Digest, "\n") {
			if line == "" {
				lines = append(lines, ">")
			} else {
				lines = append(lines, "> "+line+"  ")
			}
		}
	}
	receipt := "无回执"
	if call.ReceiptID != nil {
		receipt = fmt.Sprintf("回执 #%d", *call.ReceiptID)
	}
	usage := fmt.Sprintf("%s · 输入 %d tokens · 输出 %d tokens", receipt, call.TokensIn, call.TokensOut)
	if call.Reused {
		usage += " · 复用已收到的结果"
	}
	return append(lines, "", usage)
}
