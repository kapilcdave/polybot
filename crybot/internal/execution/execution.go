package execution

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"crybot/config"
	"crybot/internal/broker"
	"crybot/internal/signal"
)

type Report struct {
	Venue       string
	Crypto      string
	WindowStart int64
	Side        string
	Status      string
	Price       float64
	Size        float64
	DryRun      bool
}

type Worker struct {
	cfg    config.AppConfig
	logger *slog.Logger
}

func NewWorker(cfg config.AppConfig, logger *slog.Logger) *Worker {
	return &Worker{cfg: cfg, logger: logger}
}

func (w *Worker) Run(ctx context.Context, kalshi broker.KalshiOrderPlacer, in <-chan signal.Signal, out chan<- Report) {
	defer close(out)
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-in:
			if !ok {
				return
			}

			order := broker.KalshiOrder{
				Ticker:        sig.KalshiTicker,
				Side:          sideToKalshi(sig.Side),
				Action:        "buy",
				Count:         int(math.Max(1, w.cfg.DefaultOrderSize)),
				YesPriceFloat: signalToPrice(sig),
				ClientOrderID: fmt.Sprintf("%s-%d-%d", sig.Crypto, sig.WindowStart, time.Now().UTC().UnixNano()),
			}

			if w.cfg.DryRun {
				select {
				case out <- Report{
					Venue:       "kalshi",
					Crypto:      sig.Crypto,
					WindowStart: sig.WindowStart,
					Side:        sig.Side,
					Status:      "dry_run",
					Price:       order.YesPriceFloat,
					Size:        float64(order.Count),
					DryRun:      true,
				}:
				case <-ctx.Done():
				}
				continue
			}

			resp, err := kalshi.PlaceOrder(ctx, order)
			status := "submitted"
			if err != nil {
				status = "rejected"
				w.logger.Error("kalshi order failed", slog.String("error", err.Error()))
			} else if resp.Status != "" {
				status = resp.Status
			}

			select {
			case out <- Report{
				Venue:       "kalshi",
				Crypto:      sig.Crypto,
				WindowStart: sig.WindowStart,
				Side:        sig.Side,
				Status:      status,
				Price:       order.YesPriceFloat,
				Size:        float64(order.Count),
				DryRun:      false,
			}:
			case <-ctx.Done():
			}
		}
	}
}

func sideToKalshi(side string) string {
	if side == "KalshiDown" {
		return "no"
	}
	return "yes"
}

func signalToPrice(sig signal.Signal) float64 {
	price := sig.ReferencePrice + 0.02
	if price > 0.99 {
		price = 0.99
	}
	if price < 0.01 {
		price = 0.01
	}
	return price
}
