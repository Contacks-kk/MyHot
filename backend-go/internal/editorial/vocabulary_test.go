package editorial

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"aihot/backend-go/internal/interests"
)

func TestIndustryVocabularyAndStructurePromptLoadFromSharedPack(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	industry := filepath.Join(repositoryRoot, "shared", "industry")
	t.Setenv("INDUSTRY_VOCABULARY_FILE", filepath.Join(industry, "vocabulary.json"))
	t.Setenv("INDUSTRY_CATEGORIES_FILE", filepath.Join(industry, "categories.json"))
	t.Setenv("INDUSTRY_ENTITIES_FILE", filepath.Join(industry, "entities.json"))
	vocabulary, err := LoadVocabulary()
	if err != nil {
		t.Fatal(err)
	}
	categories, err := interests.LoadCategories()
	if err != nil {
		t.Fatal(err)
	}
	entities, err := interests.LoadEntityEntries()
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := LoadPromptsFrom(filepath.Join(industry, "prompts"), "MyHOT")
	if err != nil {
		t.Fatal(err)
	}
	system, err := vocabulary.StructureSystem(prompts, categories, entities)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"你是 MyHOT", "六选一", "ai-models（模型）", "产品更新", "openai（OpenAI/ChatGPT/Sora）"} {
		if !strings.Contains(system, expected) {
			t.Errorf("structure system is missing %q", expected)
		}
	}
	if strings.Contains(system, "{{") {
		t.Fatal("structure prompt contains an unexpanded template token")
	}
	if len(vocabulary.IdentityLexicon) < 20 {
		t.Fatalf("identity lexicon has only %d entries", len(vocabulary.IdentityLexicon))
	}
}

func TestNormalizeStructureRequiresGroundedQuotesAndKnownEntities(t *testing.T) {
	vocabulary := Vocabulary{
		CategoryTags: []string{"模型发布", "行业动态", "其他"},
		TopicTags:    []string{"Agent"},
		EntityTags:   []string{"OpenAI"},
		TagSynonyms:  map[string]string{"发布": "模型发布", "行业": "行业动态"},
	}
	body := "OpenAI 发布 GPT-6。该功能目前向企业客户开放。"
	input := Input{Title: "OpenAI 发布 GPT-6", URL: "https://openai.com/blog", BodyText: &body,
		Source: Source{Name: "OpenAI", Kind: "rss", Tier: "T1"}}
	category := "not-a-category"
	data := Structure{
		Scope: "single", Category: &category,
		Tags:     []string{"发布", "#Agent", "陌生标签", "模型发布"},
		Subjects: []string{" OpenAI ", "not-in-catalog", "openai"},
		Fact: &Fact{Title: "发布 GPT-6", Evidence: stringPointer("OpenAI 发布 GPT-6。"), Conditions: []Condition{
			{Quote: "该功能目前向企业客户开放。"}, {Quote: "企业客户开放"}, {Quote: "模型免费提供"},
		}},
	}
	result := vocabulary.NormalizeStructure(data, input, []interests.Category{{Key: "ai-models"}}, map[string]interests.Entity{"openai": {Name: "OpenAI"}})
	if result.Category != nil {
		t.Fatal("unknown category must be discarded")
	}
	if strings.Join(result.Tags, ",") != "模型发布,Agent" {
		t.Fatalf("normalized tags = %#v", result.Tags)
	}
	if strings.Join(result.Subjects, ",") != "openai" {
		t.Fatalf("normalized subjects = %#v", result.Subjects)
	}
	if result.Fact == nil || result.Fact.Evidence == nil || *result.Fact.Evidence != "OpenAI 发布 GPT-6。" {
		t.Fatalf("normalized fact evidence = %#v", result.Fact)
	}
	if len(result.Fact.Conditions) != 2 {
		t.Fatalf("grounded conditions = %#v", result.Fact.Conditions)
	}
	composite := data
	composite.Scope = "composite"
	if got := vocabulary.NormalizeStructure(composite, input, []interests.Category{{Key: "ai-models"}}, map[string]interests.Entity{"openai": {Name: "OpenAI"}}); got.Fact != nil {
		t.Fatalf("composite scope must have no fact, got %#v", got.Fact)
	}
}

func TestIdentityGuardUsesIndustryLexiconAndPublisherContext(t *testing.T) {
	vocabulary := loadCurrentVocabulary(t)
	owner := "openai"
	input := WritingInput{
		Title: "OpenAI 发布 GPT 5.5", Text: "GPT 5.5 开放测试", SourceKind: "rss",
		SourceName: "OpenAI 官方博客", DocumentURL: "https://blog.openai.com/news", SourceOwnerEntity: &owner,
	}
	allowed, publisher, sourceOwner := vocabulary.IdentityContext(input)
	if publisher != "openai" || sourceOwner != "openai" || strings.Join(allowed, ",") != "openai" {
		t.Fatalf("identity context = %#v, publisher=%q owner=%q", allowed, publisher, sourceOwner)
	}
	copy := vocabulary.EnforceIdentity(input, "OpenAI 发布 GPT-5.5", "GPT-5.5 正式开放测试。")
	if copy.IdentityGuard.Outcome != "pass" || copy.TitleZh == "" || copy.SummaryZh == "" {
		t.Fatalf("supported copy was rejected: %#v", copy)
	}
	unsupported := vocabulary.EnforceIdentity(input, "Anthropic 发布 Claude 5", "Meta 同步推出新模型。")
	if unsupported.IdentityGuard.Outcome != "fallback" || unsupported.TitleZh != input.Title || unsupported.SummaryZh != "" {
		t.Fatalf("unsupported company names were not guarded: %#v", unsupported)
	}
	if strings.Join(unsupported.IdentityGuard.UnsupportedTitleEntityIDs, ",") != "anthropic" || strings.Join(unsupported.IdentityGuard.UnsupportedSummaryIDs, ",") != "meta" {
		t.Fatalf("unsupported entity ids = %#v", unsupported.IdentityGuard)
	}
}

func loadCurrentVocabulary(t *testing.T) Vocabulary {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	t.Setenv("INDUSTRY_VOCABULARY_FILE", filepath.Join(repositoryRoot, "shared", "industry", "vocabulary.json"))
	vocabulary, err := LoadVocabulary()
	if err != nil {
		t.Fatal(err)
	}
	return vocabulary
}
