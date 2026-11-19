package publiccontent

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

func TestHotTopicsRepositoryRechecksTheSavedRepresentativeAgainstCurrentPublication(t *testing.T) {
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

	prefix := fmt.Sprintf("v1hot_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	publicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	factID := prefix + "_fact"
	ruleVersion := prefix + "_ranking"
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM hot_rankings WHERE rule_version = ?", ruleVersion).Error
		_ = orm.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM facts WHERE public_id = ?", factID).Error
		_ = orm.Exec("DELETE FROM stories WHERE public_id = ?", publicID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := orm.Exec("INSERT INTO sources(id,name,kind) VALUES (?, 'Fixture source（channel）', 'external')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO stories(public_id,title) VALUES (?, 'Fixture story')", publicID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO facts(public_id,story_id,title) VALUES (?, (SELECT id FROM stories WHERE public_id = ?), 'Fixture fact')", factID, publicID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
		VALUES (?, ?, ?, 'https://source.example/fixture', 'Fixture article', ?, ?)`, articleID, sourceID, prefix+"_identity", now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,url,channel,discovered_at,timeline_at,sort_at,visibility,eligible,selected)
		VALUES (?, ?, 'Fixture article', 'https://source.example/fixture', 'news', ?, ?, ?, 'public', true, false)`, articleID, sourceID, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO fact_articles(fact_id,article_id,role) VALUES ((SELECT id FROM facts WHERE public_id = ?), ?, 'primary')`, factID, articleID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO hot_rankings(computed_at,rule_version,entries,evidence,published)
		VALUES (now() + interval '1 hour', ?, jsonb_build_array(jsonb_build_object(
		'rank', 1,
		'storyId', (SELECT id FROM stories WHERE public_id = ?),
		'storyPublicId', ?::text, 'representativeItemId', ?::text, 'sourceCount', 2, 'signalCount', 1,
		'participantCount', 3, 'sourceNames', jsonb_build_array('Fixture source（channel）'))), '{}', true)`, ruleVersion, publicID, publicID, articleID).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := NewRepository(orm).HotTopics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RepresentativeSource == nil || *rows[0].RepresentativeSource != "Fixture source（channel）" || rows[0].RepresentativeURL != "https://source.example/fixture" || rows[0].StoryTitle != "Fixture story" {
		t.Fatalf("current hot topics = %#v", rows)
	}
	siteRanking, err := NewRepository(orm).SiteHotRanking(context.Background(), now)
	if err != nil || siteRanking == nil || len(siteRanking.Entries) != 1 || siteRanking.Entries[0].StoryPublicID != publicID || siteRanking.Entries[0].RepresentativeURL != "https://source.example/fixture" {
		t.Fatalf("current site hot ranking = %#v, err=%v", siteRanking, err)
	}
	details, err := NewRepository(orm).SiteHotDetails(context.Background(), siteRanking.Entries, siteRanking.ComputedAt, now)
	if err != nil || details.Texts[siteRanking.Entries[0].StoryID].Latest == nil || *details.Texts[siteRanking.Entries[0].StoryID].Latest != "Fixture article" {
		t.Fatalf("site hot details = %#v, err=%v", details, err)
	}
	if err := orm.Exec("UPDATE publications SET visibility = 'withdrawn' WHERE article_id = ?", articleID).Error; err != nil {
		t.Fatal(err)
	}
	rows, err = NewRepository(orm).HotTopics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("withdrawn saved representative remained visible: %#v", rows)
	}
	siteRanking, err = NewRepository(orm).SiteHotRanking(context.Background(), now)
	if err != nil || siteRanking == nil || len(siteRanking.Entries) != 0 {
		t.Fatalf("withdrawn site hot representative remained visible: %#v, err=%v", siteRanking, err)
	}
}

/** TestSiteHotFacesFollowCurrentStorySignals preserves source icon, avatar ordering, and event scope. */
func TestSiteHotFacesFollowCurrentStorySignals(t *testing.T) {
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

	prefix := fmt.Sprintf("sitehotfaces_%d", time.Now().UnixNano())
	storyPublicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	otherStoryPublicID := fmt.Sprintf("00000000-0000-4000-8001-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	storyIDs := make(map[string]int64, 2)
	for _, publicID := range []string{storyPublicID, otherStoryPublicID} {
		if err := orm.Exec("INSERT INTO stories(public_id,title) VALUES (?, 'Share face fixture')", publicID).Error; err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := orm.Table("stories").Select("id").Where("public_id = ?", publicID).Scan(&id).Error; err != nil {
			t.Fatal(err)
		}
		storyIDs[publicID] = id
	}
	type sourceFixture struct {
		key     string
		name    string
		iconURL *string
	}
	iconURL := "https://images.example.invalid/source-icon.png"
	sources := []sourceFixture{
		{key: "icon", name: prefix + "-icon", iconURL: &iconURL},
		{key: "latest", name: prefix + "-latest"},
		{key: "tie", name: prefix + "-tie"},
		{key: "null", name: prefix + "-null"},
		{key: "empty", name: prefix + "-empty"},
		{key: "outside", name: prefix + "-outside"},
		{key: "none", name: prefix + "-none"},
		{key: "same-a", name: prefix + "-same"},
		{key: "same-z", name: prefix + "-same"},
	}
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM story_signals WHERE story_id IN ?", []int64{storyIDs[storyPublicID], storyIDs[otherStoryPublicID]}).Error
		_ = orm.Exec("DELETE FROM articles WHERE id LIKE ?", prefix+"-%").Error
		_ = orm.Exec("DELETE FROM stories WHERE id IN ?", []int64{storyIDs[storyPublicID], storyIDs[otherStoryPublicID]}).Error
		_ = orm.Exec("DELETE FROM sources WHERE id LIKE ?", prefix+"-%").Error
	})
	for _, source := range sources {
		sourceID := prefix + "-" + source.key
		if err := orm.Exec("INSERT INTO sources(id,name,kind,tier,icon_url) VALUES (?, ?, 'rss', 'T1', ?)", sourceID, source.name, source.iconURL).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	add := func(sourceKey, suffix string, offset time.Duration, xPost *string, publicIDs ...string) {
		t.Helper()
		sourceID := prefix + "-" + sourceKey
		articleID := prefix + "-" + sourceKey + "-" + suffix
		at := now.Add(offset)
		if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,x_post)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, articleID, sourceID, articleID, "https://example.invalid/"+articleID, articleID, at, at, xPost).Error; err != nil {
			t.Fatal(err)
		}
		for _, publicID := range publicIDs {
			if err := orm.Exec(`INSERT INTO story_signals(story_id,article_id,participant_key,source_id,kind,observed_at)
				VALUES (?, ?, ?, ?, 'editorial', ?)`, storyIDs[publicID], articleID, sourceID, sourceID, at).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	post := func(raw string) *string { return &raw }
	mainStory := storyPublicID
	add("icon", "old", 0, post(`{"avatarUrl":"https://images.example.invalid/old.png"}`), mainStory)
	add("icon", "new", time.Minute, post(`{"avatarUrl":"https://images.example.invalid/ignored.png"}`), mainStory)
	add("latest", "valid", 0, post(`{"avatarUrl":"https://images.example.invalid/latest.png"}`), mainStory)
	add("latest", "no-key", time.Minute, post(`{}`), mainStory)
	add("tie", "a", time.Minute, post(`{"avatarUrl":"https://images.example.invalid/tie-a.png"}`), mainStory)
	add("tie", "z", time.Minute, post(`{"avatarUrl":"https://images.example.invalid/tie-z.png"}`), mainStory)
	add("null", "old", 0, post(`{"avatarUrl":"https://images.example.invalid/null-old.png"}`), mainStory)
	add("null", "new", time.Minute, post(`{"avatarUrl":null}`), mainStory)
	add("empty", "old", 0, post(`{"avatarUrl":"https://images.example.invalid/empty-old.png"}`), mainStory)
	add("empty", "new", time.Minute, post(`{"avatarUrl":""}`), mainStory)
	add("outside", "only", time.Minute, post(`{"avatarUrl":"https://images.example.invalid/outside.png"}`), otherStoryPublicID)
	add("none", "no-avatar", 0, nil, mainStory)
	add("same-a", "avatar", time.Minute, post(`{"avatarUrl":"https://images.example.invalid/same-a.png"}`), mainStory)
	add("same-z", "avatar", time.Minute, post(`{"avatarUrl":"https://images.example.invalid/same-z.png"}`), mainStory)

	details := SiteHotDetails{Faces: []SiteHotFaceRecord{}}
	if err := NewRepository(orm).loadSiteHotFaces(context.Background(), &details, []int64{storyIDs[mainStory]}); err != nil {
		t.Fatal(err)
	}
	faces := make(map[string]*string, len(details.Faces))
	for _, face := range details.Faces {
		faces[face.Name] = face.URL
	}
	checks := map[string]*string{
		prefix + "-icon":   &iconURL,
		prefix + "-latest": stringPointer("https://images.example.invalid/latest.png"),
		prefix + "-tie":    stringPointer("https://images.example.invalid/tie-z.png"),
		prefix + "-null":   nil,
		prefix + "-none":   nil,
	}
	for name, want := range checks {
		got, exists := faces[name]
		if !exists || (got == nil) != (want == nil) || got != nil && *got != *want {
			t.Fatalf("face %q = %v, want %v", name, got, want)
		}
	}
	if got := faces[prefix+"-empty"]; got == nil || *got != "" {
		t.Fatalf("empty avatar key = %v, want an empty value rather than the older image", got)
	}
	if _, exists := faces[prefix+"-outside"]; exists {
		t.Fatal("a signal from another story entered this story's face set")
	}
	if faces[prefix+"-same"] == nil || *faces[prefix+"-same"] != "https://images.example.invalid/same-z.png" {
		t.Fatalf("same-name source precedence = %v", faces[prefix+"-same"])
	}
}

/** TestSiteHotCoversFollowCurrentVisibilityAndRights drops withdrawn or summary-only images. */
func TestSiteHotCoversFollowCurrentVisibilityAndRights(t *testing.T) {
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

	prefix := fmt.Sprintf("sitehotcover_%d", time.Now().UnixNano())
	sourceID := prefix + "_source"
	factID := prefix + "_fact"
	publicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	articleIDs := []string{prefix + "_representative", prefix + "_primary", prefix + "_fallback"}
	now := time.Now().UTC().Truncate(time.Second)
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM publications WHERE article_id IN ?", articleIDs).Error
		_ = orm.Exec("DELETE FROM articles WHERE id IN ?", articleIDs).Error
		_ = orm.Exec("DELETE FROM facts WHERE public_id = ?", factID).Error
		_ = orm.Exec("DELETE FROM stories WHERE public_id = ?", publicID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec("INSERT INTO sources(id,name,kind,tier,participation_mode) VALUES (?, 'Cover fixture', 'rss', 'T1', 'editorial')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO stories(public_id,title) VALUES (?, 'Cover fixture')", publicID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO facts(public_id,story_id,title) VALUES (?, (SELECT id FROM stories WHERE public_id = ?), 'Cover fixture')", factID, publicID).Error; err != nil {
		t.Fatal(err)
	}
	var storyID int64
	if err := orm.Table("stories").Select("id").Where("public_id = ?", publicID).Scan(&storyID).Error; err != nil {
		t.Fatal(err)
	}
	images := []string{"[]", `[{"kind":"image","url":"https://images.example.invalid/primary.png","width":800,"height":450}]`, `[{"kind":"image","url":"https://images.example.invalid/fallback.png","width":800,"height":450}]`}
	for index, articleID := range articleIDs {
		if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,media)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?::jsonb)`, articleID, sourceID, articleID, "https://example.invalid/"+articleID, articleID, now, now, images[index]).Error; err != nil {
			t.Fatal(err)
		}
		bodyMode := "full"
		if index == 0 {
			bodyMode = "summary"
		}
		score := []int{70, 90, 80}[index]
		if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,url,channel,discovered_at,timeline_at,sort_at,visible_after,
			visibility,eligible,selected,body_mode,story_id,fact_id,score)
			VALUES (?, ?, ?, ?, 'news', ?, ?, ?, ?, 'public', true, true, ?, ?,
			(SELECT id FROM facts WHERE public_id = ?), ?)`, articleID, sourceID, articleID, "https://example.invalid/"+articleID,
			now, now, now, now, bodyMode, storyID, factID, score).Error; err != nil {
			t.Fatal(err)
		}
	}
	repository := NewRepository(orm)
	cover := func() *SiteHotCoverRecord {
		t.Helper()
		details := SiteHotDetails{Covers: make(map[int64]SiteHotCoverRecord)}
		if err := repository.loadSiteHotCovers(context.Background(), &details, []int64{storyID}, []string{articleIDs[0]}, now); err != nil {
			t.Fatal(err)
		}
		result, exists := details.Covers[storyID]
		if !exists {
			return nil
		}
		return &result
	}
	if got := cover(); got == nil || got.URL != "https://images.example.invalid/primary.png" {
		t.Fatalf("initial cover = %#v", got)
	}
	if err := orm.Exec("UPDATE publications SET visibility = 'withdrawn' WHERE article_id = ?", articleIDs[1]).Error; err != nil {
		t.Fatal(err)
	}
	if got := cover(); got == nil || got.URL != "https://images.example.invalid/fallback.png" {
		t.Fatalf("withdrawn cover did not fall back to a current public report: %#v", got)
	}
	if err := orm.Exec("UPDATE publications SET body_mode = 'summary' WHERE article_id = ?", articleIDs[2]).Error; err != nil {
		t.Fatal(err)
	}
	if got := cover(); got != nil {
		t.Fatalf("summary-only media remained a cover: %#v", got)
	}
}

/** TestSiteHotRankingRechecksAfterAnEmptyRead preserves cold-read recovery for published rankings. */
func TestSiteHotRankingRechecksAfterAnEmptyRead(t *testing.T) {
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
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	tx := orm.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	if err := tx.Exec(`CREATE TEMP TABLE hot_rankings (
		id bigint PRIMARY KEY,
		computed_at timestamptz NOT NULL,
		published boolean NOT NULL,
		entries jsonb NOT NULL
	) ON COMMIT DROP`).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(tx)
	now := time.Date(2026, time.November, 18, 1, 0, 0, 0, time.UTC)
	empty, err := repository.SiteHotRanking(context.Background(), now)
	if err != nil || empty != nil {
		t.Fatalf("initial empty ranking = %#v, err=%v", empty, err)
	}
	computedAt := now.Add(time.Minute)
	if err := tx.Exec(`INSERT INTO hot_rankings (id, computed_at, published, entries) VALUES (1, ?, true, '[]'::jsonb)`, computedAt).Error; err != nil {
		t.Fatal(err)
	}
	latest, err := repository.SiteHotRanking(context.Background(), now)
	if err != nil || latest == nil || !latest.ComputedAt.Equal(computedAt) || latest.Entries == nil || len(latest.Entries) != 0 {
		t.Fatalf("ranking after initial empty read = %#v, err=%v", latest, err)
	}
}
