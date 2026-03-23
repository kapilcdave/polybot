package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"crybot/config"
	"crybot/internal/state"
)

type PolymarketBroker struct {
	cfg        config.AppConfig
	logger     *slog.Logger
	httpClient *http.Client
}

func NewPolymarketBroker(cfg config.AppConfig, logger *slog.Logger) *PolymarketBroker {
	return &PolymarketBroker{
		cfg:    cfg,
		logger: logger,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (p *PolymarketBroker) Name() string { return "polymarket" }

func (p *PolymarketBroker) Run(ctx context.Context, out chan<- state.Update) error {
	assetUpdates := make(chan polyWSUpdate, 256)
	subscriptions := make(chan []string, 8)
	if p.cfg.PolymarketEnableWS {
		wsCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			if err := p.runMarketWS(wsCtx, subscriptions, assetUpdates); err != nil && ctx.Err() == nil {
				p.logger.Warn("polymarket websocket stopped", slog.String("error", err.Error()))
			}
		}()
	} else {
		p.logger.Info("polymarket market websocket disabled; using gamma polling only")
	}

	knownAssets := make(map[string]polyAssetBinding)
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case wsUpdate := <-assetUpdates:
			binding, ok := knownAssets[wsUpdate.AssetID]
			if !ok {
				continue
			}
			upPrice := wsUpdate.Price
			downPrice := 1 - wsUpdate.Price
			if wsUpdate.AssetID == binding.DownAssetID {
				upPrice = 1 - wsUpdate.Price
				downPrice = wsUpdate.Price
			}
			select {
			case out <- state.Update{
				Type:           state.UpdatePolymarket,
				Crypto:         binding.Crypto,
				WindowStart:    binding.WindowStart,
				Timestamp:      wsUpdate.Timestamp,
				PolymarketID:   binding.MarketID,
				PolymarketSlug: binding.Slug,
				PolymarketAsset: []string{
					binding.UpAssetID,
					binding.DownAssetID,
				},
				PolyUpPrice:   upPrice,
				PolyDownPrice: downPrice,
				PolyFeeImpact: binding.FeeImpact,
			}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ticker.C:
			windowStart := state.WindowStart(time.Now().UTC())
			bindings, err := p.refreshMarkets(ctx, windowStart, out)
			if err != nil {
				p.logger.Warn("polymarket refresh failed", slog.String("error", err.Error()))
				continue
			}
			for assetID, binding := range bindings {
				knownAssets[assetID] = binding
			}
			assets := make([]string, 0, len(bindings))
			for assetID := range bindings {
				assets = append(assets, assetID)
			}
			if p.cfg.PolymarketEnableWS {
				select {
				case subscriptions <- assets:
				default:
				}
			}
		}
	}
}

type gammaMarket struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	Question      string `json:"question"`
	Outcomes      string `json:"outcomes"`
	OutcomePrices string `json:"outcomePrices"`
	ClobTokenIDs  string `json:"clobTokenIds"`
	Closed        bool   `json:"closed"`
	Active        bool   `json:"active"`
	Fee           string `json:"fee"`
	FeesEnabled   bool   `json:"feesEnabled"`
	Tokens        []struct {
		TokenID string `json:"token_id"`
		Outcome string `json:"outcome"`
	} `json:"tokens"`
}

type polyAssetBinding struct {
	MarketID    string
	Slug        string
	Crypto      string
	WindowStart int64
	UpAssetID   string
	DownAssetID string
	FeeImpact   float64
}

func (p *PolymarketBroker) refreshMarkets(ctx context.Context, windowStart int64, out chan<- state.Update) (map[string]polyAssetBinding, error) {
	bindings := make(map[string]polyAssetBinding)
	for _, symbol := range p.cfg.Symbols {
		crypto := strings.ToLower(symbol)
		market, usedWindowStart, err := p.findMarket(ctx, crypto, windowStart)
		if err != nil {
			p.logger.Debug("polymarket market unavailable",
				slog.String("crypto", crypto),
				slog.Int64("window_start", windowStart),
				slog.String("error", err.Error()),
			)
			continue
		}

		upPrice, downPrice, err := parseOutcomePrices(market.Outcomes, market.OutcomePrices)
		if err != nil {
			return nil, err
		}
		upAssetID, downAssetID := parseTokenIDs(market)
		feeImpact := parseFeeImpact(market.Fee, market.FeesEnabled, p.cfg.PolymarketFeeBps)

		update := state.Update{
			Type:            state.UpdatePolymarket,
			Crypto:          crypto,
			WindowStart:     usedWindowStart,
			Timestamp:       time.Now().UTC(),
			PolymarketID:    market.ID,
			PolymarketSlug:  market.Slug,
			PolymarketAsset: compactStrings(upAssetID, downAssetID),
			PolyUpPrice:     upPrice,
			PolyDownPrice:   downPrice,
			PolyFeeImpact:   feeImpact,
		}
		select {
		case out <- update:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		binding := polyAssetBinding{
			MarketID:    market.ID,
			Slug:        market.Slug,
			Crypto:      crypto,
			WindowStart: usedWindowStart,
			UpAssetID:   upAssetID,
			DownAssetID: downAssetID,
			FeeImpact:   feeImpact,
		}
		if upAssetID != "" {
			bindings[upAssetID] = binding
		}
		if downAssetID != "" {
			bindings[downAssetID] = binding
		}
	}
	return bindings, nil
}

func (p *PolymarketBroker) findMarket(ctx context.Context, crypto string, windowStart int64) (gammaMarket, int64, error) {
	candidates := []int64{windowStart, windowStart - 900, windowStart + 900}
	for _, ts := range candidates {
		slug := fmt.Sprintf("%s-updown-15m-%d", crypto, ts)
		market, err := p.fetchBySlug(ctx, slug)
		if err == nil {
			return market, ts, nil
		}
	}
	return gammaMarket{}, 0, fmt.Errorf("no market found for %s around %d", crypto, windowStart)
}

func (p *PolymarketBroker) fetchBySlug(ctx context.Context, slug string) (gammaMarket, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.PolymarketGammaURL+"/markets?slug="+slug, nil)
	if err != nil {
		return gammaMarket{}, err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return gammaMarket{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return gammaMarket{}, fmt.Errorf("gamma status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return gammaMarket{}, err
	}

	var markets []gammaMarket
	if err := json.Unmarshal(body, &markets); err != nil {
		return gammaMarket{}, err
	}
	if len(markets) == 0 {
		return gammaMarket{}, fmt.Errorf("slug %s not found", slug)
	}
	return markets[0], nil
}

type polyWSUpdate struct {
	AssetID   string
	Price     float64
	Timestamp time.Time
}

func (p *PolymarketBroker) runMarketWS(ctx context.Context, subscriptions <-chan []string, out chan<- polyWSUpdate) error {
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, p.cfg.PolymarketMarketWSURL, nil)
	if err != nil {
		return describeWSError("polymarket", err, resp)
	}
	defer conn.Close()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case assets := <-subscriptions:
				if len(assets) == 0 {
					continue
				}
				msg := map[string]any{
					"assets_ids":             assets,
					"type":                   "market",
					"custom_feature_enabled": true,
				}
				if err := conn.WriteJSON(msg); err != nil {
					p.logger.Warn("polymarket subscribe failed", slog.String("error", err.Error()))
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return err
			}
			var msg struct {
				EventType string `json:"event_type"`
				AssetID   string `json:"asset_id"`
				BestBid   string `json:"best_bid"`
				BestAsk   string `json:"best_ask"`
				Price     string `json:"price"`
			}
			if err := json.Unmarshal(payload, &msg); err != nil {
				continue
			}
			if msg.AssetID == "" {
				continue
			}
			price := midpoint(msg.BestBid, msg.BestAsk)
			if price == 0 {
				price, _ = parseOutcome(msg.Price)
			}
			select {
			case out <- polyWSUpdate{AssetID: msg.AssetID, Price: price, Timestamp: time.Now().UTC()}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func compactStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func parseOutcomePrices(outcomesRaw, pricesRaw string) (float64, float64, error) {
	var outcomes []string
	if err := json.Unmarshal([]byte(outcomesRaw), &outcomes); err != nil {
		return 0, 0, err
	}
	var prices []string
	if err := json.Unmarshal([]byte(pricesRaw), &prices); err != nil {
		return 0, 0, err
	}
	if len(outcomes) != len(prices) {
		return 0, 0, fmt.Errorf("outcome length mismatch")
	}

	var up, down float64
	for i, outcome := range outcomes {
		price, err := parseOutcome(prices[i])
		if err != nil {
			return 0, 0, err
		}
		switch strings.ToLower(outcome) {
		case "up", "yes":
			up = price
		case "down", "no":
			down = price
		}
	}
	return up, down, nil
}

func parseTokenIDs(market gammaMarket) (string, string) {
	var upAssetID, downAssetID string
	for _, token := range market.Tokens {
		switch strings.ToLower(token.Outcome) {
		case "up", "yes":
			upAssetID = token.TokenID
		case "down", "no":
			downAssetID = token.TokenID
		}
	}
	return upAssetID, downAssetID
}

func parseFeeImpact(fee string, enabled bool, fallbackBps float64) float64 {
	if !enabled {
		return 0
	}
	if fee != "" {
		if parsed, err := parseOutcome(fee); err == nil {
			return parsed
		}
	}
	return fallbackBps / 10000
}

func parseOutcome(raw string) (float64, error) {
	var value float64
	_, err := fmt.Sscanf(raw, "%f", &value)
	return value, err
}

func midpoint(bestBid, bestAsk string) float64 {
	bid, err1 := parseOutcome(bestBid)
	ask, err2 := parseOutcome(bestAsk)
	if err1 != nil || err2 != nil || bid == 0 || ask == 0 {
		return 0
	}
	return (bid + ask) / 2
}
