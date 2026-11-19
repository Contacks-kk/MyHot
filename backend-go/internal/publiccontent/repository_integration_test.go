package publiccontent

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/topics"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAvailabilityReadsOnlyPublicEditorialItems(t *testing.T) {
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

	prefix := fmt.Sprintf("publiccontent_%d", time.Now().UnixNano())
	sourceIDs := []string{prefix + "_editorial", prefix + "_heat"}
	articleIDs := []string{prefix + "_public", prefix + "_summary", prefix + "_withdrawn", prefix + "_heat"}
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM publications WHERE article_id LIKE ?", prefix+"_%").Error
		_ = orm.Exec("DELETE FROM articles WHERE id LIKE ?", prefix+"_%").Error
		_ = orm.Exec("DELETE FROM sources WHERE id LIKE ?", prefix+"_%").Error
	})
	if err := orm.Exec("INSERT INTO sources (id,name,kind,participation_mode) VALUES (?, 'OpenAI（官网动态）', 'external', 'editorial'), (?, 'Heat-only', 'external', 'hot_signal')", sourceIDs[0], sourceIDs[1]).Error; err != nil {
		t.Fatal(err)
	}
	for i, articleID := range articleIDs {
		sourceID := sourceIDs[0]
		visibility := "public"
		if i == 2 {
			visibility = "withdrawn"
		}
		if i == 3 {
			sourceID = sourceIDs[1]
		}
		if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?, ?, ?, ?, 'Fixture', now(), now())`, articleID, sourceID, articleID, "https://example.invalid/"+articleID).Error; err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			visibility = "summary-only"
		}
		if err := orm.Exec(`INSERT INTO publications (article_id,title,source_id,channel,url,discovered_at,timeline_at,visibility)
			VALUES (?, 'Fixture', ?, 'news', ?, now(), now(), ?)`, articleID, sourceID, "https://example.invalid/"+articleID, visibility).Error; err != nil {
			t.Fatal(err)
		}
	}

	got, err := NewService(NewRepository(orm)).ItemAvailability(context.Background(), articleIDs)
	if err != nil {
		t.Fatal(err)
	}
	if got[articleIDs[0]] != (ItemAvailability{Status: "public", SourceName: "OpenAI"}) || got[articleIDs[1]] != (ItemAvailability{Status: "summary-only"}) || got[articleIDs[2]].Status != "unavailable" || got[articleIDs[3]].Status != "unavailable" {
		t.Fatalf("availability projection = %#v", got)
	}
}

func TestTopicSummariesFollowSelectedSeatsAndCompanyIdentityRules(t *testing.T) {
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

	prefix := fmt.Sprintf("topic_index_%d", time.Now().UnixNano())
	sourceID := prefix + "_source"
	ids := []string{prefix + "_named", prefix + "_only_entity", prefix + "_ambiguous", prefix + "_unselected", prefix + "_scheduled", prefix + "_withdrawn", prefix + "_not_seat"}
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM articles WHERE id LIKE ?", prefix+"_%").Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec("INSERT INTO sources (id,name,kind) VALUES (?, 'Topic fixture', 'rss')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	insert := func(id, title string, tags []string, selected, seat bool, visibility string, visibleAfter time.Time) {
		t.Helper()
		if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, sourceID, id, "https://example.invalid/"+id, title, now, now).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec(`INSERT INTO publications (article_id,source_id,title,url,channel,tags,discovered_at,timeline_at,visibility,eligible,selected,seat,visible_after)
			VALUES (?, ?, ?, ?, 'news', ?, ?, ?, ?, true, ?, ?, ?)`, id, sourceID, title, "https://example.invalid/"+id, tags, now, now, visibility, selected, seat, visibleAfter).Error; err != nil {
			t.Fatal(err)
		}
	}
	insert(ids[0], "OpenAI launches", []string{"entity:openai", "Agent"}, true, true, "public", now.Add(-time.Minute))
	insert(ids[1], "Unnamed company notice", []string{"entity:openai"}, true, true, "public", now.Add(-time.Minute))
	insert(ids[2], "Other company notice", []string{"entity:openai", "entity:anthropic"}, true, true, "public", now.Add(-time.Minute))
	insert(ids[3], "OpenAI unselected", []string{"entity:openai", "Agent"}, false, true, "public", now.Add(-time.Minute))
	insert(ids[4], "OpenAI scheduled", []string{"entity:openai", "Agent"}, true, true, "public", now.Add(time.Minute))
	insert(ids[5], "OpenAI withdrawn", []string{"entity:openai", "Agent"}, true, true, "withdrawn", now.Add(-time.Minute))
	insert(ids[6], "OpenAI no seat", []string{"entity:openai", "Agent"}, true, false, "public", now.Add(-time.Minute))

	pattern := `(?<![A-Za-z])(OpenAI|ChatGPT)(?![A-Za-z])`
	anthropicPattern := `(?<![A-Za-z])(Anthropic)(?![A-Za-z])`
	definitions := []topics.Definition{
		{Slug: "openai", Tags: []string{"entity:openai"}, Pattern: &pattern, Position: 0},
		{Slug: "agent", Tags: []string{"Agent"}, Position: 1},
		{Slug: "anthropic", Tags: []string{"entity:anthropic"}, Pattern: &anthropicPattern, Position: 2},
	}
	repository := NewRepository(orm)
	rows, err := repository.Summaries(context.Background(), definitions, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, row := range rows {
		if row.ID != nil {
			got[row.Slug] = append(got[row.Slug], *row.ID)
		}
	}
	wantCompany := []string{ids[0], ids[1]}
	wantField := []string{ids[0]}
	if fmt.Sprint(got["openai"]) != fmt.Sprint(wantCompany) || fmt.Sprint(got["agent"]) != fmt.Sprint(wantField) {
		t.Fatalf("topic seats = %#v, want company %v and field %v", got, wantCompany, wantField)
	}

	page, err := repository.Page(context.Background(), definitions[0], 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.Recent != 2 || page.PoolTotal != 4 || page.Latest == nil || len(page.Items) != 2 {
		t.Fatalf("topic page projection = %#v", page)
	}
	if page.Items[0].ID != ids[1] || page.Items[1].ID != ids[0] || page.Items[0].SourceName != "Topic fixture" {
		t.Fatalf("topic page items = %#v", page.Items)
	}

	if err := orm.Exec("UPDATE publications SET visibility = 'withdrawn' WHERE article_id = ?", ids[0]).Error; err != nil {
		t.Fatal(err)
	}
	rows, err = repository.Summaries(context.Background(), definitions, now)
	if err != nil {
		t.Fatal(err)
	}
	got = map[string][]string{}
	for _, row := range rows {
		if row.ID != nil {
			got[row.Slug] = append(got[row.Slug], *row.ID)
		}
	}
	page, err = repository.Page(context.Background(), definitions[0], 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got["openai"]) != fmt.Sprint([]string{ids[1]}) || len(got["agent"]) != 0 || page.Total != 1 || page.Latest == nil || page.Latest.Title != "Unnamed company notice" || len(page.Items) != 1 || page.Items[0].ID != ids[1] {
		t.Fatalf("withdrawal remained in the uncached topic index or page: summaries=%#v page=%#v", got, page)
	}

	if err := orm.Exec("UPDATE publications SET tags = ? WHERE article_id = ?", []string{"entity:anthropic"}, ids[1]).Error; err != nil {
		t.Fatal(err)
	}
	rows, err = repository.Summaries(context.Background(), definitions, now)
	if err != nil {
		t.Fatal(err)
	}
	got = map[string][]string{}
	for _, row := range rows {
		if row.ID != nil {
			got[row.Slug] = append(got[row.Slug], *row.ID)
		}
	}
	page, err = repository.Page(context.Background(), definitions[2], 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["openai"]) != 0 || fmt.Sprint(got["anthropic"]) != fmt.Sprint([]string{ids[1]}) || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != ids[1] {
		t.Fatalf("corrected entity topic membership did not refresh: summaries=%#v page=%#v", got, page)
	}
}

func TestStoryFollowupsRepositoryKeepsOnlyCurrentSelectedEvidence(t *testing.T) {
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

	nonce := time.Now().UnixNano()
	prefix := fmt.Sprintf("followups_%d", nonce)
	storyPublicID := fmt.Sprintf("%08x-0000-4000-8000-%012x", uint32(nonce), uint64(nonce)&0xffffffffffff)
	now := time.Now().UTC().Truncate(time.Millisecond)
	const ownedSource = "owned"
	sourceIDs := []string{prefix + "_generic", prefix + "_owned"}
	cleanup := func() {
		_ = orm.Exec("DELETE FROM facts WHERE public_id LIKE ?", prefix+"_fact_%").Error
		_ = orm.Exec("DELETE FROM stories WHERE public_id = ?::uuid", storyPublicID).Error
		_ = orm.Exec("DELETE FROM articles WHERE id LIKE ?", prefix+"_%").Error
		_ = orm.Exec("DELETE FROM sources WHERE id LIKE ?", prefix+"_%").Error
	}
	t.Cleanup(cleanup)
	if err := orm.Exec(`INSERT INTO sources (id,name,kind,tier,participation_mode,config,owner_entity_id)
		VALUES (?, 'Generic', 'rss', 'T2', 'editorial', '{}', NULL), (?, 'Owned', 'rss', 'T2', 'editorial', '{"publisherRole":"organization"}', ?)`, sourceIDs[0], sourceIDs[1], ownedSource).Error; err != nil {
		t.Fatal(err)
	}
	var storyID int64
	if err := orm.Raw("INSERT INTO stories (public_id,title) VALUES (?::uuid,'Fixture') RETURNING id", storyPublicID).Scan(&storyID).Error; err != nil {
		t.Fatal(err)
	}
	var factIDs []int64
	for i := 0; i < 10; i++ {
		var factID int64
		if err := orm.Raw(`INSERT INTO facts (public_id,story_id,title,subject) VALUES (?,?,?,?) RETURNING id`, fmt.Sprintf("%s_fact_%02d", prefix, i), storyID, "Fixture", "ChatGPT").Scan(&factID).Error; err != nil {
			t.Fatal(err)
		}
		factIDs = append(factIDs, factID)
	}
	insertReport := func(id, sourceID string, factID int64, role string, selected bool, visibility string, release time.Time, composite bool, sortAt time.Time) {
		t.Helper()
		if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?,?,?,?,?,?,?)`, id, sourceID, id, "https://example.invalid/"+id, id, now, now).Error; err != nil {
			t.Fatal(err)
		}
		output := "{}"
		if composite {
			output = `{"scope":"composite"}`
		}
		var analysisID int64
		if err := orm.Raw(`INSERT INTO analyses (article_id,input_revision,origin,output) VALUES (?,1,'rule',?::jsonb) RETURNING id`, id, output).Scan(&analysisID).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec(`INSERT INTO publications (article_id,analysis_id,source_id,title,url,channel,discovered_at,timeline_at,sort_at,story_id,fact_id,selected,visible_after,visibility,body_mode,score)
			VALUES (?,?,?,?,?,'news',?,?,?,?,?,?,?,?,?,?)`, id, analysisID, sourceID, id, "https://example.invalid/"+id, now, now, sortAt, storyID, factID, selected, release, visibility, "full", 70).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec("INSERT INTO fact_articles (fact_id,article_id,role) VALUES (?,?,?)", factID, id, role).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, factID := range factIDs {
		sortAt := now.Add(-time.Duration(i+1) * time.Hour)
		genericID := fmt.Sprintf("%s_generic_%02d", prefix, i)
		insertReport(genericID, sourceIDs[0], factID, "report", true, "public", now.Add(-time.Hour), false, sortAt)
		if i == 0 {
			// A source that owns the fact's subject is an eligible representative even without T1 tier.
			insertReport(prefix+"_owned_report", sourceIDs[1], factID, "primary", true, "public", now.Add(-time.Hour), false, sortAt.Add(time.Minute))
		}
	}
	insertReport(prefix+"_mention", sourceIDs[0], factIDs[0], "mention", true, "public", now.Add(-time.Hour), false, now)
	insertReport(prefix+"_composite", sourceIDs[0], factIDs[0], "report", true, "public", now.Add(-time.Hour), true, now)
	insertReport(prefix+"_unselected", sourceIDs[0], factIDs[0], "report", false, "public", now.Add(-time.Hour), false, now)
	insertReport(prefix+"_withdrawn", sourceIDs[0], factIDs[0], "report", true, "withdrawn", now.Add(-time.Hour), false, now)
	insertReport(prefix+"_future", sourceIDs[0], factIDs[0], "report", true, "public", now.Add(time.Hour), false, now)

	rows, err := NewRepository(orm).StoryFollowups(context.Background(), storyPublicID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 11 {
		t.Fatalf("followup evidence count = %d, want 11", len(rows))
	}
	foundOwned := false
	for _, row := range rows {
		if strings.Contains(row.ID, "_mention") || strings.Contains(row.ID, "_composite") || strings.Contains(row.ID, "_unselected") || strings.Contains(row.ID, "_withdrawn") || strings.Contains(row.ID, "_future") {
			t.Fatalf("ineligible row escaped followup query: %#v", row)
		}
		if row.ID == prefix+"_owned_report" {
			foundOwned = row.FactID == factIDs[0] && row.FactSubject != nil && *row.FactSubject == "ChatGPT" && row.OwnerEntityID != nil && *row.OwnerEntityID == ownedSource && row.PublisherRole != nil && *row.PublisherRole == "organization"
		}
	}
	if !foundOwned {
		t.Fatal("owner and publisher role were not joined for the eligible report")
	}
}

func TestGroupReportsRepositoryAppliesEvidenceVisibilityAndFilters(t *testing.T) {
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

	prefix := fmt.Sprintf("group_reports_%d", time.Now().UnixNano())
	factPublicID := prefix + "_fact"
	sourceIDs := []string{prefix + "_t1", prefix + "_t2"}
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM facts WHERE public_id = ?", factPublicID).Error
		_ = orm.Exec("DELETE FROM articles WHERE id LIKE ?", prefix+"_%").Error
		_ = orm.Exec("DELETE FROM sources WHERE id LIKE ?", prefix+"_%").Error
	})
	if err := orm.Exec(`INSERT INTO sources (id,name,kind,tier,participation_mode) VALUES (?, 'T1 fixture', 'rss', 'T1', 'editorial'), (?, 'T2 fixture', 'rss', 'T2', 'editorial')`, sourceIDs[0], sourceIDs[1]).Error; err != nil {
		t.Fatal(err)
	}
	var factID int64
	if err := orm.Raw("INSERT INTO facts (public_id,title) VALUES (?, 'Fixture') RETURNING id", factPublicID).Scan(&factID).Error; err != nil {
		t.Fatal(err)
	}
	insertReport := func(id, sourceID, channel, category, role, visibility string, eligible, selected bool, visibleAfter time.Time, composite bool, timelineAt time.Time) {
		t.Helper()
		if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, sourceID, id, "https://example.invalid/"+id, id, timelineAt, timelineAt).Error; err != nil {
			t.Fatal(err)
		}
		output := "{}"
		if composite {
			output = `{"scope":"composite"}`
		}
		var analysisID int64
		if err := orm.Raw(`INSERT INTO analyses (article_id,input_revision,origin,output) VALUES (?,1,'rule',?::jsonb) RETURNING id`, id, output).Scan(&analysisID).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec(`INSERT INTO publications (article_id,analysis_id,source_id,title,url,channel,category,tags,discovered_at,timeline_at,visibility,eligible,selected,visible_after)
			VALUES (?, ?, ?, ?, ?, ?, ?, ARRAY[?]::text[], ?, ?, ?, ?, ?, ?)`, id, analysisID, sourceID, id, "https://example.invalid/"+id, channel, category, prefix, timelineAt, timelineAt, visibility, eligible, selected, visibleAfter).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec("INSERT INTO fact_articles (fact_id,article_id,role) VALUES (?,?,?)", factID, id, role).Error; err != nil {
			t.Fatal(err)
		}
	}
	insertReport(prefix+"_news", sourceIDs[1], "news", "ai-models", "report", "public", true, false, time.Time{}, false, now.Add(-time.Minute))
	insertReport(prefix+"_x", sourceIDs[0], "x", "ai-models", "primary", "public", true, false, time.Time{}, false, now)
	insertReport(prefix+"_paper", sourceIDs[1], "news", "paper", "report", "public", true, false, time.Time{}, false, now.Add(-2*time.Minute))
	insertReport(prefix+"_mention", sourceIDs[0], "news", "ai-models", "mention", "public", true, false, time.Time{}, false, now.Add(time.Minute))
	insertReport(prefix+"_composite", sourceIDs[0], "news", "ai-models", "report", "public", true, false, time.Time{}, true, now.Add(time.Minute))
	insertReport(prefix+"_withdrawn", sourceIDs[0], "news", "ai-models", "report", "withdrawn", true, false, time.Time{}, false, now.Add(time.Minute))
	insertReport(prefix+"_ineligible", sourceIDs[0], "news", "ai-models", "report", "public", false, false, time.Time{}, false, now.Add(time.Minute))
	insertReport(prefix+"_scheduled", sourceIDs[0], "news", "ai-models", "report", "public", true, true, now.Add(time.Hour), false, now.Add(time.Minute))

	repository := NewRepository(orm)
	rows, err := repository.GroupReports(context.Background(), GroupReportsQuery{FactPublicID: factPublicID, Channel: "all", Tag: prefix}, now)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		gotIDs = append(gotIDs, row.ID)
	}
	wantIDs := []string{prefix + "_x", prefix + "_news", prefix + "_paper"}
	if fmt.Sprint(gotIDs) != fmt.Sprint(wantIDs) {
		t.Fatalf("group reports = %v, want %v", gotIDs, wantIDs)
	}
	rows, err = repository.GroupReports(context.Background(), GroupReportsQuery{FactPublicID: factPublicID, Channel: "firstParty", Category: "ai-models", Tag: prefix}, now)
	if err != nil || len(rows) != 1 || rows[0].ID != prefix+"_x" {
		t.Fatalf("first-party category filter = %#v, %v", rows, err)
	}
}
