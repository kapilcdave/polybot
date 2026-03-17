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
	byClobToken  map[string]string
	marketTokens map[string][2]string
	subscribed   []string

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
		byClobToken:  make(map[string]string),
		marketTokens: make(map[string][2]string),
	}
}

func (p *PolyClient) WithContext(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ctx = ctx
}

func (p *PolyClient) FetchSportsMarkets() ([]SportsMarket, error) {
	collected := make(map[string]SportsMarket)
	tokenMap := make(map[string]tokenMapping)
	byClobToken := make(map[string]string)
	marketTokens := make(map[string][2]string)

	req, err := http.NewRequestWithContext(p.ctx, http.MethodGet, polyGammaAPI+"/markets?active=true&closed=false&limit=1000", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gabagool-sports/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("[poly] fetch sports markets: %w", err)
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("[poly] fetch sports markets status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload []struct {
		ID            string          `json:"id"`
		Question      string          `json:"question"`
		Slug          string          `json:"slug"`
		Active        bool            `json:"active"`
		Closed        bool            `json:"closed"`
		EndDate       string          `json:"endDate"`
		GameStart     string          `json:"gameStartTime"`
		BestBid       interface{}     `json:"bestBid"`
		BestAsk       interface{}     `json:"bestAsk"`
		OutcomePrices json.RawMessage `json:"outcomePrices"`
		ClobTokenIDs  json.RawMessage `json:"clobTokenIds"`
		Events        []struct {
			Title string `json:"title"`
			Slug  string `json:"slug"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("[poly] decode sports markets: %w", err)
	}
	resp.Body.Close()

	for _, raw := range payload {
		if !raw.Active || raw.Closed {
			continue
		}
		tokenIDs := parseTokenIDs(raw.ClobTokenIDs)
		if len(tokenIDs) < 2 {
			continue
		}

		contextText := raw.Question + " " + raw.Slug
		for _, event := range raw.Events {
			contextText += " " + event.Title + " " + event.Slug
		}

		league := detectLeague(contextText)
		if league == "" && !looksLikeSportsQuestion(contextText, raw.GameStart) {
			continue
		}
		if league == "" {
			league = inferLeagueFromTeams(contextText)
		}
		if league == "" {
			continue
		}

		home, away := parseTeams(raw.Question, league)
		yesPrice, noPrice := parseOutcomePrices(raw.OutcomePrices)
		bestBid := normalizePolyPrice(floatValue(raw.BestBid))
		bestAsk := normalizePolyPrice(floatValue(raw.BestAsk))
		if bestBid > 0 {
			yesPrice = bestBid
		}
		if noPrice == 0 && bestAsk > 0 {
			noPrice = maxFloat(0, 1-bestAsk)
		}

		market := SportsMarket{
			Platform:     "POLY",
			MarketID:     raw.ID,
			ClobTokenIDs: [2]string{tokenIDs[0], tokenIDs[1]},
			HomeTeam:     home,
			AwayTeam:     away,
			League:       league,
			GameTime:     parseTimeAny(raw.GameStart, raw.EndDate),
			Question:     raw.Question,
			YesBid:       normalizePolyPrice(yesPrice),
			NoBid:        normalizePolyPrice(noPrice),
			YesAsk:       bestAsk,
			NoAsk:        maxFloat(0, 1-bestBid),
			UpdatedAt:    time.Now().UTC(),
			ClosesAt:     parseTimeAny(raw.EndDate, raw.GameStart),
		}
		collected[market.MarketID] = market
		tokenMap[tokenIDs[0]] = tokenMapping{MarketID: market.MarketID, Side: "YES"}
		tokenMap[tokenIDs[1]] = tokenMapping{MarketID: market.MarketID, Side: "NO"}
		byClobToken[tokenIDs[0]] = market.MarketID
		byClobToken[tokenIDs[1]] = market.MarketID
		marketTokens[market.MarketID] = [2]string{tokenIDs[0], tokenIDs[1]}
	}

	out := make([]SportsMarket, 0, len(collected))
	for _, market := range collected {
		out = append(out, market)
	}

	p.mu.Lock()
	p.markets = collected
	p.tokenMap = tokenMap
	p.byClobToken = byClobToken
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
	conn.SetReadLimit(2 << 20)

	p.mu.Lock()
	if p.conn != nil {
		_ = p.conn.Close()
	}
	p.conn = conn
	p.mu.Unlock()
	go p.pingLoop(conn)
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
	if len(tokenIDs) == 0 {
		return nil
	}

	for start := 0; start < len(tokenIDs); start += 100 {
		end := minInt(start+100, len(tokenIDs))
		payload := map[string]any{"assets_ids": tokenIDs[start:end]}
		if start == 0 {
			payload["type"] = "market"
			payload["custom_feature_enabled"] = true
		} else {
			payload["operation"] = "subscribe"
			payload["custom_feature_enabled"] = true
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

		_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))

		_, payload, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[poly] websocket read error: %v", err)
			p.closeConn()
			if !sleepContext(p.ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		p.handleRaw(payload, out)
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

func (p *PolyClient) handleRaw(raw []byte, out chan<- MarketUpdate) {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, item := range list {
			p.handleMessage(item, out)
		}
		return
	}
	if !json.Valid(raw) {
		log.Printf("[poly] non-json frame: %s", strings.TrimSpace(string(raw)))
		return
	}
	p.handleMessage(raw, out)
}

func (p *PolyClient) handleMessage(raw json.RawMessage, out chan<- MarketUpdate) {
	var envelope struct {
		EventType string          `json:"event_type"`
		AssetID   string          `json:"asset_id"`
		Bids      json.RawMessage `json:"bids"`
		BestBid   interface{}     `json:"best_bid"`
		Changes   []struct {
			AssetID string      `json:"asset_id"`
			Price   interface{} `json:"price"`
			BestBid interface{} `json:"best_bid"`
		} `json:"price_changes"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		log.Printf("[poly] decode websocket message: %v", err)
		return
	}

	switch envelope.EventType {
	case "price_change":
		for _, change := range envelope.Changes {
			price := bestAvailableFloat(change.BestBid, change.Price)
			if price > 0 {
				p.applyTokenUpdate(change.AssetID, price, out)
			}
		}
	case "book":
		var bids []struct {
			Price interface{} `json:"price"`
		}
		if err := json.Unmarshal(envelope.Bids, &bids); err != nil {
			return
		}
		if len(bids) > 0 {
			p.applyTokenUpdate(envelope.AssetID, floatValue(bids[0].Price), out)
		}
	case "best_bid_ask":
		price := floatValue(envelope.BestBid)
		if price > 0 {
			p.applyTokenUpdate(envelope.AssetID, price, out)
		}
	}
}

func (p *PolyClient) applyTokenUpdate(tokenID string, bestBid float64, out chan<- MarketUpdate) {
	if tokenID == "" || bestBid <= 0 {
		return
	}

	p.mu.Lock()
	marketID, ok := p.byClobToken[tokenID]
	if !ok {
		p.mu.Unlock()
		return
	}
	mapping, ok := p.tokenMap[tokenID]
	if !ok {
		p.mu.Unlock()
		return
	}
	market, ok := p.markets[marketID]
	if !ok {
		p.mu.Unlock()
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
	p.markets[marketID] = market
	p.mu.Unlock()

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
	conn := p.conn
	p.mu.RUnlock()
	if conn != nil {
		go p.pingLoop(conn)
	}
	if len(subs) > 0 {
		return p.Subscribe(subs)
	}
	return nil
}

func (p *PolyClient) pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.writeMu.Lock()
			err := conn.WriteMessage(websocket.TextMessage, []byte("PING"))
			p.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (p *PolyClient) closeConn() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
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

func floatValue(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0
		}
		return f
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

func bestAvailableFloat(values ...interface{}) float64 {
	for _, value := range values {
		if price := floatValue(value); price > 0 {
			return price
		}
	}
	return 0
}

func parseOutcomePrices(raw json.RawMessage) (float64, float64) {
	var prices []string
	if err := json.Unmarshal(raw, &prices); err == nil && len(prices) >= 2 {
		return normalizePolyPrice(floatValue(prices[0])), normalizePolyPrice(floatValue(prices[1]))
	}
	return 0, 0
}

func looksLikeSportsQuestion(text, gameStart string) bool {
	s := strings.ToLower(text)
	if strings.TrimSpace(gameStart) != "" {
		return true
	}
	return strings.Contains(s, " vs ") ||
		strings.Contains(s, " vs. ") ||
		strings.Contains(s, " @ ") ||
		strings.Contains(s, " finals") ||
		strings.Contains(s, " conference") ||
		strings.Contains(s, " stanley cup") ||
		strings.Contains(s, " world series") ||
		strings.Contains(s, "march madness")
}

func inferLeagueFromTeams(text string) string {
	s := strings.ToLower(text)
	switch {
	case strings.Contains(s, "stanley cup"), strings.Contains(s, "nhl"):
		return "NHL"
	case strings.Contains(s, "world series"), strings.Contains(s, "mlb"):
		return "MLB"
	case strings.Contains(s, "ncaa"), strings.Contains(s, "march madness"), strings.Contains(s, "final four"):
		return "NCAAB"
	case strings.Contains(s, "nba"), strings.Contains(s, "western conference"), strings.Contains(s, "eastern conference"):
		return "NBA"
	case strings.Contains(s, "nfl"), strings.Contains(s, "super bowl"):
		return "NFL"
	default:
		return ""
	}
}

func normalizePolyPrice(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v > 1 {
		return v / 100
	}
	return v
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
