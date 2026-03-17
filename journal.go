package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type OrderJournal struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	byID     map[string]ArbOrder
	todayPnL float64
}

func OpenOrderJournal(path string) (*OrderJournal, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}

	j := &OrderJournal{
		path: path,
		file: file,
		byID: make(map[string]ArbOrder),
	}

	if err := j.loadLocked(); err != nil {
		file.Close()
		return nil, err
	}
	return j, nil
}

func (j *OrderJournal) loadLocked() error {
	if _, err := j.file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek journal: %w", err)
	}
	scanner := bufio.NewScanner(j.file)
	today := time.Now().UTC().Format("2006-01-02")
	for scanner.Scan() {
		var order ArbOrder
		if err := json.Unmarshal(scanner.Bytes(), &order); err != nil {
			return fmt.Errorf("decode journal: %w", err)
		}
		j.byID[order.ID] = order
		if order.State == StateComplete && order.UpdatedAt.UTC().Format("2006-01-02") == today {
			j.todayPnL += order.NetProfit
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan journal: %w", err)
	}
	_, err := j.file.Seek(0, 2)
	return err
}

func (j *OrderJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.file.Close()
}

func (j *OrderJournal) Upsert(order ArbOrder) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	prev, hadPrev := j.byID[order.ID]
	if hadPrev && prev.State == StateComplete && prev.UpdatedAt.UTC().Format("2006-01-02") == time.Now().UTC().Format("2006-01-02") {
		j.todayPnL -= prev.NetProfit
	}
	if order.State == StateComplete && order.UpdatedAt.UTC().Format("2006-01-02") == time.Now().UTC().Format("2006-01-02") {
		j.todayPnL += order.NetProfit
	}
	j.byID[order.ID] = order

	line, err := json.Marshal(order)
	if err != nil {
		return fmt.Errorf("encode journal row: %w", err)
	}
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append journal row: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("sync journal: %w", err)
	}
	return nil
}

func (j *OrderJournal) OpenPositions() []ArbOrder {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]ArbOrder, 0)
	for _, order := range j.byID {
		switch order.State {
		case StateLeg1Filled, StateLeg2Sent, StateLeg2Failed:
			out = append(out, order)
		}
	}
	return out
}

func (j *OrderJournal) TodayPnL() float64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.todayPnL
}
