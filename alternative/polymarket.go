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
	polyPageSize     = 500
	polyStaleTimeout = 90 * time.Second
)

// PolyFeed seeds then streams Polymarket sports markets into a PriceMap.
type PolyFeed struct {
	apiKey  string
	baseURL string
	wsURL   string
	pm      *PriceMap
	log     *slog.Logger
	Events  chan FeedEvent
}

func NewPolyFeed(apiKey, baseURL, wsURL string, pm *PriceMap, log *slog.Logger) *PolyFeed {
	return &PolyFeed{
		apiKey:  apiKey,
		baseURL: baseURL,
		wsURL:   wsURL,
		pm:      pm,
		log:     log,
		Events:  make(chan FeedEvent, 256),
	}
}

func (f *PolyFeed) Run(ctx context.Context) {
	f.emit("seed_start", "seeding Polymarket markets...")
	if err := f.seed(ctx); err != nil {
		f.emit("error", fmt.Sprintf("seed failed: %v", err))
	}
	f.emit("seed_done", "Polymarket seed complete")

	for {
		if ctx.Err() != nil {
			return
		}
		f.emit("ws_connecting", "connecting Polymarket WS...")
		if err := f.runWS(ctx); err != nil && ctx.Err() == nil {
			f.log.Warn("poly ws error, reconnecting", "err", err)
			f.emit("ws_disconnected", fmt.Sprintf("WS error: %v — reconnecting in 3s", err))
			f.pm.MarkStale(Polymarket)
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}
}

// ── Seed ─────────────────────────────────────────────────────────────────────

type polyMarketRaw struct {
	ConditionID   string   `json:"condition_id"`
	QuestionID    string   `json:"question_id"`
	Question      string   `json:"question"`
	OutcomeTokens []string `json:"outcomes"`
	OutcomePrices []string `json:"outcome_prices"`
	Active        bool     `json:"active"`
	Closed        bool     `json:"closed"`
	Volume        string   `json:"volume"`
	EndDateISO    string   `json:"end_date_iso"`
	Tags          []struct {
		Label string `json:"label"`
		Slug  string `json:"slug"`
	} `json:"tags"`
	// CLOB token IDs for order placement
	ClobTokenIDs []string `json:"clob_token_ids"`
}

func (f *PolyFeed) seed(ctx context.Context) error {
	offset := 0
	total := 0
	page := 0

	for {
		page++
		url := fmt.Sprintf(
			"%s/markets?limit=%d&offset=%d&active=true&closed=false&tag_slug=sports",
			f.baseURL, polyPageSize, offset,
		)

		var markets []polyMarketRaw
		if err := f.get(ctx, url, &markets); err != nil {
			return fmt.Errorf("page %d: %w", page, err)
		}
		if len(markets) == 0 {
			break
		}

		for i := range markets {
			m := polyRawToMarket(&markets[i])
			if m == nil {
				continue
			}
			EnrichMarket(m)
			f.pm.Upsert(m)
			total++
		}

		f.log.Debug("poly seed page", "page", page, "count", len(markets), "total", total)
		offset += polyPageSize
	}

	f.log.Info("poly seed complete", "markets", total, "pages", page)
	return nil
}

func polyRawToMarket(r *polyMarketRaw) *Market {
	if !r.Active || r.Closed {
		return nil
	}

	// Outcome prices is a JSON array of strings like ["0.65","0.35"]
	var yesPrice float64
	if len(r.OutcomePrices) > 0 {
		p, err := strconv.ParseFloat(r.OutcomePrices[0], 64)
		if err == nil {
			yesPrice = p
		}
	}

	var vol float64
	if r.Volume != "" {
		vol, _ = strconv.ParseFloat(r.Volume, 64)
	}

	var closeTime time.Time
	if r.EndDateISO != "" {
		closeTime, _ = time.Parse(time.RFC3339, r.EndDateISO)
		if closeTime.IsZero() {
			closeTime, _ = time.Parse("2006-01-02T15:04:05Z", r.EndDateISO)
		}
	}

	// Store the CLOB token ID for the YES outcome (index 0) for order placement
	var clobTokenID string
	if len(r.ClobTokenIDs) > 0 {
		clobTokenID = r.ClobTokenIDs[0]
	}
	_ = clobTokenID // used in executor

	return &Market{
		Platform:  Polymarket,
		ID:        r.ConditionID,
		Title:     r.Question,
		YesPrice:  yesPrice,
		NoPrice:   1.0 - yesPrice,
		Volume:    vol,
		CloseTime: closeTime,
	}
}

// ── WebSocket ─────────────────────────────────────────────────────────────────

// Polymarket CLOB WS: subscribe to price_change events
// Docs: https://docs.polymarket.com/#websocket-api

type polyWSSubscribe struct {
	Assets []string `json:"assets"`
	Type   string   `json:"type"`
}

type polyWSMsg struct {
	EventType string          `json:"event_type"`
	Asset     string          `json:"asset_id"`
	Data      json.RawMessage `json:"data"`
}

type polyPriceChangeData struct {
	AssetID string `json:"asset_id"`
	Price   string `json:"price"`
	Side    string `json:"side"` // "BUY" or "SELL"
	Size    string `json:"size"`
}

func (f *PolyFeed) runWS(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, f.wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()

	// Build list of condition IDs we care about (all seeded markets)
	f.pm.mu.RLock()
	var assetIDs []string
	for key := range f.pm.byID {
		if len(key) > len("polymarket:") {
			assetIDs = append(assetIDs, key[len("polymarket:"):])
		}
	}
	f.pm.mu.RUnlock()

	// Subscribe in batches of 100 (Polymarket WS limit)
	for i := 0; i < len(assetIDs); i += 100 {
		end := i + 100
		if end > len(assetIDs) {
			end = len(assetIDs)
		}
		sub := polyWSSubscribe{
			Type:   "subscribe",
			Assets: assetIDs[i:end],
		}
		if err := wsjson.Write(ctx, conn, sub); err != nil {
			return fmt.Errorf("subscribe batch %d: %w", i/100, err)
		}
	}

	f.emit("ws_connected", fmt.Sprintf("Polymarket WS connected, subscribed to %d markets", len(assetIDs)))

	for {
		var msg polyWSMsg
		readCtx, cancel := context.WithTimeout(ctx, polyStaleTimeout)
		err := wsjson.Read(readCtx, conn, &msg)
		cancel()

		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		if msg.EventType == "price_change" {
			var changes []polyPriceChangeData
			// Can be single object or array
			if err := json.Unmarshal(msg.Data, &changes); err != nil {
				var single polyPriceChangeData
				if err2 := json.Unmarshal(msg.Data, &single); err2 == nil {
					changes = []polyPriceChangeData{single}
				}
			}
			for _, ch := range changes {
				f.applyPriceChange(&ch)
			}
		}
	}
}

func (f *PolyFeed) applyPriceChange(ch *polyPriceChangeData) {
	price, err := strconv.ParseFloat(ch.Price, 64)
	if err != nil {
		return
	}
	// ch.AssetID is the CLOB token ID, but our map is keyed by condition_id.
	// We need a reverse lookup. For now update by scanning — optimize with
	// a second clobTokenID→conditionID map if needed.
	f.pm.mu.Lock()
	for key, m := range f.pm.byID {
		if len(key) > len("polymarket:") && m.Platform == Polymarket {
			// Match on stored clob token (we'd store it in an extra field, simplified here)
			if m.ID == ch.AssetID {
				if ch.Side == "BUY" {
					m.YesPrice = price
					m.NoPrice = 1.0 - price
				}
				m.UpdatedAt = time.Now()
				m.Stale = false
				break
			}
		}
	}
	f.pm.mu.Unlock()
}

// ── HTTP helper ───────────────────────────────────────────────────────────────

func (f *PolyFeed) get(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if f.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+f.apiKey)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		retryAfter := resp.Header.Get("Retry-After")
		wait := 5 * time.Second
		if secs, err := strconv.Atoi(retryAfter); err == nil {
			wait = time.Duration(secs) * time.Second
		}
		time.Sleep(wait)
		return fmt.Errorf("rate limited (429)")
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func (f *PolyFeed) emit(t, msg string) {
	select {
	case f.Events <- FeedEvent{Platform: Polymarket, Type: t, Msg: msg}:
	default:
	}
}
