package main

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"
)

type ResolvedMarket struct {
	City       string    `json:"city"`
	EventDate  string    `json:"event_date"` // YYYY-MM-DD
	ThresholdF float64   `json:"threshold_f"`
	OutcomeYes bool      `json:"outcome_yes"`
	ResolvedAt time.Time `json:"resolved_at"`
}

// CalibrationStore holds historical outcomes for reliability adjustment.
type CalibrationStore struct {
	mu     sync.RWMutex
	byKey  map[string][]ResolvedMarket
	source string
}

func NewCalibrationStore(path string) *CalibrationStore {
	s := &CalibrationStore{byKey: make(map[string][]ResolvedMarket), source: path}
	s.Load()
	return s
}

func (c *CalibrationStore) Load() {
	if c.source == "" {
		return
	}
	file, err := os.Open(c.source)
	if err != nil {
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var r ResolvedMarket
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			continue
		}
		key := r.City + "|" + r.EventDate
		c.byKey[key] = append(c.byKey[key], r)
	}
}

// ReliabilityBeta returns alpha/beta pseudo-counts per city for Platt-like smoothing.
func (c *CalibrationStore) ReliabilityBeta(city string) (alpha, beta float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var wins, losses float64 = 1, 1 // Laplace smoothing
	for key, markets := range c.byKey {
		if !startsWithCity(key, city) {
			continue
		}
		for _, m := range markets {
			if m.OutcomeYes {
				wins++
			} else {
				losses++
			}
		}
	}
	return wins, losses
}

func startsWithCity(key, city string) bool {
	return len(key) >= len(city) && key[:len(city)] == city
}
