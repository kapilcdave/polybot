package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type AppConfig struct {
	BinanceAPIKey         string
	BinanceAPISecret      string
	KalshiAPIKey          string
	KalshiAPISecretPath   string
	PolymarketAPIKey      string
	Symbols               []string
	FeeThreshold          float64
	PolymarketFeeBps      float64
	DryRun                bool
	LogLevel              string
	KalshiBaseURL         string
	KalshiWSURL           string
	PolymarketGammaURL    string
	PolymarketMarketWSURL string
	PolymarketEnableWS    bool
	BinanceWSURL          string
	BinanceRESTURL        string
	PollInterval          time.Duration
	MaxSignalAge          time.Duration
	DefaultOrderSize      float64
	KalshiTickerHints     map[string][]string
}

func LoadFromEnv() (AppConfig, error) {
	loadDotEnvIfPresent()

	cfg := AppConfig{
		BinanceAPIKey:         os.Getenv("BINANCE_API_KEY"),
		BinanceAPISecret:      os.Getenv("BINANCE_API_SECRET"),
		KalshiAPIKey:          os.Getenv("KALSHI_API_KEY"),
		KalshiAPISecretPath:   os.Getenv("KALSHI_API_SECRET_PATH"),
		PolymarketAPIKey:      os.Getenv("POLYMARKET_API_KEY"),
		Symbols:               splitCSV(defaultString("SYMBOLS", "BTC,ETH,SOL,XRP")),
		FeeThreshold:          defaultFloat("FEE_THRESHOLD", 0.03),
		PolymarketFeeBps:      defaultFloat("POLYMARKET_FEE_BPS", 70),
		DryRun:                defaultBool("DRY_RUN", true),
		LogLevel:              defaultString("LOG_LEVEL", "INFO"),
		KalshiBaseURL:         defaultString("KALSHI_BASE_URL", "https://api.elections.kalshi.com/trade-api/v2"),
		KalshiWSURL:           defaultString("KALSHI_WS_URL", "wss://api.elections.kalshi.com/trade-api/ws/v2"),
		PolymarketGammaURL:    defaultString("POLYMARKET_GAMMA_URL", "https://gamma-api.polymarket.com"),
		PolymarketMarketWSURL: defaultString("POLYMARKET_MARKET_WS_URL", "wss://ws-subscriptions-clob.polymarket.com/ws/market"),
		PolymarketEnableWS:    defaultBool("POLYMARKET_ENABLE_WS", false),
		BinanceWSURL:          defaultString("BINANCE_WS_URL", "wss://stream.binance.com:9443/stream"),
		BinanceRESTURL:        defaultString("BINANCE_REST_URL", "https://api.binance.com/api/v3"),
		PollInterval:          defaultDuration("POLYMARKET_POLL_INTERVAL", 2*time.Second),
		MaxSignalAge:          defaultDuration("MAX_SIGNAL_AGE", 500*time.Millisecond),
		DefaultOrderSize:      defaultFloat("DEFAULT_ORDER_SIZE", 1),
		KalshiTickerHints: map[string][]string{
			"btc": splitCSV(defaultString("KALSHI_TICKER_HINTS_BTC", "KXBTCD,KXBTC,BTC")),
			"eth": splitCSV(defaultString("KALSHI_TICKER_HINTS_ETH", "KXETHD,KXETH,ETH")),
			"sol": splitCSV(defaultString("KALSHI_TICKER_HINTS_SOL", "KXSOLD,KXSOL,SOL")),
			"xrp": splitCSV(defaultString("KALSHI_TICKER_HINTS_XRP", "KXXRPD,KXXRP,XRP")),
		},
	}

	if cfg.KalshiAPIKey != "" && cfg.KalshiAPISecretPath == "" {
		return AppConfig{}, fmt.Errorf("KALSHI_API_SECRET_PATH is required when KALSHI_API_KEY is set")
	}

	return cfg, nil
}

func loadDotEnvIfPresent() {
	for _, path := range []string{".env", filepath.Join("..", ".env"), "env", filepath.Join("..", "env")} {
		if err := loadEnvFile(path); err == nil {
			return
		}
	}
}

func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}

	return scanner.Err()
}

func defaultString(key, value string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return value
}

func defaultBool(key string, value bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return value
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return value
	}
	return parsed
}

func defaultFloat(key string, value float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return value
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return value
	}
	return parsed
}

func defaultDuration(key string, value time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return value
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return value
	}
	return parsed
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if p := strings.ToUpper(strings.TrimSpace(part)); p != "" {
			out = append(out, p)
		}
	}
	return out
}
