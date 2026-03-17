package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	kalshiPageSize = 200
	kalshiStaleTimeout = 90 * time.Second
)

// KalshiFeed seeds then streams Kalshi sports markets into a PriceMap.
type KalshiFeed struct {
	apiKey  string
	baseURL string
	wsURL   string
	pm      *PriceMap
	log     *slog.Logger
	Events  chan FeedEvent
}

type FeedEvent struct {
	Platform Platform
	Type     string // "seed_done", "ws_connected", "ws_disconnected", "update", "error"
	Msg      string
}

func NewKalshiFeed(apiKey, baseURL, wsURL string, pm *PriceMap, log *slog.Logger) *KalshiFeed {
	return &KalshiFeed{
		apiKey:  apiKey,
		baseURL: baseURL,
		wsURL:   wsURL,
		pm:      pm,
		log:     log,
		Events:  make(chan FeedEvent, 256),
	}
}

// Run starts the feed. It seeds first, then connects the WS.
// Reconnects indefinitely on WS failure. Blocks until ctx is cancelled.
func (f *KalshiFeed) Run(ctx context.Context) {
	f.emit("seed_start", "seeding Kalshi markets...")
	if err := f.seed(ctx); err != nil {
		f.emit("error", fmt.Sprintf("seed failed: %v", err))
		// Don't abort — WS might still work with partial data
	}
	f.emit("seed_done", "Kalshi seed complete")

	for {
		if ctx.Err() != nil {
			return
		}
		f.emit("ws_connecting", "connecting Kalshi WS...")
		if err := f.runWS(ctx); err != nil && ctx.Err() == nil {
			f.log.Warn("kalshi ws error, reconnecting", "err", err)
			f.emit("ws_disconnected", fmt.Sprintf("WS error: %v — reconnecting in 3s", err))
			f.pm.MarkStale(Kalshi)
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}
}

// ── Seed ─────────────────────────────────────────────────────────────────────

type kalshiMarketsResp struct {
	Markets []kalshiMarketRaw `json:"markets"`
	Cursor  string            `json:"cursor"`
}

type kalshiMarketRaw struct {
	Ticker      string  `json:"ticker"`
	Title       string  `json:"title"`
	YesBid      float64 `json:"yes_bid"`
	YesAsk      float64 `json:"yes_ask"`
	NoBid       float64 `json:"no_bid"`
	NoAsk       float64 `json:"no_ask"`
	LastPrice   float64 `json:"last_price"`
	Volume      float64 `json:"volume"`
	CloseTime   string  `json:"close_time"`
	Status      string  `json:"status"`
}

func (f *KalshiFeed) seed(ctx context.Context) error {
	cursor := ""
	total := 0
	page := 0

	for {
		page++
		url := fmt.Sprintf("%s/markets?limit=%d&status=open&category=sports", f.baseURL, kalshiPageSize)
		if cursor != "" {
			url += "&cursor=" + cursor
		}

		var resp kalshiMarketsResp
		if err := f.get(ctx, url, &resp); err != nil {
			return fmt.Errorf("page %d: %w", page, err)
		}

		for i := range resp.Markets {
			m := kalshiRawToMarket(&resp.Markets[i])
			EnrichMarket(m)
			f.pm.Upsert(m)
			total++
		}

		f.log.Debug("kalshi seed page", "page", page, "count", len(resp.Markets), "total", total)

		if resp.Cursor == "" || len(resp.Markets) == 0 {
			break
		}
		cursor = resp.Cursor
	}

	f.log.Info("kalshi seed complete", "markets", total, "pages", page)
	return nil
}

func kalshiRawToMarket(r *kalshiMarketRaw) *Market {
	// Best mid price: average of best bid/ask
	yesPrice := (r.YesBid + r.YesAsk) / 2.0
	if yesPrice == 0 {
		yesPrice = r.LastPrice
	}

	var closeTime time.Time
	if r.CloseTime != "" {
		closeTime, _ = time.Parse(time.RFC3339, r.CloseTime)
	}

	return &Market{
		Platform:  Kalshi,
		ID:        r.Ticker,
		Title:     r.Title,
		YesPrice:  yesPrice,
		NoPrice:   1.0 - yesPrice,
		Volume:    r.Volume,
		CloseTime: closeTime,
	}
}

// ── WebSocket ─────────────────────────────────────────────────────────────────

type kalshiWSMsg struct {
	ID   int             `json:"id"`
	Type string          `json:"type"`
	Msg  json.RawMessage `json:"msg"`
}

type kalshiSubscribeMsg struct {
	ID  int    `json:"id"`
	Cmd string `json:"cmd"`
	Params struct {
		Channels []string `json:"channels"`
	} `json:"params"`
}

type kalshiTickerMsg struct {
	MarketTicker string  `json:"market_ticker"`
	YesBid       float64 `json:"yes_bid"`
	YesAsk       float64 `json:"yes_ask"`
	NoBid        float64 `json:"no_bid"`
	NoAsk        float64 `json:"no_ask"`
	LastPrice    float64 `json:"last_price"`
}

func (f *KalshiFeed) runWS(ctx context.Context) error {
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + f.apiKey},
		},
	}
	conn, _, err := websocket.Dial(ctx, f.wsURL, opts)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()

	// Subscribe to ticker channel for all sports markets
	sub := kalshiSubscribeMsg{ID: 1, Cmd: "subscribe"}
	sub.Params.Channels = []string{"ticker"}
	if err := wsjson.Write(ctx, conn, sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	f.emit("ws_connected", "Kalshi WS connected")

	// Stale detection: if we don't hear anything for 90s, reconnect
	deadline := time.NewTimer(kalshiStaleTimeout)
	defer deadline.Stop()

	for {
		var raw kalshiWSMsg
		readCtx, cancel := context.WithTimeout(ctx, kalshiStaleTimeout)
		err := wsjson.Read(readCtx, conn, &raw)
		cancel()

		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		deadline.Reset(kalshiStaleTimeout)

		if raw.Type == "ticker" {
			var tick kalshiTickerMsg
			if err := json.Unmarshal(raw.Msg, &tick); err != nil {
				continue
			}
			f.applyTick(&tick)
		}
	}
}

func (f *KalshiFeed) applyTick(tick *kalshiTickerMsg) {
	yesPrice := (tick.YesBid + tick.YesAsk) / 2.0
	if yesPrice == 0 {
		yesPrice = tick.LastPrice
	}

	// Update in price map — market must already exist from seed
	f.pm.mu.Lock()
	nativeKey := "kalshi:" + tick.MarketTicker
	if m, ok := f.pm.byID[nativeKey]; ok {
		m.YesPrice = yesPrice
		m.NoPrice = 1.0 - yesPrice
		m.UpdatedAt = time.Now()
		m.Stale = false
	}
	f.pm.mu.Unlock()
}

// ── HTTP helper ───────────────────────────────────────────────────────────────

func (f *KalshiFeed) get(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		// Rate limited — wait and return retriable error
		retryAfter := resp.Header.Get("Retry-After")
		wait := 5 * time.Second
		if secs, err := strconv.Atoi(retryAfter); err == nil {
			wait = time.Duration(secs) * time.Second
		}
		f.log.Warn("kalshi rate limited", "wait", wait)
		time.Sleep(wait)
		return fmt.Errorf("rate limited (429)")
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func (f *KalshiFeed) emit(t, msg string) {
	select {
	case f.Events <- FeedEvent{Platform: Kalshi, Type: t, Msg: msg}:
	default:
	}
}
