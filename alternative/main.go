package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/you/arbbot/config"
	"github.com/you/arbbot/display"
	"github.com/you/arbbot/executor"
	"github.com/you/arbbot/feed"
	"github.com/you/arbbot/matcher"
)

func main() {
	// ── Logger ────────────────────────────────────────────────────────────────
	logFile, err := os.OpenFile("arbbot.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	// Log to file only — terminal display handles stdout
	log := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	if cfg.KalshiAPIKey == "" || cfg.PolyAPIKey == "" {
		fmt.Fprintf(os.Stderr, `
Missing API keys. Set environment variables:
  export KALSHI_API_KEY=your_key
  export POLY_API_KEY=your_key
  export POLY_PRIVATE_KEY=your_eth_private_key_hex
  export DRY_RUN=false   # only when ready to trade live

`)
		os.Exit(1)
	}

	// ── Context + shutdown ────────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Info("shutdown signal received", "signal", sig)
		cancel()
	}()

	// ── Components ────────────────────────────────────────────────────────────
	priceMap := feed.NewPriceMap()

	kalshiFeed := feed.NewKalshiFeed(
		cfg.KalshiAPIKey, cfg.KalshiBaseURL, cfg.KalshiWSURL,
		priceMap, log,
	)
	polyFeed := feed.NewPolyFeed(
		cfg.PolyAPIKey, cfg.PolyBaseURL, cfg.PolyWSURL,
		priceMap, log,
	)

	m := matcher.New(priceMap, cfg, log)

	journal, err := executor.OpenJournal(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open order journal: %v\n", err)
		os.Exit(1)
	}

	kalshiOrders := executor.NewKalshiOrderClient(cfg.KalshiAPIKey, cfg.KalshiBaseURL, log)
	polyOrders, err := executor.NewPolyOrderClient(cfg.PolyAPIKey, cfg.PolyPrivateKey, cfg.PolyBaseURL, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "poly order client error: %v\n", err)
		os.Exit(1)
	}

	exec := executor.New(cfg, journal, kalshiOrders, polyOrders, log)

	term := display.New(priceMap, exec)

	// ── Startup banner ────────────────────────────────────────────────────────
	display.PrintStartupBanner(cfg.DryRun)

	// ── Recovery: check for open positions from previous run ──────────────────
	fmt.Print("  Checking for open positions from previous run...")
	if err := exec.RecoverOpenPositions(ctx); err != nil {
		fmt.Printf(" %sFAILED: %v%s\n", "\033[31m", err, "\033[0m")
	} else {
		fmt.Print(" OK\n")
	}
	fmt.Println()

	// ── Start feeds ───────────────────────────────────────────────────────────
	go kalshiFeed.Run(ctx)
	go polyFeed.Run(ctx)

	// Forward feed events to terminal log
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-kalshiFeed.Events:
				term.AddLog(fmt.Sprintf("[kalshi] %s", ev.Msg))
			case ev := <-polyFeed.Events:
				term.AddLog(fmt.Sprintf("[poly] %s", ev.Msg))
			}
		}
	}()

	// Wait for at least one feed to seed before starting matcher
	// In production you'd want both seeded, but tolerate partial for resilience
	fmt.Print("  Waiting for initial seed (Kalshi)...")
	waitForSeed(ctx, kalshiFeed.Events, "seed_done")
	fmt.Println(" done")

	fmt.Print("  Waiting for initial seed (Polymarket)...")
	waitForSeed(ctx, polyFeed.Events, "seed_done")
	fmt.Println(" done\n")

	// ── Start matcher + executor ───────────────────────────────────────────────
	go m.Run(ctx)
	go exec.Run(ctx, m.Opps)

	// Forward detected opps to terminal
	go func() {
		for opp := range m.Opps {
			term.AddOpp(opp)
		}
	}()

	// ── Start terminal display ────────────────────────────────────────────────
	term.Start()
	stopDisplay := make(chan struct{})
	go term.RunRedrawLoop(stopDisplay)

	// ── Wait for shutdown ─────────────────────────────────────────────────────
	<-ctx.Done()
	close(stopDisplay)
	time.Sleep(100 * time.Millisecond) // let final redraw finish
	term.Stop()

	fmt.Println("\n  Shutdown complete. Check arbbot.log for full history.")

	// Print final P&L
	if pnl, err := journal.TodayPnL(); err == nil {
		pnlStr := fmt.Sprintf("$%.2f", pnl)
		if pnl >= 0 {
			fmt.Printf("  Today's realized P&L: \033[32m%s\033[0m\n", pnlStr)
		} else {
			fmt.Printf("  Today's realized P&L: \033[31m%s\033[0m\n", pnlStr)
		}
	}
	fmt.Println()
}

// waitForSeed blocks until a "seed_done" event appears on the events channel
// or context is cancelled.
func waitForSeed(ctx context.Context, events <-chan feed.FeedEvent, eventType string) {
	timeout := time.After(120 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			fmt.Print(" (timed out — continuing anyway)")
			return
		case ev := <-events:
			if ev.Type == eventType {
				return
			}
		}
	}
}
