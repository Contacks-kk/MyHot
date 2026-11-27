package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// DefaultSessionTTL matches the previous 30-day PostgreSQL session lifetime.
	DefaultSessionTTL = 30 * 24 * time.Hour

	// DefaultSessionRefreshThreshold renews Redis TTL and browser cookie only once half the TTL has elapsed.
	DefaultSessionRefreshThreshold = 15 * 24 * time.Hour

	sessionKeyPrefix          = "auth:session:"
	userSessionsKeyPrefix     = "auth:user_sessions:"
	userSessionPruneThreshold = 16
	userSessionIndexSlack     = time.Hour
)

// ErrSessionStoreUnavailable indicates Redis is unconfigured or unreachable during session operations.
var ErrSessionStoreUnavailable = errors.New("session store is unavailable")

// SessionRecord holds the authenticated user state persisted under a hashed session key.
type SessionRecord struct {
	UserID    int64  `json:"userId"`
	Email     string `json:"email"`
	CSRF      string `json:"csrf"`
	TokenHash string `json:"-"`
	CreatedAt int64  `json:"createdAt"`
}

// SessionStore abstracts persistent session storage for account authentication.
type SessionStore interface {
	Create(ctx context.Context, record SessionRecord, ttl time.Duration) error
	Resolve(ctx context.Context, tokenHash string, ttl, refreshThreshold time.Duration, touch bool) (Session, bool, error)
	Delete(ctx context.Context, userID int64, tokenHash string) error
	RevokeUser(ctx context.Context, userID int64) error
}

// RedisSessionStore stores user sessions in Redis with throttled Lua sliding expiration.
type RedisSessionStore struct {
	client *redis.Client
}

var resolveAndTouchSessionScript = redis.NewScript(`
local payload = redis.call("GET", KEYS[1])
if not payload then
	return nil
end
local touch = tonumber(ARGV[1])
local ttlSeconds = tonumber(ARGV[2])
local refreshThreshold = tonumber(ARGV[3])
local renewed = 0
if touch == 1 then
	local remaining = redis.call("TTL", KEYS[1])
	if remaining >= 0 and remaining < refreshThreshold then
		redis.call("EXPIRE", KEYS[1], ttlSeconds)
		renewed = 1
	end
end
return {payload, renewed}
`)

/** NewRedisSessionStore initializes a Redis-backed session store; a nil client treats operations as unavailable. */
func NewRedisSessionStore(client *redis.Client) *RedisSessionStore {
	return &RedisSessionStore{
		client: client,
	}
}

/** Create writes a hashed session payload into Redis and indexes it under the user's active session set. */
func (s *RedisSessionStore) Create(ctx context.Context, record SessionRecord, ttl time.Duration) error {
	if s == nil || s.client == nil {
		return ErrSessionStoreUnavailable
	}
	if record.TokenHash == "" || record.UserID <= 0 {
		return errors.New("invalid session record")
	}

	// Normalize timestamps and encode session state without leaking raw tokens
	if record.CreatedAt == 0 {
		record.CreatedAt = time.Now().UTC().Unix()
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if ttl < time.Second {
		ttl = time.Second
	}

	// Persist session value, update per-user revocation index, and maintain bounded set TTL
	sessionKey := sessionKey(record.TokenHash)
	userKey := userSessionsKey(record.UserID)
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, sessionKey, string(payload), ttl)
	pipe.SAdd(ctx, userKey, record.TokenHash)
	pipe.Expire(ctx, userKey, ttl+userSessionIndexSlack)
	cardCmd := pipe.SCard(ctx, userKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}

	// Opportunistically prune expired session handles if repeated logins bloat the per-user index
	if cardCmd.Val() > userSessionPruneThreshold {
		s.pruneStaleUserSessions(ctx, userKey, record.TokenHash)
	}
	return nil
}

/** Resolve fetches a hashed session and atomically extends Redis TTL when remaining lifetime falls below threshold. */
func (s *RedisSessionStore) Resolve(ctx context.Context, tokenHash string, ttl, refreshThreshold time.Duration, touch bool) (Session, bool, error) {
	if s == nil || s.client == nil {
		return Session{}, false, ErrSessionStoreUnavailable
	}
	if tokenHash == "" {
		return Session{}, false, ErrNoSession
	}

	touchFlag := 0
	if touch {
		touchFlag = 1
	}
	ttlSeconds := max(1, int64(ttl.Seconds()))
	thresholdSeconds := max(0, int64(refreshThreshold.Seconds()))

	// Execute GET + throttled EXPIRE atomically in a single Redis round trip
	rawResult, err := resolveAndTouchSessionScript.Run(
		ctx,
		s.client,
		[]string{sessionKey(tokenHash)},
		touchFlag,
		ttlSeconds,
		thresholdSeconds,
	).Result()
	if errors.Is(err, redis.Nil) {
		return Session{}, false, ErrNoSession
	}
	if err != nil {
		return Session{}, false, err
	}

	items, ok := rawResult.([]any)
	if !ok || len(items) != 2 {
		return Session{}, false, fmt.Errorf("unexpected redis session script response: %T", rawResult)
	}
	payload, ok := items[0].(string)
	if !ok {
		return Session{}, false, fmt.Errorf("unexpected redis session payload type: %T", items[0])
	}
	renewedInt, err := parseLuaInt(items[1])
	if err != nil {
		return Session{}, false, err
	}

	var record SessionRecord
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return Session{}, false, err
	}
	renewed := renewedInt == 1

	// Keep the user revocation index TTL ahead of any slid session lifetime
	if renewed && record.UserID > 0 {
		userKey := userSessionsKey(record.UserID)
		pipe := s.client.Pipeline()
		pipe.SAdd(ctx, userKey, tokenHash)
		pipe.Expire(ctx, userKey, ttl+userSessionIndexSlack)
		_, _ = pipe.Exec(ctx)
	}

	return Session{
		ID:    record.UserID,
		Email: record.Email,
		CSRF:  record.CSRF,
	}, renewed, nil
}

/** Delete removes a single session key and unregisters its handle from the user revocation index. */
func (s *RedisSessionStore) Delete(ctx context.Context, userID int64, tokenHash string) error {
	if s == nil || s.client == nil {
		return ErrSessionStoreUnavailable
	}
	if tokenHash == "" {
		return nil
	}

	pipe := s.client.Pipeline()
	pipe.Del(ctx, sessionKey(tokenHash))
	if userID > 0 {
		pipe.SRem(ctx, userSessionsKey(userID), tokenHash)
	}
	_, err := pipe.Exec(ctx)
	return err
}

/** RevokeUser invalidates every active session indexed for userID and deletes the index itself. */
func (s *RedisSessionStore) RevokeUser(ctx context.Context, userID int64) error {
	if s == nil || s.client == nil {
		return ErrSessionStoreUnavailable
	}
	if userID <= 0 {
		return nil
	}

	userKey := userSessionsKey(userID)
	handles, err := s.client.SMembers(ctx, userKey).Result()
	if err != nil {
		return err
	}

	keys := make([]string, 0, len(handles)+1)
	for _, handle := range handles {
		if handle != "" {
			keys = append(keys, sessionKey(handle))
		}
	}
	keys = append(keys, userKey)

	return s.client.Del(ctx, keys...).Err()
}

/** CountActiveUserSessions counts non-expired Redis sessions belonging to userID after pruning dead handles. */
func (s *RedisSessionStore) CountActiveUserSessions(ctx context.Context, userID int64) (int, error) {
	if s == nil || s.client == nil {
		return 0, ErrSessionStoreUnavailable
	}
	if userID <= 0 {
		return 0, nil
	}

	userKey := userSessionsKey(userID)
	handles, err := s.client.SMembers(ctx, userKey).Result()
	if err != nil {
		return 0, err
	}
	if len(handles) == 0 {
		return 0, nil
	}

	pipe := s.client.Pipeline()
	checks := make([]*redis.IntCmd, len(handles))
	for i, handle := range handles {
		checks[i] = pipe.Exists(ctx, sessionKey(handle))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}

	active := 0
	var expired []any
	for i, handle := range handles {
		if checks[i].Val() > 0 {
			active++
		} else {
			expired = append(expired, handle)
		}
	}
	if len(expired) > 0 {
		_ = s.client.SRem(ctx, userKey, expired...).Err()
	}
	return active, nil
}

func (s *RedisSessionStore) pruneStaleUserSessions(ctx context.Context, userKey, keepHash string) {
	handles, err := s.client.SMembers(ctx, userKey).Result()
	if err != nil || len(handles) == 0 {
		return
	}

	pipe := s.client.Pipeline()
	checks := make([]*redis.IntCmd, len(handles))
	for i, handle := range handles {
		checks[i] = pipe.Exists(ctx, sessionKey(handle))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return
	}

	var stale []any
	for i, handle := range handles {
		if handle != keepHash && checks[i].Val() == 0 {
			stale = append(stale, handle)
		}
	}
	if len(stale) > 0 {
		_ = s.client.SRem(ctx, userKey, stale...).Err()
	}
}

func sessionKey(tokenHash string) string {
	return sessionKeyPrefix + tokenHash
}

func userSessionsKey(userID int64) string {
	return userSessionsKeyPrefix + strconv.FormatInt(userID, 10)
}

func parseLuaInt(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("unexpected lua integer value: %T", value)
	}
}
