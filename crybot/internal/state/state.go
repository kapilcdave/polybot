package state

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

type EventKey struct {
	Crypto      string
	WindowStart int64
}

type EventWindow struct {
	Crypto               string
	WindowStart          int64
	WindowEnd            int64
	KalshiTicker         string
	KalshiYesPrice       float64
	KalshiNoPrice        float64
	PolymarketMarketID   string
	PolymarketSlug       string
	PolymarketAssetIDs   []string
	PolymarketUpPrice    float64
	PolymarketDownPrice  float64
	BinanceSpotPrice     float64
	WindowOpenSpotPrice  float64
	WindowOpenObserved   bool
	BinanceMidTime       time.Time
	KalshiLastUpdate     time.Time
	PolymarketLastUpdate time.Time
	BinanceLastUpdate    time.Time
	TimeRemaining        float64
	FeesOnPolymarket     float64
	UpdatedAt            time.Time
}

type UpdateType string

const (
	UpdateBinance    UpdateType = "binance"
	UpdateKalshi     UpdateType = "kalshi"
	UpdatePolymarket UpdateType = "polymarket"
)

type Update struct {
	Type            UpdateType
	Crypto          string
	WindowStart     int64
	Timestamp       time.Time
	KalshiTicker    string
	KalshiYesPrice  float64
	KalshiNoPrice   float64
	PolymarketID    string
	PolymarketSlug  string
	PolymarketAsset []string
	PolyUpPrice     float64
	PolyDownPrice   float64
	PolyFeeImpact   float64
	SpotPrice       float64
	BinanceMidTime  time.Time
	WindowOpenPrice float64
	OpenObserved    bool
}

type Manager struct {
	logger *slog.Logger
}

func NewManager(logger *slog.Logger) *Manager {
	return &Manager{logger: logger}
}

func (m *Manager) Run(ctx context.Context, updates <-chan Update, out chan<- EventWindow) {
	defer close(out)
	windows := make(map[EventKey]EventWindow)

	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-updates:
			if !ok {
				return
			}

			key := EventKey{Crypto: strings.ToLower(update.Crypto), WindowStart: update.WindowStart}
			window := windows[key]
			if window.Crypto == "" {
				window = EventWindow{
					Crypto:      key.Crypto,
					WindowStart: key.WindowStart,
					WindowEnd:   key.WindowStart + 900,
				}
			}

			switch update.Type {
			case UpdateBinance:
				window.BinanceSpotPrice = update.SpotPrice
				if update.OpenObserved {
					window.WindowOpenSpotPrice = update.WindowOpenPrice
					window.WindowOpenObserved = true
				}
				window.BinanceMidTime = update.BinanceMidTime
				window.BinanceLastUpdate = update.Timestamp
			case UpdateKalshi:
				window.KalshiTicker = update.KalshiTicker
				window.KalshiYesPrice = update.KalshiYesPrice
				window.KalshiNoPrice = update.KalshiNoPrice
				window.KalshiLastUpdate = update.Timestamp
			case UpdatePolymarket:
				window.PolymarketMarketID = update.PolymarketID
				window.PolymarketSlug = update.PolymarketSlug
				window.PolymarketAssetIDs = update.PolymarketAsset
				window.PolymarketUpPrice = update.PolyUpPrice
				window.PolymarketDownPrice = update.PolyDownPrice
				window.FeesOnPolymarket = update.PolyFeeImpact
				window.PolymarketLastUpdate = update.Timestamp
			}

			now := time.Now().UTC()
			window.TimeRemaining = float64(window.WindowEnd - now.Unix())
			window.UpdatedAt = now
			windows[key] = window

			select {
			case out <- window:
			case <-ctx.Done():
				return
			}

			m.gc(now, windows)
		}
	}
}

func (m *Manager) gc(now time.Time, windows map[EventKey]EventWindow) {
	for key, window := range windows {
		if now.Unix() > window.WindowEnd+300 {
			delete(windows, key)
		}
	}
}

func WindowStart(ts time.Time) int64 {
	return (ts.UTC().Unix() / 900) * 900
}
