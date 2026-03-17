package matcher

import (
	"context"
	"log/slog"
	"time"

	"github.com/you/arbbot/config"
	"github.com/you/arbbot/feed"
)

// ArbOpportunity is a confirmed arb with both legs priced and edge calculated.
type ArbOpportunity struct {
	Key string

	KalshiMarket *feed.Market
	PolyMarket   *feed.Market

	// Which side to buy on each platform
	// If KalshiYes is true, buy YES on Kalshi, NO on Polymarket (or vice versa)
	BuyKalshiYes bool
	BuyPolyYes   bool

	// Prices at the moment of detection
	KalshiPrice float64 // price we'd pay
	PolyPrice   float64 // price we'd pay

	// Edge after fees
	RawEdge  float64 // 1 - (KalshiPrice + PolyPrice) before fees
	NetEdge  float64 // after platform fees
	DetectedAt time.Time
}

// Matcher scans the PriceMap for arb opportunities and sends them on Opps.
type Matcher struct {
	pm     *feed.PriceMap
	cfg    *config.Config
	log    *slog.Logger
	Opps   chan ArbOpportunity
	seen   map[string]time.Time // key -> last time we sent this opp (cooldown)
}

const oppCooldown = 10 * time.Second

func New(pm *feed.PriceMap, cfg *config.Config, log *slog.Logger) *Matcher {
	return &Matcher{
		pm:   pm,
		cfg:  cfg,
		log:  log,
		Opps: make(chan ArbOpportunity, 64),
		seen: make(map[string]time.Time),
	}
}

// Run scans for arbs every 100ms. This is the hot loop.
// At 100ms latency you won't beat HFT, but for prediction market arb
// (which is sticky for seconds/minutes), this is more than fast enough.
func (m *Matcher) Run(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.scan()
		}
	}
}

func (m *Matcher) scan() {
	pairs := m.pm.AllPairs()
	for _, pair := range pairs {
		km := pair[0] // kalshi
		pm := pair[1] // polymarket

		if km == nil || pm == nil {
			continue
		}

		// Skip stale prices — don't trade on outdated data
		if km.Stale || pm.Stale {
			continue
		}

		// Skip if either price hasn't been updated recently
		if time.Since(km.UpdatedAt) > 60*time.Second || time.Since(pm.UpdatedAt) > 60*time.Second {
			continue
		}

		// Skip extremely illiquid markets
		if km.Volume < 1000 || pm.Volume < 1000 {
			continue
		}

		// Check both arb directions:
		// Direction A: Buy YES on Kalshi + Buy NO on Polymarket (i.e., bet against YES on Poly)
		//   We pay: km.YesPrice + (1 - pm.YesPrice) = km.YesPrice + pm.NoPrice
		//   Edge if that sum < 1.0
		// Direction B: Buy NO on Kalshi + Buy YES on Polymarket
		//   We pay: km.NoPrice + pm.YesPrice
		//   Edge if that sum < 1.0

		dirA_cost := km.YesPrice + (1.0 - pm.YesPrice)
		dirB_cost := km.NoPrice + pm.YesPrice

		for dir, cost := range map[string]float64{"A": dirA_cost, "B": dirB_cost} {
			rawEdge := 1.0 - cost
			if rawEdge <= 0 {
				continue
			}

			// Subtract fees from edge
			// Kalshi fee: ~7% of profit on the winning leg
			// Approximation: winning leg pays out 1.0, we keep (1 - feeRate) of profit
			kFeeImpact := m.cfg.KalshiFeeRate * rawEdge * 0.5  // rough half-leg estimate
			pFeeImpact := m.cfg.PolyFeeRate * rawEdge * 0.5
			netEdge := rawEdge - kFeeImpact - pFeeImpact

			if netEdge < m.cfg.MinEdgePct {
				continue
			}

			// Cooldown: don't spam the same opp
			cooldownKey := km.Key + ":" + dir
			if last, ok := m.seen[cooldownKey]; ok && time.Since(last) < oppCooldown {
				continue
			}
			m.seen[cooldownKey] = time.Now()

			opp := ArbOpportunity{
				Key:          km.Key,
				KalshiMarket: km,
				PolyMarket:   pm,
				BuyKalshiYes: dir == "A",
				BuyPolyYes:   dir == "B",
				KalshiPrice:  func() float64 {
					if dir == "A" { return km.YesPrice }
					return km.NoPrice
				}(),
				PolyPrice: func() float64 {
					if dir == "A" { return 1.0 - pm.YesPrice }
					return pm.YesPrice
				}(),
				RawEdge:    rawEdge,
				NetEdge:    netEdge,
				DetectedAt: time.Now(),
			}

			m.log.Info("arb detected",
				"key", opp.Key,
				"dir", dir,
				"kalshi_price", opp.KalshiPrice,
				"poly_price", opp.PolyPrice,
				"net_edge_pct", netEdge*100,
			)

			select {
			case m.Opps <- opp:
			default:
				m.log.Warn("opp channel full, dropping", "key", opp.Key)
			}
		}
	}
}
