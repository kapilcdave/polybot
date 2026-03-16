package main

import (
	"context"
	"sync"
	"time"
)

// One sports market on one platform
type SportsMarket struct {
	Platform  string
	MarketID  string
	HomeTeam  string
	AwayTeam  string
	League    string
	GameTime  time.Time
	Question  string
	YesBid    float64
	NoBid     float64
	YesAsk    float64
	NoAsk     float64
	UpdatedAt time.Time
	ClosesAt  time.Time
}

// A matched pair — same game on both platforms
type MatchedGame struct {
	Kalshi    SportsMarket
	Poly      SportsMarket
	MatchedAt time.Time
	League    string
	HomeTeam  string
	AwayTeam  string
}

// A detected arb opportunity
type ArbOpportunity struct {
	Game        MatchedGame
	Direction   string
	BuyYesAt    string
	YesPrice    float64
	BuyNoAt     string
	NoPrice     float64
	Combined    float64
	GrossProfit float64
	KalshiFee   float64
	PolyFee     float64
	NetProfit   float64
	SeenAt      time.Time
	ExpiresAt   time.Time
}

// Passed on the shared updates channel from both WS clients
type MarketUpdate struct {
	Platform string
	Market   SportsMarket
}

type Config struct {
	KalshiAPIKeyID      string
	KalshiPrivateKeyPath string
	PolyAPIKey          string
	ArbThreshold        float64
}

type MarketClient interface {
	WithContext(context.Context)
	FetchSportsMarkets() ([]SportsMarket, error)
}

type DisplayRow struct {
	Game        MatchedGame
	Opportunities []ArbOpportunity
	CombinedA   float64
	CombinedB   float64
	BestSpread  float64
}

type DisplayState struct {
	Now          time.Time
	MatchedCount int
	Rows         []DisplayRow
	Opportunities []ArbOpportunity
	OppsSeen     int
	LogPath      string
}

type SessionStats struct {
	mu            sync.RWMutex
	startedAt     time.Time
	totalOpps     int
	bestSpread    float64
	bestNet       float64
	lastSeenByKey map[string]time.Time
}

func NewSessionStats(start time.Time) *SessionStats {
	return &SessionStats{
		startedAt:     start,
		lastSeenByKey: make(map[string]time.Time),
	}
}

func (s *SessionStats) Record(arb ArbOpportunity) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := arbKey(arb)
	if last, ok := s.lastSeenByKey[key]; ok && arb.SeenAt.Sub(last) < 30*time.Second {
		return false
	}
	s.lastSeenByKey[key] = arb.SeenAt
	s.totalOpps++
	if arb.GrossProfit > s.bestSpread {
		s.bestSpread = arb.GrossProfit
	}
	if arb.NetProfit > s.bestNet {
		s.bestNet = arb.NetProfit
	}
	return true
}

func (s *SessionStats) Snapshot() (totalOpps int, bestSpread float64, bestNet float64, startedAt time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalOpps, s.bestSpread, s.bestNet, s.startedAt
}

type tokenMapping struct {
	MarketID string
	Side     string
}

