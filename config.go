package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func LoadConfig(path string) (Config, error) {
	cfg := Config{
		ArbThreshold:  ArbThreshold,
		MinEdgePct:    1 - ArbThreshold,
		KalshiFeeRate: KalshiFeePct,
		PolyFeeFlat:   PolyFeeFlat,
		MaxOrderUSDC:  500,
		MaxDailyLoss:  200,
		DryRun:        true,
		LogPath:       "arbbot.log",
		JournalPath:   "arb_orders.jsonl",
	}

	file, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("load config: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)

		switch key {
		case "KALSHI_API_KEY_ID":
			cfg.KalshiAPIKeyID = value
		case "KALSHI_PRIVATE_KEY_PATH":
			cfg.KalshiPrivateKeyPath = value
		case "POLY_API_KEY":
			cfg.PolyAPIKey = value
		case "POLY_API_KEY_ID":
			cfg.PolyAPIKeyID = value
		case "POLY_API_SECRET":
			cfg.PolyAPISecret = value
		case "POLY_PRIVATE_KEY":
			cfg.PolyPrivateKey = value
		case "ARB_THRESHOLD":
			parsed, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil {
				return cfg, fmt.Errorf("invalid ARB_THRESHOLD %q: %w", value, parseErr)
			}
			cfg.ArbThreshold = parsed
			cfg.MinEdgePct = 1 - parsed
		case "MIN_EDGE_PCT":
			parsed, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil {
				return cfg, fmt.Errorf("invalid MIN_EDGE_PCT %q: %w", value, parseErr)
			}
			cfg.MinEdgePct = parsed / 100
			cfg.ArbThreshold = 1 - cfg.MinEdgePct
		case "KALSHI_FEE_RATE":
			parsed, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil {
				return cfg, fmt.Errorf("invalid KALSHI_FEE_RATE %q: %w", value, parseErr)
			}
			cfg.KalshiFeeRate = parsed
		case "POLY_FEE_FLAT":
			parsed, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil {
				return cfg, fmt.Errorf("invalid POLY_FEE_FLAT %q: %w", value, parseErr)
			}
			cfg.PolyFeeFlat = parsed
		case "MAX_ORDER_USDC":
			parsed, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil {
				return cfg, fmt.Errorf("invalid MAX_ORDER_USDC %q: %w", value, parseErr)
			}
			cfg.MaxOrderUSDC = parsed
		case "MAX_DAILY_LOSS":
			parsed, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil {
				return cfg, fmt.Errorf("invalid MAX_DAILY_LOSS %q: %w", value, parseErr)
			}
			cfg.MaxDailyLoss = parsed
		case "DRY_RUN":
			cfg.DryRun = value != "false" && value != "0"
		case "LOG_PATH":
			if value != "" {
				cfg.LogPath = value
			}
		case "JOURNAL_PATH":
			if value != "" {
				cfg.JournalPath = value
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	if cfg.KalshiAPIKeyID == "" {
		return cfg, fmt.Errorf("missing KALSHI_API_KEY_ID in .env")
	}
	if cfg.KalshiPrivateKeyPath == "" {
		return cfg, fmt.Errorf("missing KALSHI_PRIVATE_KEY_PATH in .env")
	}
	if !cfg.DryRun {
		return cfg, fmt.Errorf("DRY_RUN=false is not supported yet; live execution has not been verified")
	}

	activeArbThreshold = cfg.ArbThreshold
	activeKalshiFeePct = cfg.KalshiFeeRate
	activePolyFeeFlat = cfg.PolyFeeFlat
	return cfg, nil
}
