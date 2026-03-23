package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

func main() {
	flag.BoolVar(&debugMatching, "debug-matching", false, "log canonical seed keys and unmatched parse reasons")
	flag.Parse()

	startedAt := time.Now()
	cfg, err := LoadConfig(".env")
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	closeLog, err := initFileLogging(cfg.LogPath)
	if err != nil {
		log.Printf("logging setup warning: %v", err)
	} else {
		defer closeLog()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	kalshi, err := NewKalshiClient(cfg.KalshiAPIKeyID, cfg.KalshiPrivateKeyPath)
	if err != nil {
		log.Fatalf("[kalshi] init: %v", err)
	}
	kalshi.WithContext(ctx)

	poly := NewPolyClient()
	poly.WithContext(ctx)

	kalshiMarkets, err := kalshi.FetchSportsMarkets()
	if err != nil {
		log.Fatalf("[kalshi] fetch markets: %v", err)
	}
	polyMarkets, err := poly.FetchSportsMarkets()
	if err != nil {
		log.Fatalf("[poly] fetch markets: %v", err)
	}
	log.Printf("[kalshi] discovered %d sports markets", len(kalshiMarkets))
	log.Printf("[poly] discovered %d sports markets", len(polyMarkets))
	log.Printf("[config] arb_threshold=%.4f min_edge_pct=%.2f dry_run=%t", cfg.ArbThreshold, cfg.MinEdgePct*100, cfg.DryRun)

	if err := kalshi.Connect(); err != nil {
		log.Printf("[kalshi] initial websocket connect failed: %v", err)
	}
	if err := poly.Connect(); err != nil {
		log.Printf("[poly] initial websocket connect failed: %v", err)
	}

	matcher := NewGameMatcher()
	matcher.Refresh("KALSHI", kalshiMarkets)
	matcher.Refresh("POLY", polyMarkets)
	log.Printf("[matcher] initial matches: %d", len(matcher.GetAllMatches()))

	if err := kalshi.SubscribeToMarkets(extractMarketIDs(kalshiMarkets)); err != nil {
		log.Printf("[kalshi] subscribe failed: %v", err)
	}
	if err := poly.Subscribe(poly.TokenIDs()); err != nil {
		log.Printf("[poly] subscribe failed: %v", err)
	}

	loggerCSV, err := NewCSVLogger("sports_arb_log.csv")
	if err != nil {
		log.Fatalf("logger error: %v", err)
	}
	defer loggerCSV.Close()
	journal, err := OpenOrderJournal(cfg.JournalPath)
	if err != nil {
		log.Fatalf("journal error: %v", err)
	}
	defer journal.Close()
	executor := NewExecutor(cfg, NewKalshiOrderClient(cfg.KalshiAPIKeyID, kalshi.PrivateKey()), mustPolyOrderClient(cfg), journal)

	display := NewDisplay()
	stats := NewSessionStats(startedAt)
	updates := make(chan MarketUpdate, 512)
	refreshes := make(chan refreshResult, 16)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	safeGo(ctx, "kalshi-listener", func() { kalshi.Listen(updates) })
	safeGo(ctx, "poly-listener", func() { poly.Listen(updates) })
	safeGo(ctx, "market-refresh", func() { runMarketRefresh(ctx, kalshi, poly, refreshes, 5*time.Minute) })

	renderTicker := time.NewTicker(500 * time.Millisecond)
	defer renderTicker.Stop()

	for {
		select {
		case update := <-updates:
			matcher.Update(update)
			processArbs(ctx, matcher, stats, loggerCSV, executor)
		case result := <-refreshes:
			if result.err != nil {
				log.Printf("%s", result.err)
				continue
			}
			matcher.Refresh(result.platform, result.markets)
			if result.platform == "KALSHI" {
				if err := kalshi.SubscribeToMarkets(extractMarketIDs(result.markets)); err != nil {
					log.Printf("[kalshi] resubscribe failed: %v", err)
				}
			} else {
				if err := poly.Subscribe(poly.TokenIDs()); err != nil {
					log.Printf("[poly] resubscribe failed: %v", err)
				}
			}
		case <-renderTicker.C:
			display.Render(buildDisplayState(matcher, stats, executor, loggerCSV.path, cfg.LogPath))
		case <-ctx.Done():
			printSummary(matcher, stats)
			return
		case <-sigCh:
			cancel()
			printSummary(matcher, stats)
			return
		}
	}
}

type refreshResult struct {
	platform string
	markets  []SportsMarket
	err      error
}

func runMarketRefresh(ctx context.Context, kalshi *KalshiClient, poly *PolyClient, out chan<- refreshResult, every time.Duration) {
	defer recoverGoroutine("market-refresh")

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			kalshiMarkets, err := kalshi.FetchSportsMarkets()
			select {
			case out <- refreshResult{platform: "KALSHI", markets: kalshiMarkets, err: err}:
			case <-ctx.Done():
				return
			}

			polyMarkets, err := poly.FetchSportsMarkets()
			select {
			case out <- refreshResult{platform: "POLY", markets: polyMarkets, err: err}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func processArbs(ctx context.Context, matcher *GameMatcher, stats *SessionStats, csvLogger *CSVLogger, executor *Executor) {
	matches := matcher.GetAllMatches()
	for _, match := range matches {
		for _, arb := range Check(match) {
			if stats.Record(arb) {
				if err := csvLogger.Log(arb); err != nil {
					log.Printf("logger error: %v", err)
				}
				executor.HandleOpportunity(ctx, arb)
			}
		}
	}
}

func extractMarketIDs(markets []SportsMarket) []string {
	out := make([]string, 0, len(markets))
	for _, market := range markets {
		out = append(out, market.MarketID)
	}
	sort.Strings(out)
	return out
}

func printSummary(matcher *GameMatcher, stats *SessionStats) {
	totalOpps, bestSpread, _, startedAt := stats.Snapshot()
	duration := time.Since(startedAt).Round(time.Second)
	fmt.Printf("\nmatched games: %d\n", len(matcher.GetAllMatches()))
	fmt.Printf("arb opportunities found: %d\n", totalOpps)
	fmt.Printf("best spread seen: %d¢\n", toCentsInt(bestSpread))
	fmt.Printf("session duration: %s\n", duration)
}

func safeGo(ctx context.Context, name string, fn func()) {
	go func() {
		defer recoverGoroutine(name)
		select {
		case <-ctx.Done():
			return
		default:
			fn()
		}
	}()
}

func recoverGoroutine(name string) {
	if r := recover(); r != nil {
		log.Printf("[%s] recovered panic: %v", name, r)
	}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func initFileLogging(path string) (func() error, error) {
	if strings.TrimSpace(path) == "" {
		return func() error { return nil }, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", path, err)
	}
	log.SetOutput(file)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	return file.Close, nil
}

func mustPolyOrderClient(cfg Config) *PolyOrderClient {
	client, err := NewPolyOrderClient(cfg)
	if err != nil {
		log.Fatalf("[poly] order client init: %v", err)
	}
	return client
}
