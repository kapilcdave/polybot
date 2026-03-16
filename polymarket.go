package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	polyRESTBase = "https://clob.polymarket.com"
	polyGammaAPI = "https://gamma-api.polymarket.com"
	polyWSURL    = "wss://ws-subscriptions-clob.polymarket.com/ws/market"
)

type PolyClient struct {
	http   *http.Client
	dialer *websocket.Dialer

	mu           sync.RWMutex
	ctx          context.Context
	conn         *websocket.Conn
	markets      map[string]SportsMarket
	tokenMap     map[string]tokenMapping
	subscribed   []string
	marketTokens map[string][2]string

	writeMu sync.Mutex
}

func NewPolyClient() *PolyClient {
	return &PolyClient{
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
		dialer: &websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
		},
		ctx:          context.Background(),
		markets:      make(map[string]SportsMarket),
		tokenMap:     make(map[string]tokenMapping),
		marketTokens: make(map[string][2]string),
	}
}

func (p *PolyClient) WithContext(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ctx = ctx
}

func (p *PolyClient) FetchSportsMarkets() ([]SportsMarket, error) {
	tags := []string{"sports", "nba", "nhl", "mlb", "ncaa"}
	collected := make(map[string]SportsMarket)
	tokenMap := make(map[string]tokenMapping)
	marketTokens := make(map[string][2]string)

	for _, tag := range tags {
		url := fmt.Sprintf("%s/markets?tag=%s&active=true&limit=200", polyGammaAPI, tag)
		req, err := http.NewRequestWithContext(p.ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "gabagool-sports/1.0")
		req.Header.Set("Accept", "application/json")

		resp, err := p.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("[poly] fetch sports markets tag=%s: %w", tag, err)
		}
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, fmt.Errorf("[poly] fetch sports markets tag=%s status=%d body=%s", tag, resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var payload []struct {
			ID           string          `json:"id"`
			Question     string          `json:"question"`
			Active       bool            `json:"active"`
			Closed       bool            `json:"closed"`
			EndDate      string          `json:"endDate"`
			GameStart    string          `json:"gameStartTime"`
			Slug         string          `json:"slug"`
			Tags         []string        `json:"tags"`
			Outcomes     json.RawMessage `json:"outcomes"`
			CLOBTokenIDs json.RawMessage `json:"clobTokenIds"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("[poly] decode sports markets tag=%s: %w", tag, err)
		}
		resp.Body.Close()

		for _, raw := range payload {
			if !raw.Active || raw.Closed {
				continue
			}
			tokenIDs := parseTokenIDs(raw.CLOBTokenIDs)
			if len(tokenIDs) < 2 {
				continue
			}
			league := detectLeague(raw.Question + " " + raw.Slug + " " + strings.Join(raw.Tags, " "))
			if league == "" && tag == "ncaa" {
				league = "NCAAB"
			}
			if league == "" {
				continue
			}

			home, away := parseTeams(raw.Question, league)
			market := SportsMarket{
				Platform:  "POLY",
				MarketID:  raw.ID,
				HomeTeam:  home,
				AwayTeam:  away,
				League:    league,
				GameTime:  parseTimeAny(raw.GameStart, raw.EndDate),
				Question:  raw.Question,
				UpdatedAt: time.Now().UTC(),
				ClosesAt:  parseTimeAny(raw.EndDate),
			}
			collected[market.MarketID] = market
			tokenMap[tokenIDs[0]] = tokenMapping{MarketID: market.MarketID, Side: "YES"}
			tokenMap[tokenIDs[1]] = tokenMapping{MarketID: market.MarketID, Side: "NO"}
			marketTokens[market.MarketID] = [2]string{tokenIDs[0], tokenIDs[1]}
		}
	}

	out := make([]SportsMarket, 0, len(collected))
	for _, market := range collected {
		out = append(out, market)
	}

	p.mu.Lock()
	p.markets = collected
	p.tokenMap = tokenMap
	p.marketTokens = marketTokens
	p.mu.Unlock()
	return out, nil
}

func (p *PolyClient) Connect() error {
	conn, _, err := p.dialer.DialContext(p.ctx, polyWSURL, http.Header{
		"User-Agent": []string{"gabagool-sports/1.0"},
		"Accept":     []string{"application/json"},
	})
	if err != nil {
		return fmt.Errorf("[poly] dial websocket: %w", err)
	}

	p.mu.Lock()
	if p.conn != nil {
		_ = p.conn.Close()
	}
	p.conn = conn
	p.mu.Unlock()
	return nil
}

func (p *PolyClient) Subscribe(tokenIDs []string) error {
	p.mu.Lock()
	p.subscribed = append([]string(nil), tokenIDs...)
	conn := p.conn
	p.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("[poly] subscribe: websocket not connected")
	}

	for start := 0; start < len(tokenIDs); start += 100 {
		end := minInt(start+100, len(tokenIDs))
		payload := map[string]any{
			"type":      "market",
			"assets_ids": tokenIDs[start:end],
		}
		p.writeMu.Lock()
		err := conn.WriteJSON(payload)
		p.writeMu.Unlock()
		if err != nil {
			return fmt.Errorf("[poly] subscribe batch: %w", err)
		}
	}
	return nil
}

func (p *PolyClient) Listen(out chan<- MarketUpdate) {
	defer recoverGoroutine("poly-listen")

	backoff := time.Second
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		p.mu.RLock()
		conn := p.conn
		p.mu.RUnlock()
		if conn == nil {
			if err := p.reconnect(); err != nil {
				log.Printf("[poly] reconnect failed: %v", err)
				if !sleepContext(p.ctx, backoff) {
					return
				}
				backoff = minDuration(backoff*2, 30*time.Second)
				continue
			}
			backoff = time.Second
			continue
		}

		var raw map[string]any
		if err := conn.ReadJSON(&raw); err != nil {
			log.Printf("[poly] websocket read error: %v", err)
			p.closeConn()
			if !sleepContext(p.ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		p.handleMessage(raw, out)
	}
}

func (p *PolyClient) handleMessage(raw map[string]any, out chan<- MarketUpdate) {
	eventType := stringValue(raw["event_type"])
	switch eventType {
	case "price_change":
		tokenID := stringValue(raw["asset_id"])
		bestBid := floatValue(raw["best_bid"])
		p.applyTokenUpdate(tokenID, bestBid, out)
	case "book":
		tokenID := stringValue(raw["asset_id"])
		bids, _ := raw["bids"].([]any)
		if len(bids) == 0 {
			return
		}
		bid, _ := bids[0].(map[string]any)
		bestBid := floatValue(bid["price"])
		p.applyTokenUpdate(tokenID, bestBid, out)
	}
}

func (p *PolyClient) applyTokenUpdate(tokenID string, bestBid float64, out chan<- MarketUpdate) {
	p.mu.Lock()
	defer p.mu.Unlock()

	mapping, ok := p.tokenMap[tokenID]
	if !ok {
		return
	}
	market, ok := p.markets[mapping.MarketID]
	if !ok {
		return
	}

	if bestBid > 1 {
		bestBid = bestBid / 100
	}
	if mapping.Side == "YES" {
		market.YesBid = bestBid
	} else {
		market.NoBid = bestBid
	}
	market.UpdatedAt = time.Now().UTC()
	p.markets[mapping.MarketID] = market

	select {
	case out <- MarketUpdate{Platform: "POLY", Market: market}:
	case <-p.ctx.Done():
	}
}

func (p *PolyClient) reconnect() error {
	if err := p.Connect(); err != nil {
		return err
	}
	p.mu.RLock()
	subs := append([]string(nil), p.subscribed...)
	p.mu.RUnlock()
	if len(subs) > 0 {
		return p.Subscribe(subs)
	}
	return nil
}

func (p *PolyClient) closeConn() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
}

func (p *PolyClient) TokenIDs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.tokenMap))
	for tokenID := range p.tokenMap {
		out = append(out, tokenID)
	}
	return out
}

func parseTokenIDs(raw json.RawMessage) []string {
	var direct []string
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		encoded = strings.TrimSpace(encoded)
		if encoded == "" {
			return nil
		}
		if strings.HasPrefix(encoded, "[") {
			var parsed []string
			if err := json.Unmarshal([]byte(encoded), &parsed); err == nil {
				return parsed
			}
		}
		return []string{encoded}
	}
	return nil
}

func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return ""
	}
}

func floatValue(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	case json.Number:
		f, _ := x.Float64()
		return f
	default:
		return 0
	}
}
