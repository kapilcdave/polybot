package main

import "time"

type WeatherLocation struct {
	Name               string
	SeriesTicker       string
	Lat                float64
	Lon                float64
	Timezone           string
	PolymarketKeywords []string
}

type Market struct {
	Platform     string    `json:"platform"`
	ID           string    `json:"id"`
	SeriesTicker string    `json:"series_ticker,omitempty"`
	Question     string    `json:"question"`
	City         string    `json:"city"`
	Settlement   string    `json:"settlement_station,omitempty"`
	SettlementTZ string    `json:"settlement_tz,omitempty"`
	ThresholdF   float64   `json:"threshold_f"`
	EventDate    time.Time `json:"event_date"`
	YesBid       float64   `json:"yes_bid"`
	YesAsk       float64   `json:"yes_ask"`
	NoBid        float64   `json:"no_bid"`
	NoAsk        float64   `json:"no_ask"`
	YesSizeUSD   float64   `json:"yes_size_usd,omitempty"`
	NoSizeUSD    float64   `json:"no_size_usd,omitempty"`
	URL          string    `json:"url,omitempty"`
}

type ForecastEstimate struct {
	City          string    `json:"city"`
	EventDate     time.Time `json:"event_date"`
	ThresholdF    float64   `json:"threshold_f"`
	ProbYes       float64   `json:"prob_yes"`
	Members       int       `json:"members"`
	MaxTemps      []float64 `json:"max_temps,omitempty"`
	Model         string    `json:"model"`
	ForecastedAt  time.Time `json:"forecasted_at"`
	SettlementRef string    `json:"settlement_ref"`
}

type Opportunity struct {
	Market      Market           `json:"market"`
	Estimate    ForecastEstimate `json:"estimate"`
	Side        string           `json:"side"`
	Ask         float64          `json:"ask"`
	Fair        float64          `json:"fair"`
	Edge        float64          `json:"edge"`
	StakeUSD    float64          `json:"stake_usd"`
	Contracts   float64          `json:"contracts"`
	ExpectedEV  float64          `json:"expected_ev"`
	GeneratedAt time.Time        `json:"generated_at"`
}

type ScanResult struct {
	StartedAt     time.Time     `json:"started_at"`
	CompletedAt   time.Time     `json:"completed_at"`
	MarketsSeen   int           `json:"markets_seen"`
	Opps          []Opportunity `json:"opportunities"`
	Errors        []string      `json:"errors"`
	LastForecasts int           `json:"last_forecasts"`
}

type Position struct {
	Key        string    `json:"key"`
	Market     Market    `json:"market"`
	Side       string    `json:"side"`
	EntryPrice float64   `json:"entry_price"`
	StakeUSD   float64   `json:"stake_usd"`
	Contracts  float64   `json:"contracts"`
	FairAtOpen float64   `json:"fair_at_open"`
	OpenedAt   time.Time `json:"opened_at"`
}

type SimulationSnapshot struct {
	BankrollUSD float64    `json:"bankroll_usd"`
	CashUSD     float64    `json:"cash_usd"`
	EquityUSD   float64    `json:"equity_usd"`
	Positions   []Position `json:"positions"`
	Trades      int        `json:"trades"`
}

type AppState struct {
	LastScan ScanResult         `json:"last_scan"`
	Sim      SimulationSnapshot `json:"simulation"`
	Now      time.Time          `json:"now"`
}
