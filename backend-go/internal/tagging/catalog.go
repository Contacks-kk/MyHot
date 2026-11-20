package tagging

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/model"
	"aihot/backend-go/internal/topics"
	"golang.org/x/text/unicode/norm"
)

type Label struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	NameKey    string          `json:"-"`
	Kind       string          `json:"kind"`
	Definition string          `json:"definition"`
	Aliases    model.TextArray `json:"aliases"`
	ParentID   *string         `json:"parentId,omitempty"`
	Status     string          `json:"status"`
	MergedInto *string         `json:"-"`
	Builtin    bool            `json:"builtin"`
}

type matcher struct {
	id      string
	pattern *regexp.Regexp
}

/** nameKey makes punctuation, width, spaces and case irrelevant to exact deduplication. */
func nameKey(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, norm.NFKC.String(value))
}

/** Builtins derives the stable followable labels from the existing entity and theme catalog. */
func Builtins(entities map[string]interests.Entity, definitions []topics.Definition) []Label {
	items := make([]Label, 0, len(entities)+len(definitions))
	for id, e := range entities {
		items = append(items, Label{
			ID:         id,
			Name:       e.Name,
			NameKey:    nameKey(e.Name),
			Kind:       "entity",
			Definition: "追踪 " + e.Name + " 的公司与产品动态。",
			Aliases:    append(append(model.TextArray{}, e.Aliases...), e.OtherNames...),
			Status:     "active",
			Builtin:    true,
		})
	}
	for _, d := range definitions {
		if d.Group != "field" {
			continue
		}
		aliases := append(model.TextArray{}, d.Aliases...)
		if d.Slug == "agent" {
			aliases = append(aliases, "Agent", "智能体", "AI Agent")
		}
		items = append(items, Label{
			ID:         d.Slug,
			Name:       d.Name,
			NameKey:    nameKey(d.Name),
			Kind:       "topic",
			Definition: d.Definition,
			Aliases:    aliases,
			Status:     "active",
			Builtin:    true,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

/** compileCatalog prepares bounded English term and literal Chinese matches once per indexing batch. */
func compileCatalog(labels []Label) ([]matcher, string) {
	var rules []matcher
	for _, l := range labels {
		var terms []string
		for _, term := range append([]string{l.Name}, l.Aliases...) {
			term = strings.TrimSpace(norm.NFKC.String(term))
			if len([]rune(term)) < 2 {
				continue
			}
			escaped := regexp.QuoteMeta(term)
			if !strings.ContainsFunc(term, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
				escaped = `(?:^|[^a-z0-9])` + escaped + `(?:$|[^a-z0-9])`
			}
			terms = append(terms, escaped)
		}
		if len(terms) > 0 {
			rules = append(rules, matcher{l.ID, regexp.MustCompile("(?i)" + strings.Join(terms, "|"))})
		}
	}
	versionRules := make([][2]string, 0, len(rules))
	for _, rule := range rules {
		versionRules = append(versionRules, [2]string{rule.id, rule.pattern.String()})
	}
	data, _ := json.Marshal(versionRules)
	return rules, fmt.Sprintf("%x", sha256.Sum256(data))
}

/** matchArticle returns the IDs whose vocabulary actually appears in the article. */
func matchArticle(rules []matcher, title, excerpt string) []string {
	content := norm.NFKC.String(title + "\n" + excerpt)
	var ids []string
	for _, rule := range rules {
		if rule.pattern.MatchString(content) {
			ids = append(ids, rule.id)
		}
	}
	return ids
}
