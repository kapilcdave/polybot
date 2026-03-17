package executor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

// PolyOrderClient handles order placement on Polymarket's CLOB.
// Polymarket orders require EIP-712 signing with your wallet's private key.
type PolyOrderClient struct {
	apiKey     string
	privateKey *ecdsa.PrivateKey
	baseURL    string
	log        *slog.Logger
	http       *http.Client
}

func NewPolyOrderClient(apiKey, privateKeyHex, baseURL string, log *slog.Logger) (*PolyOrderClient, error) {
	if privateKeyHex == "" {
		return &PolyOrderClient{
			apiKey:  apiKey,
			baseURL: baseURL,
			log:     log,
			http:    &http.Client{Timeout: 10 * time.Second},
		}, nil
	}

	privKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	return &PolyOrderClient{
		apiKey:     apiKey,
		privateKey: privKey,
		baseURL:    baseURL,
		log:        log,
		http:       &http.Client{Timeout: 10 * time.Second},
	}, nil
}

type polyOrderRequest struct {
	OrderType  string `json:"orderType"` // "GTC" (good-till-cancel) or "FOK" (fill-or-kill)
	TokenID    string `json:"tokenID"`   // CLOB token ID for YES or NO outcome
	Side       string `json:"side"`      // "BUY"
	Price      string `json:"price"`     // decimal string e.g. "0.65"
	Size       string `json:"size"`      // USDC size
	Signature  string `json:"signature"` // EIP-712 signature
	Expiration string `json:"expiration"` // unix ts, "0" = no expiration
}

type polyOrderResponse struct {
	OrderID string `json:"orderID"`
	Status  string `json:"status"` // "matched", "live", "canceled"
	TakerAmount string `json:"takerAmount"`
	MakerAmount string `json:"makerAmount"`
}

// PlaceOrder places a FOK (fill-or-kill) order on Polymarket.
// FOK is preferred for arb — if it doesn't fill immediately at the price, cancel.
// clientOrderID is used for logging/journal only (Poly uses order IDs from response).
func (c *PolyOrderClient) PlaceOrder(
	ctx context.Context,
	clientOrderID string,
	tokenID string, // CLOB token ID for the outcome we're buying
	side string,    // "yes" or "no" — mapped to BUY on the correct token
	price float64,
	sizeUSDC float64,
	dryRun bool,
) (orderID string, fillPrice float64, err error) {

	if dryRun {
		c.log.Info("[DRY RUN] polymarket order",
			"tokenID", tokenID, "side", side,
			"price", price, "sizeUSDC", sizeUSDC,
			"clientOrderID", clientOrderID,
		)
		return "DRY_" + clientOrderID, price, nil
	}

	// Build and sign the order
	req := polyOrderRequest{
		OrderType:  "FOK",
		TokenID:    tokenID,
		Side:       "BUY",
		Price:      strconv.FormatFloat(price, 'f', 4, 64),
		Size:       strconv.FormatFloat(sizeUSDC, 'f', 2, 64),
		Expiration: "0",
	}

	if c.privateKey != nil {
		sig, err := c.signOrder(req)
		if err != nil {
			return "", 0, fmt.Errorf("sign order: %w", err)
		}
		req.Signature = sig
	}

	var resp polyOrderResponse
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err = c.postOrder(ctx, req)
		if err == nil {
			break
		}
		if isNonRetriable(err) {
			return "", 0, err
		}
		if attempt < 3 {
			c.log.Warn("poly order attempt failed, retrying", "attempt", attempt, "err", err)
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
	}
	if err != nil {
		return "", 0, fmt.Errorf("poly order failed after 3 attempts: %w", err)
	}

	if resp.Status == "canceled" || resp.Status == "" {
		return "", 0, fmt.Errorf("poly FOK order not filled (status=%s)", resp.Status)
	}

	// Calculate actual fill price from taker/maker amounts
	if resp.TakerAmount != "" && resp.MakerAmount != "" {
		taker, _ := strconv.ParseFloat(resp.TakerAmount, 64)
		maker, _ := strconv.ParseFloat(resp.MakerAmount, 64)
		if maker > 0 {
			fillPrice = taker / maker
		}
	}
	if fillPrice == 0 {
		fillPrice = price
	}

	return resp.OrderID, fillPrice, nil
}

func (c *PolyOrderClient) postOrder(ctx context.Context, req polyOrderRequest) (polyOrderResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return polyOrderResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/order", bytes.NewReader(body))
	if err != nil {
		return polyOrderResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return polyOrderResponse{}, err
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))

	switch httpResp.StatusCode {
	case 200, 201:
		var out polyOrderResponse
		if err := json.Unmarshal(respBody, &out); err != nil {
			return polyOrderResponse{}, fmt.Errorf("decode: %w", err)
		}
		return out, nil
	case 400:
		return polyOrderResponse{}, &nonRetriableError{fmt.Sprintf("bad request: %s", respBody)}
	case 401, 403:
		return polyOrderResponse{}, &nonRetriableError{"auth error"}
	case 429:
		return polyOrderResponse{}, fmt.Errorf("rate limited")
	default:
		return polyOrderResponse{}, fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, respBody)
	}
}

// signOrder creates a minimal EIP-712 signature for a Polymarket order.
// NOTE: This is a simplified version. In production, use Polymarket's
// official Go SDK or the full EIP-712 typed data signing spec from their docs:
// https://docs.polymarket.com/#signing-orders
func (c *PolyOrderClient) signOrder(req polyOrderRequest) (string, error) {
	if c.privateKey == nil {
		return "", fmt.Errorf("no private key configured")
	}

	// Simplified: hash the order fields and sign.
	// Real implementation needs full EIP-712 domain separator + struct hash.
	msgBytes := []byte(fmt.Sprintf("%s:%s:%s:%s:%s",
		req.TokenID, req.Side, req.Price, req.Size, req.Expiration,
	))
	hash := crypto.Keccak256Hash(msgBytes)
	sig, err := crypto.Sign(hash.Bytes(), c.privateKey)
	if err != nil {
		return "", err
	}
	// Convert to hex
	return fmt.Sprintf("0x%x", sig), nil
}

// WalletAddress returns the Ethereum address derived from the private key.
func (c *PolyOrderClient) WalletAddress() string {
	if c.privateKey == nil {
		return ""
	}
	pub := c.privateKey.Public().(*ecdsa.PublicKey)
	addr := crypto.PubkeyToAddress(*pub)
	return addr.Hex()
}

// Stub for go-ethereum dependency — remove once go-ethereum is in go.mod
type ecdsaStub struct{}
var _ = (*big.Int)(nil)
