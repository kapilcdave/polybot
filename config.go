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
		ArbThreshold: ArbThreshold,
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
		case "ARB_THRESHOLD":
			parsed, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil {
				return cfg, fmt.Errorf("invalid ARB_THRESHOLD %q: %w", value, parseErr)
			}
			cfg.ArbThreshold = parsed
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

	activeArbThreshold = cfg.ArbThreshold
	return cfg, nil
}
