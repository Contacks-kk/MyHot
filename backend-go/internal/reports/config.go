package reports

import (
	"aihot/backend-go/internal/promptpath"
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
)

type Release struct {
	Category string `json:"category"`
	Tag      string `json:"tag"`
	Unit     string `json:"unit"`
}

type ReportingConfig struct {
	PlainTerms []string `json:"plainTerms"`
	Release    *Release `json:"release"`
}

func LoadReportingConfig() (ReportingConfig, error) {
	path := strings.TrimSpace(os.Getenv("INDUSTRY_REPORTING_FILE"))
	if path == "" {
		for _, candidate := range []string{filepath.Join("backend-go", "config", "reporting.json"), filepath.Join("config", "reporting.json"), filepath.Join("..", "..", "config", "reporting.json"), filepath.Join("/app", "backend-go", "config", "reporting.json")} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return ReportingConfig{}, errors.New("backend-go/config/reporting.json not found")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReportingConfig{}, err
	}
	var config ReportingConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return ReportingConfig{}, err
	}
	if config.PlainTerms == nil {
		config.PlainTerms = []string{}
	}
	if config.Release != nil && (config.Release.Category == "" || config.Release.Tag == "") {
		return ReportingConfig{}, errors.New("backend-go/config/reporting.json release needs category and tag")
	}
	return config, nil
}

type PromptFiles struct {
	Period     string
	Sections   string
	NoSections string
	Version    string
	Directory  string
}

func LoadPromptFiles() (PromptFiles, error) {
	configured := promptpath.Configured()
	if configured != "" {
		return readPromptFiles(configured)
	}
	for _, candidate := range []string{filepath.Join("backend-go", "config", "prompts"), filepath.Join("config", "prompts"), filepath.Join("..", "..", "config", "prompts"), filepath.Join("..", "backend-go", "config", "prompts"), filepath.Join("/app", "backend-go", "config", "prompts")} {
		files, err := readPromptFiles(candidate)
		if err == nil {
			return files, nil
		}
	}
	return PromptFiles{}, errors.New("industry report prompts were not found")
}

func readPromptFiles(directory string) (PromptFiles, error) {
	names := []string{"report-period", "report-period-sections", "report-period-no-sections"}
	texts := make(map[string]string, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(directory, name+".md"))
		if err != nil {
			return PromptFiles{}, err
		}
		texts[name] = string(data)
	}
	used := make(map[string]string)
	var expand func(string, []string) (string, error)
	expand = func(name string, stack []string) (string, error) {
		for _, existing := range stack {
			if existing == name {
				return "", fmt.Errorf("report prompt include cycle: %s", strings.Join(append(stack, name), " -> "))
			}
		}
		text, ok := texts[name]
		if !ok {
			data, err := os.ReadFile(filepath.Join(directory, name+".md"))
			if err != nil {
				return "", err
			}
			text = string(data)
			texts[name] = text
		}
		used[name] = text
		var expansionErr error
		expanded := includePattern.ReplaceAllStringFunc(text, func(token string) string {
			match := includePattern.FindStringSubmatch(token)
			inner, err := expand(match[1], append(stack, name))
			if err != nil {
				expansionErr = err
				return ""
			}
			return inner
		})
		if expansionErr != nil {
			return "", expansionErr
		}
		return expanded, nil
	}
	expandedPrompts := make(map[string]string, len(names))
	for _, name := range names {
		text, err := expand(name, nil)
		if err != nil {
			return PromptFiles{}, err
		}
		expandedPrompts[name] = text
	}
	keys := make([]string, 0, len(used))
	for name := range used {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, name := range keys {
		_, _ = fmt.Fprintf(hash, "%s\n%s\n", name, used[name])
	}
	return PromptFiles{Period: expandedPrompts[names[0]], Sections: expandedPrompts[names[1]], NoSections: expandedPrompts[names[2]],
		Version: "report-period+report-period-sections+report-period-no-sections@" + hex.EncodeToString(hash.Sum(nil))[:10], Directory: directory}, nil
}

var includePattern = regexp.MustCompile(`\{\{>\s*([A-Za-z][\w.-]*)\s*\}\}`)
var valuePattern = regexp.MustCompile(`\{\{\s*([A-Za-z][\w.-]*)\s*\}\}`)

func renderPrompt(text string, values map[string]string) (string, error) {
	var missing string
	output := valuePattern.ReplaceAllStringFunc(text, func(token string) string {
		name := valuePattern.FindStringSubmatch(token)[1]
		value, ok := values[name]
		if !ok {
			missing = name
			return ""
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("report prompt has no value for {{%s}}", missing)
	}
	return output, nil
}

func (p PromptFiles) PeriodSystem(siteName, kindName, span, sentences, chars string, sections []string) (string, error) {
	sectionPrompt := p.NoSections
	sectionExample := "{}"
	columns := ""
	if len(sections) > 0 {
		sectionPrompt = p.Sections
		sectionExample = `{"` + sections[0] + `": "..."}`
		quoted := make([]string, len(sections))
		for i, section := range sections {
			quoted[i] = "「" + section + "」"
		}
		columns = strings.Join(quoted, "")
	}
	sectionPrompt, err := renderPrompt(sectionPrompt, map[string]string{"columns": columns})
	if err != nil {
		return "", err
	}
	return renderPrompt(p.Period, map[string]string{
		"siteName": siteName, "kindName": kindName, "span": span, "sentences": sentences, "chars": chars,
		"sections": sectionPrompt, "sectionsExample": sectionExample,
	})
}
