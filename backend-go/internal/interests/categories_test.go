package interests

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadCategoryKeysReadsSharedIndustryCatalog(t *testing.T) {
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
