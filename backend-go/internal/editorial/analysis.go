package editorial

import (
	"strings"

	"aihot/backend-go/internal/interests"
)

type PrefilterResult struct {
	Label     string
	Reason    string
	Model     string
	ReceiptID int64
	Reused    bool
}

type ScoreResult struct {
	Model      string
	Threshold  int
	Values     []int
	ReceiptIDs []int64
	Reused     bool
	Refused    bool
}

type WritingResult struct {
	Kind          string
	Model         *string
	TitleZh       string
	SummaryZh     string
	ReasonZh      *string
	ItemType      *string
	AuthorRole    *string
	IdentityGuard *IdentityGuard
	ReceiptIDs    []int64
	Reused        bool
}

type AnalysisRun struct {
	Prefilter PrefilterResult
	Scores    *ScoreResult
	Writing   *WritingResult
	Structure *NormalizedStructure
}

type AnalysisOutput struct {
	Relevance    string
	Selected     bool
	Score        *int
	Scores       []int
	ScoreModel   *string
	ScoreRefused bool
	Threshold    *int
	Category     *string
	Tags         []string
	Subjects     []string
	TitleZh      string
	SummaryZh    string
	ReasonZh     *string
	Scope        string
	Fact         *Fact
}

func NormalizeAnalysis(run AnalysisRun, entities map[string]interests.Entity) AnalysisOutput {
	label := run.Prefilter.Label
	title := ""
	summary := ""
	var reason *string
	if run.Writing != nil {
		title = collapseWhitespace(run.Writing.TitleZh)
		summary = strings.TrimSpace(run.Writing.SummaryZh)
		reason = run.Writing.ReasonZh
	}
	relevance := "pass"
	if label == "BLOCK" {
		relevance = "block"
	} else if run.Writing != nil && (title == "" || summary == "") {
		relevance = "unknown"
	}
	var values []int
	var sum *int
	var scoreModel *string
	var threshold *int
	scoreRefused := false
	if run.Scores != nil {
		scoreModel = stringPointer(run.Scores.Model)
		threshold = intPointer(run.Scores.Threshold)
		scoreRefused = run.Scores.Refused
		if !run.Scores.Refused && len(run.Scores.Values) == 2 {
			values = append([]int{}, run.Scores.Values...)
			total := values[0] + values[1]
			sum = &total
		}
	}
	selected := relevance == "pass" && sum != nil && threshold != nil && *sum >= 2*(*threshold)
	var score *int
	if sum != nil {
		value := *sum / 2
		score = &value
	}
	var category *string
	tags := []string{}
	subjects := []string{}
	scope := "unknown"
	var fact *Fact
	if run.Structure != nil {
		category = run.Structure.Category
		tags = append(tags, run.Structure.Tags...)
		subjects = append(subjects, run.Structure.Subjects...)
		scope = run.Structure.Scope
		fact = run.Structure.Fact
		for _, subject := range subjects {
			entity := entities[subject]
			if entity.DisplayTag == nil || *entity.DisplayTag == "" || contains(tags, *entity.DisplayTag) {
				continue
			}
			tags = append(tags, *entity.DisplayTag)
		}
	}
	return AnalysisOutput{
		Relevance: relevance, Selected: selected, Score: score, Scores: values, ScoreModel: scoreModel,
		ScoreRefused: scoreRefused, Threshold: threshold, Category: category, Tags: tags, Subjects: subjects,
		TitleZh: title, SummaryZh: summary, ReasonZh: reason, Scope: scope, Fact: fact,
	}
}

func intPointer(value int) *int { return &value }
