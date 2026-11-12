package editorial

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var promptToken = regexp.MustCompile(`\{\{(>\s*)?([A-Za-z][\w.-]*)\s*\}\}`)

// PromptPack keeps the industry prompt wording and its content-derived version together.
// Prompt files remain the source of truth; no prompt text is duplicated in Go.
type PromptPack struct {
	directory string
	siteName  string
	mu        sync.RWMutex
	raw       map[string]string
}

func LoadPrompts(siteName string) (*PromptPack, error) {
	directory := strings.TrimSpace(os.Getenv("INDUSTRY_PROMPT_DIR"))
	if directory == "" {
		for _, candidate := range []string{filepath.Join("shared", "industry", "prompts"), filepath.Join("..", "shared", "industry", "prompts"), filepath.Join("..", "..", "shared", "industry", "prompts"), filepath.Join("/app", "shared", "industry", "prompts")} {
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				directory = candidate
				break
			}
		}
	}
	if directory == "" {
		return nil, errors.New("industry prompt directory not found")
	}
	return LoadPromptsFrom(directory, siteName)
}

func LoadPromptsFrom(directory, siteName string) (*PromptPack, error) {
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("industry prompt directory not found")
	}
	if strings.TrimSpace(siteName) == "" {
		return nil, errors.New("site name is required for editorial prompts")
	}
	return &PromptPack{directory: directory, siteName: siteName, raw: make(map[string]string)}, nil
}

// Render expands {{> include}} files and fills named prompt values.
func (p *PromptPack) Render(name string, values map[string]string) (string, error) {
	text, _, err := p.expand(name, nil)
	if err != nil {
		return "", err
	}
	all := make(map[string]string, len(values)+1)
	all["siteName"] = p.siteName
	for key, value := range values {
		all[key] = value
	}
	var renderErr error
	text = promptToken.ReplaceAllStringFunc(text, func(token string) string {
		match := promptToken.FindStringSubmatch(token)
		if match[1] != "" {
			renderErr = fmt.Errorf("prompt %s has an unexpanded include %s", name, match[2])
			return ""
		}
		value, ok := all[match[2]]
		if !ok {
			renderErr = fmt.Errorf("prompt %s: no value for {{%s}}", name, match[2])
			return ""
		}
		return value
	})
	if renderErr != nil {
		return "", renderErr
	}
	return text, nil
}

// Version returns the same sorted, transitive file hash format used by the TypeScript pipeline.
func (p *PromptPack) Version(names ...string) (string, error) {
	used := make(map[string]string)
	for _, name := range names {
		if _, _, err := p.expand(name, nil, used); err != nil {
			return "", err
		}
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
	return strings.Join(names, "+") + "@" + hex.EncodeToString(hash.Sum(nil))[:10], nil
}

func (p *PromptPack) expand(name string, stack []string, usedMaps ...map[string]string) (string, []string, error) {
	for _, parent := range stack {
		if parent == name {
			return "", nil, fmt.Errorf("prompt include cycle: %s", strings.Join(append(stack, name), " -> "))
		}
	}
	raw, err := p.rawFile(name)
	if err != nil {
		return "", nil, err
	}
	if len(usedMaps) > 0 {
		usedMaps[0][name] = raw
	}
	used := []string{name}
	var includeErr error
	expanded := promptToken.ReplaceAllStringFunc(raw, func(token string) string {
		match := promptToken.FindStringSubmatch(token)
		if match[1] == "" {
			return token
		}
		inner, innerUsed, err := p.expand(match[2], append(stack, name), usedMaps...)
		if err != nil {
			includeErr = err
			return ""
		}
		used = append(used, innerUsed...)
		return inner
	})
	if includeErr != nil {
		return "", nil, includeErr
	}
	return expanded, used, nil
}

func (p *PromptPack) rawFile(name string) (string, error) {
	p.mu.RLock()
	raw, ok := p.raw[name]
	p.mu.RUnlock()
	if ok {
		return raw, nil
	}
	data, err := os.ReadFile(filepath.Join(p.directory, name+".md"))
	if err != nil {
		return "", fmt.Errorf("read prompt %s: %w", name, err)
	}
	raw = string(data)
	p.mu.Lock()
	if existing, exists := p.raw[name]; exists {
		raw = existing
	} else {
		p.raw[name] = raw
	}
	p.mu.Unlock()
	return raw, nil
}
