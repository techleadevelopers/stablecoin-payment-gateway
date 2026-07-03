package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/models"

	_ "github.com/lib/pq"
)

type DB struct {
	SQL *sql.DB
}

type OrderInput struct {
	ID                string
	Status            string
	AmountBRL         float64
	AmountUSDT        float64
	FeeBRL            float64
	PayoutBRL         float64
	Address           string
	Asset             string
	Network           string
	RateLocked        float64
	RateLockExpiresAt time.Time
	PixCpf            string
	PixPhone          string
	DerivationIndex   *int
}

type BuyOrder struct {
	ID                string          `json:"id"`
	Status            string          `json:"status"`
	AmountBRL         float64         `json:"amount_brl"`
	FeeBRL            float64         `json:"fee_brl"`
	PayoutBRL         float64         `json:"payout_brl"`
	CryptoAmount      float64         `json:"crypto_amount"`
	Asset             string          `json:"asset"`
	DestAddress       string          `json:"dest_address"`
	RateLocked        float64         `json:"rate_locked"`
	RateLockExpiresAt time.Time       `json:"rate_lock_expires_at"`
	PixPayload        json.RawMessage `json:"pix_payload,omitempty"`
	TxHashOut         *string         `json:"tx_hash_out,omitempty"`
	Error             *string         `json:"error,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type BuyOrderInput struct {
	Status            string
	AmountBRL         float64
	FeeBRL            float64
	PayoutBRL         float64
	CryptoAmount      float64
	Asset             string
	DestAddress       string
	RateLocked        float64
	RateLockExpiresAt time.Time
	PixPayload        any
}

type PixStats struct {
	Count int
	Total float64
}

type Sweep struct {
	ID         string
	ChildIndex int
	FromAddr   string
	ToAddr     string
	Amount     float64
	Status     string
	OrderID    *string
}

func ConnectPostgres(cfg *config.Config) (*DB, error) {
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL não configurado")
	}

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log.Println("Conectando ao banco de dados PostgreSQL...")
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	wrapped := &DB{SQL: db}
	if err := wrapped.InitSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	log.Println("Conexão com PostgreSQL estabelecida com sucesso!")
	return wrapped, nil
}

func (db *DB) Close() {
	if db.SQL != nil {
		_ = db.SQL.Close()
	}
}

func (db *DB) Ping(ctx context.Context) error {
	return db.SQL.PingContext(ctx)
}

func (db *DB) InitSchema(ctx context.Context) error {
	_, err := db.SQL.ExecContext(ctx, schemaSQL)
	return err
}

func (db *DB) CreateOrder(ctx context.Context, order OrderInput) (*models.Order, error) {
	if order.ID == "" {
		order.ID = NewID()
	}
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO orders (id, status, amount_brl, btc_amount, fee_brl, payout_brl, address, asset, network, rate_locked, rate_lock_expires_at, created_at, pix_cpf, pix_phone, derivation_index)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now(),$12,$13,$14)`,
		order.ID, order.Status, order.AmountBRL, order.AmountUSDT, order.FeeBRL, order.PayoutBRL, order.Address,
		order.Asset, order.Network, order.RateLocked, order.RateLockExpiresAt, nullableString(order.PixCpf), nullableString(order.PixPhone), order.DerivationIndex,
	)
	if err != nil {
		return nil, err
	}
	_ = db.AddEvent(ctx, order.ID, "order.created", map[string]any{"amountBRL": order.AmountBRL, "amountUSDT": order.AmountUSDT})
	return db.GetOrder(ctx, order.ID)
}

func (db *DB) GetOrder(ctx context.Context, id string) (*models.Order, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT id, status, amount_brl, btc_amount, COALESCE(fee_brl,0), COALESCE(payout_brl,0), address, asset, network,
		       rate_locked, rate_lock_expires_at, created_at, COALESCE(updated_at, created_at), tx_hash, error,
		       deposit_tx, deposit_amount, pix_cpf, pix_phone, derivation_index
		FROM orders WHERE id = $1`, id)
	return scanOrder(row)
}

func (db *DB) GetPendingOrders(ctx context.Context) ([]models.Order, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, status, amount_brl, btc_amount, COALESCE(fee_brl,0), COALESCE(payout_brl,0), address, asset, network,
		       rate_locked, rate_lock_expires_at, created_at, COALESCE(updated_at, created_at), tx_hash, error,
		       deposit_tx, deposit_amount, pix_cpf, pix_phone, derivation_index
		FROM orders WHERE status = 'aguardando_deposito'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (db *DB) UpdateOrderStatus(ctx context.Context, id, status string, extra map[string]any) error {
	txHash, _ := extra["txHash"].(string)
	errMsg, _ := extra["error"].(string)
	depositTx, _ := extra["depositTx"].(string)
	var depositAmount any
	if v, ok := extra["depositAmount"]; ok {
		depositAmount = v
	}
	_, err := db.SQL.ExecContext(ctx, `
		UPDATE orders SET status = $2,
			tx_hash = COALESCE(NULLIF($3,''), tx_hash),
			error = COALESCE(NULLIF($4,''), error),
			deposit_tx = COALESCE(NULLIF($5,''), deposit_tx),
			deposit_amount = COALESCE($6, deposit_amount),
			updated_at = now()
		WHERE id = $1`, id, status, txHash, errMsg, depositTx, depositAmount)
	if err != nil {
		return err
	}
	return db.AddEvent(ctx, id, "order."+status, extra)
}

func (db *DB) AddEvent(ctx context.Context, orderID, eventType string, payload any) error {
	raw, _ := json.Marshal(payload)
	_, err := db.SQL.ExecContext(ctx,
		`INSERT INTO order_events (id, order_id, type, payload) VALUES ($1,$2,$3,$4)`,
		NewID(), orderID, eventType, raw)
	return err
}

func (db *DB) HasEvent(ctx context.Context, orderID, eventType, field, value string) (bool, error) {
	var exists bool
	err := db.SQL.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM order_events WHERE order_id = $1 AND type = $2 AND payload ->> $3 = $4)`,
		orderID, eventType, field, value).Scan(&exists)
	return exists, err
}

func (db *DB) StatsPixLast24h(ctx context.Context, pixCpf, pixPhone string) (PixStats, error) {
	if pixCpf == "" && pixPhone == "" {
		return PixStats{}, nil
	}
	var count int
	var total float64
	err := db.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*)::int, COALESCE(SUM(amount_brl),0)::float8
		FROM orders
		WHERE created_at >= now() - interval '24 hours'
		  AND (($1 <> '' AND pix_cpf = $1) OR ($2 <> '' AND pix_phone = $2))`,
		pixCpf, pixPhone).Scan(&count, &total)
	return PixStats{Count: count, Total: total}, err
}

func (db *DB) CountCompletedOrdersForPix(ctx context.Context, pixCpf, pixPhone string) (int, error) {
	var count int
	err := db.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*)::int FROM orders
		WHERE status IN ('concluida','concluída')
		  AND (($1 <> '' AND pix_cpf = $1) OR ($2 <> '' AND pix_phone = $2))`,
		pixCpf, pixPhone).Scan(&count)
	return count, err
}

func (db *DB) NextDerivationIndex(ctx context.Context) (int, error) {
	var idx int
	err := db.SQL.QueryRowContext(ctx, `SELECT COALESCE(MAX(derivation_index), -1) + 1 FROM orders`).Scan(&idx)
	return idx, err
}

func (db *DB) GetCursor(ctx context.Context, network string) (int64, bool, error) {
	var block int64
	err := db.SQL.QueryRowContext(ctx, `SELECT last_block FROM onchain_cursor WHERE network = $1`, network).Scan(&block)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return block, err == nil, err
}

func (db *DB) SaveCursor(ctx context.Context, network string, lastBlock int64) error {
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO onchain_cursor (network, last_block) VALUES ($1,$2)
		ON CONFLICT (network) DO UPDATE SET last_block = EXCLUDED.last_block, updated_at = now()`,
		network, lastBlock)
	return err
}

func (db *DB) CreateSweep(ctx context.Context, childIndex int, fromAddr, toAddr string, amount float64, orderID *string) (*Sweep, error) {
	id := NewID()
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO sweeps (id, child_index, from_addr, to_addr, amount, status, order_id)
		VALUES ($1,$2,$3,$4,$5,'pending',$6)`,
		id, childIndex, fromAddr, toAddr, amount, orderID)
	if err != nil {
		return nil, err
	}
	return &Sweep{ID: id, ChildIndex: childIndex, FromAddr: fromAddr, ToAddr: toAddr, Amount: amount, Status: "pending", OrderID: orderID}, nil
}

func (db *DB) ListPendingSweeps(ctx context.Context) ([]Sweep, error) {
	rows, err := db.SQL.QueryContext(ctx, `SELECT id, child_index, from_addr, to_addr, amount::float8, status, order_id FROM sweeps WHERE status = 'pending'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sweep
	for rows.Next() {
		var s Sweep
		var orderID sql.NullString
		if err := rows.Scan(&s.ID, &s.ChildIndex, &s.FromAddr, &s.ToAddr, &s.Amount, &s.Status, &orderID); err != nil {
			return nil, err
		}
		if orderID.Valid {
			s.OrderID = &orderID.String
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (db *DB) MarkSweep(ctx context.Context, id, status, txHash string) error {
	_, err := db.SQL.ExecContext(ctx, `UPDATE sweeps SET status = $2, tx_hash = COALESCE(NULLIF($3,''), tx_hash), updated_at = now() WHERE id = $1`, id, status, txHash)
	return err
}

func (db *DB) OrdersToSweep(ctx context.Context) ([]models.Order, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, status, amount_brl, btc_amount, COALESCE(fee_brl,0), COALESCE(payout_brl,0), address, asset, network,
		       rate_locked, rate_lock_expires_at, created_at, COALESCE(updated_at, created_at), tx_hash, error,
		       deposit_tx, deposit_amount, pix_cpf, pix_phone, derivation_index
		FROM orders o
		WHERE o.status = 'pago'
		  AND o.derivation_index IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM sweeps s WHERE s.order_id = o.id AND s.status IN ('pending','sent','confirmed'))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (db *DB) CreateBuyOrder(ctx context.Context, buy BuyOrderInput) (*BuyOrder, error) {
	id := NewID()
	rawPayload, err := json.Marshal(buy.PixPayload)
	if err != nil {
		return nil, err
	}
	_, err = db.SQL.ExecContext(ctx, `
		INSERT INTO buy_orders (id, status, amount_brl, fee_brl, payout_brl, crypto_amount, asset, dest_address, rate_locked, rate_lock_expires_at, pix_payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		id, buy.Status, buy.AmountBRL, buy.FeeBRL, buy.PayoutBRL, buy.CryptoAmount, buy.Asset, buy.DestAddress, buy.RateLocked, buy.RateLockExpiresAt, rawPayload)
	if err != nil {
		return nil, err
	}
	_ = db.AddBuyEvent(ctx, id, "buy.created", map[string]any{"amountBRL": buy.AmountBRL, "cryptoAmount": buy.CryptoAmount})
	return db.GetBuyOrder(ctx, id)
}

func (db *DB) GetBuyOrder(ctx context.Context, id string) (*BuyOrder, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT id, status, amount_brl::float8, COALESCE(fee_brl,0)::float8, COALESCE(payout_brl,0)::float8,
		       crypto_amount::float8, asset, dest_address, rate_locked::float8, rate_lock_expires_at,
		       COALESCE(pix_payload, '{}'::jsonb), tx_hash_out, error, created_at, updated_at
		FROM buy_orders WHERE id = $1`, id)
	var buy BuyOrder
	var txHashOut, errMsg sql.NullString
	if err := row.Scan(&buy.ID, &buy.Status, &buy.AmountBRL, &buy.FeeBRL, &buy.PayoutBRL, &buy.CryptoAmount, &buy.Asset,
		&buy.DestAddress, &buy.RateLocked, &buy.RateLockExpiresAt, &buy.PixPayload, &txHashOut, &errMsg, &buy.CreatedAt, &buy.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if txHashOut.Valid {
		buy.TxHashOut = &txHashOut.String
	}
	if errMsg.Valid {
		buy.Error = &errMsg.String
	}
	return &buy, nil
}

func (db *DB) UpdateBuyOrderStatus(ctx context.Context, id, status string, extra map[string]any) error {
	txHashOut, _ := extra["txHashOut"].(string)
	errMsg, _ := extra["error"].(string)
	_, err := db.SQL.ExecContext(ctx, `
		UPDATE buy_orders SET status = $2,
			tx_hash_out = COALESCE(NULLIF($3,''), tx_hash_out),
			error = COALESCE(NULLIF($4,''), error),
			updated_at = now()
		WHERE id = $1`, id, status, txHashOut, errMsg)
	if err != nil {
		return err
	}
	return db.AddBuyEvent(ctx, id, "buy."+status, extra)
}

func (db *DB) AddBuyEvent(ctx context.Context, buyOrderID, eventType string, payload any) error {
	raw, _ := json.Marshal(payload)
	_, err := db.SQL.ExecContext(ctx,
		`INSERT INTO buy_order_events (id, buy_order_id, type, payload) VALUES ($1,$2,$3,$4)`,
		NewID(), buyOrderID, eventType, raw)
	return err
}

func (db *DB) HasBuyEvent(ctx context.Context, buyOrderID, eventType, field, value string) (bool, error) {
	var exists bool
	err := db.SQL.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM buy_order_events WHERE buy_order_id = $1 AND type = $2 AND payload ->> $3 = $4)`,
		buyOrderID, eventType, field, value).Scan(&exists)
	return exists, err
}

func (db *DB) ListPendingBuys(ctx context.Context) ([]BuyOrder, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, status, amount_brl::float8, COALESCE(fee_brl,0)::float8, COALESCE(payout_brl,0)::float8,
		       crypto_amount::float8, asset, dest_address, rate_locked::float8, rate_lock_expires_at,
		       COALESCE(pix_payload, '{}'::jsonb), tx_hash_out, error, created_at, updated_at
		FROM buy_orders WHERE status = 'pago_pix'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BuyOrder
	for rows.Next() {
		var buy BuyOrder
		var txHashOut, errMsg sql.NullString
		if err := rows.Scan(&buy.ID, &buy.Status, &buy.AmountBRL, &buy.FeeBRL, &buy.PayoutBRL, &buy.CryptoAmount, &buy.Asset,
			&buy.DestAddress, &buy.RateLocked, &buy.RateLockExpiresAt, &buy.PixPayload, &txHashOut, &errMsg, &buy.CreatedAt, &buy.UpdatedAt); err != nil {
			return nil, err
		}
		if txHashOut.Valid {
			buy.TxHashOut = &txHashOut.String
		}
		if errMsg.Valid {
			buy.Error = &errMsg.String
		}
		out = append(out, buy)
	}
	return out, rows.Err()
}

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(row scanner) (*models.Order, error) {
	var o models.Order
	var status string
	var txHash, errMsg, depositTx, pixCpf, pixPhone sql.NullString
	var depositAmount sql.NullFloat64
	var derivationIndex sql.NullInt64
	err := row.Scan(&o.ID, &status, &o.AmountBRL, &o.AmountUSDT, &o.FeeBRL, &o.PayoutBRL, &o.Address, &o.Asset, &o.Network,
		&o.RateLocked, &o.RateLockExpiresAt, &o.CreatedAt, &o.UpdatedAt, &txHash, &errMsg, &depositTx, &depositAmount, &pixCpf, &pixPhone, &derivationIndex)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	o.Status = models.OrderStatus(status)
	o.TronAddress = o.Address
	if pixCpf.Valid {
		o.PixCpf = pixCpf.String
	}
	if pixPhone.Valid {
		o.PixPhone = pixPhone.String
	}
	if txHash.Valid {
		o.TxHash = &txHash.String
	}
	if errMsg.Valid {
		o.Error = &errMsg.String
	}
	if depositTx.Valid {
		o.DepositTx = &depositTx.String
	}
	if depositAmount.Valid {
		o.DepositAmount = &depositAmount.Float64
	}
	if derivationIndex.Valid {
		i := int(derivationIndex.Int64)
		o.DerivationIndex = &i
	}
	return &o, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

const schemaSQL = `
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS orders (
  id UUID PRIMARY KEY,
  status VARCHAR(32) NOT NULL,
  amount_brl NUMERIC(18,2) NOT NULL,
  btc_amount NUMERIC(28,8) NOT NULL,
  fee_brl NUMERIC(18,2),
  payout_brl NUMERIC(18,2),
  address TEXT NOT NULL,
  asset VARCHAR(16) NOT NULL DEFAULT 'USDT',
  network VARCHAR(32) NOT NULL DEFAULT 'TRON',
  rate_locked NUMERIC(28,8) NOT NULL,
  rate_lock_expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  tx_hash TEXT,
  error TEXT,
  deposit_tx TEXT,
  deposit_amount NUMERIC(28,8),
  pix_cpf TEXT,
  pix_phone TEXT,
  derivation_index INT
);

CREATE TABLE IF NOT EXISTS order_events (
  id UUID PRIMARY KEY,
  order_id UUID REFERENCES orders(id),
  type VARCHAR(64) NOT NULL,
  payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS payouts (
  id UUID PRIMARY KEY,
  order_id UUID REFERENCES orders(id),
  pix_cpf TEXT,
  pix_key TEXT,
  status VARCHAR(32) NOT NULL,
  provider_response JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS onchain_cursor (
  id SERIAL PRIMARY KEY,
  network VARCHAR(32) NOT NULL UNIQUE,
  last_block BIGINT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sweeps (
  id UUID PRIMARY KEY,
  child_index INT NOT NULL,
  from_addr TEXT NOT NULL,
  to_addr TEXT NOT NULL,
  amount NUMERIC(28,8) NOT NULL,
  tx_hash TEXT,
  status VARCHAR(32) NOT NULL DEFAULT 'pending',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  idempotency_key TEXT,
  amount_trx_fee NUMERIC(28,8),
  order_id UUID REFERENCES orders(id)
);

CREATE TABLE IF NOT EXISTS buy_orders (
  id UUID PRIMARY KEY,
  status VARCHAR(32) NOT NULL,
  amount_brl NUMERIC(18,2) NOT NULL,
  fee_brl NUMERIC(18,2),
  payout_brl NUMERIC(18,2),
  crypto_amount NUMERIC(28,8) NOT NULL,
  asset VARCHAR(16) NOT NULL DEFAULT 'USDT',
  dest_address TEXT NOT NULL,
  rate_locked NUMERIC(28,8) NOT NULL,
  rate_lock_expires_at TIMESTAMPTZ NOT NULL,
  pix_payload JSONB,
  tx_hash_out TEXT,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS buy_order_events (
  id UUID PRIMARY KEY,
  buy_order_id UUID REFERENCES buy_orders(id),
  type VARCHAR(64) NOT NULL,
  payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`
