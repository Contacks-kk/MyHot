package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aihot/backend-go/internal/receiptrecovery"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

/** equalJSON compares decoded JSON values without depending on JSONB formatting. */
func equalJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

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
	shutdownGate := newCallGate()
	shutdownGate.beginShutdown()
	shutdownService := newServiceWithGate(repository, shutdownGate)
	shutdownReuse, err := shutdownService.Do(ctx, request, func(context.Context) (Outcome, error) {
		t.Fatal("shutdown reused a completed receipt through the provider")
		return Outcome{}, nil
	})
	if err != nil || !shutdownReuse.Reused || shutdownReuse.ReceiptID != first.ReceiptID {
		t.Fatalf("shutdown reuse = %#v: %v", shutdownReuse, err)
	}
	shutdownRequest := Request{
		Service:  serviceName,
		Purpose:  "score_article",
		Identity: "shutdown",
	}
	_, err = shutdownService.Do(ctx, shutdownRequest, func(context.Context) (Outcome, error) {
		t.Fatal("shutdown started a new provider request")
		return Outcome{}, nil
	})
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("new request during shutdown error = %v", err)
	}
	shutdownKey, err := LogicalKeyFor(shutdownRequest)
	if err != nil {
		t.Fatal(err)
	}
	var shutdownReceiptCount int
	if err := db.Raw(`SELECT count(*) FROM receipts WHERE logical_key = ?`, shutdownKey).Scan(&shutdownReceiptCount).Error; err != nil || shutdownReceiptCount != 0 {
		t.Fatalf("shutdown created %d receipts: %v", shutdownReceiptCount, err)
	}
	second, err := service.Do(ctx, request, func(context.Context) (Outcome, error) {
		t.Fatal("completed receipt was sent to provider again")
		return Outcome{}, nil
	})
	if err != nil || !second.Reused || !equalJSON(second.Response, first.Response) {
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
	shutdownRetryCalls := 0
	if _, err := shutdownService.Do(ctx, retryRequest, func(context.Context) (Outcome, error) {
		shutdownRetryCalls++
		return Outcome{Response: json.RawMessage(`{"unexpected":true}`)}, nil
	}); !errors.Is(err, ErrShuttingDown) || shutdownRetryCalls != 0 {
		t.Fatalf("failed receipt retry during shutdown = %v; provider calls=%d", err, shutdownRetryCalls)
	}
	restartedService := newServiceWithGate(repository, newCallGate())
	retried, err := restartedService.Do(ctx, retryRequest, func(context.Context) (Outcome, error) {
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

/** TestPaidRequestBudgetWindowsExpireAndSerializeLastSlot verifies rolling limits and concurrent claims. */
func TestPaidRequestBudgetWindowsExpireAndSerializeLastSlot(t *testing.T) {
	db := receiptTestDB(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("receipt_budget_window_%d", time.Now().UnixNano())
	serviceNames := []string{prefix + "_minute", prefix + "_hour", prefix + "_day", prefix + "_concurrent"}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM receipts WHERE service IN ?`, serviceNames).Error
		_ = db.Exec(`DELETE FROM budgets WHERE service IN ?`, serviceNames).Error
	})

	for _, window := range []struct {
		name      string
		inside    time.Duration
		expired   time.Duration
		perMinute int
		perHour   int
		perDay    int
	}{
		{name: "minute", inside: 2 * time.Second, expired: 61 * time.Second, perMinute: 1, perHour: 100, perDay: 100},
		{name: "hour", inside: 2 * time.Minute, expired: 61 * time.Minute, perMinute: 100, perHour: 1, perDay: 100},
		{name: "day", inside: 2 * time.Hour, expired: 25 * time.Hour, perMinute: 100, perHour: 100, perDay: 1},
	} {
		t.Run(window.name, func(t *testing.T) {
			serviceName := prefix + "_" + window.name
			if err := db.Exec(`INSERT INTO budgets(service,per_minute,per_hour,per_day,note) VALUES(?,?,?,?,'test')`, serviceName, window.perMinute, window.perHour, window.perDay).Error; err != nil {
				t.Fatal(err)
			}
			service := NewService(NewRepository(db))
			firstRequest := Request{Service: serviceName, Purpose: "test", Identity: "first"}
			first, err := service.Do(ctx, firstRequest, func(context.Context) (Outcome, error) {
				return Outcome{Response: json.RawMessage(`{"ok":true}`)}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(`UPDATE receipt_attempts SET started_at=? WHERE receipt_id=?`, time.Now().Add(-window.inside), first.ReceiptID).Error; err != nil {
				t.Fatal(err)
			}
			providerCalls := 0
			nextRequest := Request{Service: serviceName, Purpose: "test", Identity: "next"}
			_, err = service.Do(ctx, nextRequest, func(context.Context) (Outcome, error) {
				providerCalls++
				return Outcome{Response: json.RawMessage(`{"ok":true}`)}, nil
			})
			var budgetError *BudgetExceededError
			if !errors.As(err, &budgetError) || providerCalls != 0 {
				t.Fatalf("exhausted %s window error=%v provider calls=%d", window.name, err, providerCalls)
			}
			if err := db.Exec(`UPDATE receipt_attempts SET started_at=? WHERE receipt_id=?`, time.Now().Add(-window.expired), first.ReceiptID).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := service.Do(ctx, nextRequest, func(context.Context) (Outcome, error) {
				providerCalls++
				return Outcome{Response: json.RawMessage(`{"ok":true}`)}, nil
			}); err != nil || providerCalls != 1 {
				t.Fatalf("expired %s window did not restore allowance: calls=%d err=%v", window.name, providerCalls, err)
			}
		})
	}

	serviceName := prefix + "_concurrent"
	if err := db.Exec(`INSERT INTO budgets(service,per_minute,per_hour,per_day,note) VALUES(?,1,100,100,'test')`, serviceName).Error; err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int64
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, identity := range []string{"first", "second"} {
		identity := identity
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := NewService(NewRepository(db)).Do(ctx, Request{Service: serviceName, Purpose: "test", Identity: identity}, func(context.Context) (Outcome, error) {
				providerCalls.Add(1)
				return Outcome{Response: json.RawMessage(`{"ok":true}`)}, nil
			})
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	successes, rejected := 0, 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var budgetError *BudgetExceededError
		if errors.As(err, &budgetError) {
			rejected++
			continue
		}
		t.Errorf("concurrent paid request failed unexpectedly: %v", err)
	}
	if successes != 1 || rejected != 1 || providerCalls.Load() != 1 {
		t.Fatalf("last budget slot successes=%d rejected=%d provider calls=%d", successes, rejected, providerCalls.Load())
	}
}

/** TestReceivedAnswerWinsAConcurrentStaleSweep preserves a response already committed by the provider. */
func TestReceivedAnswerWinsAConcurrentStaleSweep(t *testing.T) {
	db := receiptTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	ctx := context.Background()
	request := Request{Service: fmt.Sprintf("receipt_race_%d", time.Now().UnixNano()), Purpose: "test", Identity: "answer"}
	key, err := LogicalKeyFor(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM receipts WHERE logical_key=?`, key).Error })
	started := make(chan struct{})
	releaseAnswer := make(chan struct{})
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(releaseAnswer) }) }
	type callResult struct {
		value Result
		err   error
	}
	type staleResult struct {
		count int64
		err   error
	}
	firstDone := make(chan callResult, 1)
	staleDone := make(chan staleResult, 1)
	var blocker *gorm.DB
	blockerReleased := false
	firstFinished := false
	staleStarted := false
	staleFinished := false
	defer func() {
		releaseProvider()
		if blocker != nil && !blockerReleased {
			_ = blocker.Rollback().Error
		}
		if !firstFinished {
			<-firstDone
		}
		if staleStarted && !staleFinished {
			<-staleDone
		}
	}()
	service := NewService(NewRepository(db))
	go func() {
		value, err := service.Do(ctx, request, func(context.Context) (Outcome, error) {
			close(started)
			<-releaseAnswer
			return Outcome{Response: json.RawMessage(`{"answer":"committed"}`)}, nil
		})
		firstDone <- callResult{value: value, err: err}
	}()
	<-started
	var receiptID, attemptID int64
	if err := db.Raw(`SELECT r.id,a.id FROM receipts r JOIN receipt_attempts a ON a.receipt_id=r.id WHERE r.logical_key=?`, key).Row().Scan(&receiptID, &attemptID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE receipts SET updated_at=now()-interval '11 minutes' WHERE id=?`, receiptID).Error; err != nil {
		t.Fatal(err)
	}
	blocker = db.Begin()
	if blocker.Error != nil {
		t.Fatal(blocker.Error)
	}
	var lockedAttempt int64
	if err := blocker.Raw(`SELECT id FROM receipt_attempts WHERE id=? FOR UPDATE`, attemptID).Row().Scan(&lockedAttempt); err != nil {
		_ = blocker.Rollback().Error
		blockerReleased = true
		t.Fatal(err)
	}

	releaseProvider()
	waitForBlockedQuery := func(pattern string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var waiting int
			if err := db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()
				AND wait_event_type='Lock' AND query LIKE ?`, pattern).Scan(&waiting).Error; err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("database did not wait on query matching %q", pattern)
	}
	waitForBlockedQuery("%UPDATE%receipt_attempts%")
	staleStarted = true
	go func() {
		count, err := receiptrecovery.NewRepository(db).MarkStalePending(ctx, time.Now().Add(-10*time.Minute))
		staleDone <- staleResult{count: count, err: err}
	}()
	waitForBlockedQuery("%UPDATE receipts SET status%")
	if err := blocker.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	blockerReleased = true
	first := <-firstDone
	firstFinished = true
	if first.err != nil || first.value.ReceiptID != receiptID || string(first.value.Response) != `{"answer":"committed"}` {
		t.Fatalf("first paid response = %#v, err=%v", first.value, first.err)
	}
	stale := <-staleDone
	staleFinished = true
	if stale.err != nil || stale.count != 0 {
		t.Fatalf("stale sweep overwrote a received response: count=%d err=%v", stale.count, stale.err)
	}
	reused, err := service.Do(ctx, request, func(context.Context) (Outcome, error) {
		t.Fatal("received response was purchased again")
		return Outcome{}, nil
	})
	if err != nil || !reused.Reused || !equalJSON(reused.Response, []byte(`{"answer":"committed"}`)) {
		t.Fatalf("later request did not reuse the committed response: %#v err=%v", reused, err)
	}
	var status, attemptStatus string
	if err := db.Raw(`SELECT r.status,a.status FROM receipts r JOIN receipt_attempts a ON a.receipt_id=r.id WHERE r.id=?`, receiptID).Row().Scan(&status, &attemptStatus); err != nil || status != "received" || attemptStatus != "received" {
		t.Fatalf("committed statuses = %s/%s err=%v", status, attemptStatus, err)
	}
}
