package display

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/you/arbbot/executor"
	"github.com/you/arbbot/feed"
	"github.com/you/arbbot/matcher"
)

// ANSI escape codes
const (
	clrReset  = "\033[0m"
	clrRed    = "\033[31m"
	clrGreen  = "\033[32m"
	clrYellow = "\033[33m"
	clrCyan   = "\033[36m"
	clrGray   = "\033[90m"
	clrBold   = "\033[1m"
	clrBlink  = "\033[5m"

	cursorUp    = "\033[%dA"
	clearLine   = "\033[2K"
	cursorStart = "\033[0G"
	hideCursor  = "\033[?25l"
	showCursor  = "\033[?25h"
)

// Terminal is a lock-based terminal display that redraws a fixed region.
type Terminal struct {
	mu      sync.Mutex
	pm      *feed.PriceMap
	exec    *executor.Executor
	log     []string
	opps    []oppRecord
	events  []string
	lines   int // last number of lines drawn (for cursor repositioning)
	startAt time.Time
}

type oppRecord struct {
	opp  matcher.ArbOpportunity
	seen time.Time
}

func New(pm *feed.PriceMap, exec *executor.Executor) *Terminal {
	return &Terminal{
		pm:      pm,
		exec:    exec,
		startAt: time.Now(),
	}
}

func (t *Terminal) Start() {
	fmt.Print(hideCursor)
	t.draw()
}

func (t *Terminal) Stop() {
	fmt.Print(showCursor)
	fmt.Println()
}

// Redraw refreshes the terminal display. Call periodically (e.g. 500ms).
func (t *Terminal) Redraw() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.draw()
}

func (t *Terminal) AddLog(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ts := time.Now().Format("15:04:05")
	t.log = append(t.log, fmt.Sprintf("%s%s%s %s", clrGray, ts, clrReset, msg))
	if len(t.log) > 8 {
		t.log = t.log[len(t.log)-8:]
	}
}

func (t *Terminal) AddOpp(opp matcher.ArbOpportunity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.opps = append([]oppRecord{{opp: opp, seen: time.Now()}}, t.opps...)
	if len(t.opps) > 6 {
		t.opps = t.opps[:6]
	}
}

func (t *Terminal) AddEvent(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, msg)
	if len(t.events) > 5 {
		t.events = t.events[len(t.events)-5:]
	}
}

func (t *Terminal) draw() {
	// Move cursor up to overwrite previous frame
	if t.lines > 0 {
		fmt.Printf(cursorUp, t.lines)
	}

	var buf strings.Builder
	lineCount := 0

	line := func(format string, args ...any) {
		fmt.Fprintf(&buf, clearLine+cursorStart+format+"\n", args...)
		lineCount++
	}
	blank := func() { line("") }

	kalshiN, polyN, pairsN := t.pm.Stats()
	stats := t.exec.Stats
	uptime := time.Since(t.startAt).Round(time.Second)

	haltStr := ""
	if t.exec.IsHalted() {
		haltStr = clrRed + clrBold + clrBlink + " 🛑 HALTED" + clrReset
	}

	// Header
	line("%s%s ARBSCANNER%s%s  uptime:%s%s%s",
		clrBold, clrCyan, clrReset, haltStr,
		clrGray, uptime, clrReset)
	line("%s%s%s", clrCyan, strings.Repeat("─", 72), clrReset)

	// Feed status
	line("  FEEDS   kalshi:%s%d%s  poly:%s%d%s  pairs:%s%d%s",
		clrGreen, kalshiN, clrReset,
		clrGreen, polyN, clrReset,
		clrYellow, pairsN, clrReset,
	)

	// Executor stats
	pnlColor := clrGreen
	if stats.TodayPnL < 0 {
		pnlColor = clrRed
	}
	line("  EXEC     opps:%-5d  exec:%-5d  skip:%-5d  completed:%-5d  pnl:%s$%.2f%s",
		stats.TotalOpps, stats.Executed, stats.Skipped, stats.Completed,
		pnlColor, stats.TodayPnL, clrReset,
	)
	if stats.Leg2Failures > 0 {
		line("  %s⚠  LEG2 FAILURES: %d — OPEN POSITIONS REQUIRE REVIEW%s",
			clrRed+clrBold, stats.Leg2Failures, clrReset)
	}

	blank()

	// Recent arb opportunities
	line("  %sRECENT OPPS%s", clrBold, clrReset)
	if len(t.opps) == 0 {
		line("  %s(none yet)%s", clrGray, clrReset)
	}
	for _, rec := range t.opps {
		opp := rec.opp
		age := time.Since(rec.seen).Round(time.Millisecond)
		edgeColor := clrGreen
		if opp.NetEdge < 0.03 {
			edgeColor = clrYellow
		}

		kSide := "Y"
		if !opp.BuyKalshiYes {
			kSide = "N"
		}
		pSide := "Y"
		if !opp.BuyPolyYes {
			pSide = "N"
		}

		line("  %s%-28s%s  K:%s%.2f%s(%s) P:%s%.2f%s(%s)  edge:%s%+.2f%%%s  %s%s%s",
			clrCyan, shortKey(opp.Key), clrReset,
			clrGray, opp.KalshiPrice, clrReset, kSide,
			clrGray, opp.PolyPrice, clrReset, pSide,
			edgeColor, opp.NetEdge*100, clrReset,
			clrGray, age, clrReset,
		)
	}

	blank()

	// Feed events log
	line("  %sLOG%s", clrBold, clrReset)
	for _, msg := range t.log {
		line("  %s", msg)
	}

	// Pad to consistent height
	for lineCount < 22 {
		blank()
	}

	fmt.Print(buf.String())
	t.lines = lineCount
}

func shortKey(key string) string {
	if len(key) > 28 {
		return key[:25] + "..."
	}
	return key
}

// RunRedrawLoop redraws the terminal every 250ms.
func (t *Terminal) RunRedrawLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			t.Redraw()
		}
	}
}

// PrintStartupBanner prints once before the live display starts.
func PrintStartupBanner(dryRun bool) {
	fmt.Println()
	modeStr := clrGreen + "DRY RUN" + clrReset
	if !dryRun {
		modeStr = clrRed + clrBold + "⚠  LIVE TRADING" + clrReset
	}
	fmt.Printf("  %sARBSCANNER%s — Kalshi/Polymarket Sports Arbitrage\n", clrBold+clrCyan, clrReset)
	fmt.Printf("  Mode: %s\n", modeStr)
	fmt.Printf("  Press %sCtrl+C%s to exit gracefully\n\n", clrBold, clrReset)
	_ = os.Stdout.Sync()
}
