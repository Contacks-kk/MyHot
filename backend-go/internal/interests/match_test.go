package interests

import "testing"

func TestMatchUsesKeywordOrSelectedEntity(t *testing.T) {
	entities := map[string]Entity{"openai": {Name: "OpenAI", Aliases: []string{"ChatGPT"}}}
	if got := Match("New model released", "", []string{"robotics"}, []string{"openai"}, entities); len(got) != 0 {
		t.Fatalf("unexpected match: %v", got)
	}
	if got := Match("New model released", "The team announced a new system", []string{"robotics"}, nil, entities); len(got) != 0 {
		t.Fatalf("unrelated text matched: %v", got)
	}
	if got := Match("OpenAI launched a model", "", []string{"robotics"}, []string{"openai"}, entities); len(got) != 1 || got[0] != "实体：OpenAI" {
		t.Fatalf("unexpected entity match: %v", got)
	}
	if got := Match("New robotics study", "", []string{"robotics"}, nil, entities); len(got) != 1 {
		t.Fatalf("keyword did not match: %v", got)
	}
}
