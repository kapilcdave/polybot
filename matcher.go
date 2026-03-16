package main

import (
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
		return
	}

	m.refreshMatchesLocked(update.Market)
}

func (m *GameMatcher) Refresh(platform string, markets []SportsMarket) {
	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.kalshi
	if platform == "POLY" {
		target = m.poly
	}

	next := make(map[string]SportsMarket, len(markets))
	for _, market := range markets {
		next[market.MarketID] = market
	}

	for id := range target {
		if _, ok := next[id]; !ok {
			delete(target, id)
		}
	}
	for id, market := range next {
		target[id] = market
	}

	m.matched = make(map[string]MatchedGame)
	for _, market := range m.kalshi {
		m.refreshMatchesLocked(market)
	}
	for _, market := range m.poly {
		m.refreshMatchesLocked(market)
	}
}

func (m *GameMatcher) refreshMatchesLocked(sm SportsMarket) {
	if match := m.tryMatchLocked(sm); match != nil {
		m.matched[matchKey(*match)] = *match
	}

	for _, market := range m.unmatchedMarketsLocked() {
		if match := m.tryMatchLocked(market); match != nil {
			m.matched[matchKey(*match)] = *match
		}
	}
}

func (m *GameMatcher) unmatchedMarketsLocked() []SportsMarket {
	var out []SportsMarket
	for _, market := range m.kalshi {
		if !m.marketMatchedLocked(market) {
			out = append(out, market)
		}
	}
	for _, market := range m.poly {
		if !m.marketMatchedLocked(market) {
			out = append(out, market)
		}
	}
	return out
}

func (m *GameMatcher) marketMatchedLocked(sm SportsMarket) bool {
	for _, matched := range m.matched {
		if sm.Platform == "KALSHI" && matched.Kalshi.MarketID == sm.MarketID {
			return true
		}
		if sm.Platform == "POLY" && matched.Poly.MarketID == sm.MarketID {
			return true
		}
	}
	return false
}

func (m *GameMatcher) tryMatch(sm SportsMarket) *MatchedGame {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tryMatchLocked(sm)
}

func (m *GameMatcher) tryMatchLocked(sm SportsMarket) *MatchedGame {
	if m.marketMatchedLocked(sm) {
		for _, existing := range m.matched {
			if sm.Platform == "KALSHI" && existing.Kalshi.MarketID == sm.MarketID {
				match := existing
				return &match
			}
			if sm.Platform == "POLY" && existing.Poly.MarketID == sm.MarketID {
				match := existing
				return &match
			}
		}
	}

	candidates := m.poly
	if sm.Platform == "POLY" {
		candidates = m.kalshi
	}

	bestScore := 0.0
	var best SportsMarket
	for _, candidate := range candidates {
		score := matchScore(sm, candidate)
		if score > bestScore {
			bestScore = score
			best = candidate
		}
	}
	if bestScore < 0.85 {
		return nil
	}

	var matched MatchedGame
	if sm.Platform == "KALSHI" {
		matched = MatchedGame{
			Kalshi:    sm,
			Poly:      best,
			MatchedAt: time.Now().UTC(),
			League:    sm.League,
			HomeTeam:  firstNonEmpty(sm.HomeTeam, best.HomeTeam),
			AwayTeam:  firstNonEmpty(sm.AwayTeam, best.AwayTeam),
		}
	} else {
		matched = MatchedGame{
			Kalshi:    best,
			Poly:      sm,
			MatchedAt: time.Now().UTC(),
			League:    sm.League,
			HomeTeam:  firstNonEmpty(best.HomeTeam, sm.HomeTeam),
			AwayTeam:  firstNonEmpty(best.AwayTeam, sm.AwayTeam),
		}
	}
	return &matched
}

func matchScore(a, b SportsMarket) float64 {
	if a.League == "" || b.League == "" || a.League != b.League {
		return 0
	}

	teamScore := 0.0
	switch {
	case a.HomeTeam != "" && a.AwayTeam != "" && b.HomeTeam != "" && b.AwayTeam != "":
		homeScore := teamSimilarity(a.HomeTeam, b.HomeTeam)
		awayScore := teamSimilarity(a.AwayTeam, b.AwayTeam)
		crossHome := teamSimilarity(a.HomeTeam, b.AwayTeam)
		crossAway := teamSimilarity(a.AwayTeam, b.HomeTeam)
		teamScore = math.Max((homeScore+awayScore)/2, (crossHome+crossAway)/2)
	case a.HomeTeam != "" && b.HomeTeam != "":
		teamScore = teamSimilarity(a.HomeTeam, b.HomeTeam)
	case a.AwayTeam != "" && b.AwayTeam != "":
		teamScore = teamSimilarity(a.AwayTeam, b.AwayTeam)
	}

	timeScore := 0.0
	if !a.GameTime.IsZero() && !b.GameTime.IsZero() {
		diff := math.Abs(a.GameTime.Sub(b.GameTime).Hours())
		switch {
		case diff <= 2:
			timeScore = 1.0
		case diff <= 4:
			timeScore = 0.5
		case diff <= 6:
			timeScore = 0.25
		}
	}

	questionScore := 0.0
	aq := normalizeTeamName(a.Question)
	bq := normalizeTeamName(b.Question)
	for _, team := range []string{a.HomeTeam, a.AwayTeam, b.HomeTeam, b.AwayTeam} {
		team = normalizeTeamName(team)
		if team != "" && strings.Contains(aq, team) && strings.Contains(bq, team) {
			questionScore = 0.3
			break
		}
	}

	return 1.0 * (teamScore*0.6 + timeScore*0.3 + questionScore*0.1)
}

func normalizeTeamName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	replacements := []string{"the ", "will ", "beat ", "vs. ", "vs "}
	for _, prefix := range replacements {
		s = strings.ReplaceAll(s, prefix, " ")
	}
	for _, suffix := range []string{" moneyline", " wins", " win", " beat"} {
		s = strings.TrimSuffix(s, suffix)
	}
	s = strings.NewReplacer("?", " ", "!", " ", "-", " ", "—", " ", "_", " ").Replace(s)
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}

	if !isNCAATeam(fields) {
		for _, city := range cityPrefixes {
			if strings.HasPrefix(strings.Join(fields, " "), city+" ") {
				joined := strings.TrimSpace(strings.TrimPrefix(strings.Join(fields, " "), city))
				fields = strings.Fields(joined)
				break
			}
		}
	}

	return strings.Join(fields, " ")
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
	return math.Max(jaroWinkler(na, nb), tokenOverlapScore(na, nb))
}

func tokenOverlapScore(a, b string) float64 {
	aTokens := strings.Fields(a)
	bTokens := strings.Fields(b)
	if len(aTokens) == 0 || len(bTokens) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(aTokens))
	for _, token := range aTokens {
		set[token] = struct{}{}
	}
	common := 0
	for _, token := range bTokens {
		if _, ok := set[token]; ok {
			common++
		}
	}
	denom := math.Max(float64(len(aTokens)), float64(len(bTokens)))
	return float64(common) / denom
}

func jaroWinkler(s1, s2 string) float64 {
	if s1 == s2 {
		return 1
	}
	r1 := []rune(s1)
	r2 := []rune(s2)
	if len(r1) == 0 || len(r2) == 0 {
		return 0
	}

	matchDistance := maxInt(len(r1), len(r2))/2 - 1
	if matchDistance < 0 {
		matchDistance = 0
	}

	r1Matches := make([]bool, len(r1))
	r2Matches := make([]bool, len(r2))
	matches := 0

	for i := range r1 {
		start := maxInt(0, i-matchDistance)
		end := minInt(i+matchDistance+1, len(r2))
		for j := start; j < end; j++ {
			if r2Matches[j] || r1[i] != r2[j] {
				continue
			}
			r1Matches[i] = true
			r2Matches[j] = true
			matches++
			break
		}
	}
	if matches == 0 {
		return 0
	}

	transpositions := 0
	k := 0
	for i := range r1 {
		if !r1Matches[i] {
			continue
		}
		for !r2Matches[k] {
			k++
		}
		if r1[i] != r2[k] {
			transpositions++
		}
		k++
	}

	m := float64(matches)
	jaro := ((m / float64(len(r1))) + (m / float64(len(r2))) + ((m - float64(transpositions)/2) / m)) / 3

	prefix := 0
	for i := 0; i < minInt(4, minInt(len(r1), len(r2))); i++ {
		if r1[i] != r2[i] {
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
	"anaheim", "buffalo", "calgary", "carolina", "columbus", "edmonton",
	"florida", "montreal", "nashville", "new jersey", "ottawa",
	"pittsburgh", "san jose", "seattle", "st. louis", "st louis",
	"tampa bay", "vancouver", "vegas", "winnipeg",
}

func isNCAATeam(fields []string) bool {
	joined := strings.Join(fields, " ")
	for _, city := range cityPrefixes {
		if strings.HasPrefix(joined, city+" ") || joined == city {
			return false
		}
	}
	return true
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func matchKey(match MatchedGame) string {
	return match.Kalshi.MarketID + "|" + match.Poly.MarketID
}

func arbKey(arb ArbOpportunity) string {
	return strings.Join([]string{
		arb.Game.League,
		arb.Game.HomeTeam,
		arb.Game.AwayTeam,
		arb.Direction,
		formatPriceKey(arb.YesPrice),
		formatPriceKey(arb.NoPrice),
	}, "|")
}

func formatPriceKey(v float64) string {
	return strings.TrimRight(strings.TrimRight(strconvFloat(v*100), "0"), ".")
}

func strconvFloat(v float64) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(formatFloat(v))))), " ", ""), "+", ""), "0"), "."))
}

func formatFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(func() string {
		return strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(""))), "")), "")), ""))), "")), ""))), "")), ""))), "")), ""))), "")), ""))), "")), ""))), "")), "")), "")), "")), "")), "")), "")), "")), "")), "")
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

func init() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
