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

/** TestV1ItemsRepositoryAppliesPublicScopeSearchAndStableOrdering migrates V1 search and cursor scope. */
func TestV1ItemsRepositoryAppliesPublicScopeSearchAndStableOrdering(t *testing.T) {
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

	prefix := fmt.Sprintf("zzv1items_%d", time.Now().UnixNano())
	sourceID := prefix + "_source"
	selectedID, eligibleID, yieldingID := prefix+"_selected", prefix+"_eligible", prefix+"_yielding"
	withdrawnID, ineligibleID := prefix+"_withdrawn", prefix+"_ineligible"
	ids := []string{selectedID, eligibleID, yieldingID, withdrawnID, ineligibleID}
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM pool_search WHERE article_id IN ?", ids).Error
		_ = orm.Exec("DELETE FROM articles WHERE id IN ?", ids).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := orm.Exec("INSERT INTO sources(id,name,kind) VALUES (?, 'V1 fixture source', 'external')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, sourceID, id, "https://fixture.example/"+id, "Fixture "+id, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,url,channel,discovered_at,timeline_at,sort_at,visibility,eligible,selected,seat,visible_after,search_text)
		VALUES (?, ?, 'Selected OpenAI fixture', 'https://fixture.example/selected', 'news', ?, ?, ?, 'public', true, true, true, ?, 'openai selected title'),
		       (?, ?, 'Eligible body fixture', 'https://fixture.example/eligible', 'news', ?, ?, ?, 'public', true, false, false, NULL, 'other direct terms'),
		       (?, ?, 'Yielding selected fixture', 'https://fixture.example/yielding', 'news', ?, ?, ?, 'public', true, true, false, ?, 'other yielding terms'),
		       (?, ?, 'Withdrawn OpenAI fixture', 'https://fixture.example/withdrawn', 'news', ?, ?, ?, 'withdrawn', true, false, false, NULL, 'openai needle'),
		       (?, ?, 'Ineligible OpenAI fixture', 'https://fixture.example/ineligible', 'news', ?, ?, ?, 'public', false, false, false, NULL, 'openai needle')`,
		selectedID, sourceID, now.Add(-time.Minute), now.Add(-time.Minute), now.Add(-time.Minute), now.Add(-time.Minute),
		eligibleID, sourceID, now, now, now,
		yieldingID, sourceID, now.Add(-2*time.Minute), now.Add(-2*time.Minute), now.Add(-2*time.Minute), now.Add(-2*time.Minute),
		withdrawnID, sourceID, now, now, now,
		ineligibleID, sourceID, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("UPDATE publications SET category = 'security' WHERE article_id = ?", selectedID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("UPDATE publications SET category = 'research' WHERE article_id = ?", eligibleID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO pool_search(article_id,direct,body)
		VALUES (?, 'openai selected title', ''), (?, 'other direct terms', 'openai body needle'),
		       (?, 'other yielding terms', ''), (?, 'openai needle', 'openai needle'), (?, 'openai needle', 'openai needle')`,
		selectedID, eligibleID, yieldingID, withdrawnID, ineligibleID).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(orm)
	selectedRows, err := repository.V1Items(context.Background(), V1ItemsQuery{Mode: "selected", Window: "7d", By: "timeline", Limit: 10}, now, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selectedRows) != 1 || selectedRows[0].ID != selectedID || !selectedRows[0].Selected {
		t.Fatalf("selected rows = %#v", selectedRows)
	}
	if err := orm.Exec("DELETE FROM pool_search WHERE article_id = ?", selectedID).Error; err != nil {
		t.Fatal(err)
	}
	selectedSearch := "openai"
	directSelectedRows, err := repository.V1Items(context.Background(), V1ItemsQuery{Mode: "selected", Window: "7d", By: "timeline", Search: &selectedSearch, Limit: 10}, now, nil, "", []string{"openai"})
	if err != nil || len(directSelectedRows) != 1 || directSelectedRows[0].ID != selectedID {
		t.Fatalf("selected direct match without pool_search = %#v, err=%v", directSelectedRows, err)
	}

	allRows, err := repository.V1Items(context.Background(), V1ItemsQuery{Mode: "all", Window: "7d", By: "timeline", Limit: 10}, now, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	allByID := map[string]V1ItemRecord{}
	for _, row := range allRows {
		allByID[row.ID] = row
	}
	if len(allByID) != 3 || allByID[eligibleID].ID == "" || allByID[yieldingID].Selected || !allByID[selectedID].Selected {
		t.Fatalf("all rows = %#v", allRows)
	}
	publicCategory := "research"
	mergedRows, err := repository.V1Items(context.Background(), V1ItemsQuery{
		Mode:         "all",
		Window:       "7d",
		By:           "timeline",
		Category:     &publicCategory,
		CategoryKeys: []string{"security", "research"},
		Limit:        10,
	}, now, nil, "", nil)
	if err != nil || len(mergedRows) != 2 {
		t.Fatalf("merged public category rows = %#v, err=%v", mergedRows, err)
	}
	mergedIDs := map[string]bool{mergedRows[0].ID: true, mergedRows[1].ID: true}
	if !mergedIDs[selectedID] || !mergedIDs[eligibleID] {
		t.Fatalf("merged public category ids = %#v", mergedIDs)
	}

	searchRows, err := repository.V1Items(context.Background(), V1ItemsQuery{Mode: "all", Window: "7d", By: "timeline", Limit: 10}, now, nil, "", []string{"openai", "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(searchRows) != 1 || searchRows[0].ID != eligibleID {
		t.Fatalf("AND search rows = %#v", searchRows)
	}

	after := now.Add(-90 * time.Second)
	publishedRows, err := repository.V1Items(context.Background(), V1ItemsQuery{Mode: "all", Window: "7d", By: "published", Limit: 10}, now, &after, eligibleID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(publishedRows) != 1 || publishedRows[0].ID != yieldingID {
		t.Fatalf("published cursor rows = %#v, want only the next row", publishedRows)
	}
}

/** TestSitePoolRelevanceRanksPublicMatchesAndCapsTotals migrates the optimized read-path contract. */
func TestSitePoolRelevanceRanksPublicMatchesAndCapsTotals(t *testing.T) {
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

	prefix := fmt.Sprintf("zzsitepool_%d", time.Now().UnixNano())
	sourceID := prefix + "_source"
	itemID := func(number int) string {
		return fmt.Sprintf("%s-%04d", prefix, number)
	}
	cleanup := func() {
		_ = orm.Exec("DELETE FROM pool_search WHERE article_id IN (SELECT id FROM articles WHERE source_id = ?)", sourceID).Error
		_ = orm.Exec("DELETE FROM publications WHERE source_id = ?", sourceID).Error
		_ = orm.Exec("DELETE FROM articles WHERE source_id = ?", sourceID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	}
	t.Cleanup(cleanup)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := orm.Exec("INSERT INTO sources(id,name,kind,tier,participation_mode,next_fetch_at) VALUES (?, 'Site pool fixture', 'rss', 'T1', 'editorial', '2100-01-01')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,published_at,body_html,language)
		SELECT ? || '-' || lpad(n::text, 4, '0'), ?, ? || '-' || n, 'https://fixture.example/' || ? || '/' || n,
			? || CASE WHEN n = 1 THEN ' needle' ELSE ' general' END, ?, ?, ?, '<p>licensed body</p>', 'en'
		FROM generate_series(1, 2108) n`, prefix, sourceID, prefix, prefix, prefix, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications(article_id,title,source_id,channel,url,discovered_at,timeline_at,published_at,sort_at,
		eligible,selected,visible_after,visibility,search_text,tags,summary,body_mode,syndicate)
		SELECT a.id,a.title,a.source_id,'news',a.url,a.discovered_at,a.timeline_at,a.published_at,a.timeline_at,
			a.id <> ?,a.id = ?,?,CASE WHEN a.id = ? THEN 'withdrawn' ELSE 'public' END,
			? || ' needle',ARRAY[?]::text[],'fixture summary','full',false
		FROM articles a WHERE a.source_id = ?`, itemID(2108), itemID(2108), now.Add(-time.Second), itemID(2106), prefix, prefix, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO pool_search(article_id,direct,body)
		SELECT article_id,search_text,CASE WHEN right(article_id,4)::int % 2 = 0 THEN 'needle bodyonly' ELSE 'needle' END
		FROM publications WHERE source_id = ? AND eligible`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	// Mark isolated direct, body, combined, and withdrawn matches in the search index.
	sharedTerm := prefix + "-both"
	directTerm := prefix + "-direct"
	bodyTerm := prefix + "-body"
	if err := orm.Exec("UPDATE pool_search SET direct = direct || ' ' || ? WHERE article_id IN (?, ?)", sharedTerm, itemID(1), itemID(2)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("UPDATE pool_search SET body = body || ' ' || ? WHERE article_id IN (?, ?)", sharedTerm, itemID(2), itemID(3)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("UPDATE pool_search SET direct = direct || ' ' || ?, body = body || ' ' || ? WHERE article_id = ?", directTerm, bodyTerm, itemID(4)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("UPDATE pool_search SET direct = direct || ' ' || ? WHERE article_id = ?", sharedTerm, itemID(2106)).Error; err != nil {
		t.Fatal(err)
	}

	// Exercise the optimized site-pool query through the same service used by HTTP handlers.
	repository := NewRepository(orm)
	service := NewService(repository)
	tag := prefix
	search := prefix + " needle"
	filters := SitePoolQuery{Channel: "all", Tag: &tag, Search: &search, Tab: "relevance"}
	first, err := service.SitePool(context.Background(), filters, now, "", true)
	if err != nil || first.Total != sitePoolCountCap || first.PageCount != sitePoolMaxPages || len(first.Items) != sitePoolPageSize || first.Items[0].ID != itemID(1) || first.Items[1].ID != itemID(2107) {
		t.Fatalf("relevance page = %#v, err=%v", first, err)
	}
	last, err := service.SitePool(context.Background(), SitePoolQuery{Channel: "all", Tag: &tag, Search: &search, Tab: "relevance", Page: 500}, now, "", true)
	if err != nil || last.Page != sitePoolMaxPages || last.Total != sitePoolCountCap || len(last.Items) != sitePoolPageSize {
		t.Fatalf("capped last page = page %d total %d items %d, err=%v", last.Page, last.Total, len(last.Items), err)
	}
	sparseSearch := prefix + " general"
	sparse, err := service.SitePool(context.Background(), SitePoolQuery{Channel: "all", Tag: &tag, Search: &sparseSearch, Tab: "relevance", Page: 2}, now, "", true)
	if err != nil || sparse.Total != 0 || len(sparse.Items) != 0 {
		t.Fatalf("unmatched title-only terms entered relevance results: %#v, err=%v", sparse, err)
	}
	bodySearch := prefix + " bodyonly"
	beyond, err := service.SitePool(context.Background(), SitePoolQuery{Channel: "all", Tag: &tag, Search: &bodySearch, Tab: "relevance", Page: 50}, now, "", true)
	if err != nil || beyond.Total != 1052 || len(beyond.Items) != 0 || beyond.PageCount != 27 {
		t.Fatalf("empty deep relevance page lost its filtered total: %#v, err=%v", beyond, err)
	}
	shared, err := service.SitePool(context.Background(), SitePoolQuery{Channel: "all", Search: &sharedTerm, Tab: "relevance"}, now, "", true)
	if err != nil || shared.Total != 3 || len(shared.Items) != 3 || shared.Items[0].ID != itemID(2) || shared.Items[1].ID != itemID(1) || shared.Items[2].ID != itemID(3) {
		t.Fatalf("direct/body relevance ranking or withdrawn filtering changed: %#v, err=%v", shared, err)
	}
	for _, term := range []string{directTerm, bodyTerm} {
		side, err := service.SitePool(context.Background(), SitePoolQuery{Channel: "all", Search: &term, Tab: "relevance"}, now, "", true)
		if err != nil || side.Total != 1 || len(side.Items) != 1 || side.Items[0].ID != itemID(4) {
			t.Fatalf("single-field relevance for %q = %#v, err=%v", term, side, err)
		}
	}
	sharedDeepPage, err := service.SitePool(context.Background(), SitePoolQuery{Channel: "all", Search: &sharedTerm, Tab: "relevance", Page: 50}, now, "", true)
	if err != nil || sharedDeepPage.Total != 3 || sharedDeepPage.PageCount != 1 || len(sharedDeepPage.Items) != 0 {
		t.Fatalf("empty relevance page lost its total: %#v, err=%v", sharedDeepPage, err)
	}
	missingTerm := prefix + "-missing"
	empty, err := service.SitePool(context.Background(), SitePoolQuery{Channel: "all", Search: &missingTerm, Tab: "relevance"}, now, "", true)
	if err != nil || empty.Total != 0 || empty.PageCount != 1 || len(empty.Items) != 0 {
		t.Fatalf("unmatched relevance search = %#v, err=%v", empty, err)
	}
}
