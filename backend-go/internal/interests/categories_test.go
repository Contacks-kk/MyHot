package interests

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

/** TestLoadCategoryKeysReadsBackendCatalog verifies the relocated category configuration. */
func TestLoadCategoryKeysReadsBackendCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "categories.json")
	if err := os.WriteFile(path, []byte(`[{"key":"ai-models"},{"key":"paper"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDUSTRY_CATEGORIES_FILE", path)
	keys, err := LoadCategoryKeys()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"ai-models", "paper"}) {
		t.Fatalf("category keys = %#v", keys)
	}
}

func TestLoadCategoryKeysRejectsDuplicateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "categories.json")
	if err := os.WriteFile(path, []byte(`[{"key":"paper"},{"key":"paper"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDUSTRY_CATEGORIES_FILE", path)
	if _, err := LoadCategoryKeys(); err == nil {
		t.Fatal("duplicate category keys must fail startup")
	}
}

func TestLoadCategoriesKeepsPackDisplayLabels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "categories.json")
	if err := os.WriteFile(path, []byte(`[{"key":"ai-models","label":"模型"},{"key":"paper","label":"论文"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDUSTRY_CATEGORIES_FILE", path)
	categories, err := LoadCategories()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(categories, []Category{{Key: "ai-models", Label: "模型"}, {Key: "paper", Label: "论文"}}) {
		t.Fatalf("categories = %#v", categories)
	}
}

func TestLoadCategoriesIncludesReportSectionAndCommentary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "categories.json")
	if err := os.WriteFile(path, []byte(`[{"key":"opinion","label":"观点","section":"技巧与观点","commentary":true}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDUSTRY_CATEGORIES_FILE", path)
	categories, err := LoadCategories()
	if err != nil {
		t.Fatal(err)
	}
	if len(categories) != 1 || categories[0].Section != "技巧与观点" || !categories[0].Commentary {
		t.Fatalf("report category metadata = %#v", categories)
	}
}

/** TestCatalogUsesPublicCategoryLabels verifies the catalog's public mapping. */
func TestCatalogUsesPublicCategoryLabels(t *testing.T) {
	publication := PublicationSettings{
		Subject: "Technology",
		PublicCategories: PublicCategories{
			Merge:      map[string]string{"security": "research"},
			FeedLabels: map[string]string{"research": "Research feed"},
		},
	}
	unit := "new models"
	service := NewCatalogService(
		[]string{"security", "research", "products"},
		map[string]string{"security": "Security", "research": "Research", "products": "Products"},
		map[string]string{"research": "Research default"},
		publication,
		nil,
		nil,
		&unit,
	)
	got := service.Display()
	want := []CatalogCategory{
		{Key: "security", Label: "Security", PublicKey: "research", FeedLabel: "Research feed"},
		{Key: "research", Label: "Research", PublicKey: "research", FeedLabel: "Research feed"},
		{Key: "products", Label: "Products", PublicKey: "products", FeedLabel: "Products"},
	}
	if got.Subject != "Technology" || !reflect.DeepEqual(got.Categories, want) || got.Topics == nil || got.TopicGroups == nil || got.ReleaseUnit == nil || *got.ReleaseUnit != unit {
		t.Fatalf("unexpected display catalog: %#v", got)
	}
}

/** TestEntitiesBundledPathPreservesOrder verifies both entity loaders use the relocated bundled data. */
func TestEntitiesBundledPathPreservesOrder(t *testing.T) {
	t.Setenv("INDUSTRY_ENTITIES_FILE", "")
	want, err := LoadEntityEntries()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDUSTRY_ENTITIES_FILE", "../../config/entities.json")
	got, err := LoadEntityEntries()
	if err != nil {
		t.Fatal(err)
	}
	entities, err := LoadEntities()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) || len(got) == 0 || len(entities) != len(got) {
		t.Fatal("entity data or its prompt order changed after relocation")
	}
}

/** TestCategoryBundledPathPreservesCatalog verifies the relocated categories keep their order and labels. */
func TestCategoryBundledPathPreservesCatalog(t *testing.T) {
	t.Setenv("INDUSTRY_CATEGORIES_FILE", "")
	want, err := LoadCategories()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDUSTRY_CATEGORIES_FILE", "../../config/categories.json")
	got, err := LoadCategories()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || !reflect.DeepEqual(want, got) {
		t.Fatal("category catalog changed after relocation")
	}
}
