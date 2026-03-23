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
	mu         sync.Mutex
	out        io.Writer
	lastSig    string
	lastRender time.Time
}

func NewDisplay() *Display {
	return &Display{out: os.Stdout}
}

func (d *Display) Render(state DisplayState) {
	d.mu.Lock()
	defer d.mu.Unlock()

	sig := displaySignature(state)
	if sig == d.lastSig && time.Since(d.lastRender) < 10*time.Second {
		return
	}

	var b strings.Builder
	b.WriteString(ansiClear)
	b.WriteString(fmt.Sprintf("┌──────────────────────────────────────────────────────────────────────────────┐\n"))
	b.WriteString(fmt.Sprintf("│  POLYBOT SPORTS ARB  │  %-12s  │  matched: %-3d games            │\n", state.Now.Format("15:04:05.000"), state.MatchedCount))
	b.WriteString("└──────────────────────────────────────────────────────────────────────────────┘\n\n")
	b.WriteString(fmt.Sprintf("EXECUTOR  mode:%-7s  executed:%-4d  skipped:%-4d  pnl:%6s  open:%-2d",
		execModeText(state.Exec),
		state.Exec.Executed,
		state.Exec.Skipped,
		moneyCell(state.Exec.TodayPnL),
		state.Exec.OpenPositions,
	))
	if state.Exec.Halted {
		b.WriteString("  " + ansiRed + "HALTED" + ansiReset)
	}
	b.WriteString("\n")
	if state.Exec.Reason != "" {
		b.WriteString(fmt.Sprintf("reason: %s\n\n", state.Exec.Reason))
	} else {
		b.WriteString("\n")
	}
	b.WriteString("MATCHED GAMES                    K_YES  K_NO   P_YES  P_NO   BEST    EDGE\n")
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
		bestCombined := 1.0
		bestEdge := 0.0
		if len(row.Opportunities) > 0 {
			bestCombined = row.Opportunities[0].Combined
			bestEdge = row.Opportunities[0].NetProfit
			for _, arb := range row.Opportunities[1:] {
				if arb.Combined < bestCombined {
					bestCombined = arb.Combined
				}
				if arb.NetProfit > bestEdge {
					bestEdge = arb.NetProfit
				}
			}
		}
		line := fmt.Sprintf("[%-5s] %-24s %5s  %5s  %5s  %5s  %6s  %6s",
			row.Game.League,
			truncate(fmt.Sprintf("%s vs %s", titleName(row.Game.HomeTeam), titleName(row.Game.AwayTeam)), 24),
			priceCell(row.Game.Kalshi.YesAsk),
			priceCell(row.Game.Kalshi.NoAsk),
			priceCell(row.Game.Poly.YesAsk),
			priceCell(row.Game.Poly.NoAsk),
			inlineSpread(bestCombined),
			priceCell(bestEdge),
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
			b.WriteString(fmt.Sprintf("   BUY %s %s@%s + %s %s@%s = %s combined\n",
				strings.ToUpper(arb.Leg1Side),
				titleName(arb.Leg1Team),
				arb.Leg1Platform,
				strings.ToUpper(arb.Leg2Side),
				titleName(arb.Leg2Team),
				arb.Leg2Platform,
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
	b.WriteString(fmt.Sprintf("  opps seen: %d  │  csv: %s  │  log: %s  │  ctrl+c to exit\n", state.OppsSeen, state.CSVPath, state.LogPath))

	_, _ = io.WriteString(d.out, b.String())
	d.lastSig = sig
	d.lastRender = time.Now()
}

func buildDisplayState(matcher *GameMatcher, stats *SessionStats, exec *Executor, csvPath, logPath string) DisplayState {
	matches := matcher.GetAllMatches()
	rows := make([]DisplayRow, 0, len(matches))
	var opps []ArbOpportunity
	for _, match := range matches {
		found := Check(match)
		bestSpread := 0.0
		for _, arb := range found {
			if arb.NetProfit > bestSpread {
				bestSpread = arb.NetProfit
			}
		}
		rows = append(rows, DisplayRow{
			Game:          match,
			Opportunities: found,
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
		CSVPath:       csvPath,
		LogPath:       logPath,
		Exec:          exec.Snapshot(),
	}
}

func displaySignature(state DisplayState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "matched=%d|opps=%d|csv=%s|log=%s|halted=%t|reason=%s|exec=%d|skip=%d|complete=%d|open=%d|pnl=%.4f",
		state.MatchedCount,
		state.OppsSeen,
		state.CSVPath,
		state.LogPath,
		state.Exec.Halted,
		state.Exec.Reason,
		state.Exec.Executed,
		state.Exec.Skipped,
		state.Exec.Completed,
		state.Exec.OpenPositions,
		state.Exec.TodayPnL,
	)
	for _, row := range state.Rows {
		fmt.Fprintf(&b, "|%s:%s:%s:%.4f:%.4f:%.4f:%.4f",
			row.Game.League,
			row.Game.HomeTeam,
			row.Game.AwayTeam,
			row.Game.Kalshi.YesAsk,
			row.Game.Kalshi.NoAsk,
			row.Game.Poly.YesAsk,
			row.Game.Poly.NoAsk,
		)
	}
	for _, arb := range state.Opportunities {
		fmt.Fprintf(&b, "|arb:%s:%s:%s:%.4f:%.4f",
			arb.Game.League,
			arb.Game.HomeTeam,
			arb.Direction,
			arb.Leg1Price,
			arb.Leg2Price,
		)
	}
	return b.String()
}

func execModeText(snapshot ExecutorSnapshot) string {
	if snapshot.Halted {
		return "HALTED"
	}
	return "DRYRUN"
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

func moneyCell(v float64) string {
	return fmt.Sprintf("$%.2f", v)
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
