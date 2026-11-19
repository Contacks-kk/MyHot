package adminmodels

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fakeRepository struct {
	rows      OverviewRows
	overrides map[string]string
	switched  bool
	key       string
	model     *string
	actor     string
	reason    string
	before    ModelSource
	after     ModelSource
	err       error
}

func (r *fakeRepository) Overview(context.Context, time.Time) (OverviewRows, error) {
	return r.rows, r.err
}
func (r *fakeRepository) Overrides(context.Context) (map[string]string, error) {
	return r.overrides, r.err
}
func (r *fakeRepository) Switch(_ context.Context, key string, model *string, actor, reason string, before, after ModelSource) error {
	r.switched, r.key, r.model, r.actor, r.reason, r.before, r.after = true, key, model, actor, reason, before, after
	return r.err
}

func testCatalog() Catalog {
	return Catalog{
		Presets:     map[string]Preset{"named": {Service: "vendor", Model: "gpt-4"}},
		PresetOrder: []string{"named"},
		Defaults:    map[string]string{"score": "named"},
		Capabilities: []Capability{
			{Key: "score", Label: "Score", Env: "SCORE_MODEL", Default: "default", Purposes: []string{"score_article"}},
			{Key: "summary", Label: "Summary", Env: "SUMMARY_MODEL", Default: "default", Purposes: []string{"summarize_article"}},
		},
	}
}

func TestLoadCatalogKeepsPresetOrderAndRejectsDuplicateCapabilities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-catalog.json")
	data := `{"presets":{"second":{"service":"v","model":"m2"},"first":{"service":"v","model":"m1"}},"defaults":{},"capabilities":[{"key":"a","label":"A","env":"A_MODEL","default":"default","purposes":["a"]}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MODEL_CATALOG_FILE", path)
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(catalog.PresetOrder, []string{"second", "first"}) {
		t.Fatalf("preset order = %#v", catalog.PresetOrder)
	}
	duplicate := `{"presets":{},"defaults":{},"capabilities":[{"key":"a","label":"A","env":"A_MODEL","default":"default","purposes":["a"]},{"key":"a","label":"A","env":"A_MODEL","default":"default","purposes":["a"]}]}`
	if err := os.WriteFile(path, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(); err == nil {
		t.Fatal("duplicate capabilities must be rejected")
	}
}

/** TestLoadCatalogPreservesBundledPath verifies the bundled catalog remains available after relocation. */
func TestLoadCatalogPreservesBundledPath(t *testing.T) {
	t.Setenv("MODEL_CATALOG_FILE", "")
	expected, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MODEL_CATALOG_FILE", "../../config/model-catalog.json")
	actual, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) || len(actual.Capabilities) == 0 {
		t.Fatal("bundled path must load the same built-in model catalog")
	}
}

func TestOverviewResolvesSourcesAndEstimatesCachedTokenCost(t *testing.T) {
	t.Setenv("SCORE_MODEL", "named")
	t.Setenv("SUMMARY_MODEL", "unknown")
	repository := &fakeRepository{
		overrides: map[string]string{"score": "default"},
		rows: OverviewRows{
			Usage:  []UsageRow{{Purpose: "score_article", Model: stringPtr("gpt-4"), Calls: 2, OK: 2, TokensIn: int64Ptr(100), TokensOut: int64Ptr(50), TokensInUncosted: int64Ptr(100), TokensOutUncosted: int64Ptr(50), TokensCached: int64Ptr(20)}},
			Prices: []Price{{Service: "vendor", Model: "gpt-4", Currency: "USD", InputPerMTok: floatPtr(2), CachedPerMTok: floatPtr(0.5), OutputPerMTok: floatPtr(4)}},
		},
	}
	service := NewService(repository, testCatalog())
	got, err := service.Overview(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0].Current != (ModelSource{Model: "default", Source: "admin"}) || got.Capabilities[1].Current != (ModelSource{Model: "default", Source: "default"}) {
		t.Fatalf("capability choices = %#v", got.Capabilities)
	}
	if len(got.Choices) != 2 || got.Choices[1].Key != "named" || len(got.Capabilities[0].Usage) != 1 {
		t.Fatalf("choices/usage = %#v", got)
	}
	estimate := got.Capabilities[0].Usage[0].Estimate
	if estimate == nil || estimate.Currency != "USD" || estimate.Amount < 0.000369999 || estimate.Amount > 0.000370001 {
		t.Fatalf("cost estimate = %#v", estimate)
	}
}

func TestSwitchValidatesAndAuditsEffectiveSources(t *testing.T) {
	repository := &fakeRepository{overrides: map[string]string{}}
	service := NewService(repository, testCatalog())
	if _, err := service.Switch(context.Background(), "missing", stringPtr("named"), "why", "admin:1"); !IsInvalid(err) {
		t.Fatalf("unknown capability error = %v", err)
	}
	if _, err := service.Switch(context.Background(), "score", stringPtr("unknown"), "why", "admin:1"); !IsInvalid(err) {
		t.Fatalf("unknown model error = %v", err)
	}
	result, err := service.Switch(context.Background(), "score", stringPtr("named"), " use it ", "admin:1")
	if err != nil {
		t.Fatal(err)
	}
	if !repository.switched || repository.key != "score" || repository.actor != "admin:1" || repository.reason != " use it " || repository.after != (ModelSource{Model: "named", Source: "admin"}) {
		t.Fatalf("saved switch = %#v, response=%#v", repository, result)
	}
}

func stringPtr(value string) *string  { return &value }
func int64Ptr(value int64) *int64     { return &value }
func floatPtr(value float64) *float64 { return &value }
