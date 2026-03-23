package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type PolymarketClient struct {
	gammaURL string
	clobURL  string
	http     *http.Client
}

func NewPolymarketClient(cfg Config) *PolymarketClient {
	return &PolymarketClient{
		gammaURL: cfg.PolymarketGammaURL,
		clobURL:  cfg.PolymarketCLOBURL,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *PolymarketClient) DiscoverWeatherMarkets(ctx context.Context) ([]Market, error) {
	orderBooks, err := p.fetchOrderBooks(ctx)
	if err != nil {
		return nil, err
	}

	var out []Market
	offset := 0
	const pageSize = 500
	for {
		url := fmt.Sprintf("%s/markets?active=true&closed=false&limit=%d&offset=%d", p.gammaURL, pageSize, offset)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := p.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("polymarket fetch: %w", err)
		}
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, fmt.Errorf("polymarket status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var payload []struct {
			ID           string          `json:"id"`
			Question     string          `json:"question"`
			EndDate      string          `json:"endDate"`
			ClobTokenIDs json.RawMessage `json:"clobTokenIds"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("polymarket decode: %w", err)
		}
		resp.Body.Close()
		if len(payload) == 0 {
			break
		}
		for _, raw := range payload {
			city, loc, ok := detectWeatherLocation(raw.Question)
			if !ok {
				continue
			}
			st, _ := settlementForCity(city)
			threshold, ok := parseThresholdF(raw.Question)
			if !ok {
				continue
			}
			eventDate, ok := parseEventDate(raw.Question, parseTimeAny(raw.EndDate), loc.Timezone)
			if !ok {
				continue
			}
			tokenIDs := parseTokenIDs(raw.ClobTokenIDs)
			if len(tokenIDs) < 2 {
				continue
			}
			yesBook := orderBooks[tokenIDs[0]]
			noBook := orderBooks[tokenIDs[1]]
			out = append(out, Market{
				Platform:   "POLY",
				ID:         raw.ID,
				Question:   raw.Question,
				City:       city,
				Settlement: st.StationID,
				SettlementTZ: func() string {
					if st.Timezone != "" {
						return st.Timezone
					}
					return loc.Timezone
				}(),
				ThresholdF: threshold,
				EventDate:  eventDate,
				YesBid:     yesBook.Bid,
				YesAsk:     yesBook.Ask,
				NoBid:      noBook.Bid,
				NoAsk:      noBook.Ask,
				YesSizeUSD: yesBook.AskSize,
				NoSizeUSD:  noBook.AskSize,
				URL:        "https://polymarket.com/event/" + raw.ID,
			})
		}
		offset += len(payload)
		if len(payload) < pageSize {
			break
		}
	}
	return out, nil
}

type topOfBook struct {
	Bid     float64
	Ask     float64
	BidSize float64
	AskSize float64
}

func (p *PolymarketClient) fetchOrderBooks(ctx context.Context) (map[string]topOfBook, error) {
	orderBooks := make(map[string]topOfBook)
	offset := 0
	const pageSize = 500
	for {
		url := fmt.Sprintf("%s/markets?active=true&closed=false&limit=%d&offset=%d", p.gammaURL, pageSize, offset)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := p.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("polymarket prefetch: %w", err)
		}
		var payload []struct {
			ClobTokenIDs json.RawMessage `json:"clobTokenIds"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("polymarket prefetch decode: %w", err)
		}
		resp.Body.Close()
		if len(payload) == 0 {
			break
		}
		var tokenIDs []string
		for _, raw := range payload {
			tokenIDs = append(tokenIDs, parseTokenIDs(raw.ClobTokenIDs)...)
		}
		batch, err := p.fetchBooksBatch(ctx, tokenIDs)
		if err != nil {
			return nil, err
		}
		for tokenID, book := range batch {
			orderBooks[tokenID] = book
		}
		offset += len(payload)
		if len(payload) < pageSize {
			break
		}
	}
	return orderBooks, nil
}

func (p *PolymarketClient) fetchBooksBatch(ctx context.Context, tokenIDs []string) (map[string]topOfBook, error) {
	out := make(map[string]topOfBook)
	for _, tokenID := range tokenIDs {
		if tokenID == "" {
			continue
		}
		url := fmt.Sprintf("%s/book?token_id=%s", p.clobURL, tokenID)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := p.http.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode >= 300 {
			resp.Body.Close()
			continue
		}
		var payload struct {
			Bids []struct {
				Price string `json:"price"`
				Size  string `json:"size"`
			} `json:"bids"`
			Asks []struct {
				Price string `json:"price"`
				Size  string `json:"size"`
			} `json:"asks"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()
		book := topOfBook{}
		if len(payload.Bids) > 0 {
			book.Bid, _ = strconv.ParseFloat(payload.Bids[0].Price, 64)
			book.BidSize, _ = strconv.ParseFloat(payload.Bids[0].Size, 64)
		}
		if len(payload.Asks) > 0 {
			book.Ask, _ = strconv.ParseFloat(payload.Asks[0].Price, 64)
			book.AskSize, _ = strconv.ParseFloat(payload.Asks[0].Size, 64)
		}
		out[tokenID] = book
	}
	return out, nil
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

func detectWeatherLocation(question string) (string, WeatherLocation, bool) {
	s := normalizeText(question)
	if !strings.Contains(s, "temperature") && !strings.Contains(s, "high") {
		return "", WeatherLocation{}, false
	}
	for _, loc := range trackedLocations {
		for _, kw := range loc.PolymarketKeywords {
			if strings.Contains(s, normalizeText(kw)) {
				return loc.Name, loc, true
			}
		}
	}
	return "", WeatherLocation{}, false
}
