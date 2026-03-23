# Crybot

Crybot is a Go bot for hunting directional arbitrage across:

- Kalshi 15-minute crypto up/down markets
- Polymarket 15-minute crypto up/down markets
- Binance spot price for `BTC`, `ETH`, `SOL`, and `XRP`

The trading goal is directional edge, not cross-venue riskless arbitrage. Binance spot is treated as the reference signal. Polymarket is used only as a read-only signal feed. Orders are sent only to Kalshi, and only when `DRY_RUN=false`.

## Core idea

Each market window is indexed by the same shared key:

- `key = (crypto, window_start)`

The 15-minute window start is computed from UTC time using simple timestamp math:

```go
now := time.Now().UTC()
windowStart := (now.Unix() / 900) * 900
```

That same `windowStart` is then used to discover the correct market on each venue.

## Polymarket market discovery

Polymarket 15-minute crypto markets are discovered by slug math, not by scanning a large event list.

```go
slug := fmt.Sprintf("%s-updown-15m-%d", "btc", windowStart)
```

Example fetch:

```text
GET https://gamma-api.polymarket.com/markets?slug=btc-updown-15m-{window_start}
```

If the exact current slug is missing, the bot also tries:

- `window_start - 900`
- `window_start + 900`

This handles short publication lag or small clock mismatch.

## Kalshi market mapping

Kalshi markets are discovered from the live WebSocket feed. For each incoming crypto market:

1. Parse the underlying crypto from the market ticker.
2. Parse the expiration timestamp.
3. Compute:

```go
windowStart := (expirationTS / 900) * 900
```

4. Store that market under the same shared key:

- `(crypto, window_start)`

This lets Kalshi and Polymarket state land in the same `EventWindow`.

## Why this is directional, not free arbitrage

This bot explicitly does not assume the two venues settle the same way:

- Kalshi crypto contracts resolve using the CF Benchmarks real-time index averaged over the last 60 seconds.
- Polymarket 15-minute crypto markets resolve against the price to beat captured at the start of the 15-minute window.

That means price gaps between venues are useful as signal context, but not guaranteed free money.

## Architecture

The project is organized as:

```text
.
├── config/
│   └── config.go
├── internal/
│   ├── broker/
│   │   ├── binance_broker.go
│   │   ├── broker.go
│   │   ├── kalshi_broker.go
│   │   └── polymarket_broker.go
│   ├── execution/
│   │   └── execution.go
│   ├── logging/
│   │   └── logging.go
│   ├── signal/
│   │   └── signal.go
│   └── state/
│       └── state.go
├── go.mod
├── go.sum
├── main.go
└── README.md
```

Main goroutines:

- `binance_feed`: consumes Binance `bookTicker` updates and updates the current window spot price.
- `kalshi_feed`: consumes Kalshi WebSocket messages and maps 15-minute crypto markets into `(crypto, window_start)`.
- `polymarket_poller`: fetches Gamma markets by slug and subscribes to the Polymarket market WebSocket for token price updates.
- `state_manager`: owns the in-memory map of `EventWindow` objects.
- `signal_worker`: computes directional edge and emits `Signal` values.
- `execution_worker`: converts signals into Kalshi orders, respecting `DRY_RUN`.

There is no package-level mutable trading state. Coordination happens through channels.

## Trading behavior

The signal worker only considers windows when:

- Binance, Kalshi, and Polymarket data are all fresh
- more than 30 seconds remain in the current 15-minute window
- the bot has observed the window’s opening Binance reference during runtime
- expected edge remains above the configured fee threshold after fee and slippage adjustments

The current implementation:

- uses Binance spot as the directional reference
- compares Kalshi implied probability against Polymarket implied probability
- accounts for Polymarket fee impact in the signal calculation
- applies a slippage haircut before emitting a trade signal

The execution worker:

- logs orders only when `DRY_RUN=true`
- submits Kalshi orders only when `DRY_RUN=false`
- never submits Polymarket orders
- falls back to public Kalshi REST market polling for read-only market data when Kalshi WebSocket auth is not configured

## Configuration

Environment variables:

```text
BINANCE_API_KEY=
BINANCE_API_SECRET=
KALSHI_API_KEY=
KALSHI_API_SECRET_PATH=
POLYMARKET_API_KEY=
SYMBOLS=BTC,ETH,SOL,XRP
FEE_THRESHOLD=0.03
POLYMARKET_FEE_BPS=70
DRY_RUN=true
LOG_LEVEL=INFO
KALSHI_BASE_URL=https://api.elections.kalshi.com/trade-api/v2
KALSHI_WS_URL=wss://api.elections.kalshi.com/trade-api/ws/v2
POLYMARKET_GAMMA_URL=https://gamma-api.polymarket.com
POLYMARKET_MARKET_WS_URL=wss://ws-subscriptions-clob.polymarket.com/ws/market
POLYMARKET_ENABLE_WS=false
BINANCE_WS_URL=wss://stream.binance.com:9443/stream
POLYMARKET_POLL_INTERVAL=2s
MAX_SIGNAL_AGE=500ms
DEFAULT_ORDER_SIZE=1
KALSHI_TICKER_HINTS_BTC=KXBTCD,KXBTC,BTC
KALSHI_TICKER_HINTS_ETH=KXETHD,KXETH,ETH
KALSHI_TICKER_HINTS_SOL=KXSOLD,KXSOL,SOL
KALSHI_TICKER_HINTS_XRP=KXXRPD,KXXRP,XRP
```

Notes:

- `DRY_RUN` defaults to `true`.
- `POLYMARKET_API_KEY` is optional in the current read-only implementation.
- `POLYMARKET_ENABLE_WS` defaults to `false` because Gamma polling is the more robust baseline path.
- Kalshi authentication requires both `KALSHI_API_KEY` and `KALSHI_API_SECRET_PATH`.
- Kalshi crypto ticker formats can vary. If your feed uses different prefixes, adjust the `KALSHI_TICKER_HINTS_*` variables.

## Build and run

Build:

```bash
go build ./...
```

Run:

```bash
DRY_RUN=true go run .
```

To enable live Kalshi order submission:

```bash
DRY_RUN=false \
KALSHI_API_KEY=... \
KALSHI_API_SECRET_PATH=/path/to/private_key.pem \
go run .
```

## Important safety notes

- Polymarket is read-only in this bot.
- The bot does not assume settlement equivalence between Kalshi and Polymarket.
- The bot should be treated as a framework and starting point, not a production-hardened strategy engine.
- Live deployment should add stronger order sizing, position limits, duplicate-signal suppression, and venue-specific validation.

## Current limitations

- Kalshi ticker parsing relies on configurable symbol hints instead of a venue-published canonical crypto-market registry.
- The signal logic is intentionally simple and should be calibrated with real market observations before live use.
- No persistence layer is included.
- No backtesting harness is included.
- No hedge execution on Binance is included.

## Files to start with

- Entry point: [main.go](/home/kapil/source/polybot/crybot/main.go)
- State model: [internal/state/state.go](/home/kapil/source/polybot/crybot/internal/state/state.go)
- Signal logic: [internal/signal/signal.go](/home/kapil/source/polybot/crybot/internal/signal/signal.go)
- Kalshi broker: [internal/broker/kalshi_broker.go](/home/kapil/source/polybot/crybot/internal/broker/kalshi_broker.go)
- Polymarket broker: [internal/broker/polymarket_broker.go](/home/kapil/source/polybot/crybot/internal/broker/polymarket_broker.go)
