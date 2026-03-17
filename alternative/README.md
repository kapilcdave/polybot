# arbscanner — Kalshi/Polymarket Sports Arbitrage Bot

Terminal-based arb scanner and executor in Go.

## Setup

```bash
# 1. Install Go 1.22+
# https://go.dev/dl/

# 2. Get dependencies
go mod tidy

# 3. Set env vars
export KALSHI_API_KEY=your_kalshi_api_key
export POLY_API_KEY=your_poly_clob_api_key
export POLY_PRIVATE_KEY=your_eth_wallet_private_key_hex  # WITHOUT 0x prefix

# Optional tuning
export MIN_EDGE_PCT=2.5        # minimum arb edge % after fees (default: 2.5)
export MAX_ORDER_USDC=500      # max dollars per leg (default: 500)
export MAX_DAILY_LOSS=200      # halt if daily loss exceeds this (default: 200)
export DRY_RUN=true            # default: true — set false for live trading

# 4. Build
go build -o arbscanner .

# 5. Run
./arbscanner
```

## Architecture

```
main.go
  ├── feed/kalshi.go       paginated seed (cursor-based) → WS delta updates
  ├── feed/polymarket.go   paginated seed (offset-based) → WS delta updates
  ├── feed/feed.go         PriceMap: thread-safe market store, keyed by canonical key
  ├── feed/normalize.go    team alias table, market type extraction, date parsing
  ├── matcher/matcher.go   100ms scan loop, arb detection, fee-adjusted edge calc
  ├── executor/
  │   ├── executor.go      two-leg state machine, circuit breakers, daily P&L
  │   ├── kalshi_order.go  limit order + fill polling + cancel on timeout
  │   ├── poly_order.go    FOK order + EIP-712 signing
  │   └── idempotency.go   SQLite journal — every state written BEFORE HTTP call
  └── display/display.go   ANSI terminal display, redraws at 250ms
```

## Key design decisions

### Why SQLite before every HTTP call?
If the process crashes between leg1 and leg2, you have an open position.
Writing to SQLite *before* sending the HTTP request means on restart you
can detect `LEG1_FILLED` state and attempt leg2 recovery.

### Why FOK (fill-or-kill) on Polymarket?
Arb edges are thin and time-sensitive. A resting limit order that takes
30 seconds to fill is useless — prices will have converged. FOK means
"fill right now at this price or don't fill at all". Clean failure, no
dangling positions.

### Why not fuzzy matching?
See `feed/normalize.go`. Canonical key = `SORT(teamA, teamB):date:type`.
Deterministic, debuggable, O(1) lookup. When a match fails, the missing
alias is obvious from the log. Add it to `teamAliases` and it's fixed.

### Circuit breakers
- **Leg2 failure**: any unhedged position trips the breaker immediately.
  Trading halts until manually reviewed and reset.
- **Daily loss limit**: configurable via `MAX_DAILY_LOSS`.
- **Stale prices**: markets not updated in 60s are skipped.
- **Opp age**: opportunities older than 2s are skipped (prices moved).

## Adding the go-ethereum dependency

The Polymarket order signing requires EIP-712. Add to go.mod:

```
require github.com/ethereum/go-ethereum v1.13.14
```

Then `go mod tidy`. If you don't want the dependency, you can use
Polymarket's REST API key auth instead (no signing required for some endpoints).

## Expanding the alias table

When you see in the log:
```
"no match" key="" title="Charlotte Hornets vs Detroit Pistons"
```

Add to `feed/normalize.go`:
```go
"hornets": "CHA", "charlotte hornets": "CHA",
"pistons": "DET", "detroit pistons": "DET",
```

Both already exist. For any team that doesn't match, add the variant
and restart. The seed will immediately pick it up.

## Order sizing

Kalshi: contracts = floor(MAX_ORDER_USDC / yesPrice / 0.01)
Each contract pays $0.01 at expiry. So at 50¢, $500 buys 1000 contracts.

Polymarket: directly in USDC. MAX_ORDER_USDC is the spend amount.

## Monitoring open positions

```bash
sqlite3 arbbot.db "SELECT id, arb_key, state, kalshi_fill_price, poly_fill_price, realized_pnl FROM arb_orders ORDER BY created_at DESC LIMIT 20;"
```

Any rows with state `LEG2_FAILED` need manual intervention on Kalshi.

## Logs

All structured logs go to `arbbot.log` (JSON). Terminal shows a summary.

```bash
tail -f arbbot.log | jq .
```
