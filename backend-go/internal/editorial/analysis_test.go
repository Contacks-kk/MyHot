package editorial

import (
	"reflect"
	"testing"

	"aihot/backend-go/internal/interests"
)

func TestNormalizeAnalysisKeepsSelectionAndDisplayRules(t *testing.T) {
	category := "ai-models"
	structure := &NormalizedStructure{Category: &category, Tags: []string{"模型发布", "Agent"}, Subjects: []string{"openai", "anthropic"}, Scope: "single"}
	firstParty := "OpenAI"
	result := NormalizeAnalysis(AnalysisRun{
		Prefilter: PrefilterResult{Label: "PASS"},
		Scores:    &ScoreResult{Model: "glm-score", Threshold: 60, Values: []int{61, 60}},
		Writing:   &WritingResult{TitleZh: "  OpenAI 发布 GPT-6 \n", SummaryZh: " 摘要 ", ReasonZh: &firstParty},
		Structure: structure,
	}, map[string]interests.Entity{
		"openai":    {DisplayTag: stringPointer("OpenAI")},
		"anthropic": {DisplayTag: stringPointer("Anthropic")},
	})
	if result.Relevance != "pass" || !result.Selected || result.Score == nil || *result.Score != 60 || result.Threshold == nil || *result.Threshold != 60 {
		t.Fatalf("selection normalization = %#v", result)
	}
	if result.TitleZh != "OpenAI 发布 GPT-6" || result.SummaryZh != "摘要" || !reflect.DeepEqual(result.Tags, []string{"模型发布", "Agent", "OpenAI", "Anthropic"}) {
		t.Fatalf("display normalization = %#v", result)
	}
	refused := NormalizeAnalysis(AnalysisRun{Prefilter: PrefilterResult{Label: "PASS"}, Scores: &ScoreResult{Threshold: 60, Refused: true}, Writing: &WritingResult{TitleZh: "标题"}}, nil)
	if refused.Relevance != "unknown" || refused.Selected || refused.Score != nil || !refused.ScoreRefused || refused.Scores != nil {
		t.Fatalf("refused and incomplete judgement = %#v", refused)
	}
	blocked := NormalizeAnalysis(AnalysisRun{Prefilter: PrefilterResult{Label: "BLOCK"}}, nil)
	if blocked.Relevance != "block" || blocked.Selected {
		t.Fatalf("blocked judgement = %#v", blocked)
	}
}
