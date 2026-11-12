package admindeliveries

import (
	"context"
	"encoding/json"
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

func openIntegrationRepository(t *testing.T) (*gorm.DB, *GormRepository) {
	t.Helper()
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
	return orm, NewRepository(orm)
}

func TestResolveUpdatesDeliveryAndAuditInOneTransaction(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	ctx := context.Background()
	key := fmt.Sprintf("delivery-resolution-%d", time.Now().UnixNano())
	actor := "admin:delivery-test"
	if err := orm.Exec("INSERT INTO notify_targets(key,purpose,kind) VALUES(?, 'alert', 'log')", key).Error; err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := orm.Raw(`INSERT INTO deliveries(target_key,subject_kind,subject_id,dedupe_key,status)
		VALUES(?, 'test', ?, ?, 'unknown') RETURNING id`, key, key, key).Row().Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM audit_log WHERE subject = ?", fmt.Sprintf("delivery:%d", id)).Error
		_ = orm.Exec("DELETE FROM deliveries WHERE id = ?", id).Error
		_ = orm.Exec("DELETE FROM notify_targets WHERE key = ?", key).Error
	})

	result, found, err := repository.Resolve(ctx, id, "sent", "confirmed", actor)
	if err != nil || !found || result == nil || result.ID != id || result.Status != "sent" {
		t.Fatalf("resolved delivery = %#v, found=%v, err=%v", result, found, err)
	}
	var status, response string
	var sentAt *time.Time
	if err := orm.Raw("SELECT status,response,sent_at FROM deliveries WHERE id = ?", id).Row().Scan(&status, &response, &sentAt); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || response != "人工确认已送达：confirmed" || sentAt == nil {
		t.Fatalf("delivery projection = status:%s response:%q sentAt:%v", status, response, sentAt)
	}
	var auditCount int
	if err := orm.Raw("SELECT count(*) FROM audit_log WHERE subject = ? AND actor = ? AND action = 'delivery.sent'", fmt.Sprintf("delivery:%d", id), actor).Row().Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("delivery audit count = %d, err=%v", auditCount, err)
	}
	if _, found, err := repository.Resolve(ctx, id, "drop", "late", actor); err != ErrNotResolvable || !found {
		t.Fatalf("already sent delivery = found:%v err:%v", found, err)
	}
}

func TestResendClaimAndFinalAuditUseOptimisticDeliveryVersion(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	ctx := context.Background()
	key := fmt.Sprintf("delivery-resend-%d", time.Now().UnixNano())
	actor := "admin:delivery-resend-test"
	if err := orm.Exec("INSERT INTO notify_targets(key,purpose,kind,config_ref,enabled) VALUES(?, 'content', 'feishu_webhook', 'test-webhook', true)", key).Error; err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := orm.Raw(`INSERT INTO deliveries(target_key,subject_kind,subject_id,dedupe_key,status,payload)
		VALUES(?, 'test', ?, ?, 'unknown', '{"version":1}'::jsonb) RETURNING id`, key, key, key).Row().Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM audit_log WHERE subject = ?", fmt.Sprintf("delivery:%d", id)).Error
		_ = orm.Exec("DELETE FROM deliveries WHERE id = ?", id).Error
		_ = orm.Exec("DELETE FROM notify_targets WHERE key = ?", key).Error
	})

	record, found, err := repository.LoadResend(ctx, id)
	if err != nil || !found || record == nil || record.Status != "unknown" || record.ConfigRef != "test-webhook" || string(record.Payload) != `{"version":1}` {
		t.Fatalf("resend record = %#v, found=%t, err=%v", record, found, err)
	}
	newPayload := json.RawMessage(`{"version":2}`)
	claimVersion, claimed, err := repository.ClaimResend(ctx, id, record.Version, newPayload)
	if err != nil || !claimed || claimVersion == "" {
		t.Fatalf("resend claim = version:%q claimed:%t err:%v", claimVersion, claimed, err)
	}
	if _, claimed, err = repository.ClaimResend(ctx, id, record.Version, newPayload); err != nil || claimed {
		t.Fatalf("stale resend claim = claimed:%t err:%v", claimed, err)
	}
	if err := repository.FinishResend(ctx, id, claimVersion, record.Status, "sent", `{"code":0}`, actor, "confirmed again"); err != nil {
		t.Fatal(err)
	}
	var status, response, payload string
	var attempts int
	var sentAt *time.Time
	if err := orm.Raw("SELECT status,response,payload::text,attempts,sent_at FROM deliveries WHERE id = ?", id).Row().Scan(&status, &response, &payload, &attempts, &sentAt); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || response != `{"code":0}` || payload != `{"version": 2}` || attempts != 1 || sentAt == nil {
		t.Fatalf("resend result = status:%s response:%q payload:%s attempts:%d sentAt:%v", status, response, payload, attempts, sentAt)
	}
	var auditCount int
	if err := orm.Raw("SELECT count(*) FROM audit_log WHERE subject = ? AND actor = ? AND action = 'delivery.resend' AND reason = ?", fmt.Sprintf("delivery:%d", id), actor, "confirmed again").Row().Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("resend audit count = %d, err=%v", auditCount, err)
	}
}

func TestContentDeliveryClaimsRespectSiblingsDedupeAndTargetActivation(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	ctx := context.Background()
	key := fmt.Sprintf("content-delivery-%d", time.Now().UnixNano())
	activated := time.Now().UTC().Add(-time.Hour)
	if err := orm.Exec(`INSERT INTO notify_targets(key,purpose,kind,enabled,enabled_at,config_ref)
		VALUES(?, 'content', 'feishu_webhook', true, ?, 'content-hook')`, key, activated).Error; err != nil {
		t.Fatal(err)
	}
	var siblingID int64
	if err := orm.Raw(`INSERT INTO deliveries(target_key,subject_kind,subject_id,dedupe_key,status)
		VALUES(?, 'selected', 'older-sibling', 'old-fact', 'sent') RETURNING id`, key).Row().Scan(&siblingID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM deliveries WHERE target_key = ?", key).Error
		_ = orm.Exec("DELETE FROM notify_targets WHERE key = ?", key).Error
	})

	claim := ContentDeliveryClaim{ArticleID: "new-article", DedupeKey: "selected:fact:99", ContentAt: activated.Add(time.Minute),
		SentSubjects: []string{"new-article", "older-sibling"}, Card: map[string]any{"title": "fixture"}}
	if id, _, claimed, err := repository.ClaimContentDelivery(ctx, key, claim); err != nil || claimed || id != 0 {
		t.Fatalf("sent sibling must suppress another delivery: id=%d claimed=%t err=%v", id, claimed, err)
	}
	claim.SentSubjects = []string{"new-article"}
	if id, _, claimed, err := repository.ClaimContentDelivery(ctx, key, claim); err != nil || !claimed || id <= 0 {
		t.Fatalf("eligible article should reserve the target: id=%d claimed=%t err=%v", id, claimed, err)
	}
	claim.ContentAt = activated.Add(-time.Minute)
	if id, _, claimed, err := repository.ClaimContentDelivery(ctx, key, claim); err != nil || claimed || id != 0 {
		t.Fatalf("pre-activation article must be skipped: id=%d claimed=%t err=%v", id, claimed, err)
	}
	claim.ContentAt = activated.Add(time.Minute)
	if id, _, claimed, err := repository.ClaimContentDelivery(ctx, key, claim); err != nil || claimed || id != 0 {
		t.Fatalf("dedupe key must reserve once: id=%d claimed=%t err=%v", id, claimed, err)
	}
	var pendingID int64
	if err := orm.Raw(`SELECT id FROM deliveries WHERE target_key = ? AND dedupe_key = ?`, key, claim.DedupeKey).Scan(&pendingID).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.StartContentDelivery(ctx, pendingID); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishContentDelivery(ctx, pendingID, "unknown", "acknowledgement lost"); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	if err := orm.Raw("SELECT status, attempts FROM deliveries WHERE id = ?", pendingID).Row().Scan(&status, &attempts); err != nil || status != "unknown" || attempts != 1 {
		t.Fatalf("initial send outcome = status:%s attempts:%d err=%v", status, attempts, err)
	}
}

func TestMarkStaleSeparatesInterruptedSendsFromReservationsAndIsIdempotent(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	ctx := context.Background()
	key := fmt.Sprintf("delivery-recovery-%d", time.Now().UnixNano())
	if err := orm.Exec("INSERT INTO notify_targets(key,purpose,kind) VALUES(?, 'alert', 'log')", key).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-20 * time.Minute)
	recent := now.Add(-2 * time.Minute)
	ids := make(map[string]int64)
	for _, fixture := range []struct {
		name, status string
		response     *string
		updatedAt    time.Time
	}{
		{name: "sending", status: "sending", response: stringPointer("kept response"), updatedAt: old},
		{name: "pending", status: "pending", updatedAt: old},
		{name: "recent", status: "sending", updatedAt: recent},
		{name: "sent", status: "sent", updatedAt: old},
	} {
		var response any
		if fixture.response != nil {
			response = *fixture.response
		}
		var id int64
		if err := orm.Raw(`INSERT INTO deliveries(target_key,subject_kind,subject_id,dedupe_key,status,response,updated_at)
			VALUES(?, 'test', ?, ?, ?, ?, ?) RETURNING id`, key, key+"-"+fixture.name, key+"-"+fixture.name, fixture.status, response, fixture.updatedAt).Row().Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[fixture.name] = id
	}
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM deliveries WHERE target_key = ?", key).Error
		_ = orm.Exec("DELETE FROM notify_targets WHERE key = ?", key).Error
	})

	counts, err := repository.MarkStale(ctx, now.Add(-15*time.Minute))
	if err != nil || counts != (StaleCounts{Unknown: 1, Failed: 1}) {
		t.Fatalf("stale delivery recovery = %+v, err=%v", counts, err)
	}
	for _, want := range []struct {
		name, status string
		response     *string
	}{
		{name: "sending", status: "unknown", response: stringPointer("kept response")},
		{name: "pending", status: "failed", response: stringPointer("\u53d1\u9001\u524d\u8fdb\u7a0b\u4e2d\u65ad\uff0c\u6ca1\u6709\u53d1\u51fa")},
		{name: "recent", status: "sending"},
		{name: "sent", status: "sent"},
	} {
		var got struct {
			Status   string  `gorm:"column:status"`
			Response *string `gorm:"column:response"`
		}
		if err := orm.Raw("SELECT status,response FROM deliveries WHERE id = ?", ids[want.name]).Scan(&got).Error; err != nil {
			t.Fatal(err)
		}
		if got.Status != want.status || !equalStringPointer(got.Response, want.response) {
			t.Errorf("%s recovery = status:%q response:%v, want status:%q response:%v", want.name, got.Status, got.Response, want.status, want.response)
		}
	}
	if again, err := repository.MarkStale(ctx, now.Add(-15*time.Minute)); err != nil || again != (StaleCounts{}) {
		t.Fatalf("repeated recovery = %+v, err=%v", again, err)
	}
}

func stringPointer(value string) *string { return &value }

func equalStringPointer(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
