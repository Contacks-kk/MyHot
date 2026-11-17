package editorial

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"sort"
	"time"

	"aihot/backend-go/internal/receipts"
)

type SelectionUsageRepository interface {
	EvaluationUsage(context.Context, []int64) (receipts.EvaluationUsage, error)
}

type SelectionEvaluationCase struct {
	CaseID    string  `json:"caseId"`
	Title     string  `json:"title"`
	Stratum   *string `json:"stratum"`
	Gold      string  `json:"gold"`
	Decision  *string `json:"decision"`
	Score     *int    `json:"score"`
	Relevance *string `json:"relevance"`
	Category  *string `json:"category"`
	Reason    *string `json:"reason"`
	ReceiptID *int64  `json:"receiptId"`
	Error     *string `json:"error"`
}

type SelectionSummary struct {
	Model          string  `json:"model"`
	N              int     `json:"n"`
	Decisive       int     `json:"decisive"`
	Either         int     `json:"either"`
	Errors         int     `json:"errors"`
	TP             int     `json:"tp"`
	FP             int     `json:"fp"`
	FN             int     `json:"fn"`
	TN             int     `json:"tn"`
	Accuracy       float64 `json:"accuracy"`
	Precision      float64 `json:"precision"`
	Recall         float64 `json:"recall"`
	F1             float64 `json:"f1"`
	SelectedRate   float64 `json:"selectedRate"`
	GoldSelectRate float64 `json:"goldSelectRate"`
	receipts.EvaluationUsage
	WallSeconds int64 `json:"wallSeconds"`
}

type SelectionSweep struct {
	Threshold int     `json:"t"`
	Accuracy  float64 `json:"acc"`
	Precision float64 `json:"P"`
	Recall    float64 `json:"R"`
	F1        float64 `json:"F1"`
	Selected  float64 `json:"sel"`
}

type SelectionModelReport struct {
	Summary  SelectionSummary          `json:"summary"`
	Sweep    []SelectionSweep          `json:"sweep"`
	Mistakes []map[string]any          `json:"mistakes"`
	Cases    []SelectionEvaluationCase `json:"cases"`
}

type SelectionEvaluationMeta struct {
	Split         string    `json:"split"`
	N             int       `json:"n"`
	Seed          int64     `json:"seed"`
	PromptVersion string    `json:"promptVersion"`
	CreatedAt     time.Time `json:"createdAt"`
}

type SelectionEvaluationRun struct {
	Meta       SelectionEvaluationMeta         `json:"meta"`
	Models     map[string]SelectionModelReport `json:"-"`
	modelOrder []string
}

/** AddModel appends a completed report while preserving the requested model order. */
func (r *SelectionEvaluationRun) AddModel(name string, report SelectionModelReport) {
	if r.Models == nil {
		r.Models = make(map[string]SelectionModelReport)
	}
	if _, exists := r.Models[name]; !exists {
		r.modelOrder = append(r.modelOrder, name)
	}
	r.Models[name] = report
}

/** ModelNames returns the model order retained by the generated report. */
func (r SelectionEvaluationRun) ModelNames() []string {
	return append([]string(nil), r.modelOrder...)
}

/** MarshalJSON keeps model order stable for SelectBench's default model selection. */
func (r SelectionEvaluationRun) MarshalJSON() ([]byte, error) {
	var models bytes.Buffer
	models.WriteByte('{')
	first := true
	order := r.ModelNames()
	if len(order) == 0 {
		for name := range r.Models {
			order = append(order, name)
		}
		sort.Strings(order)
	}
	for _, name := range order {
		report, exists := r.Models[name]
		if !exists {
			continue
		}
		key, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(report)
		if err != nil {
			return nil, err
		}
		if !first {
			models.WriteByte(',')
		}
		models.Write(key)
		models.WriteByte(':')
		models.Write(value)
		first = false
	}
	models.WriteByte('}')
	meta, err := json.Marshal(r.Meta)
	if err != nil {
		return nil, err
	}
	return []byte(`{"meta":` + string(meta) + `,"models":` + models.String() + `}`), nil
}

/** UnmarshalJSON restores the original model order from an audit report. */
func (r *SelectionEvaluationRun) UnmarshalJSON(data []byte) error {
	var envelope struct {
		Meta   SelectionEvaluationMeta `json:"meta"`
		Models json.RawMessage         `json:"models"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Models))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("selection evaluation report has invalid models")
	}
	r.Meta = envelope.Meta
	r.Models = make(map[string]SelectionModelReport)
	r.modelOrder = nil
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("selection evaluation report has an invalid model key")
		}
		var report SelectionModelReport
		if err := decoder.Decode(&report); err != nil {
			return err
		}
		r.AddModel(name, report)
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return nil
}

/** BuildSelectionModelReport assembles legacy metrics and SelectBench case fields. */
func BuildSelectionModelReport(ctx context.Context, usage SelectionUsageRepository, model string, results []SelectionEvaluationResult, start, end int, elapsed time.Duration) (SelectionModelReport, error) {
	if usage == nil {
		return SelectionModelReport{}, errors.New("selection report requires usage and a valid sweep")
	}
	report := SelectionModelReport{
		Summary:  SelectionSummary{Model: model, N: len(results), WallSeconds: int64(math.Floor(elapsed.Seconds() + 0.5))},
		Sweep:    []SelectionSweep{},
		Mistakes: []map[string]any{},
		Cases:    make([]SelectionEvaluationCase, 0, len(results)),
	}
	ids := make([]int64, 0)
	seen := make(map[int64]bool)
	for _, result := range results {
		for _, id := range result.ReceiptIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		item := selectionCase(result)
		report.Cases = append(report.Cases, item)
		if result.Output == nil {
			report.Summary.Errors++
			continue
		}
		if item.Gold == "either" {
			report.Summary.Either++
			continue
		}
		switch {
		case result.Output.Selected && item.Gold == "select":
			report.Summary.TP++
		case result.Output.Selected:
			report.Summary.FP++
			report.Mistakes = append(report.Mistakes, map[string]any{
				"kind":    "FP",
				"title":   item.Title,
				"score":   item.Score,
				"reason":  item.Reason,
				"stratum": item.Stratum,
			})
		case item.Gold == "select":
			report.Summary.FN++
			report.Mistakes = append(report.Mistakes, map[string]any{
				"kind":      "FN",
				"title":     item.Title,
				"score":     item.Score,
				"relevance": item.Relevance,
				"stratum":   item.Stratum,
			})
		default:
			report.Summary.TN++
		}
	}
	metrics := selectionMetrics(report.Summary.TP, report.Summary.FP, report.Summary.FN, report.Summary.TN)
	report.Summary.Decisive = report.Summary.TP + report.Summary.FP + report.Summary.FN + report.Summary.TN
	report.Summary.Accuracy, report.Summary.Precision, report.Summary.Recall, report.Summary.F1 = metrics.Accuracy, metrics.Precision, metrics.Recall, metrics.F1
	report.Summary.SelectedRate = metrics.Selected
	report.Summary.GoldSelectRate = selectionRound(float64(report.Summary.TP+report.Summary.FN) / float64(max(1, report.Summary.Decisive)))
	var err error
	report.Summary.EvaluationUsage, err = usage.EvaluationUsage(ctx, ids)
	if err != nil {
		return SelectionModelReport{}, err
	}
	for threshold := start; threshold <= end; threshold += 2 {
		var tp, fp, fn, tn int
		for _, item := range report.Cases {
			if item.Decision == nil || item.Gold == "either" {
				continue
			}
			selected := item.Relevance != nil && *item.Relevance == "pass" && item.Score != nil && *item.Score >= threshold
			switch {
			case selected && item.Gold == "select":
				tp++
			case selected:
				fp++
			case item.Gold == "select":
				fn++
			default:
				tn++
			}
		}
		entry := selectionMetrics(tp, fp, fn, tn)
		entry.Threshold = threshold
		report.Sweep = append(report.Sweep, entry)
		if threshold == end || threshold == end-1 || threshold > end-2 {
			break
		}
	}
	return report, nil
}

/** selectionCase projects the first receipt and nullable output fields for SelectBench. */
func selectionCase(result SelectionEvaluationResult) SelectionEvaluationCase {
	item := SelectionEvaluationCase{CaseID: result.Row.CaseID, Title: result.Row.Material.Title, Gold: result.Row.Gold.Decision, Error: result.Error}
	if result.Row.SamplingContext != nil {
		item.Stratum = result.Row.SamplingContext.SamplingStratum
	}
	if len(result.ReceiptIDs) > 0 {
		item.ReceiptID = &result.ReceiptIDs[0]
	}
	if result.Output != nil {
		decision := "reject"
		if result.Output.Selected {
			decision = "select"
		}
		item.Decision = &decision
		item.Score = result.Output.Score
		item.Relevance = &result.Output.Relevance
		item.Category = result.Output.Category
		item.Reason = result.Output.ReasonZh
	}
	return item
}

/** selectionMetrics computes the decisive confusion-matrix measures. */
func selectionMetrics(tp, fp, fn, tn int) SelectionSweep {
	precision := float64(tp) / float64(max(1, tp+fp))
	recall := float64(tp) / float64(max(1, tp+fn))
	total := float64(max(1, tp+fp+fn+tn))
	return SelectionSweep{
		Accuracy:  selectionRound(float64(tp+tn) / total),
		Precision: selectionRound(precision),
		Recall:    selectionRound(recall),
		F1:        selectionRound(2 * precision * recall / math.Max(1e-9, precision+recall)),
		Selected:  selectionRound(float64(tp+fp) / total),
	}
}

/** selectionRound matches JavaScript toFixed(3) for nonnegative metrics. */
func selectionRound(value float64) float64 {
	rational := new(big.Rat).SetFloat64(value)
	numerator := new(big.Int).Mul(rational.Num(), big.NewInt(1000))
	quotient, remainder := new(big.Int).QuoRem(numerator, rational.Denom(), new(big.Int))
	if remainder.Mul(remainder, big.NewInt(2)).Cmp(rational.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return float64(quotient.Int64()) / 1000
}
