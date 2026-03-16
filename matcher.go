package main

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type GameMatcher struct {
	mu      sync.RWMutex
	kalshi  map[string]SportsMarket
	poly    map[string]SportsMarket
	matched map[string]MatchedGame
}

func NewGameMatcher() *GameMatcher {
	return &GameMatcher{
		kalshi:  make(map[string]SportsMarket),
		poly:    make(map[string]SportsMarket),
		matched: make(map[string]MatchedGame),
	}
}

func (m *GameMatcher) Update(update MarketUpdate) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch update.Platform {
	case "KALSHI":
		m.kalshi[update.Market.MarketID] = update.Market
	case "POLY":
		m.poly[update.Market.MarketID] = update.Market
	default:
		log.Printf("[matcher] unknown platform update: %s", update.Platform)
		return
	}

	m.rebuildMatchesLocked()
}

func (m *GameMatcher) Refresh(platform string, markets []SportsMarket) {
	m.mu.Lock()
	defer m.mu.Unlock()

	next := make(map[string]SportsMarket, len(markets))
	for _, market := range markets {
		next[market.MarketID] = market
	}

	switch platform {
	case "KALSHI":
		m.kalshi = next
	case "POLY":
		m.poly = next
	default:
		log.Printf("[matcher] unknown refresh platform: %s", platform)
		return
	}

	m.rebuildMatchesLocked()
}

func (m *GameMatcher) tryMatch(sm SportsMarket) *MatchedGame {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tryMatchLocked(sm, make(map[string]struct{}))
}

func (m *GameMatcher) tryMatchLocked(sm SportsMarket, used map[string]struct{}) *MatchedGame {
	candidates := m.poly
	if sm.Platform == "POLY" {
		candidates = m.kalshi
	}

	bestScore := 0.0
	var best SportsMarket
	for _, candidate := range candidates {
		if _, taken := used[candidate.MarketID]; taken {
			continue
		}
		score := matchScore(sm, candidate)
		if score > bestScore {
			bestScore = score
			best = candidate
		}
	}
	if bestScore < 0.85 {
		return nil
	}

	match := MatchedGame{
		MatchedAt: time.Now().UTC(),
		League:    firstNonEmpty(sm.League, best.League),
	}
	if sm.Platform == "KALSHI" {
		match.Kalshi = sm
		match.Poly = best
	} else {
		match.Kalshi = best
		match.Poly = sm
	}
	match.HomeTeam = firstNonEmpty(match.Kalshi.HomeTeam, match.Poly.HomeTeam)
	match.AwayTeam = firstNonEmpty(match.Kalshi.AwayTeam, match.Poly.AwayTeam)
	return &match
}

func (m *GameMatcher) GetAllMatches() []MatchedGame {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]MatchedGame, 0, len(m.matched))
	for _, match := range m.matched {
		out = append(out, match)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].League != out[j].League {
			return out[i].League < out[j].League
		}
		if out[i].HomeTeam != out[j].HomeTeam {
			return out[i].HomeTeam < out[j].HomeTeam
		}
		return out[i].AwayTeam < out[j].AwayTeam
	})
	return out
}

func (m *GameMatcher) rebuildMatchesLocked() {
	m.matched = make(map[string]MatchedGame)
	usedPoly := make(map[string]struct{})

	kalshiMarkets := make([]SportsMarket, 0, len(m.kalshi))
	for _, market := range m.kalshi {
		kalshiMarkets = append(kalshiMarkets, market)
	}

	sort.Slice(kalshiMarkets, func(i, j int) bool {
		if kalshiMarkets[i].League != kalshiMarkets[j].League {
			return kalshiMarkets[i].League < kalshiMarkets[j].League
		}
		if kalshiMarkets[i].GameTime.Equal(kalshiMarkets[j].GameTime) {
			return kalshiMarkets[i].MarketID < kalshiMarkets[j].MarketID
		}
		return kalshiMarkets[i].GameTime.Before(kalshiMarkets[j].GameTime)
	})

	for _, market := range kalshiMarkets {
		match := m.tryMatchLocked(market, usedPoly)
		if match == nil {
			continue
		}
		usedPoly[match.Poly.MarketID] = struct{}{}
		m.matched[matchKey(*match)] = *match
	}
}

func matchScore(a, b SportsMarket) float64 {
	if a.League == "" || b.League == "" || a.League != b.League {
		return 0
	}

	homeScore := teamSimilarity(a.HomeTeam, b.HomeTeam)
	awayScore := teamSimilarity(a.AwayTeam, b.AwayTeam)
	crossScore := (teamSimilarity(a.HomeTeam, b.AwayTeam) + teamSimilarity(a.AwayTeam, b.HomeTeam)) / 2
	teamScore := math.Max((homeScore+awayScore)/2, crossScore)

	timeScore := 0.0
	if !a.GameTime.IsZero() && !b.GameTime.IsZero() {
		diff := math.Abs(a.GameTime.Sub(b.GameTime).Hours())
		switch {
		case diff <= 2:
			timeScore = 1.0
		case diff <= 4:
			timeScore = 0.5
		case diff <= 6:
			timeScore = 0.0
		default:
			timeScore = 0.0
		}
	}

	questionScore := 0.0
	aq := normalizeTeamName(a.Question)
	bq := normalizeTeamName(b.Question)
	for _, team := range []string{a.HomeTeam, a.AwayTeam, b.HomeTeam, b.AwayTeam} {
		normalized := normalizeTeamName(team)
		if normalized != "" && strings.Contains(aq, normalized) && strings.Contains(bq, normalized) {
			questionScore = 0.3
			break
		}
	}

	return teamScore*0.6 + timeScore*0.3 + questionScore*0.1
}

func normalizeTeamName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	replacer := strings.NewReplacer(
		"the ", " ",
		"will ", " ",
		"beat ", " ",
		"vs. ", " ",
		"vs ", " ",
		"?", " ",
		"!", " ",
		",", " ",
		".", " ",
		"(", " ",
		")", " ",
		"-", " ",
		"—", " ",
	)
	s = replacer.Replace(s)
	for _, suffix := range []string{" moneyline", " wins", " win", " beat"} {
		s = strings.TrimSuffix(s, suffix)
	}
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	if looksLikeCollegeTeam(s) {
		return s
	}
	for _, city := range cityPrefixes {
		if strings.HasPrefix(s, city+" ") {
			s = strings.TrimSpace(strings.TrimPrefix(s, city))
			break
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

func teamSimilarity(a, b string) float64 {
	na := normalizeTeamName(a)
	nb := normalizeTeamName(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 1
	}
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		return 1
	}
	return math.Max(jaroWinkler(na, nb), tokenOverlap(na, nb))
}

func tokenOverlap(a, b string) float64 {
	at := strings.Fields(a)
	bt := strings.Fields(b)
	if len(at) == 0 || len(bt) == 0 {
		return 0
	}

	set := make(map[string]struct{}, len(at))
	for _, token := range at {
		set[token] = struct{}{}
	}
	common := 0
	for _, token := range bt {
		if _, ok := set[token]; ok {
			common++
		}
	}
	return float64(common) / math.Max(float64(len(at)), float64(len(bt)))
}

func jaroWinkler(a, b string) float64 {
	if a == b {
		return 1
	}
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 || len(br) == 0 {
		return 0
	}

	window := maxInt(len(ar), len(br))/2 - 1
	if window < 0 {
		window = 0
	}

	am := make([]bool, len(ar))
	bm := make([]bool, len(br))
	matches := 0

	for i := range ar {
		start := maxInt(0, i-window)
		end := minInt(i+window+1, len(br))
		for j := start; j < end; j++ {
			if bm[j] || ar[i] != br[j] {
				continue
			}
			am[i] = true
			bm[j] = true
			matches++
			break
		}
	}
	if matches == 0 {
		return 0
	}

	transpositions := 0
	k := 0
	for i := range ar {
		if !am[i] {
			continue
		}
		for !bm[k] {
			k++
		}
		if ar[i] != br[k] {
			transpositions++
		}
		k++
	}

	m := float64(matches)
	jaro := ((m / float64(len(ar))) + (m / float64(len(br))) + ((m - float64(transpositions)/2) / m)) / 3

	prefix := 0
	for i := 0; i < minInt(4, minInt(len(ar), len(br))); i++ {
		if ar[i] != br[i] {
			break
		}
		prefix++
	}
	return jaro + float64(prefix)*0.1*(1-jaro)
}

var cityPrefixes = []string{
	"atlanta", "boston", "brooklyn", "charlotte", "chicago",
	"cleveland", "dallas", "denver", "detroit", "golden state",
	"houston", "indiana", "los angeles", "la", "memphis", "miami",
	"milwaukee", "minnesota", "new orleans", "new york", "oklahoma city",
	"orlando", "philadelphia", "phoenix", "portland", "sacramento",
	"san antonio", "toronto", "utah", "washington",
	"anaheim", "buffalo", "calgary", "carolina", "columbus", "dallas",
	"detroit", "edmonton", "florida", "los angeles", "minnesota",
	"montreal", "nashville", "new jersey", "new york", "ottawa",
	"philadelphia", "pittsburgh", "san jose", "seattle", "st louis",
	"st. louis", "tampa bay", "toronto", "vancouver", "vegas", "washington",
	"winnipeg",
}

func looksLikeCollegeTeam(name string) bool {
	for _, city := range cityPrefixes {
		if name == city || strings.HasPrefix(name, city+" ") {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func matchKey(match MatchedGame) string {
	return fmt.Sprintf("%s|%s", match.Kalshi.MarketID, match.Poly.MarketID)
}

func arbKey(arb ArbOpportunity) string {
	return fmt.Sprintf("%s|%s|%s|%s|%.4f|%.4f",
		arb.Game.League,
		arb.Game.HomeTeam,
		arb.Game.AwayTeam,
		arb.Direction,
		arb.YesPrice,
		arb.NoPrice,
	)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
