package storydigest

import (
	"aihot/backend-go/internal/promptpath"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"aihot/backend-go/internal/modelcall"
)

const (
	PromptName     = "story-digest"
	MaxReports     = 40
	MaxTitleRunes  = 120
	MaxDigestRunes = 2000
)

type ModelClient interface {
	Call(context.Context, modelcall.Request) (modelcall.Result, error)
	Reject(context.Context, int64, string) error
}

type Repository interface {
	Story(context.Context, int64) (*Story, error)
	Reports(context.Context, int64, time.Time) ([]DigestReport, error)
	LastDigest(context.Context, int64) (*SavedDigest, error)
	Clear(context.Context, int64, int) (bool, error)
	Save(context.Context, SaveInput) (bool, error)
	StaleStories(context.Context, int, time.Time) ([]int64, error)
}

type Story struct {
	ID      int64
	Title   string
	Digest  *string
	Version int
	Origin  string
}

type DigestReport struct {
	StoryID        int64           `gorm:"column:story_id"`
	ID             string          `gorm:"column:id"`
	Title          string          `gorm:"column:title"`
	Summary        *string         `gorm:"column:summary"`
	SourceName     string          `gorm:"column:source_name"`
	FirstParty     bool            `gorm:"column:first_party"`
	At             time.Time       `gorm:"column:at"`
	FactID         int64           `gorm:"column:fact_id"`
	FactSubject    *string         `gorm:"column:fact_subject"`
	FactAction     *string         `gorm:"column:fact_action"`
	FactObject     *string         `gorm:"column:fact_object"`
	FactConditions *string         `gorm:"column:fact_conditions"`
	Evidence       *string         `gorm:"column:evidence"`
	StructuredFact json.RawMessage `gorm:"column:structured_fact;type:jsonb"`
}

type SavedDigest struct {
	ArticleIDs []string `gorm:"column:article_ids"`
	InputsHash *string  `gorm:"column:inputs_hash"`
	Digest     string   `gorm:"column:digest"`
	ReceiptID  *int64   `gorm:"column:receipt_id"`
}

type SaveInput struct {
	StoryID      int64
	Version      int
	StoryVersion int
	Title        string
	Digest       string
	Latest       string
	ArticleIDs   []string
	InputsHash   string
	ReceiptID    *int64
}

type Prompts struct {
	System  string
	Version string
}

type Config struct{ SiteName string }

type Service struct {
	repository Repository
	models     ModelClient
	prompts    Prompts
	now        func() time.Time
}

type DigestOutput struct {
	Title  string `json:"title"`
	Digest string `json:"digest"`
}

type Result struct {
	Updated bool `json:"updated"`
	Version int  `json:"version,omitempty"`
}

func LoadPrompts() (Prompts, error) {
	directory := promptpath.Configured()
	if directory != "" {
		return readPrompts(directory)
	}
	for _, candidate := range []string{filepath.Join("backend-go", "config", "prompts"), filepath.Join("config", "prompts"), filepath.Join("..", "..", "config", "prompts"), filepath.Join("..", "backend-go", "config", "prompts"), filepath.Join("/app", "backend-go", "config", "prompts")} {
		prompts, err := readPrompts(candidate)
		if err == nil {
			return prompts, nil
		}
	}
	return Prompts{}, errors.New("industry story digest prompt was not found")
}

func readPrompts(directory string) (Prompts, error) {
	used := make(map[string]string)
	text, err := expandPrompt(directory, PromptName, nil, used)
	if err != nil {
		return Prompts{}, err
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		_, _ = hash.Write([]byte(name + "\n" + used[name] + "\n"))
	}
	return Prompts{System: text, Version: PromptName + "@" + hex.EncodeToString(hash.Sum(nil))[:10]}, nil
}

var promptInclude = regexp.MustCompile(`\{\{>\s*([A-Za-z][\w.-]*)\s*\}\}`)

func expandPrompt(directory, name string, stack []string, used map[string]string) (string, error) {
	for _, parent := range stack {
		if parent == name {
			return "", fmt.Errorf("story digest prompt include cycle: %s", strings.Join(append(stack, name), " -> "))
		}
	}
	data, err := os.ReadFile(filepath.Join(directory, name+".md"))
	if err != nil {
		return "", err
	}
	raw := string(data)
	used[name] = raw
	var includeErr error
	expanded := promptInclude.ReplaceAllStringFunc(raw, func(token string) string {
		match := promptInclude.FindStringSubmatch(token)
		included, err := expandPrompt(directory, match[1], append(stack, name), used)
		if err != nil {
			includeErr = err
			return ""
		}
		return included
	})
	return expanded, includeErr
}

func NewService(repository Repository, models ModelClient, prompts Prompts, config Config) *Service {
	return &Service{repository: repository, models: models, prompts: Prompts{
		System: strings.ReplaceAll(prompts.System, "{{siteName}}", config.SiteName), Version: prompts.Version,
	}, now: time.Now}
}

func (s *Service) Compose(ctx context.Context, storyID int64, rewrite bool) (Result, error) {
	story, err := s.repository.Story(ctx, storyID)
	if err != nil || story == nil {
		return Result{}, err
	}
	if story.Origin == "" { // Older test fixtures and imported rows may omit the optional projection value.
		story.Origin = "model"
	}
	now := s.now()
	reports, err := s.repository.Reports(ctx, storyID, now)
	if err != nil {
		return Result{}, err
	}
	if len(reports) == 0 {
		updated, err := s.repository.Clear(ctx, storyID, story.Version)
		return Result{Updated: updated}, err
	}
	sort.SliceStable(reports, func(i, j int) bool { return reports[i].At.Before(reports[j].At) })
	ids := articleIDs(reports)
	inputHash, err := DigestInputsHash(reports)
	if err != nil {
		return Result{}, err
	}
	last, err := s.repository.LastDigest(ctx, storyID)
	if err != nil {
		return Result{}, err
	}
	sameReports := last != nil && equalStrings(sortedCopy(last.ArticleIDs), ids)
	if last != nil && sameReports && last.InputsHash != nil && *last.InputsHash == inputHash && !rewrite {
		if story.Digest != nil {
			return Result{Updated: false}, nil
		}
		return s.commit(ctx, story, reports, ids, inputHash, DigestOutput{Digest: last.Digest}, last.ReceiptID)
	}
	corrected := rewrite
	var knownIDs []string
	if last != nil {
		knownIDs = last.ArticleIDs
		dropped := false
		for _, id := range last.ArticleIDs {
			if !contains(ids, id) {
				dropped = true
				break
			}
		}
		corrected = corrected || sameReports || dropped
	}
	request, _, err := BuildInput(*story, reports, corrected, knownIDs)
	if err != nil {
		return Result{}, err
	}
	if s.models == nil {
		return Result{}, errors.New("story digest model client is not configured")
	}
	modelResult, err := s.models.Call(ctx, modelcall.Request{
		Capability: "digest", Purpose: "story_digest", Subject: fmt.Sprintf("story:%d@%d", storyID, len(ids)),
		PromptVersion: s.prompts.Version, System: s.prompts.System, User: request,
		Temperature: floatPointer(0.3), MaxTokens: 1200,
	})
	if err != nil {
		return Result{}, err
	}
	output, err := parseOutput(modelResult.Completion.Text)
	if err != nil {
		_ = s.models.Reject(context.WithoutCancel(ctx), modelResult.ReceiptID, "unusable story digest output")
		return Result{}, err
	}
	return s.commit(ctx, story, reports, ids, inputHash, output, &modelResult.ReceiptID)
}

func (s *Service) commit(ctx context.Context, story *Story, reports []DigestReport, ids []string, inputHash string, output DigestOutput, receiptID *int64) (Result, error) {
	latest := reports[len(reports)-1].Title
	version := story.Version + 1
	updated, err := s.repository.Save(ctx, SaveInput{
		StoryID: story.ID, Version: version, StoryVersion: story.Version, Title: output.Title,
		Digest: output.Digest, Latest: latest, ArticleIDs: ids, InputsHash: inputHash, ReceiptID: receiptID,
	})
	if err != nil || !updated {
		return Result{Updated: false}, err
	}
	return Result{Updated: true, Version: version}, nil
}

func (s *Service) Scan(ctx context.Context, limit int, enqueue func(context.Context, int64) error) error {
	if limit <= 0 {
		limit = 100
	}
	ids, err := s.repository.StaleStories(ctx, limit, s.now())
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := enqueue(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func BuildInput(story Story, reports []DigestReport, corrected bool, known []string) (string, string, error) {
	knownIDs := make(map[string]struct{}, len(known))
	for _, id := range known {
		knownIDs[id] = struct{}{}
	}
	start := 0
	if len(reports) > MaxReports {
		start = len(reports) - MaxReports
	}
	lines := make([]string, 0, len(reports)-start)
	for _, report := range reports[start:] {
		prefix := ""
		if !corrected {
			if _, ok := knownIDs[report.ID]; !ok {
				prefix = "【新】"
			}
		}
		evidence, err := jsonWithoutHTML(digestFactEvidence(report))
		if err != nil {
			return "", "", err
		}
		at := report.At.UTC()
		local := at.Add(8 * time.Hour)
		line := fmt.Sprintf("%s报道 %s｜事实 %d｜%s %s｜%s%s｜%s｜%s\n事实条件与来源证据：%s",
			prefix, report.ID, report.FactID, local.Format("2006-01-02"), local.Format("15:04"), report.SourceName,
			firstPartySuffix(report.FirstParty), report.Title, deref(report.Summary), evidence)
		lines = append(lines, line)
	}
	latest := reports[len(reports)-1].Title
	if corrected {
		return fmt.Sprintf("事件当前标题：%s\n\n报道内容或事实证据经过更正。请只依据下面这些报道的当前内容重写综述，不要沿用以前版本的说法。\n报道（按时间）：\n%s", story.Title, strings.Join(lines, "\n")), latest, nil
	}
	previous := ""
	if story.Digest != nil && *story.Digest != "" {
		previous = "上一版综述：" + *story.Digest + "\n"
	}
	return fmt.Sprintf("事件当前标题：%s\n%s\n报道（按时间，标【新】的是上一版之后的新报道）：\n%s", story.Title, previous, strings.Join(lines, "\n")), latest, nil
}

func DigestInputsHash(reports []DigestReport) (string, error) {
	sorted := append([]DigestReport(nil), reports...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	rows := make([]any, 0, len(sorted))
	for _, report := range sorted {
		rows = append(rows, []any{report.ID, report.FactID, report.Title, deref(report.Summary), report.SourceName,
			report.FirstParty, report.At.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"), digestFactEvidence(report)})
	}
	encoded, err := canonicalJSON(rows)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

type factCondition struct {
	Text  string `json:"text"`
	Quote string `json:"quote"`
}

type factValue struct {
	Evidence   *string         `json:"evidence"`
	Conditions []factCondition `json:"conditions"`
}

type evidenceValue struct {
	Subject             *string         `json:"subject"`
	Action              *string         `json:"action"`
	Object              *string         `json:"object"`
	Conditions          *string         `json:"conditions"`
	Evidence            *string         `json:"evidence"`
	ExtractedConditions []factCondition `json:"extractedConditions"`
	ExtractedEvidence   *string         `json:"extractedEvidence"`
}

func digestFactEvidence(report DigestReport) evidenceValue {
	var fact factValue
	if len(report.StructuredFact) > 0 && string(report.StructuredFact) != "null" {
		_ = json.Unmarshal(report.StructuredFact, &fact)
	}
	conditions := make([]factCondition, 0, len(fact.Conditions))
	for _, condition := range fact.Conditions {
		if strings.TrimSpace(condition.Text) != "" && strings.TrimSpace(condition.Quote) != "" {
			conditions = append(conditions, condition)
		}
	}
	return evidenceValue{Subject: report.FactSubject, Action: report.FactAction, Object: report.FactObject,
		Conditions: report.FactConditions, Evidence: report.Evidence, ExtractedConditions: conditions, ExtractedEvidence: fact.Evidence}
}

func parseOutput(raw string) (DigestOutput, error) {
	text := strings.TrimSpace(raw)
	if strings.HasPrefix(text, "```") {
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			text = strings.TrimSpace(text[newline+1:])
		}
		text = strings.TrimSpace(strings.TrimSuffix(text, "```"))
	}
	var output DigestOutput
	if err := json.Unmarshal([]byte(text), &output); err != nil {
		return DigestOutput{}, errors.New("model returned invalid story digest JSON")
	}
	if utf16Length(output.Title) > MaxTitleRunes || utf16Length(output.Digest) < 10 || utf16Length(output.Digest) > MaxDigestRunes {
		return DigestOutput{}, errors.New("model returned invalid story digest fields")
	}
	return output, nil
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return jsonWithoutHTML(normalized)
}

func jsonWithoutHTML(value any) ([]byte, error) {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(builder.String(), "\n")), nil
}

func articleIDs(reports []DigestReport) []string {
	ids := make([]string, len(reports))
	for i, report := range reports {
		ids[i] = report.ID
	}
	sort.Strings(ids)
	return ids
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func firstPartySuffix(firstParty bool) string {
	if firstParty {
		return "（一手）"
	}
	return ""
}
func floatPointer(value float64) *float64 { return &value }
func utf16Length(value string) int        { return len(utf16.Encode([]rune(value))) }
