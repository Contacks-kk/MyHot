package publiccontent

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSiteTimelineRepositoryGroupsFactsAndHydratesRepresentative(t *testing.T) {
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
	}
	orm, err := gorm.Open(postgres.Open(raw), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := orm.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })

	prefix := fmt.Sprintf("sitetimeline_%d", time.Now().UnixNano())
	sourceIDs := []string{prefix + "_official", prefix + "_person", prefix + "_other"}
	articleIDs := []string{prefix + "_official", prefix + "_person", prefix + "_other"}
	var factID int64
	cleanup := func() {
		if factID != 0 {
			_ = orm.Exec("DELETE FROM facts WHERE id = ?", factID).Error
		}
		_ = orm.Exec("DELETE FROM articles WHERE id LIKE ?", prefix+"_%").Error
		_ = orm.Exec("DELETE FROM sources WHERE id LIKE ?", prefix+"_%").Error
	}
	t.Cleanup(cleanup)
	if err := orm.Exec(`INSERT INTO sources (id,name,kind,tier,participation_mode,config,owner_entity_id)
		VALUES (?, 'Official fixture', 'rss', 'T2', 'editorial', '{"publisherRole":"organization"}', 'openai'),
		       (?, 'Person fixture', 'rss', 'T2', 'editorial', '{"publisherRole":"person"}', 'openai'),
		       (?, 'Other fixture', 'rss', 'T2', 'editorial', '{}', NULL)`, sourceIDs[0], sourceIDs[1], sourceIDs[2]).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := orm.Raw("INSERT INTO facts (public_id,title,subject) VALUES (?, 'Timeline fixture fact', 'OpenAI / Product') RETURNING id", prefix+"_fact").Row().Scan(&factID); err != nil {
		t.Fatal(err)
	}
	for i, id := range articleIDs {
		sourceID := sourceIDs[i]
		at := now.Add(-time.Duration(i+1) * time.Minute)
		if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, sourceID, id, "https://fixture.example/"+id, "Article "+id, at, at).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec(`INSERT INTO publications (article_id,source_id,title,summary,reason,category,tags,score,channel,url,published_at,discovered_at,timeline_at,sort_at,visibility,eligible,selected,visible_after,body_mode,fact_id)
			VALUES (?, ?, ?, 'Fixture summary', 'Fixture reason', 'ai-models', ARRAY['Agent'], ?, 'news', ?, ?, ?, ?, ?, 'public', true, true, ?, ?, ?)`,
			id, sourceID, "Article "+id, 80-i, "https://fixture.example/"+id, at, at, at, at, at, map[bool]string{true: "summary", false: "full"}[i == 0], factID).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec("INSERT INTO fact_articles (fact_id,article_id,role) VALUES (?, ?, 'report')", factID, id).Error; err != nil {
			t.Fatal(err)
		}
	}

	query := SiteTimelineQuery{Channel: "news", Tag: stringPointer("Agent"), Limit: 20}
	service := NewService(NewRepository(orm), map[string]interests.Entity{"openai": {Name: "OpenAI", Aliases: []string{"Open AI"}}})
	got, err := service.SiteTimeline(context.Background(), query, now, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cards) != 1 || got.Cards[0].Key != "f"+fmt.Sprint(factID) || got.Cards[0].Item.ID != articleIDs[0] || got.Cards[0].Item.Title != "Article "+articleIDs[0] {
		t.Fatalf("timeline projection = %#v", got.Cards)
	}
	if got.Cards[0].Group == nil || got.Cards[0].Group.ReportCount != 3 || got.Cards[0].Group.AdditionalSourceCount != 2 {
		t.Fatalf("fact grouping = %#v", got.Cards[0].Group)
	}
	if got.DayCounts[beijingDate(now.Add(-3*time.Minute))] != 1 {
		t.Fatalf("Beijing day counts = %#v", got.DayCounts)
	}
}
