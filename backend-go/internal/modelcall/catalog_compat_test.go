package modelcall_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"aihot/backend-go/internal/adminmodels"
	"aihot/backend-go/internal/modelcall"
)

func TestSiteVisionPresetRemainsAvailableToUnderstand(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	t.Setenv("MODEL_CATALOG_FILE", filepath.Join(repositoryRoot, "shared", "site", "model-catalog.json"))

	loaded, err := adminmodels.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	presets := make(map[string]modelcall.Preset, len(loaded.Presets))
	for key, preset := range loaded.Presets {
		presets[key] = modelcall.Preset{Service: preset.Service, Model: preset.Model, BaseURLEnv: preset.BaseURLEnv,
			APIKeyEnv: preset.APIKeyEnv, Extra: preset.Extra, ReasoningTokens: preset.ReasoningTokens, JSONMode: preset.JSONMode, Vision: preset.Vision}
	}
	capabilities := make(map[string]modelcall.Capability, len(loaded.Capabilities))
	for _, capability := range loaded.Capabilities {
		capabilities[capability.Key] = modelcall.Capability{Key: capability.Key, Env: capability.Env, Default: capability.Default, Vision: capability.Vision}
	}
	resolver := modelcall.NewCatalogResolver(nil, modelcall.Catalog{Presets: presets, Defaults: loaded.Defaults, Capabilities: capabilities},
		map[string]string{"UNDERSTAND_MODEL": "qwen3-vl-flash"}, nil)
	client := modelcall.NewClient(false, resolver, nil, nil)
	supported, err := client.SupportsVision(context.Background(), "understand")
	if err != nil || !supported {
		t.Fatalf("site understand preset should expose optional vision support: supported=%t err=%v", supported, err)
	}
}
