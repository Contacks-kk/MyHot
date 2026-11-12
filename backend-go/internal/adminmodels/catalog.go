package adminmodels

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Catalog struct {
	Presets      map[string]Preset `json:"presets"`
	PresetOrder  []string          `json:"-"`
	Defaults     map[string]string `json:"defaults"`
	Capabilities []Capability      `json:"capabilities"`
}

type Preset struct {
	Service         string         `json:"service"`
	Model           string         `json:"model"`
	BaseURLEnv      string         `json:"baseUrlEnv"`
	APIKeyEnv       string         `json:"apiKeyEnv"`
	Extra           map[string]any `json:"extra"`
	ReasoningTokens int            `json:"reasoningTokens"`
	JSONMode        bool           `json:"jsonMode"`
	Vision          bool           `json:"vision"`
}

type Capability struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Env      string   `json:"env"`
	Default  string   `json:"default"`
	Purposes []string `json:"purposes"`
	Vision   bool     `json:"vision"`
}

func LoadCatalog() (Catalog, error) {
	path := strings.TrimSpace(os.Getenv("MODEL_CATALOG_FILE"))
	if path == "" {
		for _, candidate := range []string{filepath.Join("shared", "site", "model-catalog.json"), filepath.Join("..", "shared", "site", "model-catalog.json"), filepath.Join("/app", "shared", "site", "model-catalog.json")} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return Catalog{}, errors.New("shared/site/model-catalog.json not found")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, err
	}
	var catalog Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return Catalog{}, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return Catalog{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(root["presets"]))
	if _, err := decoder.Token(); err != nil {
		return Catalog{}, err
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Catalog{}, err
		}
		key, ok := token.(string)
		if !ok {
			return Catalog{}, errors.New("shared/site/model-catalog.json has an invalid preset key")
		}
		catalog.PresetOrder = append(catalog.PresetOrder, key)
		var preset Preset
		if err := decoder.Decode(&preset); err != nil {
			return Catalog{}, err
		}
		catalog.Presets[key] = preset
	}
	if len(catalog.Capabilities) == 0 {
		return Catalog{}, errors.New("shared/site/model-catalog.json has no capabilities")
	}
	if catalog.Presets == nil {
		catalog.Presets = map[string]Preset{}
	}
	seen := make(map[string]struct{}, len(catalog.Capabilities))
	for _, capability := range catalog.Capabilities {
		if strings.TrimSpace(capability.Key) == "" || strings.TrimSpace(capability.Env) == "" || len(capability.Purposes) == 0 {
			return Catalog{}, errors.New("shared/site/model-catalog.json has an incomplete capability")
		}
		if _, ok := seen[capability.Key]; ok {
			return Catalog{}, errors.New("shared/site/model-catalog.json has duplicate capability keys")
		}
		seen[capability.Key] = struct{}{}
		if !catalog.validModel(capability.Default) {
			return Catalog{}, errors.New("shared/site/model-catalog.json references an unknown default model")
		}
	}
	for key, value := range catalog.Defaults {
		if _, ok := seen[key]; !ok || !catalog.validModel(value) {
			return Catalog{}, errors.New("shared/site/model-catalog.json has an invalid capability default")
		}
	}
	return catalog, nil
}

func (c Catalog) validModel(key string) bool { return key == "default" || c.Presets[key].Model != "" }

func (c Catalog) Capability(key string) (Capability, bool) {
	for _, capability := range c.Capabilities {
		if capability.Key == key {
			if configured := c.Defaults[key]; configured != "" {
				capability.Default = configured
			}
			return capability, true
		}
	}
	return Capability{}, false
}
