package editorial

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type PrefilterOutput struct {
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

type UnderstandOutput struct {
	ItemType          string   `json:"itemType"`
	AuthorRole        string   `json:"authorRole"`
	Tags              []string `json:"tags"`
	EditorialJudgment string   `json:"editorialJudgment"`
	TitleZh           string   `json:"titleZh"`
	SummaryZh         string   `json:"summaryZh"`
}

type TranslateOutput struct {
	TitleZh   string
	SummaryZh string
	BodyZh    string
}

func extractModelJSON(text string) ([]byte, error) {
	trimmed := strings.TrimSpace(text)
	fence := regexp.MustCompile("(?is)^```(?:json)?\\s*([\\s\\S]*?)\\s*```$")
	if match := fence.FindStringSubmatch(trimmed); match != nil {
		trimmed = match[1]
	}
	start, end := strings.Index(trimmed, "{"), strings.LastIndex(trimmed, "}")
	if start < 0 || end < start {
		return nil, errors.New("no JSON object in model output")
	}
	data := []byte(trimmed[start : end+1])
	if json.Valid(data) {
		return data, nil
	}
	return escapeModelJSONControls(data)
}

func escapeModelJSONControls(data []byte) ([]byte, error) {
	result := make([]byte, 0, len(data)+32)
	inString, escaped := false, false
	for _, value := range data {
		if inString {
			if escaped {
				escaped = false
			} else if value == '\\' {
				escaped = true
			} else if value == '"' {
				inString = false
			} else if value < ' ' {
				switch value {
				case '\n':
					result = append(result, `\n`...)
				case '\r':
					result = append(result, `\r`...)
				case '\t':
					result = append(result, `\t`...)
				default:
					result = append(result, fmt.Sprintf(`\u%04x`, value)...)
				}
				continue
			}
		} else if value == '"' {
			inString = true
		}
		result = append(result, value)
	}
	if !json.Valid(result) {
		return nil, errors.New("model output is not valid JSON")
	}
	return result, nil
}

func ParsePrefilter(text string) (PrefilterOutput, error) {
	var value map[string]json.RawMessage
	data, err := extractModelJSON(text)
	if err != nil {
		return PrefilterOutput{}, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return PrefilterOutput{}, err
	}
	label := strings.ToUpper(strings.TrimSpace(jsonStringValue(value["label"])))
	if label != "PASS" && label != "BLOCK" && label != "UNKNOWN" {
		return PrefilterOutput{}, errors.New("prefilter label must be PASS, BLOCK or UNKNOWN")
	}
	reason := jsonStringValue(value["reason"])
	if utf16Length(reason) > 200 {
		reason = ""
	}
	return PrefilterOutput{Label: label, Reason: reason}, nil
}

func ParseScore(text string) (int, error) {
	var value map[string]json.RawMessage
	data, err := extractModelJSON(text)
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return 0, err
	}
	raw, ok := value["attentionScore"]
	if !ok {
		return 0, errors.New("score is required")
	}
	number, err := jsonNumberCoerce(raw)
	if err != nil || number < 0 || number > 100 || number != float64(int(number)) {
		return 0, errors.New("attentionScore must be an integer from 0 to 100")
	}
	return int(number), nil
}

func ParseStructure(text string, categories []string) (Structure, error) {
	var value map[string]json.RawMessage
	data, err := extractModelJSON(text)
	if err != nil {
		return Structure{}, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return Structure{}, err
	}
	structure := Structure{Scope: "unknown", Tags: []string{}, Subjects: []string{}}
	scope := jsonStringValue(value["scope"])
	if scope == "single" || scope == "composite" || scope == "unknown" {
		structure.Scope = scope
	}
	category := jsonStringValue(value["category"])
	categorySet := make(map[string]struct{}, len(categories))
	for _, key := range categories {
		categorySet[key] = struct{}{}
	}
	if _, ok := categorySet[category]; ok && category != "" {
		structure.Category = &category
	}
	structure.Tags = boundedStrings(value["tags"], 12)
	structure.Subjects = boundedStrings(value["subjects"], 6)
	if raw := value["fact"]; len(raw) > 0 && string(raw) != "null" {
		fact, err := parseFact(raw)
		if err == nil {
			structure.Fact = fact
		}
	}
	return structure, nil
}

func ParseUnderstand(text string, vocabulary Vocabulary) (UnderstandOutput, error) {
	var value map[string]json.RawMessage
	data, err := extractModelJSON(text)
	if err != nil {
		return UnderstandOutput{}, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return UnderstandOutput{}, err
	}
	itemType := jsonStringValue(value["itemType"])
	if !contains(vocabulary.ItemTypes, itemType) {
		return UnderstandOutput{}, errors.New("itemType is invalid")
	}
	title := strings.TrimSpace(jsonStringValue(value["titleZh"]))
	summary := strings.TrimSpace(jsonStringValue(value["summaryZh"]))
	if title == "" || utf16Length(title) > 200 || summary == "" || utf16Length(summary) > 4000 {
		return UnderstandOutput{}, errors.New("understand output is incomplete")
	}
	role := jsonStringValue(value["authorRole"])
	if role != "principal" && role != "observer" && role != "relayer" {
		role = "relayer"
	}
	judgment := jsonStringValue(value["editorialJudgment"])
	if utf16Length(judgment) > 400 {
		judgment = ""
	}
	return UnderstandOutput{ItemType: itemType, AuthorRole: role, Tags: boundedStrings(value["tags"], 12), EditorialJudgment: judgment, TitleZh: title, SummaryZh: summary}, nil
}

func ParseTranslateOutput(text string) TranslateOutput {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var title, summary, body string
	titleLine, summaryLine, bodyLine := -1, -1, -1
	titlePattern := regexp.MustCompile(`^title_zh\s*[:：]\s*(.*)$`)
	summaryPattern := regexp.MustCompile(`^summary_zh\s*[:：]\s*(.*)$`)
	bodyPattern := regexp.MustCompile(`^body_zh\s*[:：]\s*(.*)$`)
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := titlePattern.FindStringSubmatch(trimmed); match != nil {
			title, titleLine = strings.TrimSpace(match[1]), index
			continue
		}
		if match := summaryPattern.FindStringSubmatch(trimmed); match != nil {
			summary, summaryLine = strings.TrimSpace(match[1]), index
			continue
		}
		if match := bodyPattern.FindStringSubmatch(trimmed); match != nil {
			body, bodyLine = strings.TrimSpace(match[1]), index
		}
	}
	if title != "" && summary == "" && bodyLine < 0 && titleLine >= 0 {
		rest := nonEmptyTrimmed(lines[titleLine+1:])
		if len(rest) > 0 {
			summary = strings.Join(rest, "\n")
		}
	}
	labelPattern := regexp.MustCompile(`^(title_zh|summary_zh|body_zh)\s*[:：]`)
	if summaryLine >= 0 {
		more := make([]string, 0)
		for _, line := range lines[summaryLine+1:] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if labelPattern.MatchString(trimmed) {
				break
			}
			more = append(more, trimmed)
		}
		parts := []string{}
		if summary != "" {
			parts = append(parts, summary)
		}
		parts = append(parts, more...)
		if len(parts) > 0 {
			summary = strings.Join(parts, "\n")
		}
	}
	if bodyLine >= 0 {
		more := make([]string, 0)
		for _, line := range lines[bodyLine+1:] {
			if labelPattern.MatchString(strings.TrimSpace(line)) {
				break
			}
			more = append(more, line)
		}
		for len(more) > 0 && strings.TrimSpace(more[len(more)-1]) == "" {
			more = more[:len(more)-1]
		}
		if body != "" {
			more = append([]string{body}, more...)
		}
		if len(more) > 0 {
			body = strings.Join(more, "\n")
		}
	}
	if title == "" && summary == "" && body == "" {
		rest := nonEmptyTrimmed(lines)
		switch len(rest) {
		case 1:
			title = rest[0]
		case 2:
			title, summary = rest[0], rest[1]
		case 3:
			title, summary = rest[0], strings.Join(rest[1:], "\n")
		default:
			if len(rest) > 3 {
				title, summary = rest[0], strings.Join(rest[1:], "\n")
			}
		}
	}
	return TranslateOutput{TitleZh: title, SummaryZh: stripEcho(summary), BodyZh: stripEcho(body)}
}

func stripEcho(text string) string {
	lines := strings.Split(text, "\n")
	for len(lines) > 0 {
		trimmed := strings.TrimSpace(lines[len(lines)-1])
		if trimmed == "" || answerEcho.MatchString(trimmed) {
			lines = lines[:len(lines)-1]
			continue
		}
		break
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func parseFact(raw json.RawMessage) (*Fact, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	title, ok := requiredString(value["title"], 80, false)
	if !ok {
		return nil, errors.New("fact title is invalid")
	}
	fact := &Fact{Title: title, Conditions: []Condition{}}
	for _, field := range []struct {
		key string
		max int
		dst **string
	}{{"subject", 80, &fact.Subject}, {"action", 80, &fact.Action}, {"object", 160, &fact.Object}, {"occurredAt", 0, &fact.OccurredAt}} {
		if rawValue, exists := value[field.key]; exists && string(rawValue) != "null" {
			parsed, valid := requiredString(rawValue, field.max, true)
			if !valid {
				return nil, errors.New("fact field is invalid")
			}
			*field.dst = &parsed
		}
	}
	if rawEvidence, exists := value["evidence"]; exists && string(rawEvidence) != "null" {
		evidence, valid := requiredString(rawEvidence, 600, true)
		if valid {
			fact.Evidence = &evidence
		}
	}
	if rawConditions, exists := value["conditions"]; exists {
		var conditions []json.RawMessage
		if json.Unmarshal(rawConditions, &conditions) != nil {
			return nil, errors.New("fact conditions must be an array")
		}
		for _, rawCondition := range conditions {
			if string(rawCondition) == "null" {
				continue
			}
			var item map[string]json.RawMessage
			if json.Unmarshal(rawCondition, &item) != nil {
				continue
			}
			quote, valid := requiredString(item["quote"], 400, true)
			if !valid || quote == "" {
				continue
			}
			condition := Condition{Quote: quote}
			if rawText, exists := item["text"]; exists {
				text, valid := requiredString(rawText, 200, false)
				if valid && text != "" {
					condition.Text = &text
				}
			}
			fact.Conditions = append(fact.Conditions, condition)
		}
	}
	return fact, nil
}

func requiredString(raw json.RawMessage, max int, trim bool) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	if trim {
		value = strings.TrimSpace(value)
	}
	if max > 0 && utf16Length(value) > max {
		return "", false
	}
	return value, true
}

func boundedStrings(raw json.RawMessage, max int) []string {
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) > max {
		return []string{}
	}
	return values
}

func jsonStringValue(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func jsonNumberCoerce(raw json.RawMessage) (float64, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	switch value := value.(type) {
	case float64:
		return value, nil
	case string:
		if strings.TrimSpace(value) == "" {
			return 0, nil
		}
		return strconv.ParseFloat(strings.TrimSpace(value), 64)
	case bool:
		if value {
			return 1, nil
		}
		return 0, nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("number coercion failed")
	}
}

func contains(values []string, match string) bool {
	for _, value := range values {
		if value == match {
			return true
		}
	}
	return false
}

func nonEmptyTrimmed(lines []string) []string {
	values := make([]string, 0, len(lines))
	for _, line := range lines {
		if value := strings.TrimSpace(line); value != "" {
			values = append(values, value)
		}
	}
	return values
}
