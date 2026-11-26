package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aihot/backend-go/internal/market"
	"github.com/coder/websocket"
)

/** marketsCatalog returns supported assets, timeframes, runtime mode, and latest quotes. */
func (s *Server) marketsCatalog(w http.ResponseWriter, r *http.Request, _ userContext) {
	if s.Market == nil {
		writeError(w, http.StatusServiceUnavailable, "行情服务暂不可用")
		return
	}

	resp, err := s.Market.Catalog(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取行情目录失败")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

/** marketCandles returns merged historical + active candles and aligned news markers for one asset. */
func (s *Server) marketCandles(w http.ResponseWriter, r *http.Request, user userContext) {
	if s.Market == nil {
		writeError(w, http.StatusServiceUnavailable, "行情服务暂不可用")
		return
	}

	q := r.URL.Query()
	symbol := strings.TrimSpace(q.Get("symbol"))
	if symbol == "" {
		symbol = "XAU/USD"
	}

	if _, ok := market.LookupSymbol(symbol); !ok {
		writeError(w, http.StatusBadRequest, "不支持的市场标的")
		return
	}

	tf := strings.TrimSpace(q.Get("tf"))
	if tf == "" {
		tf = "1m"
	}

	if !market.IsValidTimeframe(tf) {
		writeError(w, http.StatusBadRequest, "不支持的 K 线周期")
		return
	}

	limit := market.DefaultCandleLimit
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > market.MaxCandleLimit {
			writeError(w, http.StatusBadRequest, "limit 参数无效")
			return
		}
		limit = parsed
	}

	resp, err := s.Market.Candles(r.Context(), user.ID, symbol, tf, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取 K 线数据失败")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

/** marketWS upgrades an authenticated same-origin request to a real-time candlestick WebSocket stream. */
func (s *Server) marketWS(w http.ResponseWriter, r *http.Request, user userContext) {
	if s.MarketHub == nil {
		writeError(w, http.StatusServiceUnavailable, "实时推送服务暂不可用")
		return
	}

	// Enforce explicit origin check honoring trusted proxy headers before bypassing websocket.Accept's host check.
	if !sameOrigin(r, s.Config.TrustProxy) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}

	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	if symbol == "" {
		symbol = "XAU/USD"
	}
	if _, ok := market.LookupSymbol(symbol); !ok {
		writeError(w, http.StatusBadRequest, "不支持的市场标的")
		return
	}

	tf := strings.TrimSpace(r.URL.Query().Get("tf"))
	if tf == "" {
		tf = "1m"
	}
	if !market.IsValidTimeframe(tf) {
		writeError(w, http.StatusBadRequest, "不支持的 K 线周期")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	conn.SetReadLimit(4096)

	client, ok := s.MarketHub.Register(user.ID, symbol, tf)
	if !ok {
		_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	defer s.MarketHub.Unregister(client)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Dedicated writer goroutine drains bounded client queue and sends keepalive pings.
	writeErr := make(chan error, 1)
	go func() {
		pingTicker := time.NewTicker(25 * time.Second)
		defer pingTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case payload, open := <-client.Send:
				if !open {
					_ = conn.Close(websocket.StatusGoingAway, "stream closed")
					return
				}

				wCtx, wCancel := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Write(wCtx, websocket.MessageText, payload)
				wCancel()
				if err != nil {
					select {
					case writeErr <- err:
					default:
					}
					return
				}
			case <-pingTicker.C:
				pCtx, pCancel := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Ping(pCtx)
				pCancel()
				if err != nil {
					select {
					case writeErr <- err:
					default:
					}
					return
				}
			}
		}
	}()

	// Reader loop listens for dynamic subscription switches and client heartbeats.
	for {
		select {
		case <-writeErr:
			return
		default:
		}

		msgType, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}

		if msgType != websocket.MessageText {
			continue
		}

		var msg market.WSClientMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Op {
		case "subscribe":
			nextSym := strings.TrimSpace(msg.Symbol)
			nextTF := strings.TrimSpace(msg.TF)
			_, validSym := market.LookupSymbol(nextSym)
			if validSym && market.IsValidTimeframe(nextTF) {
				s.MarketHub.Subscribe(client, nextSym, nextTF)
			}
		case "ping":
			pongPayload, _ := json.Marshal(map[string]string{
				"type": "pong",
			})
			select {
			case client.Send <- pongPayload:
			default:
			}
		}
	}
}
