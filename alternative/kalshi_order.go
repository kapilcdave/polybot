package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// KalshiOrderClient handles order placement on Kalshi.
type KalshiOrderClient struct {
	apiKey  string
	baseURL string
	log     *slog.Logger
	http    *http.Client
}

func NewKalshiOrderClient(apiKey, baseURL string, log *slog.Logger) *KalshiOrderClient {
	return &KalshiOrderClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		log:     log,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

type kalshiOrderRequest struct {
	Ticker    string `json:"ticker"`
	ClientID  string `json:"client_order_id"` // idempotency key — our ArbOrder.ID + "_leg1"
	Type      string `json:"type"`            // "limit"
	Action    string `json:"action"`          // "buy"
	Side      string `json:"side"`            // "yes" or "no"
	Count     int    `json:"count"`           // number of contracts (each = $0.01 face)
	YesPrice  int    `json:"yes_price,omitempty"` // cents 1–99
	NoPrice   int    `json:"no_price,omitempty"`
}

type kalshiOrderResponse struct {
	Order struct {
		ID          string  `json:"order_id"`
		Status      string  `json:"status"` // "resting", "filled", "canceled"
		FilledPrice float64 `json:"filled_yes_price"` // cents
		FilledCount int     `json:"filled_count"`
	} `json:"order"`
}

// PlaceOrder places a limit order on Kalshi and waits for fill or timeout.
// clientOrderID must be unique per order — use ArbOrder.ID + "_k" for idempotency.
// Kalshi will reject duplicate client_order_ids, protecting against double sends.
func (c *KalshiOrderClient) PlaceOrder(
	ctx context.Context,
	clientOrderID string,
	ticker string,
	side string, // "yes" or "no"
	price float64, // 0.0–1.0
	contracts int, // number of $0.01 contracts
	dryRun bool,
) (orderID string, fillPrice float64, err error) {

	if dryRun {
		c.log.Info("[DRY RUN] kalshi order",
			"ticker", ticker, "side", side,
			"price", price, "contracts", contracts,
			"clientOrderID", clientOrderID,
		)
		return "DRY_" + clientOrderID, price, nil
	}

	priceInCents := int(price * 100)
	if priceInCents < 1 { priceInCents = 1 }
	if priceInCents > 99 { priceInCents = 99 }

	req := kalshiOrderRequest{
		Ticker:   ticker,
		ClientID: clientOrderID,
		Type:     "limit",
		Action:   "buy",
		Side:     side,
		Count:    contracts,
	}
	if side == "yes" {
		req.YesPrice = priceInCents
	} else {
		req.NoPrice = priceInCents
	}

	// Retry up to 3 times on transient errors, NOT on 400/409 (already filled etc.)
	var resp kalshiOrderResponse
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err = c.postOrder(ctx, req)
		if err == nil {
			break
		}
		if isNonRetriable(err) {
			return "", 0, fmt.Errorf("non-retriable order error: %w", err)
		}
		if attempt < 3 {
			c.log.Warn("kalshi order attempt failed, retrying",
				"attempt", attempt, "err", err)
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
	}
	if err != nil {
		return "", 0, fmt.Errorf("kalshi order failed after 3 attempts: %w", err)
	}

	orderID = resp.Order.ID
	fillPrice = resp.Order.FilledPrice / 100.0

	// If order is resting (not immediately filled), poll for fill
	if resp.Order.Status == "resting" {
		fillPrice, err = c.pollForFill(ctx, orderID, 30*time.Second)
		if err != nil {
			// Cancel the resting order to avoid position leaking
			_ = c.cancelOrder(ctx, orderID)
			return "", 0, fmt.Errorf("order did not fill in time, cancelled: %w", err)
		}
	}

	return orderID, fillPrice, nil
}

func (c *KalshiOrderClient) postOrder(ctx context.Context, req kalshiOrderRequest) (kalshiOrderResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return kalshiOrderResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/portfolio/orders", bytes.NewReader(body))
	if err != nil {
		return kalshiOrderResponse{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return kalshiOrderResponse{}, err
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))

	switch httpResp.StatusCode {
	case 200, 201:
		var out kalshiOrderResponse
		if err := json.Unmarshal(respBody, &out); err != nil {
			return kalshiOrderResponse{}, fmt.Errorf("decode response: %w", err)
		}
		return out, nil
	case 409:
		// Conflict = duplicate client_order_id = order already placed.
		// This is SAFE — parse the existing order from the response.
		var out kalshiOrderResponse
		json.Unmarshal(respBody, &out)
		return out, nil
	case 400:
		return kalshiOrderResponse{}, &nonRetriableError{fmt.Sprintf("bad request (400): %s", respBody)}
	case 401, 403:
		return kalshiOrderResponse{}, &nonRetriableError{fmt.Sprintf("auth error (%d)", httpResp.StatusCode)}
	case 429:
		return kalshiOrderResponse{}, fmt.Errorf("rate limited (429)")
	default:
		return kalshiOrderResponse{}, fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, respBody)
	}
}

func (c *KalshiOrderClient) pollForFill(ctx context.Context, orderID string, timeout time.Duration) (float64, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
			c.baseURL+"/portfolio/orders/"+orderID, nil)
		if err != nil {
			continue
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.http.Do(httpReq)
		if err != nil {
			continue
		}
		var out kalshiOrderResponse
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()

		if out.Order.Status == "filled" {
			return out.Order.FilledPrice / 100.0, nil
		}
		if out.Order.Status == "canceled" {
			return 0, fmt.Errorf("order was canceled externally")
		}
	}
	return 0, fmt.Errorf("fill timeout after %s", timeout)
}

func (c *KalshiOrderClient) cancelOrder(ctx context.Context, orderID string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/portfolio/orders/"+orderID, nil)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type nonRetriableError struct{ msg string }
func (e *nonRetriableError) Error() string { return e.msg }

func isNonRetriable(err error) bool {
	_, ok := err.(*nonRetriableError)
	return ok
}
