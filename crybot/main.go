/*
Crybot hunts directional arbitrage between Kalshi 15-minute crypto markets,
Polymarket 15-minute crypto markets, and Binance spot.

Window math

	The shared key is `(crypto, window_start)`, where:

		window_start = (now.Unix() / 900) * 900

	Example:

		now := time.Now().UTC()
		windowStart := (now.Unix() / 900) * 900
		slug := fmt.Sprintf("%s-updown-15m-%d", "btc", windowStart)

The Polymarket market can then be fetched via:

	GET https://gamma-api.polymarket.com/markets?slug=btc-updown-15m-{window_start}

Kalshi and Polymarket are both mapped into the same `(crypto, window_start)` key.
Kalshi is keyed by parsing the crypto and expiration of the incoming market ticker /
market lifecycle payload, and Polymarket is keyed directly from the slug math.

Polymarket is read-only in this bot. It is used only for signal discovery and price
comparison. The bot never places Polymarket orders.
*/
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	ossignal "os/signal"
	"strings"
	"syscall"
	"time"

	"crybot/config"
	"crybot/internal/broker"
	"crybot/internal/logging"
	"crybot/internal/state"
)

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		panic(err)
	}

	logger := logging.New(cfg.LogLevel)
	ctx, cancel := ossignal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	stateUpdates := make(chan state.Update, 2048)
	windowUpdates := make(chan state.EventWindow, 2048)
	errs := make(chan feedError, 64)

	manager := state.NewManager(logger)

	binanceBroker := broker.NewBinanceBroker(cfg, logger)
	polymarketBroker := broker.NewPolymarketBroker(cfg, logger)
	kalshiBroker, err := broker.NewKalshiBroker(cfg, logger)
	if err != nil {
		panic(err)
	}

	go manager.Run(ctx, stateUpdates, windowUpdates)
	go printSnapshots(ctx, cfg.Symbols, windowUpdates)

	go runFeed(ctx, logger, "binance", errs, func(runCtx context.Context) error {
		return binanceBroker.Run(runCtx, stateUpdates)
	})
	go runFeed(ctx, logger, "polymarket", errs, func(runCtx context.Context) error {
		return polymarketBroker.Run(runCtx, stateUpdates)
	})
	go runFeed(ctx, logger, "kalshi", errs, func(runCtx context.Context) error {
		return kalshiBroker.Run(runCtx, stateUpdates)
	})

	go func() {
		for feedErr := range errs {
			logger.Warn("feed error",
				slog.String("feed", feedErr.name),
				slog.String("error", feedErr.err.Error()),
			)
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown requested")
	<-time.After(250 * time.Millisecond)
}

type feedError struct {
	name string
	err  error
}

func runFeed(ctx context.Context, logger *slog.Logger, name string, errs chan<- feedError, fn func(context.Context) error) {
	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil {
			logger.Info("feed stopped", slog.String("feed", name))
			return
		}

		logger.Info("starting feed", slog.String("feed", name))
		err := fn(ctx)
		if ctx.Err() != nil {
			logger.Info("feed stopped", slog.String("feed", name))
			return
		}
		if err != nil {
			errs <- feedError{name: name, err: err}
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Info("feed stopped", slog.String("feed", name))
			return
		case <-timer.C:
		}
	}
}

func printSnapshots(ctx context.Context, symbols []string, updates <-chan state.EventWindow) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	latest := make(map[string]state.EventWindow)
	for {
		select {
		case <-ctx.Done():
			return
		case window, ok := <-updates:
			if !ok {
				return
			}
			currentWindow := state.WindowStart(time.Now().UTC())
			if window.WindowStart == currentWindow {
				latest[strings.ToLower(window.Crypto)] = window
			}
		case now := <-ticker.C:
			currentWindow := state.WindowStart(now.UTC())
			fmt.Printf("\n[%s] window_start=%d\n", now.UTC().Format(time.RFC3339), currentWindow)
			for _, symbol := range symbols {
				crypto := strings.ToLower(symbol)
				window, ok := latest[crypto]
				if !ok || window.WindowStart != currentWindow {
					fmt.Printf("%s binance=n/a kalshi_yes=n/a kalshi_no=n/a polymarket_up=n/a polymarket_down=n/a\n", strings.ToUpper(crypto))
					continue
				}

				spreadUp := abs(window.KalshiYesPrice - window.PolymarketUpPrice)
				spreadDown := abs(window.KalshiNoPrice - window.PolymarketDownPrice)
				maxSpread := spreadUp
				if spreadDown > maxSpread {
					maxSpread = spreadDown
				}

				fmt.Printf(
					"%s binance=%s kalshi_yes=%s kalshi_no=%s polymarket_up=%s polymarket_down=%s spread=%s",
					strings.ToUpper(crypto),
					formatSpot(window.BinanceSpotPrice),
					formatProb(window.KalshiYesPrice),
					formatProb(window.KalshiNoPrice),
					formatProb(window.PolymarketUpPrice),
					formatProb(window.PolymarketDownPrice),
					formatProb(maxSpread),
				)
				if maxSpread >= 0.10 {
					fmt.Print(" SPREAD DETECTED")
				}
				fmt.Println()
			}
		}
	}
}

func formatSpot(value float64) string {
	if value <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.4f", value)
}

func formatProb(value float64) string {
	if value <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.4f", value)
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
