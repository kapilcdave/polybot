package signal

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"time"

	"crybot/internal/state"
)

type Signal struct {
	Crypto         string
	WindowStart    int64
	KalshiTicker   string
	Side           string
	ExpectedEdge   float64
	SignalStrength float64
	Timestamp      time.Time
	ReferencePrice float64
}

type Worker struct {
	feeThreshold     float64
	polymarketFeeBps float64
	maxSignalAge     time.Duration
	logger           *slog.Logger
}

func NewWorker(feeThreshold, polymarketFeeBps float64, maxSignalAge time.Duration, logger *slog.Logger) *Worker {
	return &Worker{
		feeThreshold:     feeThreshold,
		polymarketFeeBps: polymarketFeeBps,
		maxSignalAge:     maxSignalAge,
		logger:           logger,
	}
}

func (w *Worker) Run(ctx context.Context, in <-chan state.EventWindow, out chan<- Signal) {
	defer close(out)
	for {
		select {
		case <-ctx.Done():
			return
		case window, ok := <-in:
			if !ok {
				return
			}
			sig, ok := w.Evaluate(window)
			if !ok {
				continue
			}
			select {
			case out <- sig:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (w *Worker) Evaluate(window state.EventWindow) (Signal, bool) {
	now := time.Now().UTC()
	if window.TimeRemaining <= 30 {
		return Signal{}, false
	}
	if window.KalshiTicker == "" || window.BinanceSpotPrice == 0 || window.PolymarketSlug == "" {
		return Signal{}, false
	}
	if stale(now, window.BinanceLastUpdate, w.maxSignalAge) ||
		stale(now, window.KalshiLastUpdate, w.maxSignalAge) ||
		stale(now, window.PolymarketLastUpdate, w.maxSignalAge) {
		return Signal{}, false
	}
	if !window.WindowOpenObserved || window.WindowOpenSpotPrice == 0 {
		return Signal{}, false
	}

	spotMove := (window.BinanceSpotPrice - window.WindowOpenSpotPrice) / window.WindowOpenSpotPrice
	direction := "down"
	if spotMove > 0 {
		direction = "up"
	}

	kalshiProb := chooseDirectionalPrice(direction, window.KalshiYesPrice, window.KalshiNoPrice)
	polyProb := chooseDirectionalPrice(direction, window.PolymarketUpPrice, window.PolymarketDownPrice)
	if kalshiProb <= 0 || polyProb <= 0 {
		return Signal{}, false
	}

	spotConfidence := math.Min(1, math.Abs(spotMove)*40)
	lagGap := polyProb - kalshiProb
	feeImpact := math.Max(window.FeesOnPolymarket, w.polymarketFeeBps/10000)
	slippage := 0.02

	var edge float64
	var side string
	switch {
	case lagGap >= 0.05:
		edge = lagGap - feeImpact - slippage
		side = kalshiSide(direction)
	case lagGap <= -0.05:
		edge = math.Abs(lagGap) - feeImpact - slippage
		side = kalshiSide(direction)
	default:
		return Signal{}, false
	}

	if edge < w.feeThreshold {
		return Signal{}, false
	}

	strength := math.Min(1, edge*8+spotConfidence*0.5)
	sig := Signal{
		Crypto:         strings.ToLower(window.Crypto),
		WindowStart:    window.WindowStart,
		KalshiTicker:   window.KalshiTicker,
		Side:           side,
		ExpectedEdge:   edge,
		SignalStrength: strength,
		Timestamp:      now,
		ReferencePrice: kalshiProb,
	}

	w.logger.Debug("signal emitted",
		slog.String("crypto", sig.Crypto),
		slog.Int64("window_start", sig.WindowStart),
		slog.String("side", sig.Side),
		slog.Float64("edge", sig.ExpectedEdge),
		slog.Float64("strength", sig.SignalStrength),
	)

	return sig, true
}

func stale(now, updated time.Time, maxAge time.Duration) bool {
	if updated.IsZero() {
		return true
	}
	return now.Sub(updated) > maxAge
}

func chooseDirectionalPrice(direction string, up, down float64) float64 {
	if direction == "up" {
		return up
	}
	return down
}

func kalshiSide(direction string) string {
	if direction == "up" {
		return "KalshiUp"
	}
	return "KalshiDown"
}
