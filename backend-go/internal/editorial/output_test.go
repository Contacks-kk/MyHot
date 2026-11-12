package editorial

import (
	"strings"
	"testing"
)

func TestParseModelOutputsMatchSchemaDefaultsAndBounds(t *testing.T) {
	prefilter, err := ParsePrefilter("```json\n{\"label\":\" pass \",\"reason\":\"材料明确涉及 AI\"}\n```")
	if err != nil || prefilter.Label != "PASS" || prefilter.Reason != "材料明确涉及 AI" {
		t.Fatalf("ParsePrefilter() = %#v, %v", prefilter, err)
	}
	if _, err := ParsePrefilter(`{"label":"MAYBE"}`); err == nil {
		t.Fatal("invalid prefilter label must fail")
	}
	if score, err := ParseScore("模型输出：{\"attentionScore\":61}"); err != nil || score != 61 {
		t.Fatalf("ParseScore() should extract an object from surrounding text: %d, %v", score, err)
	}
	tooLongReason := strings.Repeat("理", 201)
	prefilter, err = ParsePrefilter(`{"label":"UNKNOWN","reason":"` + tooLongReason + `"}`)
	if err != nil || prefilter.Reason != "" {
		t.Fatalf("overlong reason must use schema fallback: %#v, %v", prefilter, err)
	}
	for raw, expected := range map[string]int{`{"attentionScore":76}`: 76, `{"attentionScore":"51"}`: 51, `{"attentionScore":true}`: 1} {
		got, err := ParseScore(raw)
		if err != nil || got != expected {
			t.Errorf("ParseScore(%s) = %d, %v", raw, got, err)
		}
	}
	for _, raw := range []string{`{"attentionScore":101}`, `{"attentionScore":1.2}`, `{}`} {
		if _, err := ParseScore(raw); err == nil {
			t.Errorf("ParseScore(%s) should fail", raw)
		}
	}

	structure, err := ParseStructure(`{"scope":"bad","category":"missing","tags":["x"],"subjects":["openai"],"fact":{"title":"动作","evidence":"  原文  ","conditions":[{"quote":"  免费  ","text":" 条件 "},null]}}`, []string{"ai-models"})
	if err != nil || structure.Scope != "unknown" || structure.Category != nil || structure.Fact == nil {
		t.Fatalf("ParseStructure() = %#v, %v", structure, err)
	}
	if structure.Fact.Evidence == nil || *structure.Fact.Evidence != "原文" || len(structure.Fact.Conditions) != 1 || structure.Fact.Conditions[0].Quote != "免费" {
		t.Fatalf("fact schema normalization = %#v", structure.Fact)
	}

	vocabulary := Vocabulary{ItemTypes: []string{"industry_event"}}
	understand, err := ParseUnderstand(`{"itemType":"industry_event","titleZh":" 标题 ","summaryZh":" 摘要 ","authorRole":"bad","editorialJudgment":"判断","tags":["行业动态"]}`, vocabulary)
	if err != nil || understand.AuthorRole != "relayer" || understand.TitleZh != "标题" || understand.SummaryZh != "摘要" {
		t.Fatalf("ParseUnderstand() = %#v, %v", understand, err)
	}
}

func TestParseTranslateOutputReadsLabeledMultilineAndFallbackText(t *testing.T) {
	parsed := ParseTranslateOutput("title_zh: 中文标题\r\nsummary_zh：第一句。\r\n第二句。\r\n\r\n来源：测试信源\r\n【时间锚点】原文发布日期：昨天")
	if parsed.TitleZh != "中文标题" || parsed.SummaryZh != "第一句。\n第二句。" {
		t.Fatalf("labeled output = %#v", parsed)
	}
	body := ParseTranslateOutput("title_zh: 标题\nbody_zh: 第一段\n第二段\n来源：测试")
	if body.TitleZh != "标题" || body.BodyZh != "第一段\n第二段" {
		t.Fatalf("multiline body output = %#v", body)
	}
	fallback := ParseTranslateOutput("这是中文标题\n这是摘要正文。\n来源：测试")
	if fallback.TitleZh != "这是中文标题" || fallback.SummaryZh != "这是摘要正文。" {
		t.Fatalf("unlabeled fallback = %#v", fallback)
	}
}
