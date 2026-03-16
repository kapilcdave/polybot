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
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	kalshiRESTBase = "https://api.elections.kalshi.com/trade-api/v2"
	kalshiWSURL    = "wss://api.elections.kalshi.com/trade-api/ws/v2"
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
	data, err := os.ReadFile(pemPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("kalshi private key file not found: %s", pemPath)
		}
		return nil, fmt.Errorf("read kalshi private key: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("decode kalshi private key PEM: no PEM block found")
	}

	var privateKey *rsa.PrivateKey
	if key, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes); parseErr == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("kalshi private key is not RSA")
		}
		privateKey = rsaKey
	} else if key, parseErr := x509.ParsePKCS1PrivateKey(block.Bytes); parseErr == nil {
		privateKey = key
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
	req, err := http.NewRequestWithContext(k.ctx, http.MethodGet, kalshiRESTBase+"/markets?status=open&limit=200", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gabagool-sports/1.0")
	req.Header.Set("Accept", "application/json")

	headers, err := k.authHeaders(http.MethodGet, "/trade-api/v2/markets?status=open&limit=200")
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
		return nil, fmt.Errorf("[kalshi] fetch sports markets: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("[kalshi] fetch sports markets status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Markets []struct {
			Ticker       string `json:"ticker"`
			Title        string `json:"title"`
			SeriesTicker string `json:"series_ticker"`
			YesBid       int    `json:"yes_bid"`
			NoBid        int    `json:"no_bid"`
			YesAsk       int    `json:"yes_ask"`
			NoAsk        int    `json:"no_ask"`
			CloseTime    string `json:"close_time"`
			Expiration   string `json:"expiration_time"`
			OpenTime     string `json:"open_time"`
		} `json:"markets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("[kalshi] decode sports markets: %w", err)
	}

	markets := make([]SportsMarket, 0, len(payload.Markets))
	next := make(map[string]SportsMarket, len(payload.Markets))
	for _, raw := range payload.Markets {
		league := detectLeague(raw.SeriesTicker + " " + raw.Title)
		if league == "" {
			continue
		}
		home, away := parseTeams(raw.Title, league)
		gameTime := parseTimeAny(raw.Expiration, raw.CloseTime, raw.OpenTime)
		market := SportsMarket{
			Platform:  "KALSHI",
			MarketID:  raw.Ticker,
			HomeTeam:  home,
			AwayTeam:  away,
			League:    league,
			GameTime:  gameTime,
			Question:  raw.Title,
			YesBid:    float64(raw.YesBid) / 100,
			NoBid:     float64(raw.NoBid) / 100,
			YesAsk:    float64(raw.YesAsk) / 100,
			NoAsk:     float64(raw.NoAsk) / 100,
			UpdatedAt: time.Now().UTC(),
			ClosesAt:  parseTimeAny(raw.CloseTime, raw.Expiration),
		}
		markets = append(markets, market)
		next[market.MarketID] = market
	}

	k.mu.Lock()
	k.markets = next
	k.mu.Unlock()
	return markets, nil
}

func parseTeams(title, league string) (home, away string) {
	clean := strings.TrimSpace(title)
	lower := strings.ToLower(clean)

	switch {
	case strings.Contains(lower, " will the ") && strings.Contains(lower, " beat "):
		afterWill := lower[strings.Index(lower, "will the ")+len("will the "):]
		parts := strings.SplitN(afterWill, " beat ", 2)
		if len(parts) == 2 {
			return normalizeTeamNameWithLeague(parts[0], league), normalizeTeamNameWithLeague(strings.TrimSuffix(parts[1], "?"), league)
		}
	case strings.Contains(lower, " vs. "):
		parts := strings.SplitN(lower, " vs. ", 2)
		return normalizeTeamNameWithLeague(parts[0], league), normalizeTeamNameWithLeague(parts[1], league)
	case strings.Contains(lower, " vs "):
		parts := strings.SplitN(lower, " vs ", 2)
		return normalizeTeamNameWithLeague(parts[0], league), normalizeTeamNameWithLeague(parts[1], league)
	case strings.Contains(lower, " @ "):
		parts := strings.SplitN(lower, " @ ", 2)
		return normalizeTeamNameWithLeague(parts[1], league), normalizeTeamNameWithLeague(parts[0], league)
	case strings.Contains(lower, " moneyline"):
		return normalizeTeamNameWithLeague(strings.TrimSuffix(lower, " moneyline"), league), ""
	}

	words := strings.Fields(lower)
	if len(words) > 0 {
		return normalizeTeamNameWithLeague(strings.Join(words, " "), league), ""
	}
	return "", ""
}

func (k *KalshiClient) Connect() error {
	headers, err := k.authHeaders(http.MethodGet, "/trade-api/ws/v2")
	if err != nil {
		return err
	}
	headers.Set("User-Agent", "gabagool-sports/1.0")
	headers.Set("Accept", "application/json")

	conn, _, err := k.dialer.DialContext(k.ctx, kalshiWSURL, headers)
	if err != nil {
		return fmt.Errorf("[kalshi] dial websocket: %w", err)
	}

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
			"id":  1,
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

		var message struct {
			Type   string `json:"type"`
			Market string `json:"market_ticker"`
			YesBid int    `json:"yes_bid"`
			NoBid  int    `json:"no_bid"`
			YesAsk int    `json:"yes_ask"`
			NoAsk  int    `json:"no_ask"`
		}
		if err := conn.ReadJSON(&message); err != nil {
			log.Printf("[kalshi] websocket read error: %v", err)
			k.closeConn()
			if !sleepContext(k.ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		if message.Type != "ticker" || message.Market == "" {
			continue
		}

		k.mu.Lock()
		market, ok := k.markets[message.Market]
		if !ok {
			k.mu.Unlock()
			continue
		}
		if message.YesBid > 0 {
			market.YesBid = float64(message.YesBid) / 100
		}
		if message.NoBid > 0 {
			market.NoBid = float64(message.NoBid) / 100
		}
		if message.YesAsk > 0 {
			market.YesAsk = float64(message.YesAsk) / 100
		}
		if message.NoAsk > 0 {
			market.NoAsk = float64(message.NoAsk) / 100
		}
		market.UpdatedAt = time.Now().UTC()
		k.markets[message.Market] = market
		k.mu.Unlock()

		select {
		case out <- MarketUpdate{Platform: "KALSHI", Market: market}:
		case <-k.ctx.Done():
			return
		}
	}
}

func (k *KalshiClient) authHeaders(method, path string) (http.Header, error) {
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	message := ts + method + path
	hash := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPSS(rand.Reader, k.key, crypto.SHA256, hash[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		return nil, fmt.Errorf("sign kalshi request: %w", err)
	}

	header := make(http.Header)
	header.Set("KALSHI-ACCESS-KEY", k.apiKeyID)
	header.Set("KALSHI-ACCESS-SIGNATURE", base64.StdEncoding.EncodeToString(signature))
	header.Set("KALSHI-ACCESS-TIMESTAMP", ts)
	return header, nil
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
	normalized := normalizeTeamName(name)
	if league == "NCAAB" {
		return strings.ToLower(strings.TrimSpace(name))
	}
	return normalized
}

func detectLeague(s string) string {
	upper := strings.ToUpper(s)
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

func resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
