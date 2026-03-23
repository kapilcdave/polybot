package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

type CSVLogger struct {
	mu     sync.Mutex
	file   *os.File
	writer *csv.Writer
	path   string
}

func NewCSVLogger(path string) (*CSVLogger, error) {
	_, statErr := os.Stat(path)
	newFile := os.IsNotExist(statErr)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open csv log: %w", err)
	}

	logger := &CSVLogger{
		file:   file,
		writer: csv.NewWriter(file),
		path:   path,
	}

	if newFile {
		header := []string{
			"timestamp", "league", "home_team", "away_team", "game_time",
			"direction", "leg1_platform", "leg1_side", "leg1_team", "leg1_price_cents",
			"leg2_platform", "leg2_side", "leg2_team", "leg2_price_cents",
			"combined_cents", "gross_cents", "fees_cents", "net_cents",
			"minutes_until_game",
		}
		if err := logger.writer.Write(header); err != nil {
			file.Close()
			return nil, fmt.Errorf("write csv header: %w", err)
		}
		logger.writer.Flush()
		if err := logger.writer.Error(); err != nil {
			file.Close()
			return nil, fmt.Errorf("flush csv header: %w", err)
		}
	}

	return logger, nil
}

func (l *CSVLogger) Log(arb ArbOpportunity) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	minutesUntil := int(time.Until(arb.ExpiresAt).Minutes())
	record := []string{
		arb.SeenAt.UTC().Format(time.RFC3339),
		arb.Game.League,
		arb.Game.HomeTeam,
		arb.Game.AwayTeam,
		arb.ExpiresAt.UTC().Format(time.RFC3339),
		arb.Direction,
		arb.Leg1Platform,
		arb.Leg1Side,
		arb.Leg1Team,
		strconv.Itoa(toCentsInt(arb.Leg1Price)),
		arb.Leg2Platform,
		arb.Leg2Side,
		arb.Leg2Team,
		strconv.Itoa(toCentsInt(arb.Leg2Price)),
		strconv.Itoa(toCentsInt(arb.Combined)),
		strconv.Itoa(toCentsInt(arb.GrossProfit)),
		strconv.Itoa(toCentsInt(arb.KalshiFee + arb.PolyFee)),
		strconv.Itoa(toCentsInt(arb.NetProfit)),
		strconv.Itoa(minutesUntil),
	}

	if err := l.writer.Write(record); err != nil {
		return fmt.Errorf("write csv row: %w", err)
	}
	l.writer.Flush()
	if err := l.writer.Error(); err != nil {
		return fmt.Errorf("flush csv row: %w", err)
	}
	return nil
}

func (l *CSVLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writer.Flush()
	if err := l.writer.Error(); err != nil {
		_ = l.file.Close()
		return err
	}
	return l.file.Close()
}
