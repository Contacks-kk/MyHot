package storylinks

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type repositoryFixture struct {
	days       int
	confidence float64
	count      int
	added      int
}

func (r *repositoryFixture) LinkRelatedStories(_ context.Context, days int, confidence float64, count int) (int, error) {
	r.days, r.confidence, r.count = days, confidence, count
	return r.added, nil
}

func TestRunPreservesRelatedStoryThresholdsAndResult(t *testing.T) {
	repository := &repositoryFixture{added: 3}
	result, err := NewService(repository).Run(context.Background())
	if err != nil || result.Added != 3 {
		t.Fatalf("Run = %#v, %v", result, err)
	}
	if repository.days != 14 || repository.confidence != 0.8 || repository.count != 2 {
		t.Fatalf("related-story thresholds changed: %#v", repository)
	}
}

/** TestRepositoryLinksStoriesAfterRepeatedIndependentTies covers the public related-story threshold. */
func TestRepositoryLinksStoriesAfterRepeatedIndependentTies(t *testing.T) {
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}

	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
	}

	db, err := gorm.Open(postgres.Open(raw), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx := context.Background()
	prefix := fmt.Sprintf("storylinks_%d", time.Now().UnixNano())
	sourceID := prefix + "_source"
	storyIDs := []int64{}
	factIDs := []int64{}
	articleIDs := []string{prefix + "_a1", prefix + "_a2", prefix + "_b1", prefix + "_b2"}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM articles WHERE id LIKE ?`, prefix+"_%").Error
		_ = db.Exec(`DELETE FROM facts WHERE public_id LIKE ?`, prefix+"_%").Error
		_ = db.Exec(`DELETE FROM stories WHERE title LIKE ?`, prefix+"_%").Error
		_ = db.Exec(`DELETE FROM sources WHERE id=?`, sourceID).Error
	})

	if err := db.Exec(`INSERT INTO sources(id,name,kind) VALUES (?, 'Story link fixture', 'rss')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	for index, suffix := range []string{"a", "b"} {
		var story struct{ ID int64 }
		publicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", (time.Now().UnixNano()+int64(index))&0xffffffffffff)
		if err := db.Raw(`INSERT INTO stories(public_id,title) VALUES (?,?) RETURNING id`, publicID, prefix+"_"+suffix).Scan(&story).Error; err != nil {
			t.Fatal(err)
		}
		storyIDs = append(storyIDs, story.ID)
		var fact struct{ ID int64 }
		if err := db.Raw(`INSERT INTO facts(public_id,story_id,title) VALUES (?,?,'fixture fact') RETURNING id`, prefix+"_"+suffix, story.ID).Scan(&fact).Error; err != nil {
			t.Fatal(err)
		}
		factIDs = append(factIDs, fact.ID)
	}

	now := time.Now().UTC()
	for index, articleID := range articleIDs {
		ownIndex := index / 2
		otherIndex := 1 - ownIndex
		if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at) VALUES (?,?,?,?,?,?,?)`,
			articleID, sourceID, "identity:"+articleID, "https://example.invalid/"+articleID, "Fixture report", now, now).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO fact_articles(fact_id,article_id,role) VALUES (?,?,'report')`, factIDs[ownIndex], articleID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO publications(article_id,title,source_id,channel,url,discovered_at,timeline_at,sort_at) VALUES (?,? ,?,'news',?,?,?,?)`,
			articleID, "Fixture report", sourceID, "https://example.invalid/"+articleID, now, now, now).Error; err != nil {
			t.Fatal(err)
		}
		candidates := fmt.Sprintf(`[{"id":%d,"relation":"SAME_STORY","confidence":0.95}]`, factIDs[otherIndex])
		if err := db.Exec(`INSERT INTO grouping_decisions(article_id,verdict,candidates,created_at) VALUES (?,'new-story',?::jsonb,?)`, articleID, candidates, now).Error; err != nil {
			t.Fatal(err)
		}
	}

	added, err := NewRepository(db).LinkRelatedStories(ctx, 14, 0.8, 2)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("added related-story links = %d, want both directions", added)
	}
	var links int64
	if err := db.Raw(`SELECT count(*) FROM story_links WHERE story_id IN ? AND other_id IN ? AND relation='related'`, storyIDs, storyIDs).Scan(&links).Error; err != nil {
		t.Fatal(err)
	}
	if links != 2 {
		t.Fatalf("stored related-story links = %d, want 2", links)
	}
}
