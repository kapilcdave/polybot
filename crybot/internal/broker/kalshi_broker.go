package broker

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
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"crybot/config"
	"crybot/internal/state"
)

type KalshiBroker struct {
	cfg        config.AppConfig
	logger     *slog.Logger
	httpClient *http.Client
	privateKey *rsa.PrivateKey
}

func NewKalshiBroker(cfg config.AppConfig, logger *slog.Logger) (*KalshiBroker, error) {
	broker := &KalshiBroker{
		cfg:    cfg,
		logger: logger,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
	if cfg.KalshiAPISecretPath != "" {
		key, err := loadRSAPrivateKey(cfg.KalshiAPISecretPath)
		if err != nil {
			return nil, err
		}
		broker.privateKey = key
	}
	return broker, nil
}

func (k *KalshiBroker) Name() string { return "kalshi" }

func (k *KalshiBroker) Run(ctx context.Context, out chan<- state.Update) error {
	if k.cfg.KalshiAPIKey == "" || k.privateKey == nil {
		k.logger.Warn("kalshi websocket auth unavailable, using public REST polling for market data")
		return k.runPublicPoller(ctx, out)
	}

	if err := k.runWebsocket(ctx, out); err != nil {
		k.logger.Warn("kalshi websocket failed, using public REST polling for market data", slog.String("error", err.Error()))
		return k.runPublicPoller(ctx, out)
	}
	return nil
}

func (k *KalshiBroker) runWebsocket(ctx context.Context, out chan<- state.Update) error {
	headers := http.Header{}
	signed, err := k.authHeaders("GET", "/trade-api/ws/v2")
	if err != nil {
		return err
	}
	headers = signed

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, k.cfg.KalshiWSURL, headers)
	if err != nil {
		return describeWSError("kalshi", err, resp)
	}
	defer conn.Close()

	subscribe := map[string]any{
		"id":  1,
		"cmd": "subscribe",
		"params": map[string]any{
			"channels": []string{"ticker", "market_lifecycle_v2"},
		},
	}
	if err := conn.WriteJSON(subscribe); err != nil {
		return err
	}

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		msgType := struct {
			Type string `json:"type"`
			Msg  struct {
				MarketTicker string `json:"market_ticker"`
				YesAsk       int64  `json:"yes_ask"`
				NoAsk        int64  `json:"no_ask"`
				ExpirationTS int64  `json:"expiration_ts"`
				CloseTS      int64  `json:"close_time"`
				Status       string `json:"status"`
			} `json:"msg"`
		}{}
		if err := json.Unmarshal(payload, &msgType); err != nil {
			continue
		}

		crypto, windowStart, ok := k.matchCryptoWindow(msgType.Msg.MarketTicker, msgType.Msg.ExpirationTS, msgType.Msg.CloseTS)
		if !ok {
			continue
		}

		yesPrice := centsToProb(msgType.Msg.YesAsk)
		noPrice := centsToProb(msgType.Msg.NoAsk)
		if yesPrice == 0 && noPrice == 0 {
			continue
		}

		select {
		case out <- state.Update{
			Type:           state.UpdateKalshi,
			Crypto:         crypto,
			WindowStart:    windowStart,
			Timestamp:      time.Now().UTC(),
			KalshiTicker:   msgType.Msg.MarketTicker,
			KalshiYesPrice: yesPrice,
			KalshiNoPrice:  noPrice,
		}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (k *KalshiBroker) runPublicPoller(ctx context.Context, out chan<- state.Update) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		windowStart := state.WindowStart(time.Now().UTC())
		if err := k.pollMarkets(ctx, windowStart, out); err != nil {
			k.logger.Warn("kalshi REST poll failed", slog.String("error", err.Error()))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (k *KalshiBroker) pollMarkets(ctx context.Context, windowStart int64, out chan<- state.Update) error {
	minClose := windowStart - 900
	maxClose := windowStart + 1800
	url := fmt.Sprintf("%s/markets?limit=200&min_close_ts=%d&max_close_ts=%d", k.cfg.KalshiBaseURL, minClose, maxClose)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kalshi markets status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Markets []struct {
			Ticker        string `json:"ticker"`
			Status        string `json:"status"`
			CloseTime     string `json:"close_time"`
			ExpirationTS  string `json:"expiration_ts"`
			YesAsk        int64  `json:"yes_ask"`
			NoAsk         int64  `json:"no_ask"`
			YesAskDollars string `json:"yes_ask_dollars"`
			NoAskDollars  string `json:"no_ask_dollars"`
		} `json:"markets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}

	for _, market := range payload.Markets {
		expirationTS := parseKalshiTime(market.ExpirationTS)
		closeTS := parseKalshiTime(market.CloseTime)
		crypto, mappedWindowStart, ok := k.matchCryptoWindow(market.Ticker, expirationTS, closeTS)
		if !ok {
			continue
		}

		yesPrice := centsToProb(market.YesAsk)
		noPrice := centsToProb(market.NoAsk)
		if yesPrice == 0 && market.YesAskDollars != "" {
			yesPrice, _ = parseMoney(market.YesAskDollars)
		}
		if noPrice == 0 && market.NoAskDollars != "" {
			noPrice, _ = parseMoney(market.NoAskDollars)
		}
		if yesPrice == 0 && noPrice == 0 {
			continue
		}

		select {
		case out <- state.Update{
			Type:           state.UpdateKalshi,
			Crypto:         crypto,
			WindowStart:    mappedWindowStart,
			Timestamp:      time.Now().UTC(),
			KalshiTicker:   market.Ticker,
			KalshiYesPrice: yesPrice,
			KalshiNoPrice:  noPrice,
		}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

func (k *KalshiBroker) PlaceOrder(ctx context.Context, order KalshiOrder) (KalshiOrderResponse, error) {
	if k.cfg.KalshiAPIKey == "" || k.privateKey == nil {
		return KalshiOrderResponse{}, fmt.Errorf("kalshi credentials not configured")
	}

	body := map[string]any{
		"ticker":            order.Ticker,
		"side":              order.Side,
		"action":            order.Action,
		"count":             order.Count,
		"yes_price_dollars": fmt.Sprintf("%.4f", order.YesPriceFloat),
		"client_order_id":   order.ClientOrderID,
		"type":              "limit",
		"time_in_force":     "fill_or_kill",
	}

	rawBody, err := json.Marshal(body)
	if err != nil {
		return KalshiOrderResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.cfg.KalshiBaseURL+"/portfolio/orders", strings.NewReader(string(rawBody)))
	if err != nil {
		return KalshiOrderResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	headers, err := k.authHeaders("POST", "/trade-api/v2/portfolio/orders")
	if err != nil {
		return KalshiOrderResponse{}, err
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return KalshiOrderResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return KalshiOrderResponse{}, fmt.Errorf("kalshi order rejected: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Order struct {
			OrderID string `json:"order_id"`
			Status  string `json:"status"`
		} `json:"order"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return KalshiOrderResponse{}, err
	}

	return KalshiOrderResponse{OrderID: payload.Order.OrderID, Status: payload.Order.Status}, nil
}

var unixRegex = regexp.MustCompile(`(\d{10})`)

func (k *KalshiBroker) matchCryptoWindow(ticker string, expirationTS, closeTS int64) (string, int64, bool) {
	tickerUpper := strings.ToUpper(ticker)
	for crypto, hints := range k.cfg.KalshiTickerHints {
		for _, hint := range hints {
			if strings.Contains(tickerUpper, strings.ToUpper(hint)) {
				if expirationTS == 0 {
					expirationTS = closeTS
				}
				if expirationTS == 0 {
					if match := unixRegex.FindStringSubmatch(ticker); len(match) == 2 {
						fmt.Sscanf(match[1], "%d", &expirationTS)
					}
				}
				if expirationTS == 0 {
					return "", 0, false
				}
				return crypto, (expirationTS / 900) * 900, true
			}
		}
	}
	return "", 0, false
}

func centsToProb(value int64) float64 {
	if value <= 0 {
		return 0
	}
	return float64(value) / 100
}

func parseMoney(raw string) (float64, error) {
	var value float64
	_, err := fmt.Sscanf(raw, "%f", &value)
	return value, err
}

func parseKalshiTime(raw string) int64 {
	if raw == "" {
		return 0
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC().Unix()
	}
	var unix int64
	if _, err := fmt.Sscanf(raw, "%d", &unix); err == nil {
		return unix
	}
	return 0
}

func describeWSError(name string, err error, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("%s websocket dial failed: %w", name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s websocket dial failed: %w (status=%d body=%q)", name, err, resp.StatusCode, strings.TrimSpace(string(body)))
}

func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("invalid PEM file")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key is not RSA")
		}
		return rsaKey, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func (k *KalshiBroker) authHeaders(method, path string) (http.Header, error) {
	ts := fmt.Sprintf("%d", time.Now().UTC().UnixMilli())
	signingText := ts + method + path
	hashed := sha256.Sum256([]byte(signingText))
	sig, err := rsa.SignPSS(rand.Reader, k.privateKey, crypto.SHA256, hashed[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		return nil, err
	}

	header := http.Header{}
	header.Set("KALSHI-ACCESS-KEY", k.cfg.KalshiAPIKey)
	header.Set("KALSHI-ACCESS-SIGNATURE", base64.StdEncoding.EncodeToString(sig))
	header.Set("KALSHI-ACCESS-TIMESTAMP", ts)
	return header, nil
}
