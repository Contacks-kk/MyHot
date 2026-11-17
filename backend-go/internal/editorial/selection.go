package editorial

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type SelectionRules struct {
	Thresholds      map[string]int `json:"thresholds"`
	UnderstandFloor int            `json:"understandFloor"`
}

func LoadSelectionRules() (SelectionRules, error) {
	path := strings.TrimSpace(os.Getenv("INDUSTRY_SELECTION_FILE"))
	if path == "" {
		for _, candidate := range []string{filepath.Join("backend-go", "config", "selection.json"), filepath.Join("config", "selection.json"), filepath.Join("..", "..", "config", "selection.json"), filepath.Join("/app", "backend-go", "config", "selection.json")} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return SelectionRules{}, errors.New("backend-go/config/selection.json not found")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SelectionRules{}, err
	}
	var rules SelectionRules
	if err := json.Unmarshal(data, &rules); err != nil {
		return SelectionRules{}, err
	}
	if len(rules.Thresholds) == 0 || rules.UnderstandFloor < 0 || rules.UnderstandFloor > 100 {
		return SelectionRules{}, errors.New("backend-go/config/selection.json is invalid")
	}
	for tier, threshold := range rules.Thresholds {
		if strings.TrimSpace(tier) == "" || threshold < 0 || threshold > 100 {
			return SelectionRules{}, errors.New("backend-go/config/selection.json contains an invalid threshold")
		}
	}
	return rules, nil
}

func (r SelectionRules) Threshold(tier string) (int, bool) {
	threshold, ok := r.Thresholds[tier]
	return threshold, ok
}
