package editorial

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

/** TestSelectionRulesPreservesBundledThresholds checks relocated rules through the bundled path. */
func TestSelectionRulesPreservesBundledThresholds(t *testing.T) {
	t.Setenv("INDUSTRY_SELECTION_FILE", "")
	want, err := LoadSelectionRules()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDUSTRY_SELECTION_FILE", "../../config/selection.json")
	got, err := LoadSelectionRules()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) || len(got.Thresholds) == 0 {
		t.Fatal("selection thresholds changed after relocation")
	}
}

/** TestPromptDirectoryResolvesBundledPack verifies prompts load from the Go configuration bundle. */
func TestPromptDirectoryResolvesBundledPack(t *testing.T) {
	t.Setenv("INDUSTRY_PROMPT_DIR", "../../config/prompts")
	pack, err := LoadPrompts("Example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pack.Version("selection-score"); err != nil {
		t.Fatal(err)
	}
}

func TestPromptPackExpandsFillsAndVersionsTransitiveFiles(t *testing.T) {
	directory := t.TempDir()
	files := map[string]string{
		"main.md":   "{{> rules}}\n站名={{siteName}}，栏目={{category}}\n",
		"rules.md":  "规则 {{> shared}}",
		"shared.md": "共享约束",
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pack, err := LoadPromptsFrom(directory, "MyHOT")
	if err != nil {
		t.Fatal(err)
	}
	got, err := pack.Render("main", map[string]string{"category": "产品"})
	if err != nil {
		t.Fatal(err)
	}
	want := "规则 共享约束\n站名=MyHOT，栏目=产品\n"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
	candidate, err := pack.RenderText("candidate", files["main.md"], map[string]string{"category": "产品"})
	if err != nil || candidate != got {
		t.Fatalf("candidate rendering differs from the pack: %q, %v", candidate, err)
	}
	version, err := pack.Version("main")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	for _, name := range []string{"main", "rules", "shared"} {
		_, _ = hash.Write([]byte(name + "\n" + files[name+".md"] + "\n"))
	}
	wantVersion := "main@" + hex.EncodeToString(hash.Sum(nil))[:10]
	if version != wantVersion {
		t.Fatalf("Version() = %q, want %q", version, wantVersion)
	}
}

func TestPromptPackRejectsMissingValuesFilesAndCycles(t *testing.T) {
	directory := t.TempDir()
	for name, value := range map[string]string{
		"missing.md": "need {{value}}",
		"left.md":    "{{> right}}",
		"right.md":   "{{> left}}",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pack, err := LoadPromptsFrom(directory, "MyHOT")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pack.Render("missing", nil); err == nil || !strings.Contains(err.Error(), "no value for {{value}}") {
		t.Fatalf("missing template value error = %v", err)
	}
	if _, err := pack.Render("absent", nil); err == nil || !strings.Contains(err.Error(), "read prompt absent") {
		t.Fatalf("missing prompt error = %v", err)
	}
	if _, err := pack.Version("left"); err == nil || !strings.Contains(err.Error(), "left -> right -> left") {
		t.Fatalf("include cycle error = %v", err)
	}
	if _, err := pack.RenderText("candidate", "{{> left}}", nil); err == nil || !strings.Contains(err.Error(), "left -> right -> left") {
		t.Fatalf("candidate include cycle error = %v", err)
	}
	if _, err := pack.RenderText("candidate", "{{value}}", nil); err == nil || !strings.Contains(err.Error(), "no value for {{value}}") {
		t.Fatalf("candidate missing value error = %v", err)
	}
}

func TestPromptPackCanRenderConcurrently(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "main.md"), []byte("{{> shared}} {{siteName}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "shared.md"), []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	pack, err := LoadPromptsFrom(directory, "MyHOT")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 12
	errors := make(chan error, workers)
	for range workers {
		go func() {
			got, err := pack.Render("main", nil)
			if err == nil && got != "stable MyHOT" {
				err = os.ErrInvalid
			}
			errors <- err
		}()
	}
	for range workers {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
}
