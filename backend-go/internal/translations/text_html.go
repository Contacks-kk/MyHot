package translations

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	urlStartPattern        = regexp.MustCompile(`https?://`)
	modelURLPattern        = regexp.MustCompile(`https?://\S+`)
	inlinePattern          = regexp.MustCompile("(`[^`\\n]+`|\\*\\*[^*\\n]+\\*\\*|\\[[^\\]\\n]+\\]\\(https?://[^)\\s]+\\))")
	listPattern            = regexp.MustCompile(`^[ \t]*([-*+•]|[0-9]+[.)])[ \t]+`)
	orderedPattern         = regexp.MustCompile(`^[ \t]*[0-9]+[.)][ \t]+`)
	quotePattern           = regexp.MustCompile(`^[ \t]*>[ \t]?`)
	headingPattern         = regexp.MustCompile(`^(#{1,6})[ \t]+(.+)$`)
	markdownLinkPattern    = regexp.MustCompile(`^\[([^\]]+)\]\((https?://[^)\s]+)\)$`)
	trailingURLPunctuation = []string{
		".", ",", ";", ":", "!", "?", "*", "'", `"`, "”", "’", "»", "…", "、", "。", "，", "；", "：", "！", "？", "）", "]", "】", "}", "」", "』", "》",
	}
)

func textToHTML(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	paragraphs := regexp.MustCompile(`\n{2,}`).Split(value, -1)
	var output strings.Builder
	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph != "" {
			output.WriteString(textBlock(paragraph))
		}
	}
	return output.String()
}

func textBlock(paragraph string) string {
	if strings.HasPrefix(paragraph, "```") {
		lines := strings.Split(paragraph, "\n")
		if len(lines) > 0 {
			lines = lines[1:]
		}
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "```" {
			lines = lines[:len(lines)-1]
		}
		return "<pre><code>" + escapeHTML(strings.Join(lines, "\n")) + "</code></pre>"
	}
	lines := make([]string, 0)
	for _, line := range strings.Split(paragraph, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > 0 && allMatch(lines, quotePattern) {
		parts := make([]string, len(lines))
		for i, line := range lines {
			parts[i] = inlineHTML(quotePattern.ReplaceAllString(line, ""))
		}
		return "<blockquote><p>" + strings.Join(parts, "<br>") + "</p></blockquote>"
	}
	if len(lines) > 1 && allMatch(lines, listPattern) {
		tag := "ul"
		if orderedPattern.MatchString(lines[0]) {
			tag = "ol"
		}
		var output strings.Builder
		output.WriteString("<" + tag + ">")
		for _, line := range lines {
			output.WriteString("<li>" + inlineHTML(listPattern.ReplaceAllString(line, "")) + "</li>")
		}
		output.WriteString("</" + tag + ">")
		return output.String()
	}
	if len(lines) == 1 {
		if match := headingPattern.FindStringSubmatch(lines[0]); match != nil {
			level := min(len(match[1])+1, 6)
			return "<h" + string(rune('0'+level)) + ">" + inlineHTML(match[2]) + "</h" + string(rune('0'+level)) + ">"
		}
	}
	paragraphLines := strings.Split(paragraph, "\n")
	parts := make([]string, len(paragraphLines))
	for i, line := range paragraphLines {
		parts[i] = inlineHTML(line)
	}
	return "<p>" + strings.Join(parts, "<br>") + "</p>"
}

func allMatch(lines []string, pattern *regexp.Regexp) bool {
	for _, line := range lines {
		if !pattern.MatchString(line) {
			return false
		}
	}
	return true
}

func inlineHTML(value string) string {
	parts := inlinePattern.FindAllStringIndex(value, -1)
	if len(parts) == 0 {
		return linkify(value)
	}
	var output strings.Builder
	last := 0
	for _, span := range parts {
		output.WriteString(linkify(value[last:span[0]]))
		part := value[span[0]:span[1]]
		switch {
		case strings.HasPrefix(part, "`"):
			output.WriteString("<code>" + escapeHTML(strings.TrimSuffix(strings.TrimPrefix(part, "`"), "`")) + "</code>")
		case strings.HasPrefix(part, "**"):
			output.WriteString("<strong>" + linkify(strings.TrimSuffix(strings.TrimPrefix(part, "**"), "**")) + "</strong>")
		default:
			match := markdownLinkPattern.FindStringSubmatch(part)
			if match == nil {
				output.WriteString(escapeHTML(part))
			} else {
				output.WriteString(link(match[2], match[1]))
			}
		}
		last = span[1]
	}
	output.WriteString(linkify(value[last:]))
	return output.String()
}

func linkify(value string) string {
	var output strings.Builder
	last := 0
	for _, span := range bareURLSpans(value) {
		candidate := value[span[0]:span[1]]
		candidate = trimUnmatchedParens(candidate)
		candidate = trimTrailingURLPunctuation(candidate)
		parsed, err := url.Parse(candidate)
		if candidate == "" || err != nil || parsed.Hostname() == "" || !strings.Contains(parsed.Hostname(), ".") {
			continue
		}
		output.WriteString(escapeHTML(value[last:span[0]]))
		output.WriteString(link(candidate, candidate))
		last = span[0] + len(candidate)
	}
	output.WriteString(escapeHTML(value[last:]))
	return output.String()
}

func bareURLSpans(value string) [][2]int {
	starts := urlStartPattern.FindAllStringIndex(value, -1)
	spans := make([][2]int, 0, len(starts))
	for _, start := range starts {
		end := start[1]
		for end < len(value) {
			current, width := utf8.DecodeRuneInString(value[end:])
			if urlRuneBoundary(current) || current == '^' || current == '|' || current == '{' || current == '}' {
				break
			}
			end += width
		}
		if end > start[0] {
			spans = append(spans, [2]int{start[0], end})
		}
	}
	return spans
}

func withoutURLs(value, replacement string) string {
	return modelURLPattern.ReplaceAllString(value, replacement)
}

func trimTrailingURLPunctuation(value string) string {
	for {
		trimmed := false
		for _, suffix := range trailingURLPunctuation {
			if strings.HasSuffix(value, suffix) {
				value = strings.TrimSuffix(value, suffix)
				trimmed = true
				break
			}
		}
		if !trimmed {
			return value
		}
	}
}

func trimUnmatchedParens(value string) string {
	for strings.HasSuffix(value, ")") && strings.Count(value, "(") < strings.Count(value, ")") {
		value = strings.TrimSuffix(value, ")")
	}
	return value
}

func escapeHTML(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(value)
}

func link(href, text string) string {
	return `<a href="` + escapeHTML(href) + `" target="_blank" rel="noopener noreferrer nofollow">` + escapeHTML(text) + `</a>`
}

func urlRuneBoundary(value rune) bool {
	return unicode.IsSpace(value) || value < 0x20 || value == '<' || value == '>' || value == '"' || value == '`' ||
		(value >= 0x3000 && value <= 0x303f) || (value >= 0x3400 && value <= 0x9fff) ||
		(value >= 0xff00 && value <= 0xffef) || (value >= 0x3040 && value <= 0x30ff) ||
		(value >= 0xac00 && value <= 0xd7af) || (value >= 0x2600 && value <= 0x27bf) ||
		value == 0xfe0f || value == 0x200d || (value >= 0x1f300 && value <= 0x1faff)
}
