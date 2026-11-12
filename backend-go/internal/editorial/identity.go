package editorial

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type WritingInput struct {
	Title             string
	Text              string
	SourceKind        string
	SourceName        string
	DocumentURL       string
	SourceOwnerEntity *string
	MainText          string
	QuotedText        string
	QuotedAuthor      string
	PublishedAt       *time.Time
}

type IdentityGuard struct {
	Outcome                   string   `json:"outcome"`
	UnsupportedTitleEntityIDs []string `json:"unsupportedTitleEntityIds"`
	UnsupportedSummaryIDs     []string `json:"unsupportedSummaryEntityIds"`
}

type FinalCopy struct {
	TitleZh       string
	SummaryZh     string
	IdentityGuard IdentityGuard
}

func writingInputOf(input Input) WritingInput {
	isX := input.Source.Kind == "x_search" || input.XPost != nil
	mainText := ""
	if isX {
		mainText = firstNonEmpty(pointerValue(input.XPost, func(post *XPost) string { return post.Text }), input.Title)
	}
	text := nullish(input.BodyText, input.Excerpt)
	if isX {
		text = mainText
	}
	quotedText, quotedAuthor := "", ""
	if input.XPost != nil && input.XPost.Quoted != nil {
		quotedText = input.XPost.Quoted.Text
		quotedAuthor = input.XPost.Quoted.Handle
	}
	kind := input.Source.Kind
	if isX {
		kind = "x_search"
	}
	return WritingInput{
		Title: input.Title, Text: text, SourceKind: kind, SourceName: input.Source.Name,
		DocumentURL: input.URL, SourceOwnerEntity: input.Source.OwnerEntityID,
		MainText: mainText, QuotedText: quotedText, QuotedAuthor: quotedAuthor, PublishedAt: input.PublishedAt,
	}
}

func (v Vocabulary) MatchEntityIDs(texts []string) []string {
	ids := make([]string, 0, len(v.IdentityLexicon))
	for _, entry := range v.IdentityLexicon {
		for _, spec := range entry.Patterns {
			pattern, err := compilePattern(spec)
			if err != nil {
				continue
			}
			matched := false
			for _, text := range texts {
				if text != "" && pattern.MatchString(text) {
					matched = true
					break
				}
			}
			if matched {
				ids = append(ids, entry.ID)
				break
			}
		}
	}
	return ids
}

func (v Vocabulary) IdentityContext(input WritingInput) (allowed []string, publisher, owner string) {
	publisher = v.publisherEntity(input.DocumentURL)
	if input.SourceOwnerEntity != nil && v.entityName(*input.SourceOwnerEntity) != "" {
		owner = *input.SourceOwnerEntity
	}
	texts := []string{input.Title, input.Text, input.MainText, input.QuotedText, input.SourceName}
	variants := append([]string{}, texts...)
	spaceVariant := regexp.MustCompile(`(?i)\b(gpt|glm)\s+([o\d])`)
	for _, text := range texts {
		variants = append(variants, spaceVariant.ReplaceAllString(text, "$1-$2"))
	}
	allowedSet := make(map[string]struct{})
	for _, id := range v.MatchEntityIDs(variants) {
		allowedSet[id] = struct{}{}
	}
	joined := strings.Join(nonEmpty(texts), "\n")
	for _, alias := range v.IdentityContextAliases {
		pattern, err := compilePattern(patternSpec{Source: alias.Source, Flags: alias.Flags})
		if err == nil && pattern.MatchString(joined) {
			allowedSet[alias.EntityID] = struct{}{}
		}
	}
	if publisher != "" {
		allowedSet[publisher] = struct{}{}
	}
	if owner != "" {
		allowedSet[owner] = struct{}{}
	}
	allowed = make([]string, 0, len(allowedSet))
	for id := range allowedSet {
		allowed = append(allowed, id)
	}
	sort.Strings(allowed)
	return allowed, publisher, owner
}

func (v Vocabulary) IdentityPrompt(pack *PromptPack, input WritingInput) (string, error) {
	_, publisher, owner := v.IdentityContext(input)
	facts := make([]string, 0, 2)
	if publisher != "" {
		facts = append(facts, "文档发布域主体="+v.entityNameOrID(publisher))
	}
	if owner != "" {
		facts = append(facts, "来源账号主体="+v.entityNameOrID(owner))
	}
	factText := "未识别到明确发布主体"
	if len(facts) > 0 {
		factText = strings.Join(facts, "；")
	}
	return pack.Render("identity-context", map[string]string{"facts": factText})
}

func (v Vocabulary) EnforceIdentity(input WritingInput, titleZh, summaryZh string) FinalCopy {
	allowedValues, _, _ := v.IdentityContext(input)
	allowed := make(map[string]struct{}, len(allowedValues))
	for _, id := range allowedValues {
		allowed[id] = struct{}{}
	}
	titleIDs := v.MatchEntityIDs([]string{titleZh})
	summaryIDs := v.MatchEntityIDs([]string{summaryZh})
	unsupportedTitle := unsupportedIDs(titleIDs, allowed)
	unsupportedSummary := unsupportedIDs(summaryIDs, allowed)
	if len(unsupportedTitle) > 0 {
		if looksChinese(input.Title) {
			titleZh = input.Title
		} else {
			titleZh = ""
		}
	}
	if len(unsupportedSummary) > 0 {
		summaryZh = ""
	}
	outcome := "pass"
	if len(unsupportedTitle)+len(unsupportedSummary) > 0 {
		outcome = "fallback"
	}
	return FinalCopy{TitleZh: titleZh, SummaryZh: summaryZh, IdentityGuard: IdentityGuard{
		Outcome: outcome, UnsupportedTitleEntityIDs: unsupportedTitle, UnsupportedSummaryIDs: unsupportedSummary,
	}}
}

func (v Vocabulary) publisherEntity(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return ""
	}
	for _, publisher := range v.PublisherDomains {
		for _, domain := range publisher.Domains {
			if host == domain || strings.HasSuffix(host, "."+domain) {
				return publisher.EntityID
			}
		}
	}
	return ""
}

func (v Vocabulary) entityName(id string) string {
	for _, entry := range v.IdentityLexicon {
		if entry.ID == id {
			return entry.Name
		}
	}
	return ""
}

func (v Vocabulary) entityNameOrID(id string) string {
	if name := v.entityName(id); name != "" {
		return name
	}
	return id
}

func unsupportedIDs(ids []string, allowed map[string]struct{}) []string {
	unsupported := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			unsupported = append(unsupported, id)
		}
	}
	return unsupported
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func looksChinese(value string) bool {
	hasHan := false
	for _, character := range value {
		if character >= 0x4E00 && character <= 0x9FFF {
			hasHan = true
		}
		if (character >= 0x3040 && character <= 0x30FF) || (character >= 0xAC00 && character <= 0xD7AF) {
			return false
		}
	}
	return hasHan
}
