package main

import (
	"testing"
	"time"
)

func TestCheckUsesExecutableAsks(t *testing.T) {
	now := time.Now().UTC()
	game := MatchedGame{
		League:   "NBA",
		HomeTeam: "lakers",
		AwayTeam: "celtics",
		Kalshi: SportsMarket{
			Platform:  "KALSHI",
			League:    "NBA",
			HomeTeam:  "lakers",
			AwayTeam:  "celtics",
			YesTeam:   "lakers",
			YesBid:    0.10,
			NoBid:     0.10,
			YesAsk:    0.70,
			NoAsk:     0.71,
			UpdatedAt: now,
			GameTime:  now.Add(time.Hour),
		},
		Poly: SportsMarket{
			Platform:  "POLY",
			League:    "NBA",
			HomeTeam:  "lakers",
			AwayTeam:  "celtics",
			YesTeam:   "lakers",
			YesBid:    0.10,
			NoBid:     0.10,
			YesAsk:    0.70,
			NoAsk:     0.71,
			UpdatedAt: now,
			GameTime:  now.Add(time.Hour),
		},
	}

	if opps := Check(game); len(opps) != 0 {
		t.Fatalf("expected no executable arb, got %d", len(opps))
	}
}

func TestCheckHandlesOppositeYesOrientation(t *testing.T) {
	now := time.Now().UTC()
	game := MatchedGame{
		League:   "NBA",
		HomeTeam: "lakers",
		AwayTeam: "celtics",
		Kalshi: SportsMarket{
			Platform:  "KALSHI",
			League:    "NBA",
			HomeTeam:  "lakers",
			AwayTeam:  "celtics",
			YesTeam:   "lakers",
			YesAsk:    0.40,
			NoAsk:     0.62,
			UpdatedAt: now,
			GameTime:  now.Add(time.Hour),
		},
		Poly: SportsMarket{
			Platform:  "POLY",
			League:    "NBA",
			HomeTeam:  "lakers",
			AwayTeam:  "celtics",
			YesTeam:   "celtics",
			YesAsk:    0.41,
			NoAsk:     0.63,
			UpdatedAt: now,
			GameTime:  now.Add(time.Hour),
		},
	}

	opps := Check(game)
	if len(opps) == 0 {
		t.Fatalf("expected at least one arb")
	}
	found := false
	for _, opp := range opps {
		if opp.Leg1Platform == "KALSHI" && opp.Leg1Side == "yes" &&
			opp.Leg2Platform == "POLY" && opp.Leg2Side == "yes" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected yes+yes arb when YES teams are opposite, got %#v", opps)
	}
}

func TestInferYesTeamRequiresExplicitProposition(t *testing.T) {
	if got := inferYesTeam("Lakers vs Celtics", "lakers", "celtics", "NBA"); got != "" {
		t.Fatalf("expected ambiguous title to be rejected, got %q", got)
	}
	if got := inferYesTeam("Will the Lakers beat the Celtics?", "lakers", "celtics", "NBA"); got != "lakers" {
		t.Fatalf("expected explicit yes team, got %q", got)
	}
}
