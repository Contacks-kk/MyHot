package market

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

const clientSendBuffer = 32

// Client represents a connected WebSocket session registered in the API broadcast hub.
type Client struct {
	UserID int64
	Send   chan []byte

	mu  sync.Mutex
	sub string
}

// Hub maintains a single shared Redis Pub/Sub consumer and fans updates out to subscribed WebSocket clients.
type Hub struct {
	redis  *RedisStore
	logger *slog.Logger

	mu      sync.RWMutex
	clients map[*Client]struct{}
	subs    map[string]map[*Client]struct{}
	closed  bool
}

/** NewHub constructs a WebSocket market broadcast hub. */
func NewHub(redis *RedisStore, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}

	return &Hub{
		redis:   redis,
		logger:  logger,
		clients: make(map[*Client]struct{}),
		subs:    make(map[string]map[*Client]struct{}),
	}
}

/** Run subscribes to Redis Pub/Sub market ticks and routes incoming messages until ctx cancels. */
func (h *Hub) Run(ctx context.Context) {
	if !h.redis.Available() {
		return
	}

	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		pubsub := h.redis.SubscribeTicks(ctx)
		if pubsub == nil {
			return
		}

		ch := pubsub.Channel()

	consumeLoop:
		for {
			select {
			case <-ctx.Done():
				_ = pubsub.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					break consumeLoop
				}

				if msg != nil && msg.Payload != "" {
					h.broadcastRaw([]byte(msg.Payload))
				}
			}
		}

		_ = pubsub.Close()
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}

		h.logger.Warn("redis market pubsub disconnected; reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, 15*time.Second)
	}
}

/** Register allocates a bounded client handle and binds its initial symbol/timeframe subscription. */
func (h *Hub) Register(userID int64, symbol, tf string) (*Client, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil, false
	}

	client := &Client{
		UserID: userID,
		Send:   make(chan []byte, clientSendBuffer),
		sub:    SubscriptionKey(symbol, tf),
	}

	h.clients[client] = struct{}{}
	h.addSubLocked(client, client.sub)

	return client, true
}

/** Subscribe moves an active client cleanly onto a new symbol/timeframe pair. */
func (h *Hub) Subscribe(client *Client, symbol, tf string) {
	if client == nil {
		return
	}

	next := SubscriptionKey(symbol, tf)

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	client.mu.Lock()
	prev := client.sub
	if prev == next {
		client.mu.Unlock()
		return
	}
	client.sub = next
	client.mu.Unlock()

	if prev != "" {
		h.removeSubLocked(client, prev)
	}
	h.addSubLocked(client, next)
}

/** Unregister removes a client and closes its outbound send channel once. */
func (h *Hub) Unregister(client *Client) {
	if client == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, exists := h.clients[client]; !exists {
		return
	}

	delete(h.clients, client)
	client.mu.Lock()
	subKey := client.sub
	client.sub = ""
	client.mu.Unlock()

	if subKey != "" {
		h.removeSubLocked(client, subKey)
	}

	close(client.Send)
}

/** Shutdown closes all connected client channels immediately on server termination. */
func (h *Hub) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	h.closed = true
	for client := range h.clients {
		close(client.Send)
	}

	h.clients = make(map[*Client]struct{})
	h.subs = make(map[string]map[*Client]struct{})
}

func (h *Hub) addSubLocked(client *Client, key string) {
	group, exists := h.subs[key]
	if !exists {
		group = make(map[*Client]struct{})
		h.subs[key] = group
	}
	group[client] = struct{}{}
}

func (h *Hub) removeSubLocked(client *Client, key string) {
	group, exists := h.subs[key]
	if !exists {
		return
	}

	delete(group, client)
	if len(group) == 0 {
		delete(h.subs, key)
	}
}

func (h *Hub) broadcastRaw(payload []byte) {
	var envelope struct {
		Symbol    string `json:"symbol"`
		Timeframe string `json:"tf"`
	}

	if err := json.Unmarshal(payload, &envelope); err != nil {
		return
	}

	key := SubscriptionKey(envelope.Symbol, envelope.Timeframe)

	h.mu.RLock()
	group := h.subs[key]
	targets := make([]*Client, 0, len(group))
	for client := range group {
		targets = append(targets, client)
	}
	h.mu.RUnlock()

	// Deliver non-blockingly so slow clients drop intermediate ticks instead of stalling Redis Pub/Sub.
	for _, client := range targets {
		select {
		case client.Send <- payload:
		default:
		}
	}
}
