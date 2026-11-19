package interests

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadPublicationSettingsReadsConfiguredPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publication.json")
	if err := os.WriteFile(path, []byte(`{"subject":"Law","allFeedLeavesOut":["drafts"],"communityFeeds":{"dev":["dev-community"],"hn":["hn-community"]},"publicCategories":{"merge":{"security":"research"},"feedLabels":{"research":"Research"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SITE_PUBLICATION_FILE", path)

	got, err := LoadPublicationSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "Law" || !reflect.DeepEqual(got.AllFeedLeavesOut, []string{"drafts"}) || !reflect.DeepEqual(got.CommunityFeeds.Dev, []string{"dev-community"}) || !reflect.DeepEqual(got.CommunityFeeds.HN, []string{"hn-community"}) || got.PublicCategories.Merge["security"] != "research" || got.PublicCategories.FeedLabels["research"] != "Research" {
		t.Fatalf("publication settings = %#v", got)
	}
}

func TestLoadPublicationSettingsInitializesOptionalCollections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publication.json")
	if err := os.WriteFile(path, []byte(`{"subject":"AI"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SITE_PUBLICATION_FILE", path)

	got, err := LoadPublicationSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.AllFeedLeavesOut == nil || got.CommunityFeeds.Dev == nil || got.CommunityFeeds.HN == nil || got.PublicCategories.Merge == nil || got.PublicCategories.FeedLabels == nil {
		t.Fatalf("optional publication collections must be initialized: %#v", got)
	}
}
