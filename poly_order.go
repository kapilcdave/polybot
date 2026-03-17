package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	polyChainID                  = 137
	polyDomainName               = "Polymarket CTF Exchange"
	polyDomainVersion            = "1"
	polyDefaultVerifyingContract = "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E"
)

var (
	eip712DomainTypeHash = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	polyOrderTypeHash    = crypto.Keccak256Hash([]byte("Order(uint256 salt,address maker,address signer,address taker,uint256 tokenId,uint256 makerAmount,uint256 takerAmount,uint256 expiration,uint256 nonce,uint256 feeRateBps,uint8 side,uint8 signatureType)"))
)

type PolyOrderClient struct {
	apiKeyID          string
	apiKey            string
	apiSecret         string
	baseURL           string
	verifyingContract common.Address
	privateKey        *ecdsa.PrivateKey
	signerAddress     common.Address
	http              *http.Client
}

type PolyOrder struct {
	Salt          *big.Int
	Maker         common.Address
	Signer        common.Address
	Taker         common.Address
	TokenID       *big.Int
	MakerAmount   *big.Int
	TakerAmount   *big.Int
	Expiration    *big.Int
	Nonce         *big.Int
	FeeRateBps    *big.Int
	Side          uint8
	SignatureType uint8
}

type PolySignedOrder struct {
	MarketID      string `json:"market,omitempty"`
	TokenID       string `json:"tokenID"`
	MakerAmount   string `json:"makerAmount"`
	TakerAmount   string `json:"takerAmount"`
	Side          uint8  `json:"side"`
	FeeRateBps    string `json:"feeRateBps"`
	Nonce         string `json:"nonce"`
	Expiration    string `json:"expiration"`
	Salt          string `json:"salt"`
	Maker         string `json:"maker"`
	Signer        string `json:"signer"`
	Taker         string `json:"taker"`
	SignatureType uint8  `json:"signatureType"`
	Signature     string `json:"signature"`
}

func NewPolyOrderClient(cfg Config) (*PolyOrderClient, error) {
	client := &PolyOrderClient{
		apiKeyID:          cfg.PolyAPIKeyID,
		apiKey:            cfg.PolyAPIKey,
		apiSecret:         cfg.PolyAPISecret,
		baseURL:           polyRESTBase,
		verifyingContract: common.HexToAddress(polyDefaultVerifyingContract),
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}

	if cfg.PolyPrivateKey == "" {
		return client, nil
	}

	keyHex := strings.TrimPrefix(cfg.PolyPrivateKey, "0x")
	privateKey, err := crypto.HexToECDSA(keyHex)
	if err != nil {
		return nil, fmt.Errorf("[poly] parse private key: %w", err)
	}

	client.privateKey = privateKey
	client.signerAddress = crypto.PubkeyToAddress(privateKey.PublicKey)
	return client, nil
}

func (p *PolyOrderClient) SignOrder(order PolyOrder) (string, error) {
	if p.privateKey == nil {
		return "", fmt.Errorf("[poly] no private key configured")
	}

	domainHash := p.domainSeparatorHash()
	structHash := p.orderStructHash(order)
	digest := crypto.Keccak256Hash([]byte{0x19, 0x01}, domainHash.Bytes(), structHash.Bytes())

	sig, err := crypto.Sign(digest.Bytes(), p.privateKey)
	if err != nil {
		return "", fmt.Errorf("[poly] sign order: %w", err)
	}
	if sig[64] < 27 {
		sig[64] += 27
	}
	return "0x" + hex.EncodeToString(sig), nil
}

func (p *PolyOrderClient) BuildSignedOrder(marketID string, order PolyOrder) (PolySignedOrder, error) {
	if order.Maker == (common.Address{}) {
		order.Maker = p.signerAddress
	}
	if order.Signer == (common.Address{}) {
		order.Signer = p.signerAddress
	}

	signature, err := p.SignOrder(order)
	if err != nil {
		return PolySignedOrder{}, err
	}

	return PolySignedOrder{
		MarketID:      marketID,
		TokenID:       bigString(order.TokenID),
		MakerAmount:   bigString(order.MakerAmount),
		TakerAmount:   bigString(order.TakerAmount),
		Side:          order.Side,
		FeeRateBps:    bigString(order.FeeRateBps),
		Nonce:         bigString(order.Nonce),
		Expiration:    bigString(order.Expiration),
		Salt:          bigString(order.Salt),
		Maker:         order.Maker.Hex(),
		Signer:        order.Signer.Hex(),
		Taker:         order.Taker.Hex(),
		SignatureType: order.SignatureType,
		Signature:     signature,
	}, nil
}

func (p *PolyOrderClient) PlaceOrder(ctx context.Context, payload PolySignedOrder) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("[poly] encode order: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/order", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gabagool-sports/1.0")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	if p.apiKeyID != "" {
		req.Header.Set("POLY-API-KEY-ID", p.apiKeyID)
	}
	if p.apiSecret != "" {
		req.Header.Set("POLY-API-SECRET", p.apiSecret)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("[poly] order status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

func (p *PolyOrderClient) domainSeparatorHash() common.Hash {
	return crypto.Keccak256Hash(
		eip712DomainTypeHash.Bytes(),
		crypto.Keccak256([]byte(polyDomainName)),
		crypto.Keccak256([]byte(polyDomainVersion)),
		padUint256(big.NewInt(polyChainID)),
		padAddress(p.verifyingContract),
	)
}

func (p *PolyOrderClient) orderStructHash(order PolyOrder) common.Hash {
	return crypto.Keccak256Hash(
		polyOrderTypeHash.Bytes(),
		padUint256(valueOrZero(order.Salt)),
		padAddress(order.Maker),
		padAddress(order.Signer),
		padAddress(order.Taker),
		padUint256(valueOrZero(order.TokenID)),
		padUint256(valueOrZero(order.MakerAmount)),
		padUint256(valueOrZero(order.TakerAmount)),
		padUint256(valueOrZero(order.Expiration)),
		padUint256(valueOrZero(order.Nonce)),
		padUint256(valueOrZero(order.FeeRateBps)),
		padUint8(order.Side),
		padUint8(order.SignatureType),
	)
}

func valueOrZero(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	return v
}

func bigString(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

func padUint256(v *big.Int) []byte {
	if v == nil {
		v = big.NewInt(0)
	}
	out := make([]byte, 32)
	return v.FillBytes(out)
}

func padUint8(v uint8) []byte {
	out := make([]byte, 32)
	out[31] = v
	return out
}

func padAddress(addr common.Address) []byte {
	out := make([]byte, 32)
	copy(out[12:], addr.Bytes())
	return out
}
