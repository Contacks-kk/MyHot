package editorial

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type SelectionGoldRow struct {
	CaseID          string                 `json:"caseId"`
	Material        SelectionGoldMaterial  `json:"material"`
	SourceFacts     SelectionGoldSource    `json:"sourceFacts"`
	SamplingContext *SelectionGoldSampling `json:"samplingContext"`
	Gold            struct {
		Decision string `json:"decision"`
	} `json:"gold"`
}

type SelectionGoldMaterial struct {
	Title         string  `json:"title"`
	OriginalTitle *string `json:"originalTitle"`
	PublishedAt   *string `json:"publishedAt"`
	SourceName    string  `json:"sourceName"`
	BodyZh        *string `json:"bodyZh"`
	BodyOriginal  *string `json:"bodyOriginal"`
}

type SelectionGoldSource struct {
	SourceKind string  `json:"sourceKind"`
	SourceTier *string `json:"sourceTier"`
	FirstParty bool    `json:"firstParty"`
	Language   *string `json:"language"`
}

type SelectionGoldSampling struct {
	BenchmarkSplit  string  `json:"benchmarkSplit"`
	SamplingStratum *string `json:"samplingStratum"`
}

/** ParseSelectionGold reads the labelled JSONL without changing its input order. */
func ParseSelectionGold(text string) ([]SelectionGoldRow, error) {
	rows := make([]SelectionGoldRow, 0)
	for index, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "//") {
			continue
		}
		var row SelectionGoldRow
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, fmt.Errorf("line %d: %w", index+1, err)
		}
		if row.CaseID == "" || row.Material.Title == "" || row.Material.SourceName == "" || row.SourceFacts.SourceKind == "" {
			return nil, fmt.Errorf("line %d: case, title, source and source kind are required", index+1)
		}
		if row.Gold.Decision != "select" && row.Gold.Decision != "reject" && row.Gold.Decision != "either" {
			return nil, fmt.Errorf("line %d: invalid gold decision", index+1)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

/** SampleSelectionGold applies the legacy split filter and deterministic seeded ordering. */
func SampleSelectionGold(rows []SelectionGoldRow, split string, limit int, seed uint32) []SelectionGoldRow {
	type keyed struct {
		row SelectionGoldRow
		key uint32
	}
	pool := make([]keyed, 0, len(rows))
	for _, row := range rows {
		if split != "all" && (row.SamplingContext == nil || row.SamplingContext.BenchmarkSplit != split) {
			continue
		}
		seed = seed*1664525 + 1013904223
		pool = append(pool, keyed{row: row, key: seed})
	}
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].key < pool[j].key })
	sample := make([]SelectionGoldRow, 0, min(len(pool), max(limit, 0)))
	for _, entry := range pool[:min(len(pool), max(limit, 0))] {
		sample = append(sample, entry.row)
	}
	return sample
}

/** SelectionInput converts a labelled sample to the live selection input. */
func SelectionInput(row SelectionGoldRow) (Input, error) {
	material := row.Material
	title := material.Title
	if material.OriginalTitle != nil && *material.OriginalTitle != "" {
		title = *material.OriginalTitle
	}
	var published *time.Time
	if material.PublishedAt != nil && *material.PublishedAt != "" {
		at, err := time.Parse(time.RFC3339Nano, *material.PublishedAt)
		if err != nil {
			return Input{}, fmt.Errorf("case %s has an unsupported publication date: %w", row.CaseID, err)
		}
		published = &at
	}
	body := material.BodyOriginal
	if body == nil || *body == "" {
		body = material.BodyZh
	}
	if body != nil && *body == "" {
		body = nil
	}
	tier := "T2"
	if row.SourceFacts.SourceTier != nil {
		tier = *row.SourceFacts.SourceTier
	}
	input := Input{
		ID:          "gold-" + row.CaseID,
		Revision:    1,
		BodyStatus:  "ok",
		Title:       title,
		URL:         "https://example.invalid/" + row.CaseID,
		PublishedAt: published,
		BodyText:    body,
		Media:       []Media{},
		Source: Source{
			Name:       material.SourceName,
			Kind:       row.SourceFacts.SourceKind,
			Tier:       tier,
			FirstParty: row.SourceFacts.FirstParty,
		},
	}
	if row.SourceFacts.SourceKind == "x_search" {
		text := material.Title
		if body != nil {
			text = *body
		}
		input.BodyText = nil
		input.XPost = &XPost{AuthorName: material.SourceName, Text: text}
	}
	return input, nil
}
