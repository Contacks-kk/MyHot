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
	ids := []string{selectedID, eligibleID, yieldingID}
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
		       (?, ?, 'Yielding selected fixture', 'https://fixture.example/yielding', 'news', ?, ?, ?, 'public', true, true, false, ?, 'other yielding terms')`,
		selectedID, sourceID, now.Add(-time.Minute), now.Add(-time.Minute), now.Add(-time.Minute), now.Add(-time.Minute),
		eligibleID, sourceID, now, now, now,
		yieldingID, sourceID, now.Add(-2*time.Minute), now.Add(-2*time.Minute), now.Add(-2*time.Minute), now.Add(-2*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO pool_search(article_id,direct,body) VALUES (?, 'openai selected title', ''), (?, 'other direct terms', 'openai body needle'), (?, 'other yielding terms', '')`, selectedID, eligibleID, yieldingID).Error; err != nil {
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
	for _, row := range publishedRows {
		if row.ID == selectedID || row.ID == eligibleID {
			t.Fatalf("published cursor returned an item before its cursor tuple: %#v", publishedRows)
		}
	}
}
