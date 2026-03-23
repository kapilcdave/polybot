package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	PollInterval       time.Duration
	EdgeThreshold      float64
	KellyFraction      float64
	MaxTradeUSD        float64
	StartingBankroll   float64
	SimulationMode     bool
	MaxSpreadPct       float64
	MinDepthUSD        float64
	DashboardAddr      string
	KalshiBaseURL      string
	KalshiAuthToken    string
	PolymarketGammaURL string
	PolymarketCLOBURL  string
	OpenMeteoURL       string
	ForecastDays       int
	HTTPTimeout        time.Duration
	ResolvedPath       string
}

func LoadConfig(path string) (Config, error) {
	cfg := Config{
		PollInterval:       5 * time.Minute,
		EdgeThreshold:      0.08,
		KellyFraction:      0.15,
		MaxTradeUSD:        100,
		StartingBankroll:   10000,
		SimulationMode:     true,
		MaxSpreadPct:       0.05,
		MinDepthUSD:        200,
		DashboardAddr:      ":8088",
		KalshiBaseURL:      "https://api.kalshi.com/trade-api/v2",
		PolymarketGammaURL: "https://gamma-api.polymarket.com",
		PolymarketCLOBURL:  "https://clob.polymarket.com",
		OpenMeteoURL:       "https://ensemble-api.open-meteo.com/v1/ensemble",
		ForecastDays:       10,
		HTTPTimeout:        10 * time.Second,
		ResolvedPath:       "resolved.jsonl",
	}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "POLL_INTERVAL":
			d, err := time.ParseDuration(value)
			if err != nil {
				return cfg, fmt.Errorf("invalid POLL_INTERVAL: %w", err)
			}
			cfg.PollInterval = d
		case "EDGE_THRESHOLD":
			cfg.EdgeThreshold, err = strconv.ParseFloat(value, 64)
			if err != nil {
				return cfg, fmt.Errorf("invalid EDGE_THRESHOLD: %w", err)
			}
		case "KELLY_FRACTION":
			cfg.KellyFraction, err = strconv.ParseFloat(value, 64)
			if err != nil {
				return cfg, fmt.Errorf("invalid KELLY_FRACTION: %w", err)
			}
		case "MAX_TRADE_USD":
			cfg.MaxTradeUSD, err = strconv.ParseFloat(value, 64)
			if err != nil {
				return cfg, fmt.Errorf("invalid MAX_TRADE_USD: %w", err)
			}
		case "MAX_SPREAD_PCT":
			cfg.MaxSpreadPct, err = strconv.ParseFloat(value, 64)
			if err != nil {
				return cfg, fmt.Errorf("invalid MAX_SPREAD_PCT: %w", err)
			}
		case "MIN_DEPTH_USD":
			cfg.MinDepthUSD, err = strconv.ParseFloat(value, 64)
			if err != nil {
				return cfg, fmt.Errorf("invalid MIN_DEPTH_USD: %w", err)
			}
		case "STARTING_BANKROLL":
			cfg.StartingBankroll, err = strconv.ParseFloat(value, 64)
			if err != nil {
				return cfg, fmt.Errorf("invalid STARTING_BANKROLL: %w", err)
			}
		case "HTTP_TIMEOUT":
			cfg.HTTPTimeout, err = time.ParseDuration(value)
			if err != nil {
				return cfg, fmt.Errorf("invalid HTTP_TIMEOUT: %w", err)
			}
		case "SIMULATION_MODE":
			cfg.SimulationMode = value != "false" && value != "0"
		case "DASHBOARD_ADDR":
			cfg.DashboardAddr = value
		case "KALSHI_AUTH_TOKEN":
			cfg.KalshiAuthToken = value
		case "FORECAST_DAYS":
			cfg.ForecastDays, err = strconv.Atoi(value)
			if err != nil {
				return cfg, fmt.Errorf("invalid FORECAST_DAYS: %w", err)
			}
		case "RESOLVED_PATH":
			cfg.ResolvedPath = value
		}
	}
	if err := scanner.Err(); err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	return cfg, nil
}
