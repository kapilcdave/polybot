package feed

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Platform identifies which exchange a market is from.
type Platform string

const (
	Kalshi     Platform = "kalshi"
	Polymarket Platform = "polymarket"
)

// MarketType is the bet type we care about.
type MarketType string

const (
	Moneyline MarketType = "moneyline"
	Series    MarketType = "series"
	Spread    MarketType = "spread"
	Total     MarketType = "total"
	Unknown   MarketType = "unknown"
)

// Market is the normalized representation of a market from either platform.
type Market struct {
	Platform   Platform
	ID         string  // native platform ID
	Title      string  // raw title
	YesPrice   float64 // probability of YES outcome (0.0–1.0)
	NoPrice    float64
	Volume     float64
	CloseTime  time.Time

	// Normalized fields set by the matcher
	TeamA     string
	TeamB     string
	GameDate  string     // YYYY-MM-DD
	Type      MarketType
	Key       string     // canonical match key

	UpdatedAt time.Time
	Stale     bool       // true if WS hasn't sent update in >60s
}

// CanonicalKey builds the match key from normalized fields.
// Teams are sorted so LAL:GSW == GSW:LAL.
// Returns "" if the market can't be keyed (missing teams, bad type).
func CanonicalKey(teamA, teamB, date string, mtype MarketType) string {
	if teamA == "" || teamB == "" || date == "" {
		return ""
	}
	if mtype == Spread || mtype == Total || mtype == Unknown {
		return ""
	}
	teams := []string{teamA, teamB}
	sort.Strings(teams)
	return fmt.Sprintf("%s:%s:%s:%s", teams[0], teams[1], date, string(mtype))
}

// PriceMap holds all live markets from both platforms, keyed by canonical key.
// It's the single source of truth for the matcher.
type PriceMap struct {
	mu sync.RWMutex

	// platform-native lookup: platform+id -> Market
	byID map[string]*Market

	// canonical key -> [kalshi market, poly market]
	// A pair is complete when both sides are non-nil
	byKey map[string][2]*Market // [0]=kalshi [1]=poly
}

func NewPriceMap() *PriceMap {
	return &PriceMap{
		byID:  make(map[string]*Market),
		byKey: make(map[string][2]*Market),
	}
}

func platformIdx(p Platform) int {
	if p == Kalshi {
		return 0
	}
	return 1
}

// Upsert adds or updates a market. Thread-safe.
// Returns the updated market and whether a complete pair now exists for its key.
func (pm *PriceMap) Upsert(m *Market) (pair [2]*Market, complete bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	nativeKey := string(m.Platform) + ":" + m.ID
	m.UpdatedAt = time.Now()
	pm.byID[nativeKey] = m

	if m.Key != "" {
		idx := platformIdx(m.Platform)
		p := pm.byKey[m.Key]
		p[idx] = m
		pm.byKey[m.Key] = p
		return p, p[0] != nil && p[1] != nil
	}
	return [2]*Market{}, false
}

// GetPair returns the pair for a given canonical key. Thread-safe.
func (pm *PriceMap) GetPair(key string) ([2]*Market, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.byKey[key]
	return p, ok && p[0] != nil && p[1] != nil
}

// AllPairs returns all complete pairs. Thread-safe.
func (pm *PriceMap) AllPairs() [][2]*Market {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	var out [][2]*Market
	for _, p := range pm.byKey {
		if p[0] != nil && p[1] != nil {
			cp := p
			out = append(out, cp)
		}
	}
	return out
}

// MarkStale marks all markets from a platform as stale.
func (pm *PriceMap) MarkStale(platform Platform) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	prefix := string(platform) + ":"
	for k, m := range pm.byID {
		if strings.HasPrefix(k, prefix) {
			m.Stale = true
		}
	}
}

// Stats returns counts for display.
func (pm *PriceMap) Stats() (kalshiN, polyN, pairsN int) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for k := range pm.byID {
		if strings.HasPrefix(k, "kalshi:") {
			kalshiN++
		} else {
			polyN++
		}
	}
	for _, p := range pm.byKey {
		if p[0] != nil && p[1] != nil {
			pairsN++
		}
	}
	return
}
