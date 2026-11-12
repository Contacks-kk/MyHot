package editorial

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

var (
	scriptElement = regexp.MustCompile(`(?is)<script[^>]*>[\s\S]*?</script>`)
	styleElement  = regexp.MustCompile(`(?is)<style[^>]*>[\s\S]*?</style>`)
	htmlElement   = regexp.MustCompile(`<[^>]+>`)
	webURL        = regexp.MustCompile(`(?i)https?://\S+`)
	horizontalWS  = regexp.MustCompile(`[ \t]+`)
	newlineWS     = regexp.MustCompile(`[ \t]*\n[ \t]*`)
	manyNewlines  = regexp.MustCompile(`\n{3,}`)
	noiseHandles  = regexp.MustCompile(`@[A-Za-z0-9_]+`)
	noiseHashtags = regexp.MustCompile(`#[A-Za-z0-9_]+`)
	englishRuns   = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.#/-]*(?:\s+[A-Za-z][A-Za-z0-9+.#/-]*)+`)
	answerEcho    = regexp.MustCompile(`^(来源[:：]|【已核验身份上下文】|这些事实只用于防止|原始标题[:：]|【时间锚点】)`)
)

func clampText(value string, maxChars int) string {
	runes := []rune(value)
	if len(runes) <= maxChars {
		return value
	}
	if maxChars <= 0 {
		return "…"
	}
	return string(runes[:maxChars-1]) + "…"
}

func isShortTweet(value string) bool {
	if value == "" {
		return false
	}
	cjk, length := 0, utf16Length(value)
	for _, character := range value {
		if character >= 0x4E00 && character <= 0x9FFF {
			cjk++
		}
	}
	limit := 500
	if float64(cjk) > float64(length)*0.3 {
		limit = 100
	}
	return length < limit
}

func cleanArticleTextForLLM(value string) string {
	if value == "" {
		return ""
	}
	value = scriptElement.ReplaceAllString(value, " ")
	value = styleElement.ReplaceAllString(value, " ")
	value = htmlElement.ReplaceAllString(value, " ")
	value = webURL.ReplaceAllString(value, " ")
	value = strings.ReplaceAll(value, "&nbsp;", " ")
	value = strings.ReplaceAll(value, "&amp;", "&")
	value = strings.ReplaceAll(value, "&lt;", "<")
	value = strings.ReplaceAll(value, "&gt;", ">")
	value = strings.ReplaceAll(value, "&quot;", `"`)
	value = strings.ReplaceAll(value, "&#39;", "'")
	value = horizontalWS.ReplaceAllString(value, " ")
	value = newlineWS.ReplaceAllString(value, "\n")
	value = manyNewlines.ReplaceAllString(value, "\n\n")
	return strings.TrimSpace(value)
}

func needsShortTweetTranslation(value string) bool {
	clean := webURL.ReplaceAllString(value, " ")
	clean = noiseHandles.ReplaceAllString(clean, " ")
	clean = noiseHashtags.ReplaceAllString(clean, " ")
	if !looksChinese(clean) || chineseDensity(clean) < 0.65 {
		return true
	}
	for _, run := range englishRuns.FindAllString(clean, -1) {
		if len(strings.Join(strings.Fields(run), "")) >= 10 {
			return true
		}
	}
	return false
}

func chineseDensity(value string) float64 {
	chinese, total := 0, 0
	for _, character := range value {
		if unicode.IsSpace(character) {
			continue
		}
		total++
		if character >= 0x4E00 && character <= 0x9FFF {
			chinese++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(chinese) / float64(total)
}

func isShortTweetInput(input WritingInput) bool {
	return input.SourceKind == "x_search" && isShortTweet(firstNonEmpty(input.MainText, input.Title))
}

func anchorDate(value *time.Time) string {
	if value == nil {
		return "未注明"
	}
	return value.Add(8 * time.Hour).UTC().Format("2006-01-02")
}

func sourceName(value string) string {
	if strings.TrimSpace(value) == "" {
		return "（未注明）"
	}
	return strings.TrimSpace(value)
}

func (v Vocabulary) buildIdentityPrompt(pack *PromptPack, input WritingInput) (string, error) {
	return v.IdentityPrompt(pack, input)
}

func (v Vocabulary) BuildArticlePrompt(pack *PromptPack, input WritingInput, today time.Time) (string, error) {
	identity, err := v.buildIdentityPrompt(pack, input)
	if err != nil {
		return "", err
	}
	body := ""
	if input.Text != "" {
		body = clampText(cleanArticleTextForLLM(input.Text), 6000)
	} else {
		body, err = pack.Render("summarize-article-empty", nil)
		if err != nil {
			return "", err
		}
	}
	return pack.Render("summarize-article", map[string]string{
		"publishedDate": anchorDate(input.PublishedAt), "today": today.UTC().Add(8 * time.Hour).UTC().Format("2006-01-02"),
		"sourceName": sourceName(input.SourceName), "identity": identity, "title": input.Title, "body": body,
	})
}

func (v Vocabulary) BuildShortTweetPrompt(pack *PromptPack, input WritingInput) (string, error) {
	return v.buildTweetPrompt(pack, input, true)
}

func (v Vocabulary) BuildLongTweetPrompt(pack *PromptPack, input WritingInput) (string, error) {
	return v.buildTweetPrompt(pack, input, false)
}

func (v Vocabulary) buildTweetPrompt(pack *PromptPack, input WritingInput, short bool) (string, error) {
	identity, err := v.buildIdentityPrompt(pack, input)
	if err != nil {
		return "", err
	}
	name := "summarize-long-post"
	if short {
		name = "summarize-short-post"
	}
	text, err := pack.Render(name, map[string]string{
		"sourceName": sourceName(input.SourceName), "identity": identity,
		"post": clampText(firstNonEmpty(input.MainText, input.Text, input.Title), 4000),
	})
	if err != nil {
		return "", err
	}
	if input.QuotedText == "" {
		return text, nil
	}
	quotedName := "summarize-long-post-quoted"
	if short {
		quotedName = "summarize-short-post-quoted"
	}
	label := "引用推文"
	if input.QuotedAuthor != "" {
		label = "@" + input.QuotedAuthor
	}
	quoted, err := pack.Render(quotedName, map[string]string{"quotedLabel": label, "quotedText": clampText(input.QuotedText, 1500)})
	if err != nil {
		return "", err
	}
	return text + "\n\n" + quoted, nil
}

func compactAnswerFirstSummary(summary string, maxChars int) string {
	text := strings.TrimSpace(regexp.MustCompile(`\s*\n+\s*`).ReplaceAllString(strings.TrimSpace(summary), " "))
	if utf16Length(text) <= maxChars {
		return text
	}
	sentences := regexp.MustCompile(`[^。！？!?]+[。！？!?]?`).FindAllString(text, -1)
	result := ""
	for _, sentence := range sentences {
		if utf16Length(result+sentence) > maxChars {
			break
		}
		result += sentence
		if utf16Length(result) >= 80 {
			break
		}
	}
	if utf16Length(result) >= 50 {
		return strings.TrimSpace(result)
	}
	clauses := regexp.MustCompile(`[^，；：、,;:]+[，；：、,;:]?`).FindAllString(text, -1)
	result = ""
	for _, clause := range clauses {
		if utf16Length(result+clause)+1 > maxChars {
			break
		}
		result += clause
		if utf16Length(result) >= 80 {
			break
		}
	}
	if utf16Length(result) >= 50 {
		result = regexp.MustCompile(`[，；：、,;:]$`).ReplaceAllString(result, "")
		return result + "。"
	}
	return text
}

func answerFirstSummaryLengthOK(summary string, input WritingInput) bool {
	trimmed := strings.TrimSpace(summary)
	sourceText := input.Text
	if input.SourceKind != "x_search" {
		sourceText = cleanArticleTextForLLM(sourceText)
	}
	sourceLength := utf16Length(strings.TrimSpace(sourceText))
	filtered := 0
	for _, part := range regexp.MustCompile(`[。！？!?]+`).Split(trimmed, -1) {
		if strings.TrimSpace(part) != "" {
			filtered++
		}
	}
	rich := sourceLength >= 500
	return utf16Length(trimmed) <= 200 && utf16Length(trimmed) >= boolInt(rich, 80, 50) && filtered <= 3 && (!rich || filtered >= 2)
}

func boolInt(value bool, whenTrue, whenFalse int) int {
	if value {
		return whenTrue
	}
	return whenFalse
}

func finalizeCopy(v Vocabulary, input WritingInput, titleZh, summaryZh string) FinalCopy {
	if !isShortTweetInput(input) && summaryZh != "" && !answerFirstSummaryLengthOK(summaryZh, input) {
		summaryZh = compactAnswerFirstSummary(summaryZh, 190)
	}
	return v.EnforceIdentity(input, titleZh, summaryZh)
}
