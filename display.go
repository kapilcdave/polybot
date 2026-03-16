package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiClear  = "\033[2J\033[H"
)

type Display struct {
	mu  sync.Mutex
	out io.Writer
}

func NewDisplay() *Display {
	return &Display{out: os.Stdout}
}

func (d *Display) Render(state DisplayState) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var b strings.Builder
	b.WriteString(ansiClear)
	b.WriteString(fmt.Sprintf("┌──────────────────────────────────────────────────────────────────────────────┐\n"))
	b.WriteString(fmt.Sprintf("│  POLYBOT SPORTS ARB  │  %-12s  │  matched: %-3d games            │\n", state.Now.Format("15:04:05.000"), state.MatchedCount))
	b.WriteString("└──────────────────────────────────────────────────────────────────────────────┘\n\n")
	b.WriteString("MATCHED GAMES                    K_YES  K_NO   P_YES  P_NO   K+P_NO  K+P_YES\n")
	b.WriteString("──────────────────────────────────────────────────────────────────────────────\n")

	rows := append([]DisplayRow(nil), state.Rows...)
	sort.Slice(rows, func(i, j int) bool {
		ai := len(rows[i].Opportunities) > 0
		aj := len(rows[j].Opportunities) > 0
		if ai != aj {
			return ai
		}
		if rows[i].BestSpread != rows[j].BestSpread {
			return rows[i].BestSpread > rows[j].BestSpread
		}
		return rows[i].Game.HomeTeam < rows[j].Game.HomeTeam
	})
	if len(rows) > 20 {
		rows = rows[:20]
	}

	for _, row := range rows {
		line := fmt.Sprintf("[%-5s] %-24s %5s  %5s  %5s  %5s  %6s  %6s",
			row.Game.League,
			truncate(fmt.Sprintf("%s vs %s", titleName(row.Game.HomeTeam), titleName(row.Game.AwayTeam)), 24),
			priceCell(row.Game.Kalshi.YesBid),
			priceCell(row.Game.Kalshi.NoBid),
			priceCell(row.Game.Poly.YesBid),
			priceCell(row.Game.Poly.NoBid),
			inlineSpread(row.CombinedA),
			inlineSpread(row.CombinedB),
		)
		if len(row.Opportunities) > 0 {
			line = ansiBold + ansiYellow + line + ansiReset
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n──────────────────────────────────────────────────────────────────────────────\n")
	b.WriteString("⚡ ARB OPPORTUNITIES\n\n")
	if len(state.Opportunities) == 0 {
		b.WriteString("none\n")
	} else {
		for _, arb := range state.Opportunities {
			status := countdownText(arb.ExpiresAt)
			b.WriteString(ansiBold + ansiYellow)
			b.WriteString(fmt.Sprintf("⚡ [%s] %s vs %s\n", arb.Game.League, titleName(arb.Game.HomeTeam), titleName(arb.Game.AwayTeam)))
			b.WriteString(ansiReset)
			b.WriteString(fmt.Sprintf("   BUY YES@%s %s + NO@%s %s = %s combined\n",
				arb.BuyYesAt,
				priceCell(arb.YesPrice),
				arb.BuyNoAt,
				priceCell(arb.NoPrice),
				priceCell(arb.Combined),
			))
			b.WriteString(fmt.Sprintf("   gross: +%s  fees: -%s  NET: +%s/contract\n",
				priceCell(arb.GrossProfit),
				priceCell(arb.KalshiFee+arb.PolyFee),
				priceCell(arb.NetProfit),
			))
			b.WriteString(fmt.Sprintf("   game starts: %s  (%s)\n\n", arb.ExpiresAt.In(easternLocation()).Format("3:04 PM ET"), status))
		}
	}

	b.WriteString("──────────────────────────────────────────────────────────────────────────────\n")
	b.WriteString(fmt.Sprintf("  opps seen: %d  │  csv: %s  │  ctrl+c to exit\n", state.OppsSeen, state.LogPath))

	_, _ = io.WriteString(d.out, b.String())
}

func buildDisplayState(matcher *GameMatcher, stats *SessionStats, logPath string) DisplayState {
	matches := matcher.GetAllMatches()
	rows := make([]DisplayRow, 0, len(matches))
	var opps []ArbOpportunity
	for _, match := range matches {
		found := Check(match)
		combinedA := match.Kalshi.YesBid + match.Poly.NoBid
		combinedB := match.Kalshi.NoBid + match.Poly.YesBid
		bestSpread := 1.0 - minFloat(combinedA, combinedB)
		rows = append(rows, DisplayRow{
			Game:          match,
			Opportunities: found,
			CombinedA:     combinedA,
			CombinedB:     combinedB,
			BestSpread:    bestSpread,
		})
		opps = append(opps, found...)
	}

	sort.Slice(opps, func(i, j int) bool {
		if opps[i].ExpiresAt.Equal(opps[j].ExpiresAt) {
			return opps[i].NetProfit > opps[j].NetProfit
		}
		return opps[i].ExpiresAt.Before(opps[j].ExpiresAt)
	})

	total, _, _, _ := stats.Snapshot()
	return DisplayState{
		Now:           time.Now(),
		MatchedCount:  len(matches),
		Rows:          rows,
		Opportunities: opps,
		OppsSeen:      total,
		LogPath:       logPath,
	}
}

func inlineSpread(v float64) string {
	cell := priceCell(v)
	if v < activeArbThreshold {
		return cell + " ARB⚡"
	}
	return cell
}

func countdownText(t time.Time) string {
	if time.Now().After(t) {
		return ansiRed + "EXPIRED" + ansiReset
	}
	until := time.Until(t).Round(time.Minute)
	hours := int(until.Hours())
	mins := int(until.Minutes()) % 60
	if hours > 0 {
		return fmt.Sprintf("must trade in next %dh %02dm", hours, mins)
	}
	return fmt.Sprintf("must trade in next %d min", int(until.Minutes()))
}

func truncate(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-1]) + "…"
}

func priceCell(v float64) string {
	return fmt.Sprintf("%d¢", toCentsInt(v))
}

func toCentsInt(v float64) int {
	if v <= 0 {
		return 0
	}
	return int(v*100 + 0.5)
}

func titleName(s string) string {
	parts := strings.Fields(strings.TrimSpace(s))
	for i, part := range parts {
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func easternLocation() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.FixedZone("ET", -5*60*60)
	}
	return loc
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
