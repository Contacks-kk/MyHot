package hotranking

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/heatsnapshot"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestHotRankingRepositoriesComputeAndPersistCurrentHeat(t *testing.T) {
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

	prefix := fmt.Sprintf("hotrank_%d", time.Now().UnixNano())
	publicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	articleIDs := []string{prefix + "_article_a", prefix + "_article_b"}
	sourceIDs := []string{prefix + "_source_a", prefix + "_source_b"}
	factID := prefix + "_fact"
	var rankingID int64
	t.Cleanup(func() {
		if rankingID != 0 {
			_ = orm.Exec("DELETE FROM hot_rankings WHERE id = ?", rankingID).Error
		}
		_ = orm.Exec("DELETE FROM facts WHERE public_id = ?", factID).Error
		_ = orm.Exec("DELETE FROM stories WHERE public_id = ?", publicID).Error
		_ = orm.Exec("DELETE FROM articles WHERE id IN ?", articleIDs).Error
		_ = orm.Exec("DELETE FROM sources WHERE id IN ?", sourceIDs).Error
	})

	now := time.Now().UTC().Truncate(time.Millisecond)
	first := now.Add(-90 * time.Minute)
	for i, sourceID := range sourceIDs {
		if err := orm.Exec(`INSERT INTO sources(id,name,kind,tier,participation_mode,enabled,last_ok_at,created_at)
			VALUES (?, ?, 'rss', ?, 'editorial', true, ?, ?)`, sourceID, fmt.Sprintf("Fixture source %d", i), []string{"T1", "T2"}[i], now, now.Add(-7*24*time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := orm.Exec(`INSERT INTO stories(public_id,title,first_report_at,latest_at) VALUES (?, 'Fixture launch', ?, ?)`, publicID, first, first).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO facts(public_id,story_id,title,subject)
		VALUES (?, (SELECT id FROM stories WHERE public_id = ?), 'Fixture launch', 'Fixture Corp')`, factID, publicID).Error; err != nil {
		t.Fatal(err)
	}
	for i, articleID := range articleIDs {
		if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, articleID, sourceIDs[i], prefix+"_identity_"+fmt.Sprint(i), "https://fixture.invalid/"+articleID, "Fixture report", first, first).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,url,channel,discovered_at,timeline_at,visibility,eligible,selected,body_mode)
			VALUES (?, ?, 'Fixture report', ?, 'news', ?, ?, 'public', true, true, 'summary')`, articleID, sourceIDs[i], "https://fixture.invalid/"+articleID, first, first).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec("UPDATE publications SET visible_after = ? WHERE article_id = ?", first, articleID).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec(`INSERT INTO fact_articles(fact_id,article_id,role) VALUES ((SELECT id FROM facts WHERE public_id = ?), ?, 'report')`, factID, articleID).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec(`INSERT INTO story_signals(story_id,article_id,participant_key,source_id,kind,observed_at)
			VALUES ((SELECT id FROM stories WHERE public_id = ?), ?, ?, ?, 'editorial', ?)`, publicID, articleID, "source:"+sourceIDs[i], sourceIDs[i], first).Error; err != nil {
			t.Fatal(err)
		}
	}

	var storyID int64
	if err := orm.Table("stories").Select("id").Where("public_id = ?", publicID).Scan(&storyID).Error; err != nil {
		t.Fatal(err)
	}
	heatRepository := heatsnapshot.NewRepository(orm)
	storyRows, err := heatRepository.HeatRows(context.Background(), now, []string{}, []int64{storyID}, heatsnapshot.CommunityFeeds{})
	if err != nil {
		t.Fatal(err)
	}
	if len(storyRows) != 1 || storyRows[0].StoryID != storyID || storyRows[0].Participants != 2 || storyRows[0].EditorialParticipants != 2 || storyRows[0].BehindParticipants != 0 {
		t.Fatalf("computed heat rows = %#v", storyRows)
	}
	participants, err := heatRepository.Participants(context.Background(), storyID, now, heatsnapshot.CommunityFeeds{})
	if err != nil {
		t.Fatal(err)
	}
	if len(participants) != 2 {
		t.Fatalf("current heat participants = %#v", participants)
	}
	reports, err := NewRepository(orm).Reports(context.Background(), storyID, now)
	if err != nil || len(reports) != 2 {
		t.Fatalf("current representative reports = %#v, err=%v", reports, err)
	}
	entry := Entry{Rank: 1, StoryID: storyID, StoryPublicID: publicID, Title: "Fixture launch", RepresentativeItemID: reports[0].ID}
	rankingID, err = NewRepository(orm).Save(context.Background(), now, []Entry{entry}, 1)
	if err != nil || rankingID == 0 {
		t.Fatalf("save hot ranking id=%d, err=%v", rankingID, err)
	}
	var stored struct {
		RuleVersion string `gorm:"column:rule_version"`
		Entries     []byte `gorm:"column:entries"`
		Evidence    []byte `gorm:"column:evidence"`
	}
	if err := orm.Table("hot_rankings").Select("rule_version, entries, evidence").Where("id = ?", rankingID).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.RuleVersion != ruleVersion || !strings.Contains(string(stored.Entries), publicID) || !strings.Contains(string(stored.Evidence), `"windowHours":48`) {
		t.Fatalf("saved hot ranking = %#v", stored)
	}
}
