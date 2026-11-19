package grouping

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
)

var evaluationRelations = []Relation{RelationSameOccurrence, RelationSameStory, RelationUnrelated, RelationRoundup}

type EvaluationSamplingContext struct {
	BenchmarkSplit  string `json:"benchmarkSplit,omitempty"`
	SamplingStratum string `json:"samplingStratum,omitempty"`
}

type EvaluationGoldRow struct {
	CaseID          string                     `json:"caseId"`
	A               EvaluationGoldReport       `json:"a"`
	B               EvaluationGoldReport       `json:"b"`
	SamplingContext *EvaluationSamplingContext `json:"samplingContext,omitempty"`
	Gold            EvaluationGold             `json:"gold"`
}

type EvaluationGoldReport struct {
	Title       string  `json:"title"`
	Source      string  `json:"source"`
	FirstParty  bool    `json:"firstParty"`
	PublishedAt *string `json:"publishedAt"`
	Summary     *string `json:"summary"`
	Frame       *Frame  `json:"frame"`
}

type EvaluationGold struct {
	Relation Relation `json:"relation"`
}

/** SampleRelationGold filters before applying the legacy seeded shuffle and sample limit. */
func SampleRelationGold(rows []EvaluationGoldRow, split string, limit int, seed uint32) []EvaluationGoldRow {
	type keyedRow struct {
		row EvaluationGoldRow
		key uint32
	}
	pool := make([]keyedRow, 0, len(rows))
	for _, row := range rows {
		if split != "all" && (row.SamplingContext == nil || row.SamplingContext.BenchmarkSplit != split) {
			continue
		}
		seed = seed*1664525 + 1013904223
		pool = append(pool, keyedRow{
			row: row,
			key: seed,
		})
	}
	sort.SliceStable(pool, func(i, j int) bool {
		return pool[i].key < pool[j].key
	})
	result := make([]EvaluationGoldRow, 0, min(len(pool), max(limit, 0)))
	for _, entry := range pool[:min(len(pool), max(limit, 0))] {
		result = append(result, entry.row)
	}
	return result
}

/** SafeEvaluationReportNamePart turns a split label into a bounded report filename component. */
func SafeEvaluationReportNamePart(value string) string {
	value = strings.TrimSpace(value)
	var safe strings.Builder
	separator := false
	for _, char := range value {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-'
		if allowed {
			safe.WriteRune(char)
			separator = false
			continue
		}
		if !separator {
			safe.WriteByte('-')
			separator = true
		}
	}
	result := strings.Trim(safe.String(), "-")
	if len(result) > 80 {
		result = result[:80]
	}
	if result == "" {
		return "all"
	}
	return result
}

type EvaluationPrediction struct {
	CaseID     string   `json:"caseId"`
	Gold       Relation `json:"gold"`
	Relation   Relation `json:"relation"`
	Confidence float64  `json:"confidence"`
	Stratum    *string  `json:"stratum,omitempty"`
}

type EvaluationClassMetrics struct {
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	Support   int     `json:"support"`
}

type EvaluationMetrics struct {
	SampleSize      int                                 `json:"sampleSize"`
	Evaluated       int                                 `json:"evaluated"`
	Errors          int                                 `json:"errors"`
	Accuracy        float64                             `json:"accuracy"`
	MacroF1         float64                             `json:"macroF1"`
	ConfusionMatrix map[Relation]map[Relation]int       `json:"confusionMatrix"`
	PerClass        map[Relation]EvaluationClassMetrics `json:"perClass"`
}

type EvaluationTieMetrics struct {
	Threshold float64 `json:"threshold"`
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	FN        int     `json:"fn"`
	TN        int     `json:"tn"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	Accuracy  float64 `json:"accuracy"`
}

/** EvaluateRelations builds the legacy-compatible confusion matrix and class metrics. */
func EvaluateRelations(predictions []EvaluationPrediction, totalCases int) (EvaluationMetrics, error) {
	result := EvaluationMetrics{
		SampleSize:      totalCases,
		Evaluated:       len(predictions),
		Errors:          max(0, totalCases-len(predictions)),
		ConfusionMatrix: make(map[Relation]map[Relation]int),
		PerClass:        make(map[Relation]EvaluationClassMetrics),
	}
	for _, gold := range evaluationRelations {
		result.ConfusionMatrix[gold] = make(map[Relation]int)
		for _, predicted := range evaluationRelations {
			result.ConfusionMatrix[gold][predicted] = 0
		}
	}
	for _, prediction := range predictions {
		row, ok := result.ConfusionMatrix[prediction.Gold]
		if !ok {
			return EvaluationMetrics{}, fmt.Errorf("invalid gold relation %q", prediction.Gold)
		}
		if _, ok := row[prediction.Relation]; !ok {
			return EvaluationMetrics{}, fmt.Errorf("invalid predicted relation %q", prediction.Relation)
		}
		row[prediction.Relation]++
	}
	correct := 0
	for _, relation := range evaluationRelations {
		tp := result.ConfusionMatrix[relation][relation]
		fp, support := 0, 0
		for _, other := range evaluationRelations {
			support += result.ConfusionMatrix[relation][other]
			if other != relation {
				fp += result.ConfusionMatrix[other][relation]
			}
		}
		precision := float64(tp) / float64(max(1, tp+fp))
		recall := float64(tp) / float64(max(1, support))
		metrics := EvaluationClassMetrics{
			Precision: evaluationRound(precision),
			Recall:    evaluationRound(recall),
			F1:        evaluationRound(2 * precision * recall / math.Max(1e-9, precision+recall)),
			Support:   support,
		}
		result.PerClass[relation] = metrics
		result.MacroF1 += metrics.F1
		correct += tp
	}
	result.Accuracy = evaluationRound(float64(correct) / float64(max(1, len(predictions))))
	result.MacroF1 = evaluationRound(result.MacroF1 / float64(len(evaluationRelations)))
	return result, nil
}

/** EvaluateStoryTies scores story linkage decisions at the specified confidence threshold. */
func EvaluateStoryTies(predictions []EvaluationPrediction, threshold float64) EvaluationTieMetrics {
	result := EvaluationTieMetrics{Threshold: threshold}
	for _, prediction := range predictions {
		gold := prediction.Gold == RelationSameOccurrence || prediction.Gold == RelationSameStory
		predicted := (prediction.Relation == RelationSameOccurrence || prediction.Relation == RelationSameStory) && prediction.Confidence >= threshold
		switch {
		case predicted && gold:
			result.TP++
		case predicted:
			result.FP++
		case gold:
			result.FN++
		default:
			result.TN++
		}
	}
	precision := float64(result.TP) / float64(max(1, result.TP+result.FP))
	recall := float64(result.TP) / float64(max(1, result.TP+result.FN))
	result.Precision = evaluationRound(precision)
	result.Recall = evaluationRound(recall)
	result.F1 = evaluationRound(2 * precision * recall / math.Max(1e-9, precision+recall))
	result.Accuracy = evaluationRound(float64(result.TP+result.TN) / float64(max(1, len(predictions))))
	return result
}

/** evaluationRound matches JavaScript toFixed(3) for nonnegative report metrics. */
func evaluationRound(value float64) float64 {
	rational := new(big.Rat).SetFloat64(value)
	if rational == nil {
		return value
	}
	numerator := new(big.Int).Mul(rational.Num(), big.NewInt(1000))
	quotient, remainder := new(big.Int).QuoRem(numerator, rational.Denom(), new(big.Int))
	if remainder.Mul(remainder, big.NewInt(2)).Cmp(rational.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return float64(quotient.Int64()) / 1000
}
