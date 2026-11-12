package grouping

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/modelcall"
)

type JudgeModelClient interface {
	Call(context.Context, modelcall.Request) (modelcall.Result, error)
	Complete(context.Context, int64) error
	Reject(context.Context, int64, string) error
}

type BatchJudgement struct {
	Verdicts  map[int64]Verdict
	Selection SelectionValue
	ReceiptID int64
}

type PairJudgement struct {
	Relation   Relation
	Difference string
	Confidence float64
	ReceiptID  int64
}

type SignalJudgement struct {
	Verdicts  map[int64]Verdict
	ReceiptID int64
}

type JudgeService struct {
	models  JudgeModelClient
	prompts *editorial.PromptPack
}

func NewJudgeService(models JudgeModelClient, prompts *editorial.PromptPack) *JudgeService {
	return &JudgeService{models: models, prompts: prompts}
}

func (s *JudgeService) JudgeBatch(ctx context.Context, articleID string, query Report, candidates []Candidate, reading []ReadingContext) (BatchJudgement, error) {
	if err := validateJudgeService(s); err != nil {
		return BatchJudgement{}, err
	}
	version, err := s.prompts.Version("group-batch")
	if err != nil {
		return BatchJudgement{}, err
	}
	system, err := s.prompts.Render("group-batch", nil)
	if err != nil {
		return BatchJudgement{}, err
	}
	result, err := s.models.Call(ctx, modelcall.Request{Capability: "group", Purpose: "group_article", Subject: "article:" + articleID,
		PromptVersion: version, System: system, User: batchPrompt(query, candidates, "新报道", reading), Temperature: floatPointer(0),
		MaxTokens: 400 + 120*len(candidates)})
	if err != nil {
		return BatchJudgement{}, err
	}
	parsed, err := ParseBatchResult([]byte(result.Completion.Text), len(candidates))
	if err != nil {
		return BatchJudgement{}, s.rejectResult(ctx, result.ReceiptID, err)
	}
	verdicts, err := VerdictsByFact(parsed.Decisions, candidates)
	if err != nil {
		return BatchJudgement{}, s.rejectResult(ctx, result.ReceiptID, err)
	}
	selection := parsed.Selection
	if !hasSelectedCoverage(candidates, reading) {
		selection = SelectionValue{AddsValue: true, Reason: "没有已公开精选的相关报道"}
	}
	return BatchJudgement{Verdicts: verdicts, Selection: selection, ReceiptID: result.ReceiptID}, nil
}

func (s *JudgeService) ConfirmMerge(ctx context.Context, articleID string, query Report, candidate Candidate) (PairJudgement, error) {
	if err := validateJudgeService(s); err != nil {
		return PairJudgement{}, err
	}
	version, err := s.prompts.Version("group-pair", "group-signal")
	if err != nil {
		return PairJudgement{}, err
	}
	system, err := s.prompts.Render("group-pair", nil)
	if err != nil {
		return PairJudgement{}, err
	}
	result, err := s.models.Call(ctx, modelcall.Request{Capability: "groupReview", Purpose: "group_review",
		Subject: fmt.Sprintf("article:%s:fact:%d", articleID, candidate.FactID), PromptVersion: version,
		System: system, User: pairPrompt(query, candidate.Report), Temperature: floatPointer(0), MaxTokens: 400})
	if err != nil {
		return PairJudgement{}, err
	}
	parsed, err := ParsePairResult([]byte(result.Completion.Text))
	if err != nil {
		return PairJudgement{}, s.rejectResult(ctx, result.ReceiptID, err)
	}
	return PairJudgement{Relation: parsed.Relation, Difference: parsed.Difference, Confidence: parsed.Confidence, ReceiptID: result.ReceiptID}, nil
}

func (s *JudgeService) JudgeSignal(ctx context.Context, articleID string, query Report, candidates []Candidate) (SignalJudgement, error) {
	if err := validateJudgeService(s); err != nil {
		return SignalJudgement{}, err
	}
	version, err := s.prompts.Version("group-pair", "group-signal")
	if err != nil {
		return SignalJudgement{}, err
	}
	system, err := s.prompts.Render("group-signal", nil)
	if err != nil {
		return SignalJudgement{}, err
	}
	result, err := s.models.Call(ctx, modelcall.Request{Capability: "group", Purpose: "group_signal", Subject: "article:" + articleID,
		PromptVersion: version, System: system, User: batchPrompt(query, candidates, "帖子", nil), Temperature: floatPointer(0),
		MaxTokens: 150 + 60*len(candidates)})
	if err != nil {
		return SignalJudgement{}, err
	}
	parsed, err := ParseSignalResult([]byte(result.Completion.Text), len(candidates))
	if err != nil {
		return SignalJudgement{}, s.rejectResult(ctx, result.ReceiptID, err)
	}
	verdicts, err := VerdictsByFact(parsed.Decisions, candidates)
	if err != nil {
		return SignalJudgement{}, s.rejectResult(ctx, result.ReceiptID, err)
	}
	return SignalJudgement{Verdicts: verdicts, ReceiptID: result.ReceiptID}, nil
}

func (s *JudgeService) CompleteReceipt(ctx context.Context, receiptID int64) error {
	if err := validateJudgeService(s); err != nil {
		return err
	}
	return s.models.Complete(ctx, receiptID)
}

func (s *JudgeService) rejectResult(ctx context.Context, receiptID int64, cause error) error {
	rejectErr := s.models.Reject(ctx, receiptID, "unusable grouping judgement: "+cause.Error())
	if rejectErr != nil {
		return rejectErr
	}
	return &JudgementOutputError{ReceiptID: receiptID, Cause: cause}
}

type JudgementOutputError struct {
	ReceiptID int64
	Cause     error
}

func (e *JudgementOutputError) Error() string {
	return "grouping model response is unusable: " + e.Cause.Error()
}
func (e *JudgementOutputError) Unwrap() error { return e.Cause }

func hasSelectedCoverage(candidates []Candidate, reading []ReadingContext) bool {
	if len(reading) > 0 {
		return true
	}
	for _, candidate := range candidates {
		if candidate.Selected {
			return true
		}
	}
	return false
}

func batchPrompt(query Report, candidates []Candidate, queryLabel string, reading []ReadingContext) string {
	list := make([]string, 0, len(candidates))
	for index, candidate := range candidates {
		parts := []string{describeReport(candidate.Report, fmt.Sprintf("候选 C%d", index+1),
			fmt.Sprintf("（该事实已有 %d 篇报道；事实标题：%s）", candidate.Members, candidate.FactTitle), 360)}
		if queryLabel == "新报道" {
			if candidate.Selected && candidate.SelectedReport != nil {
				parts = append(parts, describeReport(*candidate.SelectedReport, "已公开精选", "（增量只与这篇已展示的内容比较）", 360))
			} else {
				parts = append(parts, "【尚无公开精选】该候选只用于身份判断。")
			}
		}
		list = append(list, strings.Join(parts, "\n"))
	}
	background := make([]string, 0, len(reading))
	for index, item := range reading {
		parts := []string{describeReport(item.Report, fmt.Sprintf("已公开精选阅读背景 R%d", index+1), "（只比较信息覆盖，不是事实候选）", 2000)}
		if item.SourceText != nil && *item.SourceText != "" {
			parts = append(parts, "【该已选报道的原文证据】\n"+truncateUTF16(*item.SourceText, 6000)+"\n【原文证据结束】")
		}
		background = append(background, strings.Join(parts, "\n"))
	}
	backgroundText := strings.Join(background, "\n\n")
	prompt := describeReport(query, queryLabel, "", 360)
	if len(list) > 0 {
		prompt += "\n\n" + strings.Join(list, "\n\n")
	}
	if backgroundText != "" {
		prompt += "\n\n" + backgroundText
	}
	prompt += "\n\n" + queryLabel + "与每个候选的关系是什么？"
	if backgroundText != "" {
		prompt += "同时比较所有已公开精选内容，判断新增价值；无事实候选时 decisions=[]。"
	}
	return prompt
}

func pairPrompt(a, b Report) string {
	return describeReport(a, "报道 A", "", 360) + "\n\n" + describeReport(b, "报道 B", "", 360) + "\n\n这两篇报道是什么关系？"
}

func describeReport(report Report, label, extra string, summaryLimit int) string {
	when := "未知"
	if report.At != nil {
		beijing := report.At.UTC().Add(8 * time.Hour)
		when = beijing.Format("2006-01-02 15:04")
	}
	firstParty := ""
	if report.FirstParty {
		firstParty = "（当事方/官方）"
	}
	parts := []string{
		"【" + label + "】" + extra,
		"标题：" + report.Title,
		"来源：" + report.Source + firstParty + "｜发布时间：" + when,
		"摘要：" + fallback(truncateUTF16(stringValue(report.Summary), summaryLimit), "（无）"),
	}
	if report.Scope != "" && report.Scope != "unknown" {
		scope := "单一具体发生或议题"
		if report.Scope == "composite" {
			scope = "多个独立发生的综合稿"
		}
		parts = append(parts, "资料范围："+scope)
	}
	if frame := report.Frame; frame != nil && (stringValue(frame.Subject) != "" || stringValue(frame.Action) != "" || stringValue(frame.Object) != "") {
		parts = append(parts, fmt.Sprintf("事实要素：主体=%s；动作=%s；对象=%s；日期=%s",
			fallback(stringValue(frame.Subject), "?"), fallback(stringValue(frame.Action), "?"), fallback(stringValue(frame.Object), "?"), fallback(stringValue(frame.OccurredAt), "未知")))
	}
	if report.SourceText != nil && *report.SourceText != "" {
		parts = append(parts, "【原文证据】\n"+truncateUTF16(*report.SourceText, 6000)+"\n【原文证据结束】")
	}
	return strings.Join(parts, "\n")
}

func fallback(value, empty string) string {
	if value == "" {
		return empty
	}
	return value
}

func floatPointer(value float64) *float64 { return &value }

func validateJudgeService(s *JudgeService) error {
	if s == nil || s.models == nil || s.prompts == nil {
		return errors.New("grouping judge is unavailable")
	}
	return nil
}
