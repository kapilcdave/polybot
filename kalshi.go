package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	kalshiRESTBases = []string{
		"https://api.kalshi.com/trade-api/v2",
		"https://api.elections.kalshi.com/trade-api/v2",
	}
	kalshiWSURLs = []string{
		"wss://api.kalshi.com/trade-api/ws/v2",
		"wss://api.elections.kalshi.com/trade-api/ws/v2",
	}
)

type KalshiClient struct {
	apiKeyID string
	key      *rsa.PrivateKey
	http     *http.Client
	dialer   *websocket.Dialer

	mu         sync.RWMutex
	ctx        context.Context
	conn       *websocket.Conn
	markets    map[string]SportsMarket
	subscribed []string

	writeMu sync.Mutex
}

func NewKalshiClient(apiKeyID, pemPath string) (*KalshiClient, error) {
	resolvedPath := resolvePath(pemPath)
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("kalshi private key file not found: %s", resolvedPath)
		}
		return nil, fmt.Errorf("read kalshi private key: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("decode kalshi private key PEM: no PEM block found")
	}

	var privateKey *rsa.PrivateKey
	if parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes); parseErr == nil {
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("kalshi private key is not RSA")
		}
		privateKey = rsaKey
	} else if parsed, parseErr := x509.ParsePKCS1PrivateKey(block.Bytes); parseErr == nil {
		privateKey = parsed
	} else {
		return nil, fmt.Errorf("parse kalshi private key: unsupported key format")
	}

	return &KalshiClient{
		apiKeyID: apiKeyID,
		key:      privateKey,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
		dialer: &websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
		},
		ctx:     context.Background(),
		markets: make(map[string]SportsMarket),
	}, nil
}

func (k *KalshiClient) WithContext(ctx context.Context) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ctx = ctx
}

func (k *KalshiClient) FetchSportsMarkets() ([]SportsMarket, error) {
	var (
		cursor  string
		markets []SportsMarket
		next    = make(map[string]SportsMarket)
	)
	for page := 0; page < 5; page++ {
		path := "/trade-api/v2/markets?status=open&limit=200"
		if cursor != "" {
			path += "&cursor=" + cursor
		}

		resp, err := k.doAuthedGET(path)
		if err != nil {
			return nil, fmt.Errorf("[kalshi] fetch sports markets: %w", err)
		}

		var payload struct {
			Cursor  string `json:"cursor"`
			Markets []struct {
				Ticker       string      `json:"ticker"`
				Title        string      `json:"title"`
				SeriesTicker string      `json:"series_ticker"`
				YesBid       interface{} `json:"yes_bid"`
				NoBid        interface{} `json:"no_bid"`
				YesAsk       interface{} `json:"yes_ask"`
				NoAsk        interface{} `json:"no_ask"`
				CloseTime    string      `json:"close_time"`
				Expiration   string      `json:"expiration_time"`
			} `json:"markets"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("[kalshi] decode sports markets: %w", err)
		}
		resp.Body.Close()

		for _, raw := range payload.Markets {
			league := detectLeague(raw.SeriesTicker + " " + raw.Title + " " + raw.Ticker)
			if league == "" {
				continue
			}
			home, away := parseTeams(raw.Title, league)
			market := SportsMarket{
				Platform:  "KALSHI",
				MarketID:  raw.Ticker,
				HomeTeam:  home,
				AwayTeam:  away,
				League:    league,
				GameTime:  parseTimeAny(raw.Expiration, raw.CloseTime),
				Question:  raw.Title,
				YesBid:    centsToFloat(raw.YesBid),
				NoBid:     centsToFloat(raw.NoBid),
				YesAsk:    centsToFloat(raw.YesAsk),
				NoAsk:     centsToFloat(raw.NoAsk),
				UpdatedAt: time.Now().UTC(),
				ClosesAt:  parseTimeAny(raw.CloseTime, raw.Expiration),
			}
			markets = append(markets, market)
			next[market.MarketID] = market
		}

		if payload.Cursor == "" || len(payload.Markets) == 0 {
			break
		}
		cursor = payload.Cursor
	}

	k.mu.Lock()
	k.markets = next
	k.mu.Unlock()
	return markets, nil
}

func (k *KalshiClient) doAuthedGET(path string) (*http.Response, error) {
	var lastErr error
	for _, base := range kalshiRESTBases {
		req, err := http.NewRequestWithContext(k.ctx, http.MethodGet, base+strings.TrimPrefix(path, "/trade-api/v2"), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "gabagool-sports/1.0")
		req.Header.Set("Accept", "application/json")

		headers, err := k.authHeaders(http.MethodGet, path)
		if err != nil {
			return nil, err
		}
		for key, values := range headers {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}

		resp, err := k.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func parseTeams(title, league string) (home, away string) {
	raw := strings.TrimSpace(title)
	lower := strings.ToLower(raw)
	lower = strings.ReplaceAll(lower, "—", "-")

	switch {
	case strings.HasPrefix(lower, "will the ") && strings.Contains(lower, " beat "):
		after := strings.TrimPrefix(lower, "will the ")
		parts := strings.SplitN(after, " beat ", 2)
		if len(parts) == 2 {
			return normalizeTeamNameWithLeague(parts[0], league), normalizeTeamNameWithLeague(strings.TrimSuffix(parts[1], "?"), league)
		}
	case strings.HasPrefix(lower, "will ") && strings.Contains(lower, " beat "):
		after := strings.TrimPrefix(lower, "will ")
		parts := strings.SplitN(after, " beat ", 2)
		if len(parts) == 2 {
			return normalizeTeamNameWithLeague(parts[0], league), normalizeTeamNameWithLeague(strings.TrimSuffix(parts[1], "?"), league)
		}
	case strings.Contains(lower, " vs. "):
		parts := strings.SplitN(lower, " vs. ", 2)
		return normalizeTeamNameWithLeague(parts[0], league), normalizeTeamNameWithLeague(parts[1], league)
	case strings.Contains(lower, " vs "):
		parts := strings.SplitN(lower, " vs ", 2)
		return normalizeTeamNameWithLeague(parts[0], league), normalizeTeamNameWithLeague(parts[1], league)
	case strings.Contains(lower, "@"):
		parts := strings.SplitN(lower, "@", 2)
		if len(parts) == 2 {
			return normalizeTeamNameWithLeague(parts[1], league), normalizeTeamNameWithLeague(parts[0], league)
		}
	case strings.Contains(lower, " moneyline"):
		return normalizeTeamNameWithLeague(strings.TrimSuffix(lower, " moneyline"), league), ""
	}

	return normalizeTeamNameWithLeague(lower, league), ""
}

func (k *KalshiClient) Connect() error {
	headers, err := k.authHeaders(http.MethodGet, "/trade-api/ws/v2")
	if err != nil {
		return err
	}
	headers.Set("User-Agent", "gabagool-sports/1.0")
	headers.Set("Accept", "application/json")

	var conn *websocket.Conn
	for _, wsURL := range kalshiWSURLs {
		conn, _, err = k.dialer.DialContext(k.ctx, wsURL, headers)
		if err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("[kalshi] dial websocket: %w", err)
	}
	conn.SetReadLimit(1 << 20)

	k.mu.Lock()
	if k.conn != nil {
		_ = k.conn.Close()
	}
	k.conn = conn
	k.mu.Unlock()
	return nil
}

func (k *KalshiClient) SubscribeToMarkets(tickers []string) error {
	k.mu.Lock()
	k.subscribed = append([]string(nil), tickers...)
	conn := k.conn
	k.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("[kalshi] subscribe: websocket not connected")
	}

	for start := 0; start < len(tickers); start += 50 {
		end := minInt(start+50, len(tickers))
		payload := map[string]any{
			"id":  1 + (start / 50),
			"cmd": "subscribe",
			"params": map[string]any{
				"channels":       []string{"ticker"},
				"market_tickers": tickers[start:end],
			},
		}
		k.writeMu.Lock()
		err := conn.WriteJSON(payload)
		k.writeMu.Unlock()
		if err != nil {
			return fmt.Errorf("[kalshi] subscribe batch: %w", err)
		}
	}
	return nil
}

func (k *KalshiClient) Listen(out chan<- MarketUpdate) {
	defer recoverGoroutine("kalshi-listen")

	backoff := time.Second
	for {
		select {
		case <-k.ctx.Done():
			return
		default:
		}

		k.mu.RLock()
		conn := k.conn
		k.mu.RUnlock()
		if conn == nil {
			if err := k.reconnect(); err != nil {
				log.Printf("[kalshi] reconnect failed: %v", err)
				if !sleepContext(k.ctx, backoff) {
					return
				}
				backoff = minDuration(backoff*2, 30*time.Second)
				continue
			}
			backoff = time.Second
			continue
		}

		var envelope struct {
			Type string          `json:"type"`
			Msg  json.RawMessage `json:"msg"`
		}
		if err := conn.ReadJSON(&envelope); err != nil {
			log.Printf("[kalshi] websocket read error: %v", err)
			k.closeConn()
			if !sleepContext(k.ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}
		if envelope.Type != "ticker" {
			continue
		}

		var msg struct {
			MarketTicker string  `json:"market_ticker"`
			YesBid       int     `json:"yes_bid"`
			NoBid        int     `json:"no_bid"`
			YesAsk       int     `json:"yes_ask"`
			NoAsk        int     `json:"no_ask"`
			YesBidDollar float64 `json:"yes_bid_dollars"`
			NoBidDollar  float64 `json:"no_bid_dollars"`
			YesAskDollar float64 `json:"yes_ask_dollars"`
			NoAskDollar  float64 `json:"no_ask_dollars"`
		}
		if err := json.Unmarshal(envelope.Msg, &msg); err != nil {
			log.Printf("[kalshi] decode ticker message: %v", err)
			continue
		}
		if msg.MarketTicker == "" {
			continue
		}

		k.mu.Lock()
		market, ok := k.markets[msg.MarketTicker]
		if !ok {
			k.mu.Unlock()
			continue
		}

		switch {
		case msg.YesBidDollar > 0:
			market.YesBid = msg.YesBidDollar
		case msg.YesBid > 0:
			market.YesBid = float64(msg.YesBid) / 100
		}
		switch {
		case msg.NoBidDollar > 0:
			market.NoBid = msg.NoBidDollar
		case msg.NoBid > 0:
			market.NoBid = float64(msg.NoBid) / 100
		}
		switch {
		case msg.YesAskDollar > 0:
			market.YesAsk = msg.YesAskDollar
		case msg.YesAsk > 0:
			market.YesAsk = float64(msg.YesAsk) / 100
		}
		switch {
		case msg.NoAskDollar > 0:
			market.NoAsk = msg.NoAskDollar
		case msg.NoAsk > 0:
			market.NoAsk = float64(msg.NoAsk) / 100
		}
		market.UpdatedAt = time.Now().UTC()
		k.markets[msg.MarketTicker] = market
		k.mu.Unlock()

		select {
		case out <- MarketUpdate{Platform: "KALSHI", Market: market}:
		case <-k.ctx.Done():
			return
		}
	}
}

func (k *KalshiClient) authHeaders(method, path string) (http.Header, error) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	message := timestamp + method + path
	sum := sha256.Sum256([]byte(message))
	sig, err := rsa.SignPSS(rand.Reader, k.key, crypto.SHA256, sum[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		return nil, fmt.Errorf("sign kalshi request: %w", err)
	}

	headers := make(http.Header)
	headers.Set("KALSHI-ACCESS-KEY", k.apiKeyID)
	headers.Set("KALSHI-ACCESS-SIGNATURE", base64.StdEncoding.EncodeToString(sig))
	headers.Set("KALSHI-ACCESS-TIMESTAMP", timestamp)
	return headers, nil
}

func (k *KalshiClient) reconnect() error {
	if err := k.Connect(); err != nil {
		return err
	}
	k.mu.RLock()
	subs := append([]string(nil), k.subscribed...)
	k.mu.RUnlock()
	if len(subs) > 0 {
		return k.SubscribeToMarkets(subs)
	}
	return nil
}

func (k *KalshiClient) closeConn() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.conn != nil {
		_ = k.conn.Close()
		k.conn = nil
	}
}

func normalizeTeamNameWithLeague(name, league string) string {
	name = strings.TrimSpace(name)
	if league == "NCAAB" {
		return strings.ToLower(strings.Join(strings.Fields(name), " "))
	}
	return normalizeTeamName(name)
}

func detectLeague(value string) string {
	upper := strings.ToUpper(value)
	for _, league := range []string{"NBA", "NHL", "MLB", "NCAAB", "NFL"} {
		if strings.Contains(upper, league) {
			return league
		}
	}
	return ""
}

func parseTimeAny(values ...string) time.Time {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.UTC()
			}
		}
	}
	return time.Time{}
}

func centsToFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		if x > 1 {
			return x / 100
		}
		return x
	case int:
		return float64(x) / 100
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0
		}
		if f > 1 {
			return f / 100
		}
		return f
	default:
		return 0
	}
}

func resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
