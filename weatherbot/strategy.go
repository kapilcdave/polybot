package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

type Strategy struct {
	cfg       Config
	kalshi    *KalshiClient
	poly      *PolymarketClient
	meteo     *OpenMeteoClient
	nbm       *NBMClient
	simulator *Simulator
	cal       *CalibrationStore

	mu    sync.RWMutex
	state AppState
}

func NewStrategy(cfg Config) *Strategy {
	return &Strategy{
		cfg:       cfg,
		kalshi:    NewKalshiClient(cfg),
		poly:      NewPolymarketClient(cfg),
		meteo:     NewOpenMeteoClient(cfg),
		nbm:       NewNBMClient(),
		simulator: NewSimulator(cfg.StartingBankroll),
		cal:       NewCalibrationStore(cfg.ResolvedPath),
	}
}

func (s *Strategy) Run(ctx context.Context) {
	s.ScanOnce(ctx)
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ScanOnce(ctx)
		}
	}
}

func (s *Strategy) ScanOnce(ctx context.Context) ScanResult {
	started := time.Now().UTC()
	var (
		wg         sync.WaitGroup
		kalshiMkts []Market
		polyMkts   []Market
		kalshiErr  error
		polyErr    error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		kalshiMkts, kalshiErr = s.kalshi.DiscoverWeatherMarkets(ctx)
	}()
	go func() {
		defer wg.Done()
		polyMkts, polyErr = s.poly.DiscoverWeatherMarkets(ctx)
	}()
	wg.Wait()

	result := ScanResult{StartedAt: started}
	if kalshiErr != nil {
		result.Errors = append(result.Errors, kalshiErr.Error())
	}
	if polyErr != nil {
		result.Errors = append(result.Errors, polyErr.Error())
	}

	markets := append(kalshiMkts, polyMkts...)
	result.MarketsSeen = len(markets)

	forecastCache := make(map[string]ForecastEstimate)
	for _, market := range markets {
		key := fmt.Sprintf("%s|%s|%.1f", market.City, market.EventDate.In(loadLocation(cityTimezone(market.City))).Format("2006-01-02"), market.ThresholdF)
		estimate, ok := forecastCache[key]
		if !ok {
			loc, found := locationByName(market.City)
			if !found {
				result.Errors = append(result.Errors, "unknown city mapping: "+market.City)
				continue
			}
			estimate = s.forecast(ctx, loc, market, &result)
			if estimate.City == "" {
				continue
			}
			forecastCache[key] = estimate
		}
		if opp, ok := s.evaluateMarket(market, estimate); ok {
			result.Opps = append(result.Opps, opp)
			if s.cfg.SimulationMode {
				s.simulator.MaybeEnter(opp)
			}
		}
	}

	sort.Slice(result.Opps, func(i, j int) bool { return result.Opps[i].Edge > result.Opps[j].Edge })
	result.LastForecasts = len(forecastCache)
	result.CompletedAt = time.Now().UTC()

	s.mu.Lock()
	s.state = AppState{
		LastScan: result,
		Sim:      s.simulator.Snapshot(result),
		Now:      time.Now().UTC(),
	}
	s.mu.Unlock()

	log.Printf("scan complete markets=%d opps=%d forecasts=%d errors=%d", result.MarketsSeen, len(result.Opps), result.LastForecasts, len(result.Errors))
	return result
}

func (s *Strategy) evaluateMarket(m Market, est ForecastEstimate) (Opportunity, bool) {
	bestSide := ""
	bestAsk := 0.0
	fair := 0.0
	edge := 0.0

	spreadOK := func(bid, ask float64) bool {
		if bid <= 0 || ask <= 0 {
			return false
		}
		spread := ask - bid
		return spread <= s.cfg.MaxSpreadPct
	}

	depthOK := func(sizeUSD float64) bool {
		if s.cfg.MinDepthUSD <= 0 {
			return true
		}
		return sizeUSD >= s.cfg.MinDepthUSD
	}

	if m.YesAsk > 0 && m.YesBid > 0 && spreadOK(m.YesBid, m.YesAsk) && depthOK(m.YesSizeUSD) {
		yesEdge := est.ProbYes - m.YesAsk
		if yesEdge >= s.cfg.EdgeThreshold {
			bestSide = "yes"
			bestAsk = m.YesAsk
			fair = est.ProbYes
			edge = yesEdge
		}
	}
	if m.NoAsk > 0 && m.NoBid > 0 && spreadOK(m.NoBid, m.NoAsk) && depthOK(m.NoSizeUSD) {
		noFair := 1 - est.ProbYes
		noEdge := noFair - m.NoAsk
		if noEdge > edge && noEdge >= s.cfg.EdgeThreshold {
			bestSide = "no"
			bestAsk = m.NoAsk
			fair = noFair
			edge = noEdge
		}
	}
	if bestSide == "" {
		return Opportunity{}, false
	}
	globalCal = s.cal
	fair = calibrateProb(fair, m)
	stake, contracts, ev := sizeOpportunity(s.cfg, bestAsk, fair)
	if stake == 0 {
		return Opportunity{}, false
	}
	return Opportunity{
		Market:      m,
		Estimate:    est,
		Side:        bestSide,
		Ask:         bestAsk,
		Fair:        fair,
		Edge:        edge,
		StakeUSD:    stake,
		Contracts:   contracts,
		ExpectedEV:  ev,
		GeneratedAt: time.Now().UTC(),
	}, true
}

func (s *Strategy) forecast(ctx context.Context, loc WeatherLocation, market Market, result *ScanResult) ForecastEstimate {
	// Prefer NBM; fall back to Open-Meteo ensemble; placeholder until NBM implemented.
	if est, err := s.nbm.EstimateHighProb(ctx, loc, market.EventDate, market.ThresholdF); err == nil && est > 0 {
		return ForecastEstimate{
			City:          loc.Name,
			EventDate:     market.EventDate,
			ThresholdF:    market.ThresholdF,
			ProbYes:       est,
			Model:         "nbm_percentile",
			ForecastedAt:  time.Now().UTC(),
			SettlementRef: settlementStationID(loc.Name),
		}
	} else if err != nil && result != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	est, err := s.meteo.EstimateHighAbove(ctx, loc, market.EventDate, market.ThresholdF)
	if err != nil {
		if result != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s %s %.1fF: %v", market.Platform, market.City, market.ThresholdF, err))
		}
		return ForecastEstimate{}
	}
	return est
}

func sizeOpportunity(cfg Config, ask, fair float64) (stake, contracts, expectedEV float64) {
	if ask <= 0 || ask >= 1 || fair <= ask {
		return 0, 0, 0
	}
	kelly := (fair - ask) / (1 - ask)
	if kelly <= 0 {
		return 0, 0, 0
	}
	stake = minFloat(cfg.MaxTradeUSD, cfg.StartingBankroll*cfg.KellyFraction*kelly)
	if stake <= 0 {
		return 0, 0, 0
	}
	contracts = stake / ask
	expectedEV = contracts * (fair - ask)
	return stake, contracts, expectedEV
}

func (s *Strategy) Snapshot() AppState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func locationByName(name string) (WeatherLocation, bool) {
	for _, loc := range trackedLocations {
		if loc.Name == name {
			return loc, true
		}
	}
	return WeatherLocation{}, false
}

func cityTimezone(name string) string {
	loc, ok := locationByName(name)
	if !ok {
		return "UTC"
	}
	return loc.Timezone
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
