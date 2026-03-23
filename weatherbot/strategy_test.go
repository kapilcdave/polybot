package main

import (
	"testing"
	"time"
)

func TestParseThresholdF(t *testing.T) {
	v, ok := parseThresholdF("Will the high temperature in New York be 74° or above on Apr 3?")
	if !ok || v != 74 {
		t.Fatalf("expected 74F, got %v %t", v, ok)
	}
}

func TestSizeOpportunity(t *testing.T) {
	cfg := Config{StartingBankroll: 1000, KellyFraction: 0.15, MaxTradeUSD: 100}
	stake, contracts, ev := sizeOpportunity(cfg, 0.40, 0.55)
	if stake <= 0 || contracts <= 0 || ev <= 0 {
		t.Fatalf("expected positive sizing, got stake=%f contracts=%f ev=%f", stake, contracts, ev)
	}
}

func TestParseEventDate(t *testing.T) {
	got, ok := parseEventDate("Will Miami hit 90° on Jul 4?", time.Time{}, "America/New_York")
	if !ok {
		t.Fatal("expected date parse")
	}
	if got.Month() != time.July || got.Day() != 4 {
		t.Fatalf("unexpected date %s", got)
	}
}
