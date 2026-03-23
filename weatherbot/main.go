package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	var (
		mode    string
		jsonOut bool
	)
	flag.StringVar(&mode, "mode", "scan", "one of: scan, watch, dashboard")
	flag.BoolVar(&jsonOut, "json", false, "print scan output as JSON")
	flag.Parse()

	cfg, err := LoadConfig(".env")
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	strategy := NewStrategy(cfg)

	switch mode {
	case "scan":
		strategy.ScanOnce(ctx)
		printState(os.Stdout, strategy.Snapshot(), jsonOut)
	case "watch":
		runWatch(ctx, strategy, jsonOut, cfg.PollInterval)
	case "dashboard":
		runDashboard(ctx, stop, strategy, cfg.DashboardAddr)
	default:
		log.Fatalf("unsupported mode %q", mode)
	}
}

func runWatch(ctx context.Context, strategy *Strategy, jsonOut bool, every time.Duration) {
	strategy.ScanOnce(ctx)
	printState(os.Stdout, strategy.Snapshot(), jsonOut)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Println()
			strategy.ScanOnce(ctx)
			printState(os.Stdout, strategy.Snapshot(), jsonOut)
		}
	}
}

func runDashboard(ctx context.Context, stop context.CancelFunc, strategy *Strategy, addr string) {
	server := ServeDashboard(strategy, addr)
	go func() {
		log.Printf("dashboard listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("dashboard error: %v", err)
			stop()
		}
	}()
	go strategy.Run(ctx)
	<-ctx.Done()
	_ = server.Shutdown(context.Background())
	log.Printf("weatherbot stopped")
}

func printState(out *os.File, state AppState, jsonOut bool) {
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(state)
		return
	}
	RenderScan(out, state)
}
