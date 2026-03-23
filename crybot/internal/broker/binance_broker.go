package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"crybot/config"
	"crybot/internal/state"
)

type BinanceBroker struct {
	cfg        config.AppConfig
	logger     *slog.Logger
	httpClient *http.Client
}

func NewBinanceBroker(cfg config.AppConfig, logger *slog.Logger) *BinanceBroker {
	return &BinanceBroker{
		cfg:    cfg,
		logger: logger,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (b *BinanceBroker) Name() string { return "binance" }

func (b *BinanceBroker) Run(ctx context.Context, out chan<- state.Update) error {
	if err := b.runWebsocket(ctx, out); err != nil {
		b.logger.Warn("binance websocket failed, using REST polling", slog.String("error", err.Error()))
		return b.runRESTPoller(ctx, out)
	}
	return nil
}

func (b *BinanceBroker) runWebsocket(ctx context.Context, out chan<- state.Update) error {
	streams := make([]string, 0, len(b.cfg.Symbols))
	for _, symbol := range b.cfg.Symbols {
		streams = append(streams, strings.ToLower(symbol)+"usdt@bookTicker")
	}

	u, err := url.Parse(b.cfg.BinanceWSURL)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("streams", strings.Join(streams, "/"))
	u.RawQuery = q.Encode()

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return describeWSError("binance", err, resp)
	}
	defer conn.Close()

	type message struct {
		Stream string `json:"stream"`
		Data   struct {
			Symbol string `json:"s"`
			Bid    string `json:"b"`
			Ask    string `json:"a"`
		} `json:"data"`
	}

	lastUpdate := time.Now().UTC()
	lastWindowByCrypto := make(map[string]int64)
	go b.watchStale(ctx, &lastUpdate)

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var msg message
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}

		bid, err1 := parseFloat(msg.Data.Bid)
		ask, err2 := parseFloat(msg.Data.Ask)
		if err1 != nil || err2 != nil {
			continue
		}

		now := time.Now().UTC()
		lastUpdate = now
		mid := (bid + ask) / 2
		crypto := normalizeBinanceSymbol(msg.Data.Symbol)
		windowStart := state.WindowStart(now)
		prevWindow, seen := lastWindowByCrypto[crypto]
		openObserved := seen && prevWindow != windowStart
		lastWindowByCrypto[crypto] = windowStart

		select {
		case out <- state.Update{
			Type:            state.UpdateBinance,
			Crypto:          crypto,
			WindowStart:     windowStart,
			Timestamp:       now,
			SpotPrice:       mid,
			BinanceMidTime:  now,
			WindowOpenPrice: mid,
			OpenObserved:    openObserved,
		}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (b *BinanceBroker) runRESTPoller(ctx context.Context, out chan<- state.Update) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	lastWindowByCrypto := make(map[string]int64)

	for {
		for _, symbol := range b.cfg.Symbols {
			if err := b.pollSymbol(ctx, symbol, lastWindowByCrypto, out); err != nil {
				b.logger.Warn("binance REST poll failed",
					slog.String("symbol", symbol),
					slog.String("error", err.Error()),
				)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *BinanceBroker) pollSymbol(ctx context.Context, symbol string, lastWindowByCrypto map[string]int64, out chan<- state.Update) error {
	paths := []string{
		fmt.Sprintf("%s/ticker/bookTicker?symbol=%sUSDT", strings.TrimRight(b.cfg.BinanceRESTURL, "/"), strings.ToUpper(symbol)),
	}
	if strings.Contains(b.cfg.BinanceRESTURL, "binance.com") {
		paths = append(paths, fmt.Sprintf("https://api.binance.us/api/v3/ticker/bookTicker?symbol=%sUSDT", strings.ToUpper(symbol)))
	}

	var lastErr error
	for _, endpoint := range paths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}

		resp, err := b.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}

		var payload struct {
			Symbol string `json:"symbol"`
			Bid    string `json:"bidPrice"`
			Ask    string `json:"askPrice"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			lastErr = err
			continue
		}

		bid, err1 := parseFloat(payload.Bid)
		ask, err2 := parseFloat(payload.Ask)
		if err1 != nil || err2 != nil {
			lastErr = fmt.Errorf("invalid bid/ask payload")
			continue
		}

		now := time.Now().UTC()
		mid := (bid + ask) / 2
		crypto := strings.ToLower(symbol)
		windowStart := state.WindowStart(now)
		prevWindow, seen := lastWindowByCrypto[crypto]
		openObserved := seen && prevWindow != windowStart
		lastWindowByCrypto[crypto] = windowStart

		select {
		case out <- state.Update{
			Type:            state.UpdateBinance,
			Crypto:          crypto,
			WindowStart:     windowStart,
			Timestamp:       now,
			SpotPrice:       mid,
			BinanceMidTime:  now,
			WindowOpenPrice: mid,
			OpenObserved:    openObserved,
		}:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no Binance endpoint succeeded")
	}
	return lastErr
}

func (b *BinanceBroker) watchStale(ctx context.Context, lastUpdate *time.Time) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Since(*lastUpdate) > time.Second {
				b.logger.Warn("binance feed stale", slog.Duration("age", time.Since(*lastUpdate)))
			}
		}
	}
}

func normalizeBinanceSymbol(symbol string) string {
	symbol = strings.ToUpper(symbol)
	for _, quote := range []string{"USDT", "USD", "FDUSD"} {
		if strings.HasSuffix(symbol, quote) {
			return strings.ToLower(strings.TrimSuffix(symbol, quote))
		}
	}
	return strings.ToLower(symbol)
}

func parseFloat(raw string) (float64, error) {
	var value float64
	_, err := fmt.Sscanf(raw, "%f", &value)
	return value, err
}
