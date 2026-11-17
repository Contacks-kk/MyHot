package editorial

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"aihot/backend-go/internal/interests"
)

type patternSpec struct {
	Source string `json:"source"`
	Flags  string `json:"flags"`
}

type identitySpec struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Patterns []patternSpec `json:"patterns"`
}

type publisherSpec struct {
	EntityID string   `json:"entityId"`
	Domains  []string `json:"domains"`
}

type identityAliasSpec struct {
	EntityID string `json:"entityId"`
	Source   string `json:"source"`
	Flags    string `json:"flags"`
}

type Vocabulary struct {
	ItemTypes              []string            `json:"itemTypes"`
	CategoryTags           []string            `json:"categoryTags"`
	TopicTags              []string            `json:"topicTags"`
	EntityTags             []string            `json:"entityTags"`
	TagSynonyms            map[string]string   `json:"tagSynonyms"`
	IdentityLexicon        []identitySpec      `json:"identityLexicon"`
	PublisherDomains       []publisherSpec     `json:"publisherDomains"`
	IdentityContextAliases []identityAliasSpec `json:"identityContextAliases"`
}

type Structure struct {
	Scope    string   `json:"scope"`
	Category *string  `json:"category"`
	Tags     []string `json:"tags"`
	Subjects []string `json:"subjects"`
	Fact     *Fact    `json:"fact"`
}

type Fact struct {
	Title      string      `json:"title"`
	Subject    *string     `json:"subject,omitempty"`
	Action     *string     `json:"action,omitempty"`
	Object     *string     `json:"object,omitempty"`
	OccurredAt *string     `json:"occurredAt,omitempty"`
	Evidence   *string     `json:"evidence"`
	Conditions []Condition `json:"conditions"`
}

type Condition struct {
	Text  *string `json:"text,omitempty"`
	Quote string  `json:"quote"`
}

type NormalizedStructure struct {
	Category  *string
	Tags      []string
	Subjects  []string
	Scope     string
	Fact      *Fact
	Model     string `json:"-"`
	ReceiptID int64  `json:"-"`
	Reused    bool   `json:"-"`
}

func LoadVocabulary() (Vocabulary, error) {
	path := strings.TrimSpace(os.Getenv("INDUSTRY_VOCABULARY_FILE"))
	if path == "" {
		for _, candidate := range []string{filepath.Join("backend-go", "config", "vocabulary.json"), filepath.Join("config", "vocabulary.json"), filepath.Join("..", "..", "config", "vocabulary.json"), filepath.Join("/app", "backend-go", "config", "vocabulary.json")} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return Vocabulary{}, errors.New("backend-go/config/vocabulary.json not found")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Vocabulary{}, err
	}
	var vocabulary Vocabulary
	if err := json.Unmarshal(data, &vocabulary); err != nil {
		return Vocabulary{}, err
	}
	if len(vocabulary.CategoryTags) == 0 || len(vocabulary.ItemTypes) == 0 {
		return Vocabulary{}, errors.New("backend-go/config/vocabulary.json is incomplete")
	}
	for _, entry := range vocabulary.IdentityLexicon {
		for _, pattern := range entry.Patterns {
			if _, err := compilePattern(pattern); err != nil {
				return Vocabulary{}, fmt.Errorf("backend-go/config/vocabulary.json identity %s: %w", entry.ID, err)
			}
		}
	}
	for _, entry := range vocabulary.IdentityContextAliases {
		if _, err := compilePattern(patternSpec{Source: entry.Source, Flags: entry.Flags}); err != nil {
			return Vocabulary{}, fmt.Errorf("backend-go/config/vocabulary.json identity alias %s: %w", entry.EntityID, err)
		}
	}
	return vocabulary, nil
}

func (v Vocabulary) StructureSystem(pack *PromptPack, categories []interests.Category, entities []interests.EntityEntry) (string, error) {
	if pack == nil || len(categories) == 0 {
		return "", errors.New("editorial prompt inputs are incomplete")
	}
	categoryGuide := make([]string, 0, len(categories))
	for _, category := range categories {
		categoryGuide = append(categoryGuide, fmt.Sprintf("- %s（%s）：%s", category.Key, category.Label, category.Guide))
	}
	entityNames := make([]string, 0, len(entities))
	for _, entry := range entities {
		aliases := entry.Entity.Aliases
		if len(aliases) > 3 {
			aliases = aliases[:3]
		}
		entityNames = append(entityNames, fmt.Sprintf("%s（%s）", entry.ID, strings.Join(aliases, "/")))
	}
	count := strconvChinese(len(categories))
	return pack.Render("structure", map[string]string{
		"categoryCount": count,
		"categoryGuide": strings.Join(categoryGuide, "\n"),
		"categoryTags":  strings.Join(v.CategoryTags, "、"),
		"topicTags":     strings.Join(v.TopicTags, "、"),
		"entityTags":    strings.Join(v.EntityTags, "、"),
		"entities":      strings.Join(entityNames, "，"),
	})
}

func (v Vocabulary) NormalizeTags(values []string) []string {
	allowed := make(map[string]struct{}, len(v.CategoryTags)+len(v.TopicTags)+len(v.EntityTags))
	for _, tag := range append(append(append([]string{}, v.CategoryTags...), v.TopicTags...), v.EntityTags...) {
		allowed[tag] = struct{}{}
	}
	tags := make([]string, 0, 6)
	seen := make(map[string]struct{}, 6)
	for _, raw := range values {
		tag := strings.TrimPrefix(strings.TrimSpace(raw), "#")
		if normalized, ok := v.TagSynonyms[tag]; ok {
			tag = normalized
		} else if normalized, ok := v.TagSynonyms[strings.ToLower(tag)]; ok {
			tag = normalized
		}
		if tag == "" {
			continue
		}
		if _, ok := allowed[tag]; !ok {
			continue
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	categorySet := make(map[string]struct{}, len(v.CategoryTags))
	for _, tag := range v.CategoryTags {
		categorySet[tag] = struct{}{}
	}
	category := ""
	for _, tag := range tags {
		if _, ok := categorySet[tag]; ok {
			category = tag
			break
		}
	}
	if category == "" {
		category = v.CategoryTags[len(v.CategoryTags)-1]
	}
	result := []string{category}
	for _, tag := range tags {
		if tag != category && len(result) < 6 {
			result = append(result, tag)
		}
	}
	return result
}

func (v Vocabulary) NormalizeStructure(data Structure, input Input, categories []interests.Category, entities map[string]interests.Entity) NormalizedStructure {
	category := data.Category
	knownCategories := make(map[string]struct{}, len(categories))
	for _, item := range categories {
		knownCategories[item.Key] = struct{}{}
	}
	if category != nil {
		if _, ok := knownCategories[*category]; !ok {
			category = nil
		}
	}
	scope := data.Scope
	if scope != "single" && scope != "composite" && scope != "unknown" {
		scope = "unknown"
	}
	originals := make([]string, 0, 2)
	if input.XPost != nil {
		originals = append(originals, collapseWhitespace(firstNonEmpty(input.XPost.Text, input.Title)))
		if input.XPost.Quoted != nil {
			originals = append(originals, collapseWhitespace(input.XPost.Quoted.Text))
		}
	} else {
		originals = append(originals, collapseWhitespace(nullish(input.BodyText, input.Excerpt)))
	}
	hasOriginal := false
	for _, original := range originals {
		if original != "" {
			hasOriginal = true
			break
		}
	}
	if !hasOriginal {
		scope = "unknown"
	}
	var fact *Fact
	visible := BuildMaterial(input)
	if hasOriginal && scope != "composite" && data.Fact != nil {
		copy := *data.Fact
		copy.Evidence = groundedQuote(data.Fact.Evidence, visible, originals)
		copy.Conditions = make([]Condition, 0, len(data.Fact.Conditions))
		for _, condition := range data.Fact.Conditions {
			quote := groundedQuote(stringPointer(condition.Quote), visible, originals)
			if quote == nil {
				continue
			}
			text := *quote
			if condition.Text != nil {
				text = *condition.Text
			}
			copy.Conditions = append(copy.Conditions, Condition{Text: stringPointer(text), Quote: *quote})
			if len(copy.Conditions) == 4 {
				break
			}
		}
		fact = &copy
	}
	subjects := make([]string, 0, len(data.Subjects))
	seen := make(map[string]struct{}, len(data.Subjects))
	for _, subject := range data.Subjects {
		subject = strings.ToLower(strings.TrimSpace(subject))
		if _, ok := entities[subject]; !ok {
			continue
		}
		if _, ok := seen[subject]; ok {
			continue
		}
		seen[subject] = struct{}{}
		subjects = append(subjects, subject)
	}
	return NormalizedStructure{Category: category, Tags: v.NormalizeTags(data.Tags), Subjects: subjects, Scope: scope, Fact: fact}
}

func groundedQuote(quote *string, visible string, originals []string) *string {
	if quote == nil {
		return nil
	}
	text := collapseWhitespace(*quote)
	if text == "" || !strings.Contains(visible, text) {
		return nil
	}
	for _, original := range originals {
		if strings.Contains(original, text) {
			return &text
		}
	}
	return nil
}

func compilePattern(spec patternSpec) (*regexp.Regexp, error) {
	pattern := spec.Source
	if strings.Contains(spec.Flags, "i") {
		pattern = "(?i)" + pattern
	}
	return regexp.Compile(pattern)
}

func strconvChinese(value int) string {
	numbers := []string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九", "十", "十一", "十二"}
	if value >= 0 && value < len(numbers) {
		return numbers[value]
	}
	return fmt.Sprint(value)
}

func stringPointer(value string) *string { return &value }
