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

func TestSelectedSyncRepositoryReadsSnapshotAndRedactsCurrentlyUnlistedChanges(t *testing.T) {
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

	prefix := fmt.Sprintf("zzsync_%d", time.Now().UnixNano())
	sourceID, visibleID, withdrawnID := prefix+"_source", prefix+"_visible", prefix+"_withdrawn"
	var firstSeq int64
	var epochWasPresent bool
	if err := orm.Raw("SELECT EXISTS (SELECT 1 FROM settings WHERE key = ?)", "selected_ledger_epoch").Scan(&epochWasPresent).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM selected_state WHERE article_id IN ?", []string{visibleID, withdrawnID}).Error
		_ = orm.Exec("DELETE FROM selected_ledger WHERE article_id IN ?", []string{visibleID, withdrawnID}).Error
		_ = orm.Exec("DELETE FROM articles WHERE id IN ?", []string{visibleID, withdrawnID}).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
		if !epochWasPresent {
			_ = orm.Exec("DELETE FROM settings WHERE key = ?", "selected_ledger_epoch").Error
		}
	})

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := orm.Exec("INSERT INTO sources(id,name,kind) VALUES (?, 'Selected sync fixture', 'external')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	for _, articleID := range []string{visibleID, withdrawnID} {
		if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, articleID, sourceID, articleID, "https://fixture.example/"+articleID, "Fixture "+articleID, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,url,channel,discovered_at,timeline_at,sort_at,visibility,eligible,selected)
		VALUES (?, ?, 'Visible fixture', 'https://fixture.example/visible', 'news', ?, ?, ?, 'public', true, false),
		       (?, ?, 'Withdrawn fixture', 'https://fixture.example/withdrawn', 'news', ?, ?, ?, 'withdrawn', true, false)`,
		visibleID, sourceID, now, now, now, withdrawnID, sourceID, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	visiblePayload := fmt.Sprintf(`{"id":%q,"title":"Visible fixture","links":{"aihot":"/items/%s","original":"https://fixture.example/visible"}}`, visibleID, visibleID)
	withdrawnPayload := fmt.Sprintf(`{"id":%q,"title":"Withdrawn fixture","links":{"aihot":"/items/%s","original":"https://fixture.example/withdrawn"}}`, withdrawnID, withdrawnID)
	tx := orm.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext('selected_ledger'))").Error; err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Raw("SELECT coalesce(max(seq), 0) + 1 FROM selected_ledger").Scan(&firstSeq).Error; err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Exec(`INSERT INTO selected_ledger(seq,article_id,op,changed_at,visible_at,payload)
		VALUES (?, ?, 'upsert', ?, ?, ?::jsonb), (?, ?, 'upsert', ?, ?, ?::jsonb)`,
		firstSeq, visibleID, now, now, visiblePayload, firstSeq+1, withdrawnID, now, now, withdrawnPayload).Error; err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Exec("INSERT INTO selected_state(article_id,in_set,last_seq) VALUES (?,true,?), (?,false,?)", visibleID, firstSeq, withdrawnID, firstSeq+1).Error; err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}

	repository := NewRepository(orm)
	epoch, err := repository.LedgerEpoch(context.Background())
	if err != nil || epoch == "" {
		t.Fatalf("ledger epoch = %q, err=%v", epoch, err)
	}
	watermark, err := repository.LedgerWatermark(context.Background())
	if err != nil || watermark < firstSeq+1 {
		t.Fatalf("watermark = %d, err=%v", watermark, err)
	}
	snapshot, err := repository.SelectedSnapshot(context.Background(), 10, firstSeq+1, prefix, false)
	if err != nil {
		t.Fatal(err)
	}
	foundVisible, foundWithdrawn := false, false
	for _, row := range snapshot {
		if row.ArticleID == visibleID {
			foundVisible = true
		}
		if row.ArticleID == withdrawnID {
			foundWithdrawn = true
		}
	}
	if !foundVisible || foundWithdrawn {
		t.Fatalf("snapshot did not filter the current selected set: %#v", snapshot)
	}

	changes, err := repository.SelectedChanges(context.Background(), 10, firstSeq-1, firstSeq+1, now, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range changes {
		got[row.ArticleID] = row.Operation
		if row.ArticleID == withdrawnID && len(row.Payload) != 0 {
			t.Fatalf("currently unlisted article payload was returned: %s", row.Payload)
		}
	}
	if got[visibleID] != "upsert" || got[withdrawnID] != "remove" {
		t.Fatalf("changes = %#v", got)
	}
}
