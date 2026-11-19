package publiccontent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
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
	var hydratedArticleIDs []string
	callbackName := "test_capture_site_timeline_representative_hydration"
	if err := orm.Callback().Query().After("gorm:query").Register(callbackName, func(db *gorm.DB) {
		statement := db.Statement.SQL.String()
		if !strings.Contains(statement, "a.x_post") || !strings.Contains(statement, "p.article_id IN") {
			return
		}
		for _, value := range db.Statement.Vars {
			if articleID, ok := value.(string); ok && strings.HasPrefix(articleID, prefix+"_") {
				hydratedArticleIDs = append(hydratedArticleIDs, articleID)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orm.Callback().Query().Remove(callbackName) })

	// The card query may hydrate X content only for its selected representative.
	got, err := service.SiteTimeline(context.Background(), query, now, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hydratedArticleIDs, []string{articleIDs[0]}) {
		t.Fatalf("hydrated article payloads = %v, want representative %s only", hydratedArticleIDs, articleIDs[0])
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

/** TestSiteTimelinePreservesEvidenceScopeAndFilterLocalAnchors protects grouping, day counts, and pagination. */
func TestSiteTimelinePreservesEvidenceScopeAndFilterLocalAnchors(t *testing.T) {
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

	prefix := fmt.Sprintf("timeline_scope_%d", time.Now().UnixNano())
	sourceID := prefix + "_source"
	if err := orm.Exec("INSERT INTO sources (id,name,kind,tier) VALUES (?, ?, 'rss', 'T1')", sourceID, prefix).Error; err != nil {
		t.Fatal(err)
	}
	var factID int64
	if err := orm.Raw("INSERT INTO facts (public_id,title) VALUES (?, ?) RETURNING id", prefix+"_fact", "Timeline scope fixture").Row().Scan(&factID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM facts WHERE id = ?", factID).Error
		_ = orm.Exec("DELETE FROM articles WHERE source_id = ?", sourceID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})

	at := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	now := at.Add(100 * time.Second)
	type options struct {
		role       string
		scope      string
		channel    string
		visibility string
		selected   *bool
		future     bool
		missing    bool
		standalone bool
	}
	add := func(suffix string, seconds int, option options) string {
		t.Helper()
		id := prefix + "-" + suffix
		published := at.Add(time.Duration(seconds) * time.Second)
		if option.role == "" {
			option.role = "report"
		}
		if option.channel == "" {
			option.channel = "news"
		}
		if option.visibility == "" {
			option.visibility = "public"
		}
		selected := true
		if option.selected != nil {
			selected = *option.selected
		}
		visibleAfter := published
		if option.future {
			visibleAfter = now.Add(time.Second)
		}
		if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, sourceID, id, "https://fixture.example/"+id, id, published, published).Error; err != nil {
			t.Fatal(err)
		}
		analysisOutput, err := json.Marshal(map[string]string{"scope": option.scope})
		if option.scope == "" {
			analysisOutput = []byte(`{}`)
		}
		if err != nil {
			t.Fatal(err)
		}
		var analysisID int64
		if err := orm.Raw(`INSERT INTO analyses (article_id,input_revision,origin,output)
			VALUES (?, 1, 'rule', ?::jsonb) RETURNING id`, id, string(analysisOutput)).Row().Scan(&analysisID); err != nil {
			t.Fatal(err)
		}
		var publicationFact any = factID
		if option.standalone {
			publicationFact = nil
		}
		if err := orm.Exec(`INSERT INTO publications (article_id,source_id,analysis_id,title,url,discovered_at,timeline_at,sort_at,visible_after,
			selected,eligible,visibility,channel,tags,fact_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, true, ?, ?, ARRAY[?]::text[], ?)`,
			id, sourceID, analysisID, id, "https://fixture.example/"+id, published, published, published, visibleAfter,
			selected, option.visibility, option.channel, prefix, publicationFact).Error; err != nil {
			t.Fatal(err)
		}
		if !option.missing && !option.standalone {
			if err := orm.Exec("INSERT INTO fact_articles (fact_id,article_id,role) VALUES (?, ?, ?)", factID, id, option.role).Error; err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	add("old-report", 0, options{})
	add("x-report", 10, options{channel: "x"})
	standalone := []string{
		add("composite", 20, options{scope: "composite"}),
		add("mention", 30, options{role: "mention"}),
		add("removed", 40, options{missing: true}),
		add("standalone-a", 50, options{standalone: true}),
		add("standalone-z", 50, options{standalone: true}),
	}
	add("future", -50, options{future: true})
	withdrawnID := add("withdrawn", -80, options{visibility: "withdrawn"})
	noSelect := false
	add("unselected", -30, options{selected: &noSelect})

	service := NewService(NewRepository(orm), nil)
	filters := SiteTimelineQuery{Channel: "all", Tag: stringPointer(prefix), Limit: 20}
	full, err := service.SiteTimeline(context.Background(), filters, now, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for left, right := 0, len(standalone)-1; left < right; left, right = left+1, right-1 {
		standalone[left], standalone[right] = standalone[right], standalone[left]
	}
	wantKeys := make([]string, 0, len(standalone)+1)
	for _, id := range standalone {
		wantKeys = append(wantKeys, "a"+id)
	}
	wantKeys = append(wantKeys, "f"+fmt.Sprint(factID))
	gotKeys := make([]string, 0, len(full.Cards))
	for _, card := range full.Cards {
		gotKeys = append(gotKeys, card.Key)
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("timeline groups included excluded evidence or changed tie order: got=%v want=%v", gotKeys, wantKeys)
	}
	if full.Cards[len(full.Cards)-1].AnchorAt != formatV1Time(at) {
		t.Fatalf("all-channel fact anchor = %q, want earliest report %q", full.Cards[len(full.Cards)-1].AnchorAt, formatV1Time(at))
	}
	if full.Cards[len(full.Cards)-1].Item.ID == withdrawnID {
		t.Fatalf("withdrawn report became the fact representative: %#v", full.Cards[len(full.Cards)-1])
	}
	if full.DayCounts[beijingDate(at)] != len(full.Cards) {
		t.Fatalf("full timeline day counts = %#v, want %d cards for %s", full.DayCounts, len(full.Cards), beijingDate(at))
	}

	xFilter := SiteTimelineQuery{Channel: "x", Tag: stringPointer(prefix), Limit: 20}
	xResult, err := service.SiteTimeline(context.Background(), xFilter, now, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(xResult.Cards) != 1 || xResult.Cards[0].Key != "f"+fmt.Sprint(factID) || xResult.Cards[0].AnchorAt != formatV1Time(at.Add(10*time.Second)) || xResult.Cards[0].Item.ID != prefix+"-x-report" {
		t.Fatalf("channel-local fact anchor = %#v", xResult.Cards)
	}
	if xResult.DayCounts[beijingDate(at)] != 1 {
		t.Fatalf("channel-local day counts = %#v, want one grouped fact", xResult.DayCounts)
	}

	pagedQuery := filters
	pagedQuery.Limit = 2
	var pageKeys []string
	var cursor *string
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		pagedQuery.Cursor = cursor
		page, err := service.SiteTimeline(context.Background(), pagedQuery, now, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if page.DayCounts[beijingDate(at)] != len(full.Cards) {
			t.Fatalf("page %d day counts = %#v, want the full day count %d", pageNumber, page.DayCounts, len(full.Cards))
		}
		for _, card := range page.Cards {
			pageKeys = append(pageKeys, card.Key)
		}
		cursor = page.NextCursor
		if cursor == nil {
			break
		}
	}
	if !reflect.DeepEqual(pageKeys, wantKeys) {
		t.Fatalf("cursor pagination changed equal-time order: got=%v want=%v", pageKeys, wantKeys)
	}
}
