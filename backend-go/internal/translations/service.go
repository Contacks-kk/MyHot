package translations

import (
	"aihot/backend-go/internal/promptpath"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"aihot/backend-go/internal/bodyhtml"
	"aihot/backend-go/internal/modelcall"
	"github.com/PuerkitoBio/goquery"
	xhtml "golang.org/x/net/html"
)

const (
	BatchChars   = 3500
	MaxBodyChars = 60000
	XMinChars    = 60
)

type ModelClient interface {
	Call(context.Context, modelcall.Request) (modelcall.Result, error)
	Reject(context.Context, int64, string) error
}

type Prompts struct {
	Body    string
	Post    string
	Version string
}

type Service struct {
	repository Repository
	models     ModelClient
	prompts    Prompts
	now        func() time.Time
}

type Result struct {
	ArticleID string `json:"articleId"`
	Status    string `json:"status"`
	Revision  int    `json:"revision,omitempty"`
	Segments  int    `json:"segments,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type PendingResult struct {
	Done   []Result `json:"done"`
	Quotes int      `json:"quotes"`
}

type output struct {
	Translations []string `json:"t"`
}

type xPost struct {
	Text   string `json:"text"`
	Quoted struct {
		Text string `json:"text"`
		URL  string `json:"url"`
	} `json:"quoted"`
}

type PromptFiles struct {
	Body string
	Post string
}

func LoadPromptFiles() (PromptFiles, error) {
	if configured := promptpath.Configured(); configured != "" {
		return readPromptFiles(configured)
	}
	for _, candidate := range []string{filepath.Join("backend-go", "config", "prompts"), filepath.Join("config", "prompts"), filepath.Join("..", "..", "config", "prompts"), filepath.Join("..", "backend-go", "config", "prompts"), filepath.Join("/app", "backend-go", "config", "prompts")} {
		files, err := readPromptFiles(candidate)
		if err == nil {
			return files, nil
		}
	}
	return PromptFiles{}, errors.New("industry translation prompts were not found")
}

func readPromptFiles(directory string) (PromptFiles, error) {
	body, err := os.ReadFile(filepath.Join(directory, "translate-body.md"))
	if err != nil {
		return PromptFiles{}, err
	}
	post, err := os.ReadFile(filepath.Join(directory, "translate-post.md"))
	if err != nil {
		return PromptFiles{}, err
	}
	return PromptFiles{Body: string(body), Post: string(post)}, nil
}

func NewService(repository Repository, models ModelClient, files PromptFiles, siteName string) *Service {
	body := strings.ReplaceAll(files.Body, "{{siteName}}", siteName)
	post := strings.ReplaceAll(files.Post, "{{siteName}}", siteName)
	version := promptVersion(files)
	return &Service{repository: repository, models: models, prompts: Prompts{Body: body, Post: post, Version: version}, now: time.Now}
}

func promptVersion(files PromptFiles) string {
	used := []string{"translate-body", "translate-post"}
	sort.Strings(used)
	hash := sha256.New()
	contents := map[string]string{"translate-body": files.Body, "translate-post": files.Post}
	for _, name := range used {
		_, _ = hash.Write([]byte(name + "\n" + contents[name] + "\n"))
	}
	return "translate-body+translate-post@" + hex.EncodeToString(hash.Sum(nil))[:10]
}

func (s *Service) ProcessPending(ctx context.Context, limit int, budget time.Duration) (PendingResult, error) {
	if limit <= 0 {
		limit = 30
	}
	if budget <= 0 {
		budget = 4 * time.Minute
	}
	started := s.now()
	ids, err := s.repository.DueArticles(ctx, limit)
	if err != nil {
		return PendingResult{}, err
	}
	result := PendingResult{Done: make([]Result, 0, len(ids))}
	for _, id := range ids {
		if s.now().Sub(started) > budget || ctx.Err() != nil {
			break
		}
		translated, translateErr := s.TranslateArticle(ctx, id)
		outcome, reason, revision := translated.Status, translated.Reason, translated.Revision
		if translateErr != nil {
			if ctx.Err() != nil {
				break
			}
			if isPaused(translateErr) {
				break
			}
			outcome = "failed"
			reason = shortError(translateErr, 300)
			translated = Result{ArticleID: id, Status: "skipped", Reason: shortError(translateErr, 200)}
		}
		result.Done = append(result.Done, translated)
		var reasonPointer *string
		if reason != "" {
			reasonPointer = &reason
		}
		if err := s.repository.RecordAttempt(ctx, id, revision, outcome, reasonPointer); err != nil {
			return result, err
		}
	}
	left := budget - s.now().Sub(started)
	if left > 0 && ctx.Err() == nil {
		quotes, err := s.TranslateQuotes(ctx, 3, 30, left)
		if err != nil {
			if !isPaused(err) {
				return result, err
			}
			return result, nil
		}
		result.Quotes = quotes
	}
	return result, nil
}

func (s *Service) TranslateArticle(ctx context.Context, articleID string) (Result, error) {
	article, err := s.repository.Article(ctx, articleID)
	if err != nil {
		return Result{}, err
	}
	if article == nil {
		return Result{ArticleID: articleID, Status: "skipped", Reason: "not published"}, nil
	}
	result := Result{ArticleID: articleID, Revision: article.Revision}
	finish := func(status, reason string) Result {
		result.Status, result.Reason = status, reason
		return result
	}
	if !article.Selected || article.Visibility != "public" || article.BodyMode != "full" {
		return finish("skipped", "not a selected full-text item"), nil
	}
	var post xPost
	if len(article.XPost) > 0 {
		_ = json.Unmarshal(article.XPost, &post)
	}
	if article.Channel == "x" {
		text := strings.TrimSpace(post.Text)
		if text == "" && article.BodyText != nil {
			text = strings.TrimSpace(*article.BodyText)
		}
		meaningful := collapseWhitespace(withoutURLs(text, ""))
		if isChinese(article.Language, text) {
			return finish("skipped", "already Chinese"), nil
		}
		if utf16Length(meaningful) < XMinChars {
			return finish("skipped", "short post"), nil
		}
		answers, err := s.translateAll(ctx, article.ID, article.Revision, []string{text}, s.prompts.Post, "translate_body", "article:%s@%d#%d")
		if err != nil {
			return result, err
		}
		if len(answers) == 0 || answers[0] == nil {
			return finish("skipped", "translation did not line up"), nil
		}
		html := textToHTML(*answers[0])
		safeHTML, _, err := bodyhtml.Sanitize(html, "", "", s.now())
		if err != nil {
			return result, err
		}
		plain := htmlText(safeHTML)
		if _, err := s.repository.SaveTranslation(ctx, Translation{ArticleID: article.ID, Revision: article.Revision, Title: article.Title, BodyHTML: safeHTML, BodyText: plain, Complete: true}); err != nil {
			return result, err
		}
		result.Status, result.Segments = "translated", 1
		return result, nil
	}
	if article.BodyHTML == nil || isChinese(article.Language, value(article.BodyText)) {
		return finish("skipped", "no foreign-language body"), nil
	}
	doc, htmlRoot, err := parseFragment(*article.BodyHTML)
	if err != nil {
		return result, err
	}
	blocks := bodySegmentsIn(htmlRoot)
	if len(blocks) == 0 {
		return finish("skipped", "no translatable text"), nil
	}
	chosen := make([]*xhtml.Node, 0, len(blocks))
	shielded := make([]shieldedHTML, 0, len(blocks))
	budget := MaxBodyChars
	for _, block := range blocks {
		raw := innerHTML(block)
		length := utf16Length(raw)
		if length > budget {
			break
		}
		budget -= length
		protected, err := shieldHTML(innerHTML(block))
		if err != nil {
			return result, err
		}
		chosen = append(chosen, block)
		shielded = append(shielded, protected)
	}
	parts := make([]string, len(shielded))
	for index := range shielded {
		parts[index] = shielded[index].HTML
	}
	answers, err := s.translateAll(ctx, article.ID, article.Revision, parts, s.prompts.Body, "translate_body", "article:%s@%d#%d")
	if err != nil {
		return result, err
	}
	translations := make([]*string, len(answers))
	for index, answer := range answers {
		if answer == nil {
			continue
		}
		if restored, ok := unshieldHTML(*answer, shielded[index]); ok {
			translations[index] = &restored
		}
	}
	missing := make([]int, 0)
	for index, translated := range translations {
		if translated == nil {
			missing = append(missing, index)
		}
	}
	if len(missing) > 0 {
		retryParts := make([]string, len(missing))
		for index, original := range missing {
			retryParts[index] = shielded[original].HTML
		}
		retried, err := s.translateAll(ctx, article.ID, article.Revision, retryParts, s.prompts.Body, "translate_body", "article:%s@%d#%d", "retry")
		if err != nil {
			return result, err
		}
		for index, original := range missing {
			if index >= len(retried) || retried[index] == nil {
				continue
			}
			if restored, ok := unshieldHTML(*retried[index], shielded[original]); ok {
				translations[original] = &restored
			}
		}
	}
	done := 0
	for index, translated := range translations {
		if translated == nil {
			continue
		}
		setInnerHTML(chosen[index], *translated)
		done++
	}
	if done == 0 {
		return finish("skipped", "no batch translated"), nil
	}
	complete := done == len(blocks)
	updatedHTML := selectionHTML(htmlRoot)
	safeHTML, _, err := bodyhtml.Sanitize(updatedHTML, "", "", s.now())
	if err != nil {
		return result, err
	}
	safeHTML = stripGeneratedHeadingIDs(safeHTML)
	_ = doc
	if _, err := s.repository.SaveTranslation(ctx, Translation{ArticleID: article.ID, Revision: article.Revision, Title: article.Title, BodyHTML: safeHTML, BodyText: htmlText(safeHTML), Complete: complete}); err != nil {
		return result, err
	}
	result.Segments = done
	if complete {
		result.Status = "translated"
	} else {
		result.Status = "partial"
	}
	return result, nil
}

func (s *Service) translateAll(ctx context.Context, articleID string, revision int, parts []string, system, purpose, subjectFormat string, attemptTags ...string) ([]*string, error) {
	answers := make([]*string, len(parts))
	start, index := 0, 0
	attemptTag := ""
	if len(attemptTags) > 0 {
		attemptTag = attemptTags[0]
	}
	for start < len(parts) {
		end, chars := start, 0
		for end < len(parts) && (end == start || chars+utf16Length(parts[end]) <= BatchChars) {
			chars += utf16Length(parts[end])
			end++
		}
		batch := parts[start:end]
		done, err := s.translateBatch(ctx, articleID, revision, index, batch, system, purpose, subjectFormat, attemptTag)
		index++
		if err != nil {
			return nil, err
		}
		if done == nil && len(batch) > 1 {
			middle := (len(batch) + 1) / 2
			left, err := s.translateBatch(ctx, articleID, revision, index, batch[:middle], system, purpose, subjectFormat, attemptTag)
			index++
			if err != nil {
				return nil, err
			}
			right, err := s.translateBatch(ctx, articleID, revision, index, batch[middle:], system, purpose, subjectFormat, attemptTag)
			index++
			if err != nil {
				return nil, err
			}
			if left != nil && right != nil {
				done = append(left, right...)
			}
		}
		if done != nil {
			for i, translated := range done {
				answers[start+i] = &translated
			}
		}
		start = end
	}
	return answers, nil
}

func (s *Service) translateBatch(ctx context.Context, articleID string, revision, index int, parts []string, system, purpose, subjectFormat, attemptTag string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	user, err := javascriptJSON(struct {
		Segments []string `json:"segments"`
	}{Segments: parts})
	if err != nil {
		return nil, err
	}
	maxTokens := min(8000, int(math.Ceil(float64(utf16Length(strings.Join(parts, "")))*1.2))+400)
	modelResult, err := s.models.Call(ctx, modelcall.Request{
		Capability: "translate", Purpose: purpose,
		Subject: fmt.Sprintf(subjectFormat, articleID, revision, index), PromptVersion: s.prompts.Version,
		System: system, User: user, Temperature: floatPointer(0.2), MaxTokens: maxTokens, Timeout: 180 * time.Second, AttemptTag: attemptTag,
	})
	if err != nil {
		return nil, err
	}
	var response output
	if err := json.Unmarshal([]byte(extractJSON(modelResult.Completion.Text)), &response); err != nil {
		rejectErr := s.models.Reject(ctx, modelResult.ReceiptID, "unusable output: translation response was not JSON")
		return nil, errors.Join(errors.New("translation response was not JSON"), rejectErr)
	}
	if response.Translations == nil {
		rejectErr := s.models.Reject(ctx, modelResult.ReceiptID, "unusable output: translation response omitted t array")
		return nil, errors.Join(errors.New("translation response omitted t array"), rejectErr)
	}
	if len(response.Translations) != len(parts) {
		return nil, nil
	}
	return response.Translations, nil
}

func (s *Service) TranslateQuotes(ctx context.Context, days, limit int, budget time.Duration) (int, error) {
	if days <= 0 {
		days = 3
	}
	if limit <= 0 {
		limit = 30
	}
	if budget <= 0 {
		budget = 2 * time.Minute
	}
	started := s.now()
	rows, err := s.repository.QuoteCandidates(ctx, days)
	if err != nil {
		return 0, err
	}
	stored := 0
	for _, row := range rows {
		if stored >= limit || s.now().Sub(started) > budget || ctx.Err() != nil {
			break
		}
		hash := sha256Hex(row.Text)
		if row.TextHash != nil && *row.TextHash == hash || !quoteTranslatable(row.Text) {
			continue
		}
		translated, origin := value(row.OwnZH), "reused"
		if translated == "" {
			origin = "model"
			answers, err := s.translateQuotesModel(ctx, row.TweetID, row.Text)
			if err != nil {
				if isPaused(err) {
					return stored, err
				}
				continue
			}
			if len(answers) != 1 {
				continue
			}
			translated = strings.TrimSpace(answers[0])
		}
		if translated == "" {
			continue
		}
		changed, err := s.repository.SaveQuoteTranslation(ctx, QuoteTranslation{TweetID: row.TweetID, TextHash: hash, TextZH: translated, Origin: origin})
		if err != nil {
			return stored, err
		}
		if changed {
			stored++
		}
	}
	return stored, nil
}

func (s *Service) translateQuotesModel(ctx context.Context, tweetID, text string) ([]string, error) {
	user, err := javascriptJSON(struct {
		Segments []string `json:"segments"`
	}{Segments: []string{text}})
	if err != nil {
		return nil, err
	}
	maxTokens := min(4000, int(math.Ceil(float64(utf16Length(text))*1.5))+200)
	result, err := s.models.Call(ctx, modelcall.Request{
		Capability: "translate", Purpose: "translate_quoted", Subject: "quote:" + tweetID, PromptVersion: s.prompts.Version,
		System: s.prompts.Post, User: user, Temperature: floatPointer(0.2), MaxTokens: maxTokens, Timeout: 120 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	var response output
	if err := json.Unmarshal([]byte(extractJSON(result.Completion.Text)), &response); err != nil || response.Translations == nil {
		rejectErr := s.models.Reject(ctx, result.ReceiptID, "unusable output: quoted translation response was invalid")
		if err != nil {
			return nil, errors.Join(errors.New("quoted translation response was invalid"), rejectErr)
		}
		return nil, errors.Join(errors.New("quoted translation response omitted t array"), rejectErr)
	}
	return response.Translations, nil
}

func isChinese(language *string, sample string) bool {
	lang := value(language)
	if lang == "zh" {
		return true
	}
	for _, current := range []rune(truncateUTF16(sample, 400)) {
		if isCommonHan(current) && lang != "en" {
			return true
		}
	}
	return false
}

func quoteTranslatable(text string) bool {
	words := collapseWhitespace(withoutURLs(text, " "))
	letters := 0
	for _, current := range words {
		if unicode.IsLetter(current) {
			letters++
		}
		if isCommonHan(current) {
			return false
		}
	}
	return utf16Length(words) >= 10 && letters >= 2
}

func isCommonHan(value rune) bool { return value >= 0x4e00 && value <= 0x9fff }

func collapseWhitespace(value string) string { return strings.Join(strings.Fields(value), " ") }

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func javascriptJSON(value any) (string, error) {
	var output strings.Builder
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(output.String(), "\n"), nil
}

func extractJSON(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "```") {
		value = strings.TrimPrefix(value, "```")
		if strings.HasPrefix(strings.ToLower(value), "json") {
			value = value[4:]
		}
		value = strings.TrimSpace(strings.TrimSuffix(value, "```"))
	}
	start, end := strings.IndexByte(value, '{'), strings.LastIndexByte(value, '}')
	if start >= 0 && end >= start {
		return value[start : end+1]
	}
	return value
}

func shortError(err error, maximum int) string {
	value := []rune(err.Error())
	if len(value) > maximum {
		value = value[:maximum]
	}
	return string(value)
}

func isPaused(err error) bool {
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "disabled") || strings.Contains(value, "not configured") || strings.Contains(value, "budget")
}

func floatPointer(value float64) *float64 { return &value }

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

func innerHTML(node *xhtml.Node) string {
	doc := goquery.NewDocumentFromNode(node)
	return selectionHTML(doc.Selection)
}

func setInnerHTML(node *xhtml.Node, raw string) {
	doc, root, err := parseFragment(raw)
	if err != nil {
		return
	}
	for node.FirstChild != nil {
		node.RemoveChild(node.FirstChild)
	}
	for child := root.Get(0).FirstChild; child != nil; {
		next := child.NextSibling
		root.Get(0).RemoveChild(child)
		node.AppendChild(child)
		child = next
	}
	_ = doc
}

func htmlText(raw string) string {
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(`<div id="translation-root">` + raw + `</div>`))
	return strings.TrimSpace(doc.Find("#translation-root").Text())
}

func stripGeneratedHeadingIDs(raw string) string {
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(`<div id="translation-root">` + raw + `</div>`))
	doc.Find("#translation-root h2, #translation-root h3, #translation-root h4").RemoveAttr("id")
	return selectionHTML(doc.Find("#translation-root").First())
}
