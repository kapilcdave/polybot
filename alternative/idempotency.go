package executor

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// OrderState is the state machine for a two-legged arb order.
//
//	PENDING
//	  └─► LEG1_SENT     (HTTP request fired for leg 1)
//	        ├─► LEG1_FILLED   (leg 1 confirmed filled)
//	        │     └─► LEG2_SENT    (HTTP request fired for leg 2)
//	        │           ├─► COMPLETE      (both legs filled — arb closed)
//	        │           └─► LEG2_FAILED   (ALERT: open risk position, needs manual hedge)
//	        └─► LEG1_FAILED   (clean — no position opened)
type OrderState string

const (
	StatePending     OrderState = "PENDING"
	StateLeg1Sent    OrderState = "LEG1_SENT"
	StateLeg1Filled  OrderState = "LEG1_FILLED"
	StateLeg2Sent    OrderState = "LEG2_SENT"
	StateComplete    OrderState = "COMPLETE"
	StateLeg1Failed  OrderState = "LEG1_FAILED"
	StateLeg2Failed  OrderState = "LEG2_FAILED" // DANGER: open position
)

// ArbOrder is the persisted record for one arb attempt.
type ArbOrder struct {
	ID             string     // UUID, primary key
	ArbKey         string     // canonical market key
	State          OrderState
	DryRun         bool
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// Leg 1: Kalshi
	KalshiSide      string  // "yes" or "no"
	KalshiTicker    string
	KalshiPrice     float64
	KalshiCount     int     // number of contracts
	KalshiOrderID   string  // filled by exchange
	KalshiFillPrice float64

	// Leg 2: Polymarket
	PolySide        string  // "yes" or "no"
	PolyConditionID string
	PolyPrice       float64
	PolySize        float64 // USDC
	PolyOrderID     string
	PolyFillPrice   float64

	// P&L
	RealizedPnL     float64
	FailureReason   string
}

// Journal persists every order state transition to SQLite.
// This is the crash-safety layer — on restart, we can recover
// any orders in LEG1_FILLED/LEG2_SENT state and attempt to close them.
type Journal struct {
	db *sql.DB
}

func OpenJournal(path string) (*Journal, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_synchronous=FULL")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &Journal{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS arb_orders (
		id               TEXT PRIMARY KEY,
		arb_key          TEXT NOT NULL,
		state            TEXT NOT NULL,
		dry_run          INTEGER NOT NULL DEFAULT 1,
		created_at       TEXT NOT NULL,
		updated_at       TEXT NOT NULL,

		kalshi_side      TEXT,
		kalshi_ticker    TEXT,
		kalshi_price     REAL,
		kalshi_count     INTEGER,
		kalshi_order_id  TEXT,
		kalshi_fill_price REAL,

		poly_side        TEXT,
		poly_condition_id TEXT,
		poly_price       REAL,
		poly_size        REAL,
		poly_order_id    TEXT,
		poly_fill_price  REAL,

		realized_pnl     REAL,
		failure_reason   TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_arb_orders_state ON arb_orders(state);
	CREATE INDEX IF NOT EXISTS idx_arb_orders_key   ON arb_orders(arb_key);

	CREATE TABLE IF NOT EXISTS daily_pnl (
		date       TEXT PRIMARY KEY,
		realized   REAL NOT NULL DEFAULT 0,
		unrealized REAL NOT NULL DEFAULT 0
	);
	`)
	return err
}

// Insert writes a new order in PENDING state. Must be called before
// any HTTP request is made. This is the idempotency guarantee.
func (j *Journal) Insert(o *ArbOrder) error {
	o.CreatedAt = time.Now()
	o.UpdatedAt = time.Now()
	_, err := j.db.Exec(`
	INSERT INTO arb_orders
	(id, arb_key, state, dry_run, created_at, updated_at,
	 kalshi_side, kalshi_ticker, kalshi_price, kalshi_count,
	 poly_side, poly_condition_id, poly_price, poly_size)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.ID, o.ArbKey, string(o.State), boolToInt(o.DryRun),
		o.CreatedAt.Format(time.RFC3339), o.UpdatedAt.Format(time.RFC3339),
		o.KalshiSide, o.KalshiTicker, o.KalshiPrice, o.KalshiCount,
		o.PolySide, o.PolyConditionID, o.PolyPrice, o.PolySize,
	)
	return err
}

// Transition updates the state and any new fields. Wrapped in a transaction
// so a crash between state write and HTTP response doesn't corrupt state.
func (j *Journal) Transition(id string, state OrderState, update func(*ArbOrder)) error {
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Load current record
	o, err := j.loadTx(tx, id)
	if err != nil {
		return fmt.Errorf("load order %s: %w", id, err)
	}

	// Apply caller's mutations
	if update != nil {
		update(o)
	}
	o.State = state
	o.UpdatedAt = time.Now()

	_, err = tx.Exec(`
	UPDATE arb_orders SET
		state=?, updated_at=?,
		kalshi_order_id=?, kalshi_fill_price=?,
		poly_order_id=?, poly_fill_price=?,
		realized_pnl=?, failure_reason=?
	WHERE id=?`,
		string(o.State), o.UpdatedAt.Format(time.RFC3339),
		o.KalshiOrderID, o.KalshiFillPrice,
		o.PolyOrderID, o.PolyFillPrice,
		o.RealizedPnL, o.FailureReason,
		id,
	)
	if err != nil {
		return err
	}

	// Update daily P&L
	if state == StateComplete || state == StateLeg2Failed {
		today := time.Now().Format("2006-01-02")
		_, err = tx.Exec(`
		INSERT INTO daily_pnl (date, realized) VALUES (?, ?)
		ON CONFLICT(date) DO UPDATE SET realized = realized + excluded.realized`,
			today, o.RealizedPnL,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// OpenPositions returns all orders in a dangerous state (leg1 filled, leg2 pending).
// Called on startup to detect and attempt recovery.
func (j *Journal) OpenPositions() ([]*ArbOrder, error) {
	rows, err := j.db.Query(`
	SELECT id, arb_key, state, dry_run, created_at, updated_at,
	       kalshi_side, kalshi_ticker, kalshi_price, kalshi_count, kalshi_order_id, kalshi_fill_price,
	       poly_side, poly_condition_id, poly_price, poly_size, poly_order_id, poly_fill_price,
	       realized_pnl, failure_reason
	FROM arb_orders WHERE state IN (?, ?, ?)`,
		string(StateLeg1Filled), string(StateLeg2Sent), string(StateLeg2Failed),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOrders(rows)
}

// TodayPnL returns today's realized P&L.
func (j *Journal) TodayPnL() (float64, error) {
	today := time.Now().Format("2006-01-02")
	var pnl float64
	err := j.db.QueryRow(`SELECT COALESCE(realized, 0) FROM daily_pnl WHERE date = ?`, today).Scan(&pnl)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return pnl, err
}

// RecentOrders returns the last N orders for display.
func (j *Journal) RecentOrders(n int) ([]*ArbOrder, error) {
	rows, err := j.db.Query(`
	SELECT id, arb_key, state, dry_run, created_at, updated_at,
	       kalshi_side, kalshi_ticker, kalshi_price, kalshi_count, kalshi_order_id, kalshi_fill_price,
	       poly_side, poly_condition_id, poly_price, poly_size, poly_order_id, poly_fill_price,
	       realized_pnl, failure_reason
	FROM arb_orders ORDER BY created_at DESC LIMIT ?`, n,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOrders(rows)
}

func (j *Journal) loadTx(tx *sql.Tx, id string) (*ArbOrder, error) {
	row := tx.QueryRow(`
	SELECT id, arb_key, state, dry_run, created_at, updated_at,
	       kalshi_side, kalshi_ticker, kalshi_price, kalshi_count, kalshi_order_id, kalshi_fill_price,
	       poly_side, poly_condition_id, poly_price, poly_size, poly_order_id, poly_fill_price,
	       realized_pnl, failure_reason
	FROM arb_orders WHERE id=?`, id)
	orders, err := scanOrders(rowsFromRow(row))
	if err != nil {
		return nil, err
	}
	if len(orders) == 0 {
		return nil, fmt.Errorf("not found")
	}
	return orders[0], nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func boolToInt(b bool) int {
	if b { return 1 }
	return 0
}

type rowScanner interface {
	Scan(dest ...any) error
}

// rowsFromRow wraps a *sql.Row to satisfy a Rows-like interface for scanOrders.
type singleRow struct{ r *sql.Row }
func rowsFromRow(r *sql.Row) *singleRows { return &singleRows{row: r, done: false} }
type singleRows struct {
	row  *sql.Row
	done bool
}
func (s *singleRows) Next() bool        { if s.done { return false }; s.done = true; return true }
func (s *singleRows) Scan(v ...any) error { return s.row.Scan(v...) }
func (s *singleRows) Close() error       { return nil }

type rowsIface interface {
	Next() bool
	Scan(...any) error
	Close() error
}

func scanOrders(rows rowsIface) ([]*ArbOrder, error) {
	defer rows.Close()
	var out []*ArbOrder
	for rows.Next() {
		var o ArbOrder
		var state, createdAt, updatedAt string
		var dryRunInt int
		err := rows.Scan(
			&o.ID, &o.ArbKey, &state, &dryRunInt, &createdAt, &updatedAt,
			&o.KalshiSide, &o.KalshiTicker, &o.KalshiPrice, &o.KalshiCount,
			&o.KalshiOrderID, &o.KalshiFillPrice,
			&o.PolySide, &o.PolyConditionID, &o.PolyPrice, &o.PolySize,
			&o.PolyOrderID, &o.PolyFillPrice,
			&o.RealizedPnL, &o.FailureReason,
		)
		if err != nil {
			return nil, err
		}
		o.State = OrderState(state)
		o.DryRun = dryRunInt == 1
		o.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		o.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, &o)
	}
	return out, nil
}
