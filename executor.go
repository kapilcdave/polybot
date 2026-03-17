package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

type Executor struct {
	cfg     Config
	kalshi  *KalshiOrderClient
	poly    *PolyOrderClient
	journal *OrderJournal

	mu         sync.Mutex
	stats      ExecutorSnapshot
	lastByKey  map[string]time.Time
	halted     bool
	haltReason string
}

func NewExecutor(cfg Config, kalshi *KalshiOrderClient, poly *PolyOrderClient, journal *OrderJournal) *Executor {
	e := &Executor{
		cfg:       cfg,
		kalshi:    kalshi,
		poly:      poly,
		journal:   journal,
		lastByKey: make(map[string]time.Time),
	}
	e.stats.TodayPnL = journal.TodayPnL()
	openPositions := journal.OpenPositions()
	e.stats.OpenPositions = len(openPositions)
	switch {
	case len(openPositions) > 0:
		e.halted = true
		e.haltReason = "open positions in journal"
	case !cfg.DryRun:
		e.halted = true
		e.haltReason = "live execution disabled until Polymarket order builder is verified"
	}
	if e.halted {
		e.stats.Halted = true
		e.stats.Reason = e.haltReason
	}
	return e
}

func (e *Executor) Snapshot() ExecutorSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	snap := e.stats
	snap.Halted = e.halted
	snap.Reason = e.haltReason
	snap.TodayPnL = e.journal.TodayPnL()
	return snap
}

func (e *Executor) HandleOpportunity(ctx context.Context, arb ArbOpportunity) {
	e.mu.Lock()
	e.stats.TotalOpps++
	if e.halted {
		e.stats.Skipped++
		e.mu.Unlock()
		return
	}
	if time.Since(arb.SeenAt) > 2*time.Second {
		e.stats.Skipped++
		e.mu.Unlock()
		return
	}
	key := arbKey(arb)
	if last, ok := e.lastByKey[key]; ok && time.Since(last) < 10*time.Second {
		e.stats.Skipped++
		e.mu.Unlock()
		return
	}
	e.lastByKey[key] = time.Now()
	e.mu.Unlock()

	if !e.cfg.DryRun {
		e.mu.Lock()
		e.stats.Skipped++
		e.halted = true
		e.haltReason = "live execution disabled until Polymarket order builder is verified"
		e.stats.Halted = true
		e.stats.Reason = e.haltReason
		e.mu.Unlock()
		log.Printf("[executor] refusing live execution for %s: %s", arbKey(arb), e.haltReason)
		return
	}

	order := buildDryRunOrder(e.cfg, arb)
	if err := e.journal.Upsert(order); err != nil {
		log.Printf("[executor] journal insert failed: %v", err)
		return
	}

	e.transition(order, StateLeg1Sent, "")
	e.transition(order, StateLeg1Filled, "")
	e.transition(order, StateLeg2Sent, "")
	e.transition(order, StateComplete, "")

	e.mu.Lock()
	e.stats.Executed++
	e.stats.Completed++
	e.mu.Unlock()
	log.Printf("[executor] dry-run complete %s %s vs %s net=%0.4f", order.Direction, order.HomeTeam, order.AwayTeam, order.NetProfit)
}

func (e *Executor) transition(order ArbOrder, state OrderState, failure string) {
	order.State = state
	order.UpdatedAt = time.Now().UTC()
	if failure != "" {
		order.FailureReason = failure
	}
	if err := e.journal.Upsert(order); err != nil {
		log.Printf("[executor] journal transition failed state=%s id=%s err=%v", state, order.ID, err)
	}
}

func buildDryRunOrder(cfg Config, arb ArbOpportunity) ArbOrder {
	now := time.Now().UTC()
	kalshiSide, kalshiPrice := kalshiLeg(arb)
	polySide, polyPrice, polyTokenID := polyLeg(arb)
	return ArbOrder{
		ID:              fmt.Sprintf("%d-%s", now.UnixNano(), sanitizeKey(arbKey(arb))),
		ArbKey:          arbKey(arb),
		State:           StatePending,
		DryRun:          true,
		CreatedAt:       now,
		UpdatedAt:       now,
		League:          arb.Game.League,
		HomeTeam:        arb.Game.HomeTeam,
		AwayTeam:        arb.Game.AwayTeam,
		Direction:       arb.Direction,
		KalshiTicker:    arb.Game.Kalshi.MarketID,
		KalshiSide:      kalshiSide,
		KalshiPrice:     kalshiPrice,
		KalshiCount:     kalshiContracts(cfg.MaxOrderUSDC, kalshiPrice),
		KalshiOrderID:   "DRY_KALSHI",
		KalshiFillPrice: kalshiPrice,
		PolyMarketID:    arb.Game.Poly.MarketID,
		PolyTokenID:     polyTokenID,
		PolySide:        polySide,
		PolyPrice:       polyPrice,
		PolyStake:       cfg.MaxOrderUSDC,
		PolyOrderID:     "DRY_POLY",
		PolyFillPrice:   polyPrice,
		NetProfit:       arb.NetProfit,
	}
}

func kalshiLeg(arb ArbOpportunity) (string, float64) {
	if arb.BuyYesAt == "KALSHI" {
		return "yes", arb.YesPrice
	}
	return "no", arb.NoPrice
}

func polyLeg(arb ArbOpportunity) (string, float64, string) {
	if arb.BuyYesAt == "POLY" {
		return "yes", arb.YesPrice, arb.Game.Poly.ClobTokenIDs[0]
	}
	if arb.BuyNoAt == "POLY" {
		return "no", arb.NoPrice, arb.Game.Poly.ClobTokenIDs[1]
	}
	return "", 0, ""
}

func kalshiContracts(maxOrderUSDC, price float64) int {
	if maxOrderUSDC <= 0 || price <= 0 {
		return 0
	}
	contracts := int(maxOrderUSDC / (price * 0.01))
	if contracts < 1 {
		return 1
	}
	return contracts
}

func sanitizeKey(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r)
		case r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
