package siteinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSiteMetadataAndChangelogUseThePublishedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changelog.json")
	const source = `{
  "latestVersion": "2026-10-01T08:00",
  "releases": [{ "title": "保持字段内容" }]
}`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(path)

	meta, metaETag, err := service.Meta()
	if err != nil || string(meta) != `{"changelogVersion":"2026-10-01T08:00"}` || metaETag != weakETag("meta", meta) {
		t.Fatalf("unexpected site metadata: body=%s etag=%s err=%v", meta, metaETag, err)
	}
	body, etag, err := service.Changelog()
	want := `{"latestVersion":"2026-10-01T08:00","releases":[{"title":"保持字段内容"}]}`
	if err != nil || string(body) != want || etag != weakETag("changelog", []byte(want)) {
		t.Fatalf("unexpected changelog: body=%s etag=%s err=%v", body, etag, err)
	}
}

func TestWeakETagMatchesTypeScriptHashFormat(t *testing.T) {
	body := []byte(`{"changelogVersion":"v1"}`)
	sum := sha256.Sum256(body)
	want := `W/"meta-` + hex.EncodeToString(sum[:])[:16] + `"`
	if got := weakETag("meta", body); got != want {
		t.Fatalf("etag=%q, want %q", got, want)
	}
}

func TestInvalidChangelogIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changelog.json")
	if err := os.WriteFile(path, []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewService(path).Meta(); err == nil {
		t.Fatal("invalid changelog was accepted")
	}
}

func TestOpenAPIV1ExpandsRuntimeSiteAndCategoryValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openapi-v1.json")
	const source = `{"info":{"title":"{{siteName}} Public API","version":"{{version}}","description":"{{description}} {{tagline}} {{locale}} {{dailyTime}} {{categoryList}}"},"servers":[{"url":"{{siteUrl}}"}],"parameters":[{"enum":["{{categories}}"]}]}`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService("", OpenAPIConfig{
		Path: path, SiteName: "Example", SiteURL: "https://example.invalid", Description: "desc", Tagline: "tagline",
		Locale: "zh-CN", Version: "4.0.0", DailyTime: "08:00", Categories: []string{"models", "research"},
	})
	body, etag, err := service.OpenAPIV1()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	info := document["info"].(map[string]any)
	if info["title"] != "Example Public API" || info["version"] != "4.0.0" || !strings.Contains(info["description"].(string), "08:00 models, research") {
		t.Fatalf("runtime placeholders were not expanded: %#v", info)
	}
	server := document["servers"].([]any)[0].(map[string]any)
	if server["url"] != "https://example.invalid" {
		t.Fatalf("site URL was not expanded: %#v", server)
	}
	parameters := document["parameters"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(parameters["enum"], []any{"models", "research"}) {
		t.Fatalf("categories were not expanded: %#v", parameters["enum"])
	}
	sum := sha256.Sum256(body)
	wantETag := `W/"` + hex.EncodeToString(sum[:])[:16] + `"`
	if etag != wantETag {
		t.Fatalf("etag=%q want %q", etag, wantETag)
	}
}
