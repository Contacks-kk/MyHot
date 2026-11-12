package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func receiptTestDB(t *testing.T) *gorm.DB {
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

func TestPaidRequestPersistenceRetryAndBudget(t *testing.T) {
	db := receiptTestDB(t)
	ctx := context.Background()
	serviceName := fmt.Sprintf("receipt_test_%d", time.Now().UnixNano())
	if err := db.Exec(`INSERT INTO budgets(service,per_minute,per_hour,per_day,note) VALUES(?,10,20,30,'test')`, serviceName).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM receipts WHERE service IN (?,?)`, serviceName, serviceName+"_stopped").Error
		_ = db.Exec(`DELETE FROM budgets WHERE service IN (?,?)`, serviceName, serviceName+"_stopped").Error
	})
	request := Request{Service: serviceName, Model: "fixture", Purpose: "score_article", Subject: "article:test@1", Identity: map[string]any{"revision": 1}}
	repository := NewRepository(db)
	service := NewService(repository)
	firstCalls := 0
	first, err := service.Do(ctx, request, func(context.Context) (Outcome, error) {
		firstCalls++
		return Outcome{Response: json.RawMessage(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
	})
	if err != nil || firstCalls != 1 {
		t.Fatalf("first call = %#v, calls=%d, error=%v", first, firstCalls, err)
	}
	if err := service.Complete(ctx, first.ReceiptID); err != nil {
		t.Fatal(err)
	}
	second, err := service.Do(ctx, request, func(context.Context) (Outcome, error) {
		t.Fatal("completed receipt was sent to provider again")
		return Outcome{}, nil
	})
	if err != nil || !second.Reused || string(second.Response) != string(first.Response) {
		t.Fatalf("reused response = %#v: %v", second, err)
	}

	retryRequest := Request{Service: serviceName, Model: "fixture", Purpose: "score_article", Identity: map[string]any{"revision": 2}}
	_, err = service.Do(ctx, retryRequest, func(context.Context) (Outcome, error) {
		return Outcome{}, rejectedProviderError{message: "provider rejected test request"}
	})
	var rejected rejectedProviderError
	if !errors.As(err, &rejected) {
		t.Fatalf("first retry attempt error = %v", err)
	}
	retried, err := service.Do(ctx, retryRequest, func(context.Context) (Outcome, error) {
		return Outcome{Response: json.RawMessage(`{"ok":true}`)}, nil
	})
	if err != nil || retried.Reused {
		t.Fatalf("retried request = %#v: %v", retried, err)
	}
	var attempts int
	if err := db.Raw(`SELECT count(*) FROM receipt_attempts WHERE receipt_id=?`, retried.ReceiptID).Scan(&attempts).Error; err != nil || attempts != 2 {
		t.Fatalf("attempt count = %d: %v", attempts, err)
	}

	staleRequest := Request{Service: serviceName, Purpose: "score_article", Identity: "stale-placeholder"}
	staleKey, err := LogicalKeyFor(staleRequest)
	if err != nil {
		t.Fatal(err)
	}
	var staleReceiptID int64
	if err := db.Raw(`INSERT INTO receipts(logical_key,service,purpose,status,request,attempts)
		VALUES(?,?,?,'pending','{}'::jsonb,1) RETURNING id`, staleKey, serviceName, staleRequest.Purpose).Row().Scan(&staleReceiptID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO receipt_attempts(receipt_id,attempt,service,status) VALUES(?,1,?,'pending')`, staleReceiptID, serviceName).Error; err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	if _, err := service.Do(ctx, staleRequest, func(context.Context) (Outcome, error) {
		providerCalls++
		return Outcome{}, nil
	}); !errors.Is(err, ErrBusy) || providerCalls != 0 {
		t.Fatalf("fresh placeholder error=%v provider calls=%d", err, providerCalls)
	}
	if err := db.Exec(`UPDATE receipts SET updated_at=now()-interval '11 minutes' WHERE id=?`, staleReceiptID).Error; err != nil {
		t.Fatal(err)
	}
	_, err = service.Do(ctx, staleRequest, func(context.Context) (Outcome, error) {
		providerCalls++
		return Outcome{}, nil
	})
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.ReceiptID != staleReceiptID || providerCalls != 0 {
		t.Fatalf("stale placeholder error=%v provider calls=%d", err, providerCalls)
	}
	var staleStatus, attemptStatus string
	if err := db.Raw(`SELECT r.status,a.status FROM receipts r JOIN receipt_attempts a ON a.receipt_id=r.id WHERE r.id=?`, staleReceiptID).Row().Scan(&staleStatus, &attemptStatus); err != nil || staleStatus != "unknown" || attemptStatus != "unknown" {
		t.Fatalf("stale receipt statuses=%s/%s: %v", staleStatus, attemptStatus, err)
	}

	stoppedService := serviceName + "_stopped"
	if err := db.Exec(`INSERT INTO budgets(service,per_minute,per_hour,per_day,note) VALUES(?,0,0,0,'test')`, stoppedService).Error; err != nil {
		t.Fatal(err)
	}
	budgetCalls := 0
	_, err = service.Do(ctx, Request{Service: stoppedService, Purpose: "score", Identity: "budget"}, func(context.Context) (Outcome, error) {
		budgetCalls++
		return Outcome{}, nil
	})
	var budgetError *BudgetExceededError
	if !errors.As(err, &budgetError) || budgetCalls != 0 {
		t.Fatalf("stopped budget error = %v, provider calls=%d", err, budgetCalls)
	}
}

func TestPaidRequestTransactionRollbackRemovesReceiptAndAttempt(t *testing.T) {
	db := receiptTestDB(t)
	ctx := context.Background()
	request := Request{Service: fmt.Sprintf("receipt_rollback_%d", time.Now().UnixNano()), Purpose: "test", Identity: "rollback"}
	key, err := LogicalKeyFor(request)
	if err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	service := NewService(NewRepository(tx))
	_, err = service.Do(ctx, request, func(context.Context) (Outcome, error) {
		return Outcome{Response: json.RawMessage(`{"ok":true}`)}, nil
	})
	if err != nil {
		_ = tx.Rollback().Error
		t.Fatal(err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Raw(`SELECT count(*) FROM receipts WHERE logical_key=?`, key).Scan(&count).Error; err != nil || count != 0 {
		t.Fatalf("rolled back receipt count = %d: %v", count, err)
	}
}
