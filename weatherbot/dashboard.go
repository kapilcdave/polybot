package main

import (
	"encoding/json"
	"net/http"
)

func ServeDashboard(strategy *Strategy, addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(strategy.Snapshot())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(dashboardHTML))
	})
	return &http.Server{Addr: addr, Handler: mux}
}

const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Weatherbot Dashboard</title>
  <style>
    :root { color-scheme: dark; --bg:#09111a; --card:#102033; --fg:#e8f0f6; --muted:#8ea0b3; --accent:#66d9ef; --edge:#9be564; }
    body { margin:0; font-family: ui-sans-serif, system-ui, sans-serif; background: radial-gradient(circle at top, #15304d, #09111a 60%); color:var(--fg); }
    .wrap { padding:24px; display:grid; gap:16px; grid-template-columns: 1.3fr 1fr 1fr; }
    .card { background:rgba(16,32,51,.92); border:1px solid rgba(255,255,255,.08); border-radius:18px; padding:18px; box-shadow:0 10px 40px rgba(0,0,0,.25); }
    h1,h2 { margin:0 0 12px; }
    table { width:100%; border-collapse:collapse; font-size:14px; }
    th,td { text-align:left; padding:8px 6px; border-bottom:1px solid rgba(255,255,255,.06); vertical-align:top; }
    .muted { color:var(--muted); }
    .edge { color:var(--edge); font-weight:700; }
    @media (max-width: 980px) { .wrap { grid-template-columns:1fr; } }
  </style>
</head>
<body>
  <div class="wrap">
    <section class="card">
      <h1>Weather Temperature Scanner</h1>
      <div id="summary" class="muted">Loading...</div>
      <h2 style="margin-top:16px;">Opportunities</h2>
      <table id="opps"></table>
    </section>
    <section class="card">
      <h2>Simulation</h2>
      <div id="sim" class="muted"></div>
    </section>
    <section class="card">
      <h2>Errors</h2>
      <div id="errors" class="muted"></div>
    </section>
  </div>
  <script>
    async function refresh() {
      const res = await fetch('/api/state');
      const state = await res.json();
      document.getElementById('summary').textContent =
        'Last scan: ' + (state.last_scan.completed_at || 'n/a') +
        ' | markets: ' + state.last_scan.markets_seen +
        ' | forecasts: ' + state.last_scan.last_forecasts +
        ' | opps: ' + state.last_scan.opportunities.length;
      document.getElementById('sim').innerHTML =
        '<div>Bankroll: $' + state.simulation.bankroll_usd.toFixed(2) + '</div>' +
        '<div>Cash: $' + state.simulation.cash_usd.toFixed(2) + '</div>' +
        '<div>Equity: $' + state.simulation.equity_usd.toFixed(2) + '</div>' +
        '<div>Trades: ' + state.simulation.trades + '</div>';
      document.getElementById('errors').innerHTML =
        (state.last_scan.errors || []).map(e => '<div>' + e + '</div>').join('') || '<div>None</div>';
      const rows = ['<tr><th>Platform</th><th>City</th><th>Contract</th><th>Fair</th><th>Ask</th><th>Edge</th><th>Stake</th></tr>'];
      for (const opp of state.last_scan.opportunities || []) {
        rows.push('<tr><td>' + opp.market.platform + '</td><td>' + opp.market.city + '</td><td>' +
          opp.side.toUpperCase() + ' ' + opp.market.threshold_f.toFixed(1) + 'F</td><td>' +
          (opp.fair * 100).toFixed(1) + '%</td><td>' + (opp.ask * 100).toFixed(1) +
          '%</td><td class="edge">' + (opp.edge * 100).toFixed(1) + '%</td><td>$' +
          opp.stake_usd.toFixed(2) + '</td></tr>');
      }
      document.getElementById('opps').innerHTML = rows.join('');
    }
    refresh();
    setInterval(refresh, 5000);
  </script>
</body>
</html>`
