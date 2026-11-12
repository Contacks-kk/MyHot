package account

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerificationExpiryCooldownAndConsumption(t *testing.T) {
	c := newVerificationCache()
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	var code string
	timing, err := c.send("Reader@example.invalid", func(value string) error { code = value; return nil })
	if err != nil || len(code) != 6 || strings.Trim(code, "0123456789") != "" || timing.ExpiresAt.Sub(timing.ServerTime) != time.Minute {
		t.Fatalf("send: %+v %v", timing, err)
	}
	now = now.Add(59 * time.Second)
	if _, err = c.send("reader@example.invalid", func(string) error { t.Fatal("cooldown sent mail"); return nil }); err == nil {
		t.Fatal("missing cooldown")
	}
	now = now.Add(time.Second)
	if err = c.consume("reader@example.invalid", code, func() error { t.Fatal("expired code accepted"); return nil }); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	if _, err = c.send("reader@example.invalid", func(value string) error { code = value; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = c.consume("reader@example.invalid", code, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = c.consume("reader@example.invalid", code, func() error { t.Fatal("code reused"); return nil }); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
}

func TestVerificationLimitsAndCleanup(t *testing.T) {
	c := newVerificationCache()
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	start := now
	for i := 0; i < 10; i++ {
		// Groups of three fit in each rolling hour; the daily limit remains independent.
		now = start.Add(time.Duration(i/3)*time.Hour + time.Duration(i%3)*time.Minute)
		if _, err := c.send("limits@example.invalid", func(string) error { return nil }); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if i == 2 {
			now = now.Add(time.Minute)
			c.cleanup()
			_, err := c.send("limits@example.invalid", func(string) error { t.Fatal("hourly limit bypassed"); return nil })
			var limit *VerificationLimit
			if !errors.As(err, &limit) || !limit.RetryAt.Equal(start.Add(time.Hour)) {
				t.Fatalf("hourly limit: %v", err)
			}
		}
	}
	now = now.Add(time.Hour)
	c.cleanup()
	if _, err := c.send("limits@example.invalid", func(string) error { t.Fatal("daily limit bypassed"); return nil }); err == nil {
		t.Fatal("missing daily limit")
	}
	now = start.Add(28 * time.Hour)
	c.cleanup()
	if len(c.entries) != 0 {
		t.Fatal("expired limits leaked")
	}
}

func TestVerificationConcurrentSendAndConsume(t *testing.T) {
	c := newVerificationCache()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var code string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := c.send("parallel@example.invalid", func(value string) error { code = value; calls.Add(1); close(started); <-release; return nil })
		if err != nil {
			t.Error(err)
		}
	}()
	<-started
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.send("parallel@example.invalid", func(string) error { calls.Add(1); return nil }); err == nil {
				t.Error("concurrent send accepted")
			}
		}()
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate mail")
	}
	var accepted atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.consume("parallel@example.invalid", code, func() error { accepted.Add(1); return nil })
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("duplicate consumption")
	}
}

func TestVerificationSendFailureAndWrongAttempts(t *testing.T) {
	c := newVerificationCache()
	if _, err := c.send("failure@example.invalid", func(string) error { return errors.New("fake SMTP failure") }); err == nil {
		t.Fatal("failure lost")
	}
	var code string
	if _, err := c.send("failure@example.invalid", func(value string) error { code = value; return nil }); err != nil {
		t.Fatal("failed mail consumed cooldown")
	}
	for i := 0; i < 5; i++ {
		if err := c.consume("failure@example.invalid", "bad", func() error { t.Fatal("wrong code accepted"); return nil }); !errors.Is(err, ErrInvalidToken) {
			t.Fatal(err)
		}
	}
	if err := c.consume("failure@example.invalid", code, func() error { t.Fatal("exhausted code accepted"); return nil }); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	if len(c.entries["failure@example.invalid"].sent) != 1 {
		t.Fatal("failed sends counted")
	}
}

func TestVerificationDatabaseFailureKeepsCode(t *testing.T) {
	c := newVerificationCache()
	_, err := c.send("retry@example.invalid", func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	code := c.entries["retry@example.invalid"].code
	err = c.consume("retry@example.invalid", code, func() error { return errors.New("fake database failure") })
	if err == nil {
		t.Fatal("database error lost")
	}
	if err = c.consume("retry@example.invalid", code, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestVerificationLeadingZeroAndCleanupStops(t *testing.T) {
	c := newVerificationCache()
	c.entries["zero@example.invalid"] = &verificationEntry{code: "001234", expires: time.Now().Add(time.Minute)}
	if err := c.consume("zero@example.invalid", "001234", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	c.cleanup()
	if len(c.entries) != 0 {
		t.Fatal("lazy state was not reclaimed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { (&Service{verification: c}).RunVerificationCleanup(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not stop")
	}
}

func TestVerificationPrecheckDoesNotConsumeOrExtend(t *testing.T) {
	c := newVerificationCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	service := &Service{verification: c}
	c.entries["reader@example.invalid"] = &verificationEntry{code: "001234", expires: now.Add(time.Minute)}
	expires := c.entries["reader@example.invalid"].expires
	now = now.Add(30 * time.Second)
	for i := 0; i < 2; i++ {
		timing, err := service.CheckVerification("reader@example.invalid", "001234")
		if err != nil || !timing.ExpiresAt.Equal(expires) {
			t.Fatalf("precheck changed expiry: %+v %v", timing, err)
		}
	}
	if err := c.consume("reader@example.invalid", "001234", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CheckVerification("reader@example.invalid", "001234"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("consumed code accepted")
	}
	c.entries["reader@example.invalid"] = &verificationEntry{code: "001234", expires: now.Add(time.Minute)}
	for i := 0; i < 5; i++ {
		_, _ = service.CheckVerification("reader@example.invalid", "654321")
	}
	if err := c.consume("reader@example.invalid", "001234", func() error { t.Fatal("attempt budget bypassed"); return nil }); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
}
