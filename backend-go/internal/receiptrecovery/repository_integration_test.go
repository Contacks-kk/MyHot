package receiptrecovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func recoveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("database must be localhost and end with _test")
	}
	db, err := gorm.Open(postgres.Open(raw), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestReleaseRollsBackReceiptArticleAndAuditTogether(t *testing.T) {
	db := recoveryTestDB(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	actor := "test-receipt-release-" + suffix
	sourceID := "receipt-release-" + suffix
	articleID := "receipt-article-" + suffix
	logicalKey := "receipt-release:" + suffix
	receiptSubject := "article:" + articleID + "@1"
	var receiptID int64
	t.Cleanup(func() {
		_ = db.Exec("DROP TRIGGER IF EXISTS fail_receipt_release_audit ON audit_log").Error
		_ = db.Exec("DROP FUNCTION IF EXISTS fail_receipt_release_audit()").Error
		_ = db.Exec("DELETE FROM audit_log WHERE subject=?", "receipt:"+fmt.Sprint(receiptID)).Error
		_ = db.Exec("DELETE FROM receipt_attempts WHERE receipt_id=?", receiptID).Error
		_ = db.Exec("DELETE FROM receipts WHERE id=?", receiptID).Error
		_ = db.Exec("DELETE FROM articles WHERE id=?", articleID).Error
		_ = db.Exec("DELETE FROM sources WHERE id=?", sourceID).Error
	})
	if err := db.Exec(`INSERT INTO sources(id,name,kind,tier,participation_mode) VALUES(?, 'receipt release test', 'rss', 'T1', 'editorial')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,processing_state)
		VALUES(?,?,?,?,?,now(),now(),'failed')`, articleID, sourceID, "receipt-test:"+suffix, "https://example.invalid/receipt-test", "receipt test article").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO receipts(logical_key,service,purpose,subject,status,request,attempts,origin)
		VALUES(?, 'fixture', 'score_article', ?, 'unknown', '{}'::jsonb, 1, 'live') RETURNING id`, logicalKey, receiptSubject).Row().Scan(&receiptID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO receipt_attempts(receipt_id,attempt,service,origin,status,error)
		VALUES(?,1,'fixture','live','unknown','original outcome unknown')`, receiptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE FUNCTION fail_receipt_release_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.actor = '` + actor + `' THEN RAISE EXCEPTION 'injected receipt release audit failure'; END IF; RETURN NEW; END $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER fail_receipt_release_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_receipt_release_audit()").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewRepository(db).Release(ctx, receiptID, "manual", actor, "checked", boolPointer(false)); err == nil {
		t.Fatal("expected audit failure")
	}
	if err := db.Exec("DROP TRIGGER fail_receipt_release_audit ON audit_log").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP FUNCTION fail_receipt_release_audit()").Error; err != nil {
		t.Fatal(err)
	}
	var status, processingState string
	if err := db.Raw(`SELECT status FROM receipts WHERE id=?`, receiptID).Row().Scan(&status); err != nil || status != "unknown" {
		t.Fatalf("receipt release survived rollback: status=%q err=%v", status, err)
	}
	if err := db.Raw(`SELECT processing_state FROM articles WHERE id=?`, articleID).Row().Scan(&processingState); err != nil || processingState != "failed" {
		t.Fatalf("article reset survived rollback: state=%q err=%v", processingState, err)
	}
	var auditCount int
	if err := db.Raw(`SELECT count(*) FROM audit_log WHERE subject=? AND actor=?`, "receipt:"+fmt.Sprint(receiptID), actor).Scan(&auditCount).Error; err != nil || auditCount != 0 {
		t.Fatalf("audit unexpectedly persisted: count=%d err=%v", auditCount, err)
	}
	result, found, err := NewRepository(db).Release(ctx, receiptID, "manual", actor, "checked", boolPointer(false))
	if err != nil || !found || result == nil || !result.Requeued {
		t.Fatalf("manual release result=%+v found=%t err=%v", result, found, err)
	}
	var after json.RawMessage
	if err := db.Raw(`SELECT after FROM audit_log WHERE subject=? AND actor=?`, "receipt:"+fmt.Sprint(receiptID), actor).Row().Scan(&after); err != nil {
		t.Fatal(err)
	}
	var auditAfter map[string]any
	if err := json.Unmarshal(after, &auditAfter); err != nil || auditAfter["status"] != "failed" || auditAfter["requeued"] != true || auditAfter["billed"] != false {
		t.Fatalf("audit result=%s err=%v", after, err)
	}
}

/** TestReleaseResumesEveryAnalysisPurpose verifies manual receipt release repairs its owning article state. */
func TestReleaseResumesEveryAnalysisPurpose(t *testing.T) {
	db := recoveryTestDB(t).Begin()
	if db.Error != nil {
		t.Fatal(db.Error)
	}
	t.Cleanup(func() { _ = db.Rollback().Error })
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	sourceID := "receipt-purpose-" + suffix
	if err := db.Exec(`INSERT INTO sources(id,name,kind,tier,participation_mode) VALUES(?, 'receipt purpose test', 'rss', 'T1', 'editorial')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	purposes := []string{"prefilter_article", "score_article", "structure_article", "understand_article", "summarize_article", "embedding"}
	for _, purpose := range purposes {
		articleID := "receipt-purpose-" + purpose + "-" + suffix
		attemptTag := "admin:receipt-resume-" + purpose
		if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,processing_state,grouping_status,processing_attempt_tag)
			VALUES(?,?,?,?,?,now(),now(),'failed','failed',?)`, articleID, sourceID, "identity:"+articleID, "https://example.invalid/"+articleID, purpose, attemptTag).Error; err != nil {
			t.Fatal(err)
		}
		logicalKey := "receipt-purpose:" + articleID
		subject := "article:" + articleID + "@1"
		var receiptID int64
		if err := db.Raw(`INSERT INTO receipts(logical_key,service,purpose,subject,status,request,attempts,origin)
			VALUES(?, 'fixture', ?, ?, 'unknown', '{}'::jsonb, 1, 'live') RETURNING id`, logicalKey, purpose, subject).Row().Scan(&receiptID); err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO receipt_attempts(receipt_id,attempt,service,origin,status,error)
			VALUES(?,1,'fixture','live','unknown','original outcome unknown')`, receiptID).Error; err != nil {
			t.Fatal(err)
		}
		if purpose == "embedding" {
			if err := db.Exec(`UPDATE articles SET grouping_receipt_id=? WHERE id=?`, receiptID, articleID).Error; err != nil {
				t.Fatal(err)
			}
		}
		result, found, err := NewRepository(db).Release(context.Background(), receiptID, "manual release", "test", "confirmed", boolPointer(false))
		if err != nil || !found || result == nil || !result.Requeued {
			t.Fatalf("release %s = result=%+v found=%t err=%v", purpose, result, found, err)
		}
		var processingState, groupingStatus, recoveredAttemptTag string
		var groupingReceiptID sql.NullInt64
		if err := db.Raw(`SELECT processing_state,grouping_status,grouping_receipt_id,processing_attempt_tag FROM articles WHERE id=?`, articleID).
			Row().Scan(&processingState, &groupingStatus, &groupingReceiptID, &recoveredAttemptTag); err != nil {
			t.Fatal(err)
		}
		if recoveredAttemptTag != attemptTag {
			t.Fatalf("%s release changed its paid-call identity to %q", purpose, recoveredAttemptTag)
		}
		if purpose == "embedding" {
			if processingState != "failed" || groupingStatus != "pending" || groupingReceiptID.Valid {
				t.Fatalf("embedding release left article state=%s/%s receipt=%v", processingState, groupingStatus, groupingReceiptID)
			}
		} else if processingState != "new" {
			t.Fatalf("%s release left processing state %q", purpose, processingState)
		}
	}
}

func TestRecoveryMarksStaleAndAutoReleasesOnlyOnce(t *testing.T) {
	db := recoveryTestDB(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	pendingKey := "receipt-pending:" + suffix
	unknownKey := "receipt-unknown:" + suffix
	var pendingID, unknownID int64
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM audit_log WHERE subject IN (?,?)", "receipt:"+fmt.Sprint(pendingID), "receipt:"+fmt.Sprint(unknownID)).Error
		_ = db.Exec("DELETE FROM receipt_attempts WHERE receipt_id IN (?,?)", pendingID, unknownID).Error
		_ = db.Exec("DELETE FROM receipts WHERE id IN (?,?)", pendingID, unknownID).Error
	})
	if err := db.Raw(`INSERT INTO receipts(logical_key,service,purpose,status,request,attempts,origin,updated_at)
		VALUES(?, 'fixture', 'score_article', 'pending', '{}'::jsonb, 1, 'live', now()-interval '11 minutes') RETURNING id`, pendingKey).Row().Scan(&pendingID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO receipt_attempts(receipt_id,attempt,service,origin,status) VALUES(?,1,'fixture','live','pending')`, pendingID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO receipts(logical_key,service,purpose,status,request,attempts,origin,updated_at)
		VALUES(?, 'fixture', 'score_article', 'unknown', '{}'::jsonb, 1, 'live', now()-interval '31 minutes') RETURNING id`, unknownKey).Row().Scan(&unknownID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO receipt_attempts(receipt_id,attempt,service,origin,status) VALUES(?,1,'fixture','live','unknown')`, unknownID).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(db)
	service := NewService(repository)
	counts, err := service.Recover(context.Background(), time.Now())
	if err != nil || counts.Stale != 1 || counts.Released != 1 {
		t.Fatalf("recovery counts=%+v err=%v", counts, err)
	}
	var pendingStatus, pendingAttemptStatus, releasedStatus, releasedAttemptStatus string
	if err := db.Raw(`SELECT status FROM receipts WHERE id=?`, pendingID).Row().Scan(&pendingStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT status FROM receipt_attempts WHERE receipt_id=?`, pendingID).Row().Scan(&pendingAttemptStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT status FROM receipts WHERE id=?`, unknownID).Row().Scan(&releasedStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT status FROM receipt_attempts WHERE receipt_id=?`, unknownID).Row().Scan(&releasedAttemptStatus); err != nil {
		t.Fatal(err)
	}
	if pendingStatus != "unknown" || pendingAttemptStatus != "unknown" || releasedStatus != "failed" || releasedAttemptStatus != "failed" {
		t.Fatalf("recovered statuses pending=%s/%s released=%s/%s", pendingStatus, pendingAttemptStatus, releasedStatus, releasedAttemptStatus)
	}
	again, err := service.Recover(context.Background(), time.Now().Add(time.Minute))
	if err != nil || again.Released != 0 {
		t.Fatalf("second recovery=%+v err=%v", again, err)
	}
}

func boolPointer(value bool) *bool { return &value }
