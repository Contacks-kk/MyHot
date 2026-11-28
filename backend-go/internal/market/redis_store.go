package market

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	collectorLockKey = "market:collector:lock"
	maxWindowCandles = 500
)

var renewLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("EXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

var releaseLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

var reserveHistoryScript = redis.NewScript(`
local now = tonumber(ARGV[1])
if tonumber(redis.call("GET", KEYS[2]) or "0") >= 750 then return -1 end
redis.call("ZREMRANGEBYSCORE", KEYS[1], "-inf", now - 60000)
if redis.call("ZCARD", KEYS[1]) >= 8 then
	local earliest = redis.call("ZRANGE", KEYS[1], 0, 0, "WITHSCORES")
	return tonumber(earliest[2]) + 60000 - now
end
local used = redis.call("INCR", KEYS[2])
if used == 1 then redis.call("EXPIRE", KEYS[2], 172800) end
redis.call("ZADD", KEYS[1], now, KEYS[2] .. ":" .. used)
redis.call("EXPIRE", KEYS[1], 120)
return 0
`)

// RedisStore manages distributed collector coordination, hot quotes, candle windows, and tick Pub/Sub.
type RedisStore struct {
	client *redis.Client
	prefix string
}

/** NewRedisStore creates a RedisStore wrapper; client may be nil when Redis is unconfigured. */
func NewRedisStore(client *redis.Client, source string) *RedisStore {
	return &RedisStore{
		client: client,
		prefix: "market:" + source + ":",
	}
}

/** Available reports whether the underlying Redis client is initialized. */
func (s *RedisStore) Available() bool {
	return s != nil && s.client != nil
}

/** AcquireCollectorLock claims exclusive upstream market stream ownership across worker processes. */
func (s *RedisStore) AcquireCollectorLock(ctx context.Context, ownerID string, ttl time.Duration) (bool, error) {
	if !s.Available() {
		return true, nil
	}

	return s.client.SetNX(ctx, collectorLockKey, ownerID, ttl).Result()
}

/** RenewCollectorLock extends the collector lease TTL only if ownerID still holds the lock. */
func (s *RedisStore) RenewCollectorLock(ctx context.Context, ownerID string, ttl time.Duration) (bool, error) {
	if !s.Available() {
		return true, nil
	}

	seconds := max(1, int(ttl.Seconds()))
	res, err := renewLockScript.Run(ctx, s.client, []string{collectorLockKey}, ownerID, seconds).Int()
	if err != nil {
		return false, err
	}

	return res == 1, nil
}

/** ReleaseCollectorLock deletes the collector lock cleanly when shutting down. */
func (s *RedisStore) ReleaseCollectorLock(ctx context.Context, ownerID string) {
	if !s.Available() {
		return
	}

	releaseCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = releaseLockScript.Run(releaseCtx, s.client, []string{collectorLockKey}, ownerID).Err()
}

/** ReserveHistoryCredit shares Twelve Data request budgets across restarts. */
func (s *RedisStore) ReserveHistoryCredit(ctx context.Context) (bool, error) {
	if !s.Available() {
		return false, errors.New("redis is required for the market history budget")
	}

	for {
		now := time.Now().UTC()
		window := s.prefix + "credits:window"
		day := s.prefix + "credits:day:" + now.Format(time.DateOnly)
		waitMillis, err := reserveHistoryScript.Run(ctx, s.client, []string{window, day}, now.UnixMilli()).Int()
		if err != nil {
			return false, err
		}
		if waitMillis <= 0 {
			return waitMillis == 0, nil
		}

		timer := time.NewTimer(time.Duration(waitMillis) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

/** SaveQuote caches the latest quote snapshot for one symbol in Redis. */
func (s *RedisStore) SaveQuote(ctx context.Context, q QuoteSnapshot) error {
	if !s.Available() {
		return nil
	}

	raw, err := json.Marshal(q)
	if err != nil {
		return err
	}

	return s.client.HSet(ctx, s.prefix+"quotes", q.Symbol, raw).Err()
}

/** GetAllQuotes reads cached quote snapshots across all tracked assets. */
func (s *RedisStore) GetAllQuotes(ctx context.Context) (map[string]QuoteSnapshot, error) {
	out := make(map[string]QuoteSnapshot)
	if !s.Available() {
		return out, nil
	}

	// entries有3个 QQQ、XAU/USD、SPY
	entries, err := s.client.HGetAll(ctx, s.prefix+"quotes").Result()
	if err != nil {
		return nil, err
	}

	for symbol, payload := range entries {
		var q QuoteSnapshot
		//json.Unmarshal 将payload反序列化/解码
		if err := json.Unmarshal([]byte(payload), &q); err == nil {
			out[symbol] = q
		}
	}

	return out, nil
}

/** SetLiveCandle stores the active unclosed bar for a symbol and timeframe. */
func (s *RedisStore) SetLiveCandle(ctx context.Context, symbol, tf string, bar Candle) error {
	if !s.Available() {
		return nil
	}

	raw, err := json.Marshal(bar)
	if err != nil {
		return err
	}

	key := s.prefix + "candle:live:" + symbol + ":" + tf
	return s.client.Set(ctx, key, raw, 24*time.Hour).Err()
}

/** GetLiveCandle returns the currently active unclosed bar if present in Redis. */
func (s *RedisStore) GetLiveCandle(ctx context.Context, symbol, tf string) (*Candle, error) {
	if !s.Available() {
		return nil, nil
	}

	key := s.prefix + "candle:live:" + symbol + ":" + tf
	payload, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	var bar Candle
	if err := json.Unmarshal([]byte(payload), &bar); err != nil {
		return nil, err
	}

	return &bar, nil
}

/** SaveWindowCandles inserts closed candles into a bounded Redis ZSET indexed by UTC open time. */
func (s *RedisStore) SaveWindowCandles(ctx context.Context, symbol, tf string, candles []Candle) error {
	if !s.Available() || len(candles) == 0 {
		return nil
	}

	// key: twelvedata:candles:[QQQ || SPY || XAU/USD]:[1d || 1h || 1m || 1M || 5m]
	key := s.prefix + "candles:" + symbol + ":" + tf

	// pipeline 确保多指令按序原子性完成
	pipe := s.client.TxPipeline()

	// Remove existing entries sharing identical timestamps so bar rewrites do not create duplicate members.
	for _, bar := range candles {
		scoreStr := strconv.FormatInt(bar.Time, 10)
		pipe.ZRemRangeByScore(ctx, key, scoreStr, scoreStr)

		raw, err := json.Marshal(bar)
		if err != nil {
			return err
		}

		pipe.ZAdd(ctx, key, redis.Z{
			Score:  float64(bar.Time),
			Member: string(raw),
		})
	}

	// Keep the Redis hot cache bounded to maxWindowCandles entries.
	pipe.ZRemRangeByRank(ctx, key, 0, int64(-maxWindowCandles-1))
	pipe.Expire(ctx, key, 14*24*time.Hour)

	// 发送pipeline中所有指令给 Redis Server
	_, err := pipe.Exec(ctx)
	return err
}

/** GetWindowCandles loads up to limit cached candles ordered ascending from Redis. */
func (s *RedisStore) GetWindowCandles(ctx context.Context, symbol, tf string, limit int) ([]Candle, error) {
	if !s.Available() {
		return nil, nil
	}

	if limit <= 0 || limit > MaxCandleLimit {
		limit = DefaultCandleLimit
	}

	key := s.prefix + "candles:" + symbol + ":" + tf
	rawList, err := s.client.ZRange(ctx, key, int64(-limit), -1).Result()
	if err != nil {
		return nil, err
	}

	if len(rawList) == 0 {
		return nil, nil
	}

	out := make([]Candle, 0, len(rawList))
	for _, item := range rawList {
		var bar Candle
		if err := json.Unmarshal([]byte(item), &bar); err == nil {
			out = append(out, bar)
		}
	}

	return out, nil
}

/** PublishTick broadcasts a throttled candle and quote update to all API hubs over Redis Pub/Sub. */
func (s *RedisStore) PublishTick(ctx context.Context, update TickUpdate) error {
	if !s.Available() {
		return nil
	}

	raw, err := json.Marshal(update)
	if err != nil {
		return err
	}

	return s.client.Publish(ctx, s.prefix+"ticks", raw).Err()
}

/** SubscribeTicks returns a dedicated Redis Pub/Sub subscription listening to live market ticks. */
func (s *RedisStore) SubscribeTicks(ctx context.Context) *redis.PubSub {
	if !s.Available() {
		return nil
	}

	return s.client.Subscribe(ctx, s.prefix+"ticks")
}
