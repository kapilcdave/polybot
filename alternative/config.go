package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	// Kalshi
	KalshiAPIKey    string
	KalshiBaseURL   string
	KalshiWSURL     string

	// Polymarket
	PolyAPIKey      string // your CLOB API key
	PolyPrivateKey  string // wallet private key for signing
	PolyBaseURL     string
	PolyWSURL       string

	// Execution thresholds
	MinEdgePct      float64 // minimum arb edge after fees, e.g. 0.025 = 2.5%
	MaxOrderUSDC    float64 // max dollars per arb leg
	KalshiFeeRate   float64 // Kalshi takes ~7% of profit
	PolyFeeRate     float64 // Polymarket taker fee ~0%

	// Safety
	MaxDailyLoss    float64 // halt trading if realized loss exceeds this
	MaxOpenLegs     int     // max number of unhedged open legs at once
	DBPath          string  // sqlite path for order journal
	DryRun          bool    // if true, log orders but don't send
}

func Load() (*Config, error) {
	c := &Config{
		KalshiBaseURL:  "https://api.elections.kalshi.com/trade-api/v2",
		KalshiWSURL:    "wss://api.elections.kalshi.com/trade-api/v2/ws/v2",
		PolyBaseURL:    "https://clob.polymarket.com",
		PolyWSURL:      "wss://ws-subscriptions-clob.polymarket.com/ws/market",

		MinEdgePct:     0.025,
		MaxOrderUSDC:   500.0,
		KalshiFeeRate:  0.07,
		PolyFeeRate:    0.0,
		MaxDailyLoss:   200.0,
		MaxOpenLegs:    4,
		DBPath:         "arbbot.db",
		DryRun:         true, // SAFE DEFAULT — must explicitly set DRY_RUN=false
	}

	c.KalshiAPIKey = mustEnv("KALSHI_API_KEY")
	c.PolyAPIKey   = mustEnv("POLY_API_KEY")
	c.PolyPrivateKey = mustEnv("POLY_PRIVATE_KEY")

	if v := os.Getenv("MIN_EDGE_PCT"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid MIN_EDGE_PCT: %w", err)
		}
		c.MinEdgePct = f / 100.0
	}
	if v := os.Getenv("MAX_ORDER_USDC"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX_ORDER_USDC: %w", err)
		}
		c.MaxOrderUSDC = f
	}
	if v := os.Getenv("MAX_DAILY_LOSS"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX_DAILY_LOSS: %w", err)
		}
		c.MaxDailyLoss = f
	}
	if v := os.Getenv("DRY_RUN"); v == "false" || v == "0" {
		c.DryRun = false
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		c.DBPath = v
	}

	return c, nil
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	// Don't fatal on missing keys — let caller decide. Return empty string.
	return v
}
