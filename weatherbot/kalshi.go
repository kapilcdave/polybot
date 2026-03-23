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

type KalshiClient struct {
	baseURL string
	http    *http.Client
	auth    string
}

func NewKalshiClient(cfg Config) *KalshiClient {
	return &KalshiClient{
		baseURL: cfg.KalshiBaseURL,
		http:    &http.Client{Timeout: cfg.HTTPTimeout},
		auth:    strings.TrimSpace(cfg.KalshiAuthToken),
	}
}

func (k *KalshiClient) DiscoverWeatherMarkets(ctx context.Context) ([]Market, error) {
	var out []Market
	for _, loc := range trackedLocations {
		url := fmt.Sprintf("%s/markets?status=open&series_ticker=%s&limit=200", k.baseURL, loc.SeriesTicker)
		resp, err := k.do(ctx, url)
		if err != nil {
			return nil, fmt.Errorf("kalshi fetch %s: %w", loc.SeriesTicker, err)
		}
		var payload struct {
			Markets []struct {
				Ticker       string      `json:"ticker"`
				Title        string      `json:"title"`
				CloseTime    string      `json:"close_time"`
				Expiration   string      `json:"expiration_time"`
				YesBidDollar interface{} `json:"yes_bid_dollars"`
				NoBidDollar  interface{} `json:"no_bid_dollars"`
				YesAskDollar interface{} `json:"yes_ask_dollars"`
				NoAskDollar  interface{} `json:"no_ask_dollars"`
				YesBid       interface{} `json:"yes_bid"`
				NoBid        interface{} `json:"no_bid"`
				YesAsk       interface{} `json:"yes_ask"`
				NoAsk        interface{} `json:"no_ask"`
			} `json:"markets"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("kalshi decode %s: %w", loc.SeriesTicker, err)
		}
		resp.Body.Close()
		for _, raw := range payload.Markets {
			threshold, ok := parseThresholdF(raw.Title)
			if !ok {
				continue
			}
			eventDate, ok := parseEventDate(raw.Title, parseTimeAny(raw.Expiration, raw.CloseTime), loc.Timezone)
			if !ok {
				continue
			}
			if st, ok := settlementForCity(loc.Name); ok {
				loc.Timezone = st.Timezone
			}
			out = append(out, Market{
				Platform:     "KALSHI",
				ID:           raw.Ticker,
				SeriesTicker: loc.SeriesTicker,
				Question:     raw.Title,
				City:         loc.Name,
				Settlement:   settlementStationID(loc.Name),
				SettlementTZ: loc.Timezone,
				ThresholdF:   threshold,
				EventDate:    eventDate,
				YesBid:       firstPositiveFloat(centsToFloat(raw.YesBidDollar), centsToFloat(raw.YesBid)),
				NoBid:        firstPositiveFloat(centsToFloat(raw.NoBidDollar), centsToFloat(raw.NoBid)),
				YesAsk:       firstPositiveFloat(centsToFloat(raw.YesAskDollar), centsToFloat(raw.YesAsk)),
				NoAsk:        firstPositiveFloat(centsToFloat(raw.NoAskDollar), centsToFloat(raw.NoAsk)),
				URL:          "https://kalshi.com/markets/" + strings.ToLower(raw.Ticker),
			})
		}
	}
	return out, nil
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
				return parsed
			}
		}
	}
	return time.Time{}
}

func (k *KalshiClient) do(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "weatherbot/1.0")
	if k.auth != "" {
		req.Header.Set("Authorization", "Bearer "+k.auth)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := k.http.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 300 * time.Millisecond)
			continue
		}
		if resp.StatusCode >= 500 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			lastErr = fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
			time.Sleep(time.Duration(attempt+1) * 300 * time.Millisecond)
			continue
		}
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return resp, nil
	}
	return nil, lastErr
}

func centsToFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		if x > 1 {
			return x / 100
		}
		return x
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

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func settlementStationID(city string) string {
	if s, ok := settlementForCity(city); ok {
		return s.StationID
	}
	return ""
}
