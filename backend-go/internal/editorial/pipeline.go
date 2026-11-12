package editorial

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
	"aihot/backend-go/internal/receipts"
)

type ModelClient interface {
	Call(context.Context, modelcall.Request) (modelcall.Result, error)
	ResolveKey(context.Context, string) (string, error)
	SupportsVision(context.Context, string) (bool, error)
	Reject(context.Context, int64, string) error
}

type ImageLoader interface {
	FirstImage(context.Context, Input) (string, error)
}

type PromptVersions struct {
	Prefilter  string
	Score      string
	Understand string
	Summarize  string
	Structure  string
	Selection  string
	Analysis   string
}

type PromptSystems struct {
	Prefilter  string
	Score      string
	Understand string
	Structure  string
}

type AnalysisOptions struct {
	AttemptTag string
	ScoreModel string
}

type AnalysisService struct {
	models       ModelClient
	prompts      *PromptPack
	vocabulary   Vocabulary
	categories   []interests.Category
	categoryKeys []string
	entities     map[string]interests.Entity
	selection    SelectionRules
	images       ImageLoader
	now          func() time.Time
	Systems      PromptSystems
	Versions     PromptVersions
}

func NewAnalysisService(models ModelClient, prompts *PromptPack, vocabulary Vocabulary, categories []interests.Category,
	entityEntries []interests.EntityEntry, selection SelectionRules, images ImageLoader, now func() time.Time) (*AnalysisService, error) {
	if models == nil || prompts == nil || len(categories) == 0 || len(selection.Thresholds) == 0 {
		return nil, errors.New("editorial analysis dependencies are incomplete")
	}
	if now == nil {
		now = time.Now
	}
	entities := make(map[string]interests.Entity, len(entityEntries))
	for _, entry := range entityEntries {
		entities[entry.ID] = entry.Entity
	}
	categoryKeys := make([]string, len(categories))
	for index, category := range categories {
		categoryKeys[index] = category.Key
	}
	prefilter, err := prompts.Render("prefilter", nil)
	if err != nil {
		return nil, err
	}
	score, err := prompts.Render("selection-score", nil)
	if err != nil {
		return nil, err
	}
	understand, err := prompts.Render("understand", nil)
	if err != nil {
		return nil, err
	}
	structure, err := vocabulary.StructureSystem(prompts, categories, entityEntries)
	if err != nil {
		return nil, err
	}
	versions := PromptVersions{}
	if versions.Prefilter, err = prompts.Version("prefilter"); err != nil {
		return nil, err
	}
	if versions.Score, err = prompts.Version("selection-score"); err != nil {
		return nil, err
	}
	if versions.Understand, err = prompts.Version("understand"); err != nil {
		return nil, err
	}
	if versions.Summarize, err = prompts.Version("summarize-article", "summarize-article-empty", "summarize-short-post", "summarize-short-post-quoted", "summarize-long-post", "summarize-long-post-quoted", "identity-context"); err != nil {
		return nil, err
	}
	if versions.Structure, err = prompts.Version("structure"); err != nil {
		return nil, err
	}
	versions.Selection = versions.Prefilter + "+" + versions.Score
	versions.Analysis = strings.Join([]string{versions.Prefilter, versions.Score, versions.Understand, versions.Summarize, versions.Structure}, "+")
	return &AnalysisService{
		models: models, prompts: prompts, vocabulary: vocabulary, categories: categories, categoryKeys: categoryKeys,
		entities: entities, selection: selection, images: images, now: now,
		Systems: PromptSystems{Prefilter: prefilter, Score: score, Understand: understand, Structure: structure}, Versions: versions,
	}, nil
}

func (s *AnalysisService) Run(ctx context.Context, input Input, options AnalysisOptions) (AnalysisRun, error) {
	prefilter, err := s.runPrefilter(ctx, input, options)
	if err != nil {
		return AnalysisRun{}, err
	}
	run := AnalysisRun{Prefilter: prefilter}
	if prefilter.Label == "BLOCK" {
		return run, nil
	}
	if err := ctx.Err(); err != nil {
		return AnalysisRun{}, err
	}
	structureChannel := make(chan structureOutcome, 1)
	go func() {
		structure, err := s.runStructure(ctx, input, options)
		structureChannel <- structureOutcome{value: structure, err: err}
	}()
	structureResolved := false
	getStructure := func() (*NormalizedStructure, error) {
		if structureResolved {
			return run.Structure, nil
		}
		result := <-structureChannel
		structureResolved = true
		run.Structure = result.value
		return result.value, result.err
	}
	defer func() {
		if !structureResolved {
			<-structureChannel
		}
	}()
	scores, scoreErr := s.runScores(ctx, input, options)
	if scoreErr != nil {
		_, structureErr := getStructure()
		if structureErr != nil {
			return AnalysisRun{}, errors.Join(scoreErr, structureErr)
		}
		return AnalysisRun{}, scoreErr
	}
	run.Scores = scores
	if err := ctx.Err(); err != nil {
		_, _ = getStructure()
		return AnalysisRun{}, err
	}
	structure, structureErr := getStructure()
	if structureErr != nil {
		return AnalysisRun{}, structureErr
	}
	run.Structure = structure
	near := false
	if scores != nil && !scores.Refused && len(scores.Values) == 2 {
		sum := scores.Values[0] + scores.Values[1]
		near = sum >= scores.Threshold*2 || sum > s.selection.UnderstandFloor*2
	}
	if near {
		writing, err := s.runUnderstand(ctx, input, options)
		if err != nil {
			return AnalysisRun{}, err
		}
		if writing != nil {
			run.Writing = writing
		}
	}
	if run.Writing == nil {
		writing, err := s.runSummarize(ctx, input, options)
		if err != nil {
			return AnalysisRun{}, err
		}
		run.Writing = &writing
	}
	return run, nil
}

type structureOutcome struct {
	value *NormalizedStructure
	err   error
}

func (s *AnalysisService) runPrefilter(ctx context.Context, input Input, options AnalysisOptions) (PrefilterResult, error) {
	result, err := s.call(ctx, modelcall.Request{
		Capability: "prefilter", Purpose: "prefilter_article", Subject: articleSubject(input), PromptVersion: s.Versions.Prefilter,
		System: s.Systems.Prefilter, User: PrefilterUser(input), Temperature: floatPointer(0), MaxTokens: 512,
		AttemptTag: options.AttemptTag,
	})
	if err != nil {
		return PrefilterResult{}, err
	}
	parsed, err := ParsePrefilter(result.Completion.Text)
	if err != nil {
		return PrefilterResult{}, s.rejectOutput(ctx, result, result.Model, articleSubject(input), err)
	}
	if parsed.Label == "BLOCK" && MissingEvidence(input) {
		parsed.Label = "UNKNOWN"
	}
	return PrefilterResult{Label: parsed.Label, Reason: parsed.Reason, Model: result.Model, ReceiptID: result.ReceiptID, Reused: result.Reused}, nil
}

func (s *AnalysisService) runScores(ctx context.Context, input Input, options AnalysisOptions) (*ScoreResult, error) {
	threshold, ok := s.selection.Threshold(input.Source.Tier)
	if !ok {
		return nil, nil
	}
	model := options.ScoreModel
	if model == "" {
		resolved, err := s.models.ResolveKey(ctx, "score")
		if err != nil {
			return nil, err
		}
		model = resolved
	}
	call := scoreCallConfig(model)
	result := &ScoreResult{Model: model, Threshold: threshold, Values: []int{}, ReceiptIDs: []int64{}, Reused: true}
	user := BuildScoreInput(input)
	for index := 0; index < 2; index++ {
		request := modelcall.Request{
			Capability: "score", ModelKey: model, Purpose: "score_article", Subject: articleSubject(input), PromptVersion: s.Versions.Score,
			System: s.Systems.Score, User: user, Temperature: floatPointer(call.temperature), MaxTokens: call.maxTokens,
			Timeout: call.timeout, AttemptTag: stepAttemptTag(options.AttemptTag, fmt.Sprintf("score-%d", index+1)),
		}
		response, err := s.call(ctx, request)
		if err != nil {
			if contentFilter(err) {
				result.Refused = true
				result.Reused = false
				return result, nil
			}
			return nil, err
		}
		value, err := ParseScore(response.Completion.Text)
		if err != nil {
			return nil, s.rejectOutput(ctx, response, response.Model, articleSubject(input), err)
		}
		result.ReceiptIDs = append(result.ReceiptIDs, response.ReceiptID)
		result.Values = append(result.Values, value)
		result.Reused = result.Reused && response.Reused
	}
	return result, nil
}

func (s *AnalysisService) runStructure(ctx context.Context, input Input, options AnalysisOptions) (*NormalizedStructure, error) {
	result, err := s.call(ctx, modelcall.Request{
		Capability: "structure", Purpose: "structure_article", Subject: articleSubject(input), PromptVersion: s.Versions.Structure,
		System: s.Systems.Structure, User: BuildMaterial(input), Temperature: floatPointer(0.2), MaxTokens: 1200,
		AttemptTag: stepAttemptTag(options.AttemptTag, "structure"),
	})
	if err != nil {
		return nil, err
	}
	structure, err := ParseStructure(result.Completion.Text, s.categoryKeys)
	if err != nil {
		return nil, s.rejectOutput(ctx, result, result.Model, articleSubject(input), err)
	}
	normalized := s.vocabulary.NormalizeStructure(structure, input, s.categories, s.entities)
	normalized.Model = result.Model
	normalized.ReceiptID = result.ReceiptID
	normalized.Reused = result.Reused
	return &normalized, nil
}

func (s *AnalysisService) runUnderstand(ctx context.Context, input Input, options AnalysisOptions) (*WritingResult, error) {
	writingInput := writingInputOf(input)
	user := UnderstandUser(input)
	var images []string
	vision, err := s.models.SupportsVision(ctx, "understand")
	if err != nil {
		return nil, err
	}
	if vision && s.images != nil {
		if image, err := s.images.FirstImage(ctx, input); err == nil && image != "" {
			images = []string{image}
		}
	}
	call := func(withImage bool) (modelcall.Result, UnderstandOutput, error) {
		request := modelcall.Request{
			Capability: "understand", Purpose: "understand_article", Subject: articleSubject(input), PromptVersion: s.Versions.Understand,
			System: s.Systems.Understand, User: user, Temperature: floatPointer(0.2), MaxTokens: 16_384,
			Timeout: 180 * time.Second, AttemptTag: stepAttemptTag(options.AttemptTag, "understand"),
		}
		if withImage {
			request.Images = images
		}
		result, err := s.call(ctx, request)
		if err != nil {
			return modelcall.Result{}, UnderstandOutput{}, err
		}
		parsed, err := ParseUnderstand(result.Completion.Text, s.vocabulary)
		if err != nil {
			return modelcall.Result{}, UnderstandOutput{}, s.rejectOutput(ctx, result, result.Model, articleSubject(input), err)
		}
		return result, parsed, nil
	}
	result, parsed, err := call(len(images) > 0)
	if err != nil {
		if contentFilter(err) {
			return nil, nil
		}
		var rejected receipts.ProviderRejection
		if len(images) == 0 || !errors.As(err, &rejected) || !rejected.ProviderRejected() {
			return nil, err
		}
		result, parsed, err = call(false)
		if err != nil {
			if contentFilter(err) {
				return nil, nil
			}
			return nil, err
		}
	}
	copy := finalizeCopy(s.vocabulary, writingInput, parsed.TitleZh, parsed.SummaryZh)
	var modelName *string
	if result.Model != "" {
		modelName = stringPointer(result.Model)
	}
	reason := strings.TrimSpace(parsed.EditorialJudgment)
	var reasonPointer *string
	if reason != "" {
		reasonPointer = &reason
	}
	itemType, authorRole := parsed.ItemType, parsed.AuthorRole
	return &WritingResult{
		Kind: "understand", Model: modelName, TitleZh: copy.TitleZh, SummaryZh: copy.SummaryZh, ReasonZh: reasonPointer,
		ItemType: &itemType, AuthorRole: &authorRole, IdentityGuard: &copy.IdentityGuard,
		ReceiptIDs: []int64{result.ReceiptID}, Reused: result.Reused,
	}, nil
}

func (s *AnalysisService) runSummarize(ctx context.Context, input Input, options AnalysisOptions) (WritingResult, error) {
	writingInput := writingInputOf(input)
	short := isShortTweetInput(writingInput)
	main := collapseWhitespace(firstNonEmpty(writingInput.MainText, writingInput.Title))
	plain := WritingResult{ReasonZh: nil, ReceiptIDs: []int64{}, Reused: true}
	if short && !needsShortTweetTranslation(main) {
		plain.Kind, plain.TitleZh, plain.SummaryZh = "verbatim", main, main
		return plain, nil
	}
	if !short && utf16Length(strings.TrimSpace(writingInput.Text)) < 20 {
		plain.Kind = "none"
		if looksChinese(input.Title) {
			plain.TitleZh = input.Title
		}
		return plain, nil
	}
	var user string
	var err error
	if short {
		user, err = s.vocabulary.BuildShortTweetPrompt(s.prompts, writingInput)
	} else if writingInput.SourceKind == "x_search" {
		user, err = s.vocabulary.BuildLongTweetPrompt(s.prompts, writingInput)
	} else {
		user, err = s.vocabulary.BuildArticlePrompt(s.prompts, writingInput, s.now())
	}
	if err != nil {
		return WritingResult{}, err
	}
	result, err := s.call(ctx, modelcall.Request{
		Capability: "summarize", Purpose: "summarize_article", Subject: articleSubject(input), PromptVersion: s.Versions.Summarize,
		User: user, Temperature: floatPointer(0.2), MaxTokens: 2048, AttemptTag: stepAttemptTag(options.AttemptTag, "summarize"), DisableJSON: true,
	})
	if err != nil {
		return WritingResult{}, err
	}
	parsed := ParseTranslateOutput(result.Completion.Text)
	title, summary := parsed.TitleZh, parsed.SummaryZh
	switch {
	case short:
		if title == "" && looksChinese(main) {
			title = main
		}
		summary = firstNonEmpty(parsed.BodyZh, parsed.SummaryZh)
	case writingInput.SourceKind == "x_search":
		summary = firstNonEmpty(parsed.SummaryZh, parsed.BodyZh)
	default:
		if title == "" && looksChinese(writingInput.Title) {
			title = writingInput.Title
		}
	}
	copy := finalizeCopy(s.vocabulary, writingInput, title, summary)
	return WritingResult{Kind: "summarize", Model: stringPointer(result.Model), TitleZh: copy.TitleZh, SummaryZh: copy.SummaryZh,
		IdentityGuard: &copy.IdentityGuard, ReceiptIDs: []int64{result.ReceiptID}, Reused: result.Reused}, nil
}

func (s *AnalysisService) call(ctx context.Context, request modelcall.Request) (modelcall.Result, error) {
	result, err := s.models.Call(ctx, request)
	if err != nil {
		return modelcall.Result{}, err
	}
	return result, nil
}

func (s *AnalysisService) rejectOutput(ctx context.Context, result modelcall.Result, model, subject string, cause error) error {
	reason := "unusable output: " + cause.Error()
	if len(reason) > 500 {
		reason = reason[:500]
	}
	if err := s.models.Reject(ctx, result.ReceiptID, reason); err != nil {
		return err
	}
	return &modelcall.OutputError{ReceiptID: result.ReceiptID, Message: fmt.Sprintf("Model %s returned unusable output for %s: %s", model, subject, cause.Error())}
}

type scoreCallSettings struct {
	temperature float64
	maxTokens   int
	timeout     time.Duration
}

func scoreCallConfig(model string) scoreCallSettings {
	if model == "glm-5.3-flash-selection" {
		return scoreCallSettings{temperature: 1, maxTokens: 65_536, timeout: 180 * time.Second}
	}
	return scoreCallSettings{temperature: 0.2, maxTokens: 1024, timeout: 120 * time.Second}
}

func contentFilter(err error) bool {
	var providerError *provider.Error
	if !errors.As(err, &providerError) || providerError.Retryable {
		return false
	}
	return regexp.MustCompile(`contentFilter|"1301"`).MatchString(providerError.Message)
}

func articleSubject(input Input) string {
	return fmt.Sprintf("article:%s@%d", input.ID, input.Revision)
}

func stepAttemptTag(attempt, step string) string {
	if attempt == "" {
		return ""
	}
	return attempt + ":" + step
}

func floatPointer(value float64) *float64 { return &value }
