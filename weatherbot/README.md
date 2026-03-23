# Weatherbot

Read-only weather-temperature scanner and paper trader for Kalshi `KXHIGH*` markets and matching Polymarket contracts.

## What It Does

- Scans Kalshi weather high-temperature series for:
  - `KXHIGHNY`
  - `KXHIGHCHI`
  - `KXHIGHMIA`
  - `KXHIGHLAX`
  - `KXHIGHDEN`
- Scans Polymarket active markets and filters weather/temperature questions for the same cities
- Pulls Open-Meteo ensemble forecasts and estimates `P(high >= threshold)`
- Flags trades when model edge exceeds the configured threshold
- Sizes paper trades with fractional Kelly
- Prints scan results in the terminal by default
- Can run in `watch` mode or serve a simple dashboard at `http://localhost:8088`

## What It Does Not Do Yet

- Live order placement
- Settlement reconciliation
- Calibration / Brier score updates from resolved markets
- A React frontend build pipeline
- The BTC microstructure strategy

Those are separate concerns. For a real bot, weather and BTC should not share the same execution loop or risk limits.

## Run

Default one-shot CLI scan:

```bash
cd weatherbot
go run .
```

Continuous terminal mode:

```bash
go run . -mode watch
```

JSON output:

```bash
go run . -json
```

Dashboard mode:

```bash
go run . -mode dashboard
```

Optional `.env`:

```dotenv
POLL_INTERVAL=5m
EDGE_THRESHOLD=0.08
KELLY_FRACTION=0.15
MAX_TRADE_USD=100
STARTING_BANKROLL=10000
SIMULATION_MODE=true
DASHBOARD_ADDR=:8088
FORECAST_DAYS=10
HTTP_TIMEOUT=10s
KALSHI_AUTH_TOKEN=your_jwt_token_here   # required for production Kalshi API
```

## Suggested Next Improvements

1. Align each city to the exact settlement station used by the market, not just metro coordinates.
2. Add resolved-market ingestion so calibration, Brier score, and PnL are based on actual outcomes.
3. Cache Polymarket order books and forecast responses to reduce API load.
4. Add spread / liquidity filters, not just edge threshold.
5. Split weather and BTC into separate projects or services with independent bankroll controls.
6. Replace Open-Meteo-only probabilities with NBM/ECMWF + calibration against NWS settlement (NBM stub added).
