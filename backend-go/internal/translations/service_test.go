package translations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
)

type translationRepository struct {
	article     *Article
	due         []string
	saved       *Translation
	attempts    []attemptRecord
	quotes      []QuoteCandidate
	savedQuotes []QuoteTranslation
}

type attemptRecord struct {
	articleID string
	revision  int
	outcome   string
	reason    *string
}

func (r *translationRepository) DueArticles(context.Context, int) ([]string, error) {
	return r.due, nil
}
func (r *translationRepository) Article(context.Context, string) (*Article, error) {
	return r.article, nil
}
func (r *translationRepository) SaveTranslation(_ context.Context, value Translation) (bool, error) {
	r.saved = &value
	return true, nil
}
func (r *translationRepository) RecordAttempt(_ context.Context, articleID string, revision int, outcome string, reason *string) error {
	r.attempts = append(r.attempts, attemptRecord{articleID: articleID, revision: revision, outcome: outcome, reason: reason})
	return nil
}
func (r *translationRepository) QuoteCandidates(context.Context, int) ([]QuoteCandidate, error) {
	return r.quotes, nil
}
func (r *translationRepository) SaveQuoteTranslation(_ context.Context, quote QuoteTranslation) (bool, error) {
	r.savedQuotes = append(r.savedQuotes, quote)
	return true, nil
}

type translationModel struct {
	calls    []modelcall.Request
	rejectID int64
	err      error
	answer   func(modelcall.Request) string
}

func (m *translationModel) Call(_ context.Context, request modelcall.Request) (modelcall.Result, error) {
	m.calls = append(m.calls, request)
	if m.err != nil {
		return modelcall.Result{}, m.err
	}
	answer := `{"t":[]}`
	if m.answer != nil {
		answer = m.answer(request)
	}
	return modelcall.Result{ReceiptID: int64(len(m.calls) + 40), Completion: provider.Completion{Text: answer}}, nil
}
func (m *translationModel) Reject(_ context.Context, receiptID int64, _ string) error {
	m.rejectID = receiptID
	return nil
}

func testPrompts() PromptFiles {
	return PromptFiles{Body: "body {{siteName}}", Post: "post {{siteName}}"}
}

func TestPromptVersionMatchesTypeScriptContentHash(t *testing.T) {
	files := testPrompts()
	version := promptVersion(files)
	hash := sha256.Sum256([]byte("translate-body\n" + files.Body + "\ntranslate-post\n" + files.Post + "\n"))
	want := "translate-body+translate-post@" + hex.EncodeToString(hash[:])[:10]
	if version != want {
		t.Fatalf("prompt version = %q, want %q", version, want)
	}
	service := NewService(&translationRepository{}, &translationModel{}, files, "My Site")
	if service.prompts.Body != "body My Site" || service.prompts.Post != "post My Site" || service.prompts.Version != want {
		t.Fatalf("prompt pack = %#v", service.prompts)
	}
}

func TestShieldRestoresEveryLinkAndMediaExactlyOnce(t *testing.T) {
	original := `<strong>Hello <a href="https://example.com/a" title="keep">world</a></strong><img src="https://example.com/chart.png" alt="chart"><code>print(1)</code>`
	protected, err := shieldHTML(original)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(protected.HTML, `<a id="L0">world</a>`) || !strings.Contains(protected.HTML, "⟦0⟧") || !strings.Contains(protected.HTML, "⟦1⟧") {
		t.Fatalf("shielded fragment = %s", protected.HTML)
	}
	translated, ok := unshieldHTML(`译文 <a id="L0">世界</a> ⟦1⟧ ⟦0⟧`, protected)
	if !ok || !strings.Contains(translated, `href="https://example.com/a" title="keep"`) || !strings.Contains(translated, "chart.png") || !strings.Contains(translated, "print(1)") {
		t.Fatalf("restored fragment = %q, valid=%t", translated, ok)
	}
	if _, ok := unshieldHTML(`译文 <a id="L0">世界</a> ⟦0⟧ ⟦0⟧`, protected); ok {
		t.Fatal("repeated media placeholder was accepted")
	}
	if _, ok := unshieldHTML(`译文 ⟦0⟧ ⟦1⟧`, protected); ok {
		t.Fatal("dropped link was accepted")
	}
}

func TestBodyTranslationKeepsMediaAndLinkAttributesAndUsesCurrentRevision(t *testing.T) {
	repository := &translationRepository{article: &Article{
		ID: "article-1", Revision: 7, Channel: "news", Language: stringPointer("en"), Title: "Original title",
		BodyText: stringPointer("An explanation."), BodyHTML: stringPointer(`<p>Explaining <a href="https://docs.example/topic" title="docs">Neuroglancer</a> today <img src="https://img.example/chart.png" alt="chart"></p>`),
		Selected: true, BodyMode: "full", Visibility: "public",
	}}
	model := &translationModel{answer: func(request modelcall.Request) string {
		var input struct {
			Segments []string `json:"segments"`
		}
		if err := json.Unmarshal([]byte(request.User), &input); err != nil {
			t.Fatal(err)
		}
		if request.Purpose != "translate_body" || request.Subject != "article:article-1@7#0" || request.AttemptTag != "" {
			t.Fatalf("translation request = %#v", request)
		}
		if len(input.Segments) != 1 || !strings.Contains(input.Segments[0], `<a id="L0">Neuroglancer</a>`) || !strings.Contains(input.Segments[0], "⟦0⟧") {
			t.Fatalf("model segment = %#v", input.Segments)
		}
		return `{"t":["介绍 <a id=\"L0\">Neuroglancer</a> 的内容 ⟦0⟧。"]}`
	}}
	service := NewService(repository, model, testPrompts(), "MyHOT")
	result, err := service.TranslateArticle(context.Background(), "article-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "translated" || result.Revision != 7 || result.Segments != 1 || repository.saved == nil || !repository.saved.Complete {
		t.Fatalf("translation result=%+v stored=%+v", result, repository.saved)
	}
	if !strings.Contains(repository.saved.BodyHTML, `href="https://docs.example/topic"`) || !strings.Contains(repository.saved.BodyHTML, `title="docs"`) || !strings.Contains(repository.saved.BodyHTML, `src="https://img.example/chart.png"`) {
		t.Fatalf("saved media/link attributes changed: %s", repository.saved.BodyHTML)
	}
	if strings.Contains(repository.saved.BodyHTML, "id=\"sec-") || !strings.Contains(repository.saved.BodyText, "Neuroglancer") {
		t.Fatalf("saved translation projection = %+v", repository.saved)
	}
}

func TestMisalignedBlockIsRetriedThenKeepsOriginalAndMarksPartial(t *testing.T) {
	repository := &translationRepository{article: &Article{
		ID: "article-2", Revision: 1, Channel: "news", Language: stringPointer("en"), Title: "Test",
		BodyText: stringPointer("two parts"), BodyHTML: stringPointer(`<p>A <a href="https://example.com/one">link the model loses</a>.</p><p>Another paragraph.</p>`),
		Selected: true, BodyMode: "full", Visibility: "public",
	}}
	model := &translationModel{}
	model.answer = func(request modelcall.Request) string {
		var input struct {
			Segments []string `json:"segments"`
		}
		if err := json.Unmarshal([]byte(request.User), &input); err != nil {
			t.Fatal(err)
		}
		values := make([]string, len(input.Segments))
		for index, segment := range input.Segments {
			if strings.Contains(segment, `id="L0"`) {
				values[index] = "丢失链接"
			} else if strings.Contains(segment, "另一个") {
				values[index] = "另一个段落。"
			} else {
				values[index] = "保留链接和文字"
			}
		}
		return jsonString(map[string]any{"t": values})
	}
	service := NewService(repository, model, testPrompts(), "MyHOT")
	result, err := service.TranslateArticle(context.Background(), "article-2")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "partial" || result.Segments != 1 || repository.saved == nil || repository.saved.Complete {
		t.Fatalf("result=%+v stored=%+v", result, repository.saved)
	}
	if !strings.Contains(repository.saved.BodyHTML, `href="https://example.com/one"`) || !strings.Contains(repository.saved.BodyHTML, "link the model loses") || !strings.Contains(repository.saved.BodyHTML, "保留链接和文字") {
		t.Fatalf("partial body lost original or translated text: %s", repository.saved.BodyHTML)
	}
	if len(model.calls) != 2 || model.calls[1].AttemptTag != "retry" || model.calls[1].Subject != "article:article-2@1#0" {
		t.Fatalf("model retries = %#v", model.calls)
	}
}

func TestQuoteTranslationReusesOwnCompleteTranslationWithoutModelCall(t *testing.T) {
	hash := sha256Hex("The quoted post is a long English announcement")
	repository := &translationRepository{quotes: []QuoteCandidate{{TweetID: "123", Text: "The quoted post is a long English announcement", TextHash: stringPointer("old"), OwnZH: stringPointer("已经有完整中文译文")}}}
	model := &translationModel{}
	service := NewService(repository, model, testPrompts(), "MyHOT")
	count, err := service.TranslateQuotes(context.Background(), 3, 30, time.Minute)
	if err != nil || count != 1 || len(model.calls) != 0 || len(repository.savedQuotes) != 1 {
		t.Fatalf("quote translation count=%d calls=%d saved=%+v err=%v", count, len(model.calls), repository.savedQuotes, err)
	}
	if repository.savedQuotes[0].Origin != "reused" || repository.savedQuotes[0].TextHash != hash || repository.savedQuotes[0].TextZH != "已经有完整中文译文" {
		t.Fatalf("saved quote = %+v", repository.savedQuotes[0])
	}
}

func TestCanceledRunDoesNotConsumeTranslationAttempt(t *testing.T) {
	repository := &translationRepository{due: []string{"article-3"}}
	model := &translationModel{}
	service := NewService(repository, model, testPrompts(), "MyHOT")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.ProcessPending(ctx, 30, time.Minute)
	if err != nil || len(result.Done) != 0 || len(repository.attempts) != 0 || len(model.calls) != 0 {
		t.Fatalf("canceled run=%+v attempts=%+v calls=%d err=%v", result, repository.attempts, len(model.calls), err)
	}
}

func TestFailurePauseMatcherDoesNotTreatOrdinaryProviderErrorsAsPause(t *testing.T) {
	for _, test := range []struct {
		err  error
		want bool
	}{
		{errors.New("model calls are disabled"), true},
		{errors.New("model capability is not configured"), true},
		{errors.New("budget for vendor is exhausted"), true},
		{errors.New("connection reset by peer"), false},
	} {
		if got := isPaused(test.err); got != test.want {
			t.Errorf("isPaused(%q)=%t want %t", test.err, got, test.want)
		}
	}
}

func TestPendingRunDoesNotCountDisabledModelCallsButRecordsOrdinaryFailure(t *testing.T) {
	article := &Article{
		ID: "article-pending", Revision: 4, Channel: "news", Language: stringPointer("en"), Title: "Title",
		BodyText: stringPointer("English body"), BodyHTML: stringPointer("<p>English body</p>"),
		Selected: true, BodyMode: "full", Visibility: "public",
	}
	pausedRepository := &translationRepository{article: article, due: []string{article.ID}}
	pausedModel := &translationModel{err: errors.New("model calls are disabled")}
	pausedService := NewService(pausedRepository, pausedModel, testPrompts(), "MyHOT")
	if result, err := pausedService.ProcessPending(context.Background(), 30, time.Minute); err != nil || len(result.Done) != 0 || len(pausedRepository.attempts) != 0 {
		t.Fatalf("paused run = %+v, attempts=%+v, err=%v", result, pausedRepository.attempts, err)
	}

	failedRepository := &translationRepository{article: article, due: []string{article.ID}}
	failedModel := &translationModel{err: errors.New("connection reset by peer")}
	failedService := NewService(failedRepository, failedModel, testPrompts(), "MyHOT")
	result, err := failedService.ProcessPending(context.Background(), 30, time.Minute)
	if err != nil || len(result.Done) != 1 || len(failedRepository.attempts) != 1 {
		t.Fatalf("failed run = %+v, attempts=%+v, err=%v", result, failedRepository.attempts, err)
	}
	if failedRepository.attempts[0].revision != 4 || failedRepository.attempts[0].outcome != "failed" || failedRepository.attempts[0].reason == nil {
		t.Fatalf("failed attempt = %+v", failedRepository.attempts[0])
	}
}

func stringPointer(value string) *string { return &value }

func jsonString(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestNormalizeExistingHTMLTextWithoutCollapsingParagraphSpacing(t *testing.T) {
	input := `<p>第一段</p><p>第二段</p>`
	if got, want := htmlText(input), "第一段第二段"; got != want {
		t.Fatalf("HTML text = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(bareURLSpans("see https://example.com/docs。"), [][2]int{{4, 28}}) {
		t.Fatalf("bare URL span was not cut at Chinese punctuation: %#v", bareURLSpans("see https://example.com/docs。"))
	}
}
