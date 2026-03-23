package main

import (
	"math"
	"sort"
	"sync"
)

type Simulator struct {
	mu        sync.Mutex
	bankroll  float64
	cash      float64
	positions map[string]Position
	trades    int
}

func NewSimulator(startingBankroll float64) *Simulator {
	return &Simulator{
		bankroll:  startingBankroll,
		cash:      startingBankroll,
		positions: make(map[string]Position),
	}
}

func (s *Simulator) MaybeEnter(op Opportunity) {
	if op.StakeUSD <= 0 || op.Contracts <= 0 {
		return
	}
	key := marketKey(op.Market, op.Side)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.positions[key]; exists {
		return
	}
	if op.StakeUSD > s.cash {
		return
	}
	s.cash -= op.StakeUSD
	s.positions[key] = Position{
		Key:        key,
		Market:     op.Market,
		Side:       op.Side,
		EntryPrice: op.Ask,
		StakeUSD:   op.StakeUSD,
		Contracts:  op.Contracts,
		FairAtOpen: op.Fair,
		OpenedAt:   op.GeneratedAt,
	}
	s.trades++
}

func (s *Simulator) Snapshot(result ScanResult) SimulationSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	positions := make([]Position, 0, len(s.positions))
	equity := s.cash
	fairByKey := make(map[string]float64, len(result.Opps))
	for _, opp := range result.Opps {
		fairByKey[marketKey(opp.Market, opp.Side)] = opp.Fair
	}
	for _, pos := range s.positions {
		fair := pos.FairAtOpen
		if current, ok := fairByKey[pos.Key]; ok {
			fair = current
		}
		equity += fair * pos.Contracts
		positions = append(positions, pos)
	}
	sort.Slice(positions, func(i, j int) bool { return positions[i].OpenedAt.After(positions[j].OpenedAt) })
	return SimulationSnapshot{
		BankrollUSD: s.bankroll,
		CashUSD:     math.Round(s.cash*100) / 100,
		EquityUSD:   math.Round(equity*100) / 100,
		Positions:   positions,
		Trades:      s.trades,
	}
}
