package main

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
)

var debugMatching bool

var (
	reSpread  = regexp.MustCompile(`(?i)(\b[+-]\d+\.?\d*\b|\bspread\b|\bcover\b|\bats\b)`)
	reTotal   = regexp.MustCompile(`(?i)(\bover\b|\bunder\b|\btotal\b|\bo/u\b|\bover/under\b)`)
	reSeries  = regexp.MustCompile(`(?i)(\bseries\b|\badvance\b|\bplayoffs?\b|\bchampionship\b|\btitle\b|\bwinner\b|\bwins the\b|\bcup\b|\bsuper bowl\b|\bnba finals\b|\bworld series\b)`)
	reDate    = regexp.MustCompile(`(?i)(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)\w*\s+\d{1,2}`)
	reISODate = regexp.MustCompile(`\b(20\d{2}-\d{2}-\d{2})\b`)
	reYear    = regexp.MustCompile(`\b(20\d{2})\b`)
	monthMap  = map[string]string{
		"jan": "01", "feb": "02", "mar": "03", "apr": "04",
		"may": "05", "jun": "06", "jul": "07", "aug": "08",
		"sep": "09", "oct": "10", "nov": "11", "dec": "12",
	}
)

func annotateMarketForMatching(m SportsMarket) (SportsMarket, string) {
	m.MatchType = extractMarketType(m.Question)
	m.MatchDate = extractMarketDate(m.Question, m.GameTime, m.ClosesAt)

	teamA := strings.TrimSpace(m.HomeTeam)
	teamB := strings.TrimSpace(m.AwayTeam)
	if teamA == "" {
		return m, "missing primary team"
	}

	teams := []string{teamA}
	if teamB != "" {
		if teamA == teamB {
			return m, "duplicate team names"
		}
		teams = append(teams, teamB)
	}
	sort.Strings(teams)

	m.MatchBucket = fmt.Sprintf("%s:%s:%s", strings.ToUpper(strings.TrimSpace(m.League)), strings.Join(teams, ":"), m.MatchType)
	if m.MatchDate != "" {
		m.MatchKey = m.MatchBucket + ":" + m.MatchDate
	}
	return m, ""
}

func extractMarketType(title string) string {
	switch {
	case reSeries.MatchString(title):
		return "series"
	case reSpread.MatchString(title):
		return "spread"
	case reTotal.MatchString(title):
		return "total"
	default:
		return "moneyline"
	}
}

func extractMarketDate(title string, gameTime, closesAt time.Time) string {
	if iso := reISODate.FindString(title); iso != "" {
		return iso
	}
	if m := reDate.FindStringSubmatch(title); m != nil {
		parts := strings.Fields(m[0])
		if len(parts) == 2 {
			monthKey := strings.ToLower(parts[0])[:3]
			if mo, ok := monthMap[monthKey]; ok {
				day := parts[1]
				if len(day) == 1 {
					day = "0" + day
				}
				year := ""
				if yr := reYear.FindString(title); yr != "" {
					year = yr
				} else if !gameTime.IsZero() {
					year = gameTime.UTC().Format("2006")
				} else if !closesAt.IsZero() {
					year = closesAt.UTC().Format("2006")
				} else {
					year = time.Now().UTC().Format("2006")
				}
				return year + "-" + mo + "-" + day
			}
		}
	}
	if !gameTime.IsZero() {
		return gameTime.UTC().Format("2006-01-02")
	}
	if !closesAt.IsZero() {
		return closesAt.UTC().Format("2006-01-02")
	}
	return ""
}

func debugSeedMarket(platform string, market SportsMarket, reason string) {
	if !debugMatching {
		return
	}
	if market.MatchKey != "" {
		log.Printf("[%s] key=%s bucket=%s title=%q", platform, market.MatchKey, market.MatchBucket, market.Question)
		return
	}
	log.Printf("[%s] NO KEY: %s title=%q league=%q home=%q away=%q game=%q close=%q",
		platform,
		reason,
		market.Question,
		market.League,
		market.HomeTeam,
		market.AwayTeam,
		market.GameTime.UTC().Format(time.RFC3339),
		market.ClosesAt.UTC().Format(time.RFC3339),
	)
}

func matchDateDistance(a, b SportsMarket) (time.Duration, bool) {
	left := a.GameTime
	if left.IsZero() {
		left = a.ClosesAt
	}
	right := b.GameTime
	if right.IsZero() {
		right = b.ClosesAt
	}
	if !left.IsZero() && !right.IsZero() {
		if left.After(right) {
			return left.Sub(right), true
		}
		return right.Sub(left), true
	}
	if a.MatchDate != "" && b.MatchDate != "" {
		lt, err1 := time.Parse("2006-01-02", a.MatchDate)
		rt, err2 := time.Parse("2006-01-02", b.MatchDate)
		if err1 == nil && err2 == nil {
			if lt.After(rt) {
				return lt.Sub(rt), true
			}
			return rt.Sub(lt), true
		}
	}
	return 0, false
}
