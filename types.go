package main

import (
	"context"
	"sync"
	"time"
)

// One sports market on one platform
type SportsMarket struct {
	Platform     string
	MarketID     string
	ClobTokenIDs [2]string
	HomeTeam     string
	AwayTeam     string
	YesTeam      string
	League       string
	GameTime     time.Time
	Question     string
	MatchType    string
	MatchDate    string
	MatchBucket  string
	MatchKey     string
	YesBid       float64
	NoBid        float64
	YesAsk       float64
	NoAsk        float64
	UpdatedAt    time.Time
	ClosesAt     time.Time
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
	Game         MatchedGame
	Direction    string
	Leg1Platform string
	Leg1Side     string
	Leg1Price    float64
	Leg1Team     string
	Leg2Platform string
	Leg2Side     string
	Leg2Price    float64
	Leg2Team     string
	Combined     float64
	GrossProfit  float64
	KalshiFee    float64
	PolyFee      float64
	NetProfit    float64
	SeenAt       time.Time
	ExpiresAt    time.Time
}

// Passed on the shared updates channel from both WS clients
type MarketUpdate struct {
	Platform string
	Market   SportsMarket
}

type Config struct {
	KalshiAPIKeyID       string
	KalshiPrivateKeyPath string
	PolyAPIKey           string
	PolyAPIKeyID         string
	PolyAPISecret        string
	PolyPrivateKey       string
	ArbThreshold         float64
	MinEdgePct           float64
	KalshiFeeRate        float64
	PolyFeeFlat          float64
	MaxOrderUSDC         float64
	MaxDailyLoss         float64
	DryRun               bool
	LogPath              string
	JournalPath          string
}

type MarketClient interface {
	WithContext(context.Context)
	FetchSportsMarkets() ([]SportsMarket, error)
}

type DisplayRow struct {
	Game          MatchedGame
	Opportunities []ArbOpportunity
	CombinedA     float64
	CombinedB     float64
	BestSpread    float64
}

type DisplayState struct {
	Now           time.Time
	MatchedCount  int
	Rows          []DisplayRow
	Opportunities []ArbOpportunity
	OppsSeen      int
	CSVPath       string
	LogPath       string
	Exec          ExecutorSnapshot
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

type OrderState string

const (
	StatePending    OrderState = "PENDING"
	StateLeg1Sent   OrderState = "LEG1_SENT"
	StateLeg1Filled OrderState = "LEG1_FILLED"
	StateLeg2Sent   OrderState = "LEG2_SENT"
	StateComplete   OrderState = "COMPLETE"
	StateLeg1Failed OrderState = "LEG1_FAILED"
	StateLeg2Failed OrderState = "LEG2_FAILED"
	StateSkipped    OrderState = "SKIPPED"
)

type ArbOrder struct {
	ID              string     `json:"id"`
	ArbKey          string     `json:"arb_key"`
	State           OrderState `json:"state"`
	DryRun          bool       `json:"dry_run"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	League          string     `json:"league"`
	HomeTeam        string     `json:"home_team"`
	AwayTeam        string     `json:"away_team"`
	Direction       string     `json:"direction"`
	KalshiTicker    string     `json:"kalshi_ticker"`
	KalshiSide      string     `json:"kalshi_side"`
	KalshiPrice     float64    `json:"kalshi_price"`
	KalshiCount     int        `json:"kalshi_count"`
	KalshiOrderID   string     `json:"kalshi_order_id,omitempty"`
	KalshiFillPrice float64    `json:"kalshi_fill_price,omitempty"`
	PolyMarketID    string     `json:"poly_market_id"`
	PolyTokenID     string     `json:"poly_token_id"`
	PolySide        string     `json:"poly_side"`
	PolyPrice       float64    `json:"poly_price"`
	PolyStake       float64    `json:"poly_stake"`
	PolyOrderID     string     `json:"poly_order_id,omitempty"`
	PolyFillPrice   float64    `json:"poly_fill_price,omitempty"`
	NetProfit       float64    `json:"net_profit"`
	FailureReason   string     `json:"failure_reason,omitempty"`
}

type ExecutorSnapshot struct {
	Halted        bool
	Reason        string
	TotalOpps     int
	Executed      int
	Skipped       int
	Completed     int
	Leg1Failures  int
	Leg2Failures  int
	OpenPositions int
	TodayPnL      float64
}

func (m MatchedGame) GameTime() time.Time {
	if !m.Kalshi.GameTime.IsZero() {
		return m.Kalshi.GameTime
	}
	return m.Poly.GameTime
}
