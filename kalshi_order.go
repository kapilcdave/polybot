package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type KalshiOrderClient struct {
	apiKeyID string
	key      *rsa.PrivateKey
	baseURL  string
	http     *http.Client
}

func NewKalshiOrderClient(apiKeyID string, key *rsa.PrivateKey) *KalshiOrderClient {
	return &KalshiOrderClient{
		apiKeyID: apiKeyID,
		key:      key,
		baseURL:  kalshiRESTBases[0],
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type KalshiOrderRequest struct {
	Ticker   string `json:"ticker"`
	ClientID string `json:"client_order_id"`
	Type     string `json:"type"`
	Action   string `json:"action"`
	Side     string `json:"side"`
	Count    int    `json:"count"`
	YesPrice int    `json:"yes_price,omitempty"`
	NoPrice  int    `json:"no_price,omitempty"`
}

func (k *KalshiOrderClient) PlaceOrder(ctx context.Context, order KalshiOrderRequest) ([]byte, error) {
	body, err := json.Marshal(order)
	if err != nil {
		return nil, fmt.Errorf("[kalshi] encode order: %w", err)
	}

	var lastErr error
	for _, base := range kalshiRESTBases {
		respBody, err := k.doSignedJSON(ctx, http.MethodPost, base, "/portfolio/orders", body)
		if err == nil {
			return respBody, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (k *KalshiOrderClient) doSignedJSON(ctx context.Context, method, baseURL, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	headers, err := kalshiSignedHeaders(k.apiKeyID, k.key, method, path)
	if err != nil {
		return nil, err
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("User-Agent", "gabagool-sports/1.0")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := k.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("[kalshi] order status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}
