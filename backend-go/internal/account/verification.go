package account

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

const verificationLifetime = time.Minute

type VerificationLimit struct {
	RetryAt time.Time
}

func (e *VerificationLimit) Error() string { return "请稍后重试" }

type VerificationTiming struct {
	ServerTime time.Time `json:"serverTime"`
	ExpiresAt  time.Time `json:"expiresAt"`
	RetryAt    time.Time `json:"retryAt"`
}

type verificationEntry struct {
	code     string
	expires  time.Time
	sent     []time.Time
	busy     bool
	failures int
}

// Codes and limits belong to this API process; restarting it clears both.
type verificationCache struct {
	mu      sync.Mutex
	entries map[string]*verificationEntry
	now     func() time.Time
}

func newVerificationCache() *verificationCache {
	return &verificationCache{entries: make(map[string]*verificationEntry), now: time.Now}
}

func cleanVerification(entry *verificationEntry, now time.Time) {
	if !now.Before(entry.expires) {
		entry.code = ""
	}
	kept := entry.sent[:0]
	for _, sent := range entry.sent {
		if now.Before(sent.Add(24 * time.Hour)) {
			kept = append(kept, sent)
		}
	}
	entry.sent = kept
}

func (c *verificationCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for email, entry := range c.entries {
		cleanVerification(entry, now)
		if !entry.busy && entry.code == "" && len(entry.sent) == 0 {
			delete(c.entries, email)
		}
	}
}

func (s *Service) RunVerificationCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.verification.cleanup()
		}
	}
}

func (c *verificationCache) send(email string, send func(string) error) (VerificationTiming, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	c.mu.Lock()
	now := c.now()
	entry := c.entries[email]
	if entry == nil {
		entry = &verificationEntry{}
		c.entries[email] = entry
	}
	cleanVerification(entry, now)
	var retry time.Time
	if entry.busy {
		retry = now.Add(time.Minute)
	}
	if len(entry.sent) > 0 {
		retry = later(retry, entry.sent[len(entry.sent)-1].Add(time.Minute))
	}
	var hourly []time.Time
	for _, sent := range entry.sent {
		if now.Before(sent.Add(time.Hour)) {
			hourly = append(hourly, sent)
		}
	}
	if len(hourly) >= 3 {
		retry = later(retry, hourly[len(hourly)-3].Add(time.Hour))
	}
	if len(entry.sent) >= 10 {
		retry = later(retry, entry.sent[len(entry.sent)-10].Add(24*time.Hour))
	}
	if now.Before(retry) {
		c.mu.Unlock()
		return VerificationTiming{}, &VerificationLimit{RetryAt: retry}
	}
	entry.busy = true
	c.mu.Unlock()
	value, err := rand.Int(rand.Reader, big.NewInt(1000000))
	code := ""
	if err == nil {
		code = fmt.Sprintf("%06d", value.Int64())
		err = send(code)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.busy = false
	if err != nil {
		return VerificationTiming{}, err
	}
	now = c.now()
	entry.code, entry.expires, entry.failures = code, now.Add(verificationLifetime), 0
	entry.sent = append(entry.sent, now)
	return VerificationTiming{ServerTime: now, ExpiresAt: entry.expires, RetryAt: now.Add(time.Minute)}, nil
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (c *verificationCache) consume(email, code string, complete func() error) error {
	email = strings.ToLower(strings.TrimSpace(email))
	c.mu.Lock()
	entry, err := c.validate(email, code)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	entry.busy = true
	c.mu.Unlock()
	err = complete()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.busy = false
	if err == nil || errors.Is(err, ErrDuplicateAccount) {
		entry.code = ""
	}
	return err
}

// validate is called under mu; prechecks and registration share the attempt budget.
func (c *verificationCache) validate(email, code string) (*verificationEntry, error) {
	entry := c.entries[email]
	if entry == nil {
		return nil, ErrInvalidToken
	}
	cleanVerification(entry, c.now())
	if entry.busy || entry.code == "" {
		return nil, ErrInvalidToken
	}
	if len(code) != 6 || subtle.ConstantTimeCompare([]byte(entry.code), []byte(code)) != 1 {
		entry.failures++
		if entry.failures >= 5 {
			entry.code = ""
		}
		return nil, ErrInvalidToken
	}
	return entry, nil
}

func (s *Service) CheckVerification(email, code string) (VerificationTiming, error) {
	c := s.verification
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, err := c.validate(strings.ToLower(strings.TrimSpace(email)), code)
	if err != nil {
		return VerificationTiming{}, err
	}
	return VerificationTiming{ServerTime: c.now(), ExpiresAt: entry.expires}, nil
}
