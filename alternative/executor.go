package executor

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/you/arbbot/config"
	"github.com/you/arbbot/matcher"
)

// Executor receives arb opportunities and executes two-legged orders
// with full state machine persistence and circuit breakers.
type Executor struct {
	cfg       *config.Config
	journal   *Journal
	kalshi    *KalshiOrderClient
	poly      *PolyOrderClient
	log       *slog.Logger

	// Circuit breakers
	halted    atomic.Bool
	openLegs  atomic.Int32 // count of positions with only 1 leg filled

	// Stats for display
	Stats     ExecutorStats
}

type ExecutorStats struct {
	TotalOpps    int64
	Executed     int64
	Skipped      int64
	Leg1Failures int64
	Leg2Failures int64 // DANGEROUS — open position
	Completed    int64
	TodayPnL     float64
}

func New(
	cfg *config.Config,
	journal *Journal,
	kalshi *KalshiOrderClient,
	poly *PolyOrderClient,
	log *slog.Logger,
) *Executor {
	return &Executor{
		cfg:     cfg,
		journal: journal,
		kalshi:  kalshi,
		poly:    poly,
		log:     log,
	}
}

// RecoverOpenPositions checks for orders that died mid-execution and alerts.
// Call this on startup before processing new opportunities.
func (e *Executor) RecoverOpenPositions(ctx context.Context) error {
	open, err := e.journal.OpenPositions()
	if err != nil {
		return fmt.Errorf("recovery check failed: %w", err)
	}

	if len(open) == 0 {
		e.log.Info("recovery: no open positions found")
		return nil
	}

	for _, order := range open {
		e.log.Error("OPEN POSITION DETECTED — manual review required",
			"order_id", order.ID,
			"arb_key", order.ArbKey,
			"state", order.State,
			"kalshi_order_id", order.KalshiOrderID,
			"kalshi_fill", order.KalshiFillPrice,
		)

		// If leg1 is filled but leg2 hasn't been sent yet, try to send leg2 now
		if order.State == StateLeg1Filled {
			e.log.Warn("attempting leg2 recovery", "order_id", order.ID)
			go e.executeLeg2(ctx, order)
		}

		// LEG2_SENT = we sent it but don't know if it filled. This requires
		// manual checking of the Polymarket order ID.
		if order.State == StateLeg2Sent {
			e.log.Error("LEG2 STATUS UNKNOWN — check Polymarket manually",
				"poly_order_id", order.PolyOrderID,
				"order_id", order.ID,
			)
		}
	}

	return nil
}

// Run consumes arb opportunities from the matcher and executes them.
func (e *Executor) Run(ctx context.Context, opps <-chan matcher.ArbOpportunity) {
	for {
		select {
		case <-ctx.Done():
			return
		case opp := <-opps:
			atomic.AddInt64(&e.Stats.TotalOpps, 1)

			if skip, reason := e.shouldSkip(opp); skip {
				e.log.Debug("skipping opp", "key", opp.Key, "reason", reason)
				atomic.AddInt64(&e.Stats.Skipped, 1)
				continue
			}

			// Execute in goroutine so we don't block the opp channel
			go e.execute(ctx, opp)
		}
	}
}

func (e *Executor) shouldSkip(opp matcher.ArbOpportunity) (bool, string) {
	if e.halted.Load() {
		return true, "circuit breaker halted"
	}
	if int(e.openLegs.Load()) >= e.cfg.MaxOpenLegs {
		return true, fmt.Sprintf("too many open legs (%d)", e.openLegs.Load())
	}
	// Stale check: if opp was detected >2s ago, prices may have moved
	if time.Since(opp.DetectedAt) > 2*time.Second {
		return true, "opp too old"
	}
	return false, ""
}

// execute runs the full two-leg order sequence with state machine persistence.
// Every state transition is written to SQLite before the corresponding HTTP call.
func (e *Executor) execute(ctx context.Context, opp matcher.ArbOpportunity) {
	// Calculate order sizes
	// Kalshi: contracts = floor(MaxOrderUSDC / yesPrice / 0.01)
	// Each contract has $0.01 face value, price in cents
	contracts := int(math.Floor(e.cfg.MaxOrderUSDC / opp.KalshiPrice / 0.01))
	if contracts < 1 {
		contracts = 1
	}
	polySize := e.cfg.MaxOrderUSDC // USDC to spend on Polymarket leg

	// Create the order record BEFORE any HTTP calls
	order := &ArbOrder{
		ID:              uuid.New().String(),
		ArbKey:          opp.Key,
		State:           StatePending,
		DryRun:          e.cfg.DryRun,
		KalshiSide:      sideStr(opp.BuyKalshiYes),
		KalshiTicker:    opp.KalshiMarket.ID,
		KalshiPrice:     opp.KalshiPrice,
		KalshiCount:     contracts,
		PolySide:        sideStr(opp.BuyPolyYes),
		PolyConditionID: opp.PolyMarket.ID,
		PolyPrice:       opp.PolyPrice,
		PolySize:        polySize,
	}

	if err := e.journal.Insert(order); err != nil {
		e.log.Error("failed to journal order — ABORTING", "err", err, "key", opp.Key)
		return
	}

	e.log.Info("executing arb",
		"order_id", order.ID,
		"key", opp.Key,
		"kalshi_side", order.KalshiSide,
		"kalshi_price", order.KalshiPrice,
		"poly_side", order.PolySide,
		"poly_price", order.PolyPrice,
		"net_edge_pct", opp.NetEdge*100,
		"dry_run", order.DryRun,
	)

	// ── LEG 1: Kalshi ─────────────────────────────────────────────────────────

	if err := e.journal.Transition(order.ID, StateLeg1Sent, nil); err != nil {
		e.log.Error("journal transition failed", "state", StateLeg1Sent, "err", err)
		return
	}

	kalshiOrderID, kalshiFillPrice, err := e.kalshi.PlaceOrder(
		ctx,
		order.ID+"_k1",
		order.KalshiTicker,
		order.KalshiSide,
		order.KalshiPrice,
		order.KalshiCount,
		order.DryRun,
	)

	if err != nil {
		e.log.Error("LEG1 FAILED (kalshi)", "order_id", order.ID, "err", err)
		atomic.AddInt64(&e.Stats.Leg1Failures, 1)
		_ = e.journal.Transition(order.ID, StateLeg1Failed, func(o *ArbOrder) {
			o.FailureReason = err.Error()
		})
		return // Clean failure — no position opened
	}

	e.openLegs.Add(1)
	defer e.openLegs.Add(-1)

	_ = e.journal.Transition(order.ID, StateLeg1Filled, func(o *ArbOrder) {
		o.KalshiOrderID = kalshiOrderID
		o.KalshiFillPrice = kalshiFillPrice
	})

	e.log.Info("LEG1 FILLED",
		"order_id", order.ID,
		"kalshi_order_id", kalshiOrderID,
		"fill_price", kalshiFillPrice,
	)

	// ── LEG 2: Polymarket ─────────────────────────────────────────────────────

	order.KalshiOrderID = kalshiOrderID
	order.KalshiFillPrice = kalshiFillPrice
	e.executeLeg2(ctx, order)
}

// executeLeg2 is separated so it can be called from recovery as well.
func (e *Executor) executeLeg2(ctx context.Context, order *ArbOrder) {
	if err := e.journal.Transition(order.ID, StateLeg2Sent, nil); err != nil {
		e.log.Error("journal transition failed", "state", StateLeg2Sent, "err", err)
		// Don't return — still try to place the order. We just lost idempotency
		// for this exact transition, but the order ID provides it at the exchange level.
	}

	// Add a small delay to allow Polymarket price to settle
	time.Sleep(50 * time.Millisecond)

	polyOrderID, polyFillPrice, err := e.poly.PlaceOrder(
		ctx,
		order.ID+"_p2",
		order.PolyConditionID, // in real impl this is the CLOB token ID
		order.PolySide,
		order.PolyPrice,
		order.PolySize,
		order.DryRun,
	)

	if err != nil {
		// ⚠️  LEG2 FAILURE = OPEN POSITION ⚠️
		// We hold a Kalshi position with no hedge. Log loudly.
		e.log.Error("⚠️  LEG2 FAILED — OPEN POSITION",
			"order_id", order.ID,
			"arb_key", order.ArbKey,
			"kalshi_order_id", order.KalshiOrderID,
			"kalshi_side", order.KalshiSide,
			"err", err,
		)
		atomic.AddInt64(&e.Stats.Leg2Failures, 1)
		_ = e.journal.Transition(order.ID, StateLeg2Failed, func(o *ArbOrder) {
			o.FailureReason = "LEG2: " + err.Error()
		})

		// Trigger circuit breaker — halt all trading until reviewed
		e.halt("leg2 failure: " + err.Error())
		return
	}

	// ── Both legs filled ──────────────────────────────────────────────────────

	// Calculate P&L
	// We paid: kalshiFillPrice + polyFillPrice per dollar of payout
	// Payout: $1.00 per contract (one side wins)
	// PnL ≈ (1 - kalshiFillPrice - polyFillPrice) * size - fees
	pnl := (1.0 - order.KalshiFillPrice - polyFillPrice) * order.PolySize
	// Subtract Kalshi fee (applied on winning leg profit)
	kalshiFee := pnl * e.cfg.KalshiFeeRate / 2.0
	pnl -= kalshiFee

	_ = e.journal.Transition(order.ID, StateComplete, func(o *ArbOrder) {
		o.PolyOrderID = polyOrderID
		o.PolyFillPrice = polyFillPrice
		o.RealizedPnL = pnl
	})

	atomic.AddInt64(&e.Stats.Completed, 1)
	e.log.Info("✅ ARB COMPLETE",
		"order_id", order.ID,
		"key", order.ArbKey,
		"kalshi_fill", order.KalshiFillPrice,
		"poly_fill", polyFillPrice,
		"pnl_usd", pnl,
	)

	// Update today's P&L cache
	if pnl, err := e.journal.TodayPnL(); err == nil {
		e.Stats.TodayPnL = pnl
	}

	// Check daily loss limit
	if e.Stats.TodayPnL < -e.cfg.MaxDailyLoss {
		e.halt(fmt.Sprintf("daily loss limit exceeded: %.2f", e.Stats.TodayPnL))
	}
}

func (e *Executor) halt(reason string) {
	if e.halted.CompareAndSwap(false, true) {
		e.log.Error("🛑 CIRCUIT BREAKER TRIPPED", "reason", reason)
	}
}

func (e *Executor) IsHalted() bool {
	return e.halted.Load()
}

func (e *Executor) Resume() {
	e.halted.Store(false)
	e.log.Info("circuit breaker reset, resuming")
}

func sideStr(buyYes bool) string {
	if buyYes { return "yes" }
	return "no"
}
