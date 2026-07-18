package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	NFCStatusApproved        = "approved"
	NFCStatusDeclined        = "declined"
	NFCStatusRequiresFunding = "requires_funding"
)

type NFCTokenInput struct {
	TokenID   string
	TokenHash string
	Wallet    string
	DeviceID  string
	Network   string
	ExpiresAt time.Time
}

type NFCFundingInput struct {
	Wallet     string
	Network    string
	Asset      string
	DeltaMicro int64
}

type NFCAuthorizeInput struct {
	ID              string
	IdempotencyKey  string
	TokenID         string
	TokenHash       string
	Wallet          string
	Network         string
	MerchantID      string
	TerminalID      string
	ExternalRef     string
	AmountBRLMinor  int64
	USDTRate        float64
	RequiredUSDTMic int64
	HoldExpiresAt   time.Time
}

type NFCAuthorization struct {
	ID              string     `json:"id"`
	IdempotencyKey  string     `json:"-"`
	TokenID         string     `json:"token_id"`
	Wallet          string     `json:"wallet_address"`
	Network         string     `json:"network"`
	MerchantID      string     `json:"merchant_id"`
	TerminalID      string     `json:"terminal_id"`
	ExternalRef     string     `json:"external_ref,omitempty"`
	AmountBRLMinor  int64      `json:"amount_brl_minor"`
	USDTRate        float64    `json:"usdt_rate"`
	RequiredUSDTMic int64      `json:"required_usdt_micro"`
	Status          string     `json:"status"`
	ResponseCode    string     `json:"response_code"`
	Reason          string     `json:"reason,omitempty"`
	HoldExpiresAt   *time.Time `json:"hold_expires_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Idempotent      bool       `json:"idempotent,omitempty"`
}

type NFCBalance struct {
	Wallet         string    `json:"wallet_address"`
	Network        string    `json:"network"`
	Asset          string    `json:"asset"`
	AvailableMicro int64     `json:"available_usdt_micro"`
	LockedMicro    int64     `json:"locked_usdt_micro"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (db *DB) StoreNFCToken(ctx context.Context, in NFCTokenInput) error {
	_, err := db.SQL.ExecContext(ctx, `
INSERT INTO nfc_tokens (token_id, token_hash, wallet_address, device_id, network, status, expires_at)
VALUES ($1,$2,$3,$4,$5,'active',$6)
ON CONFLICT (token_id) DO UPDATE SET
  token_hash=EXCLUDED.token_hash,
  wallet_address=EXCLUDED.wallet_address,
  device_id=EXCLUDED.device_id,
  network=EXCLUDED.network,
  status='active',
  expires_at=EXCLUDED.expires_at`,
		strings.TrimSpace(in.TokenID),
		strings.TrimSpace(in.TokenHash),
		strings.ToLower(strings.TrimSpace(in.Wallet)),
		nullableString(strings.TrimSpace(in.DeviceID)),
		normalizeNFCNetwork(in.Network),
		in.ExpiresAt.UTC(),
	)
	return err
}

func (db *DB) AddNFCBalance(ctx context.Context, in NFCFundingInput) (*NFCBalance, error) {
	if in.DeltaMicro <= 0 {
		return nil, fmt.Errorf("nfc: funding delta must be positive")
	}
	asset := strings.ToUpper(firstNonEmptyDB(in.Asset, "USDT"))
	network := normalizeNFCNetwork(in.Network)
	wallet := strings.ToLower(strings.TrimSpace(in.Wallet))
	const q = `
INSERT INTO nfc_wallet_balances (wallet_address, network, asset, available_usdt_micro, locked_usdt_micro)
VALUES ($1,$2,$3,$4,0)
ON CONFLICT (wallet_address, network, asset) DO UPDATE SET
  available_usdt_micro = nfc_wallet_balances.available_usdt_micro + EXCLUDED.available_usdt_micro,
  updated_at = NOW()
RETURNING wallet_address, network, asset, available_usdt_micro, locked_usdt_micro, updated_at`
	return scanNFCBalance(db.SQL.QueryRowContext(ctx, q, wallet, network, asset, in.DeltaMicro))
}

func (db *DB) GetNFCBalance(ctx context.Context, wallet, network string) (*NFCBalance, error) {
	const q = `
SELECT wallet_address, network, asset, available_usdt_micro, locked_usdt_micro, updated_at
FROM nfc_wallet_balances
WHERE wallet_address = $1 AND network = $2 AND asset = 'USDT'`
	bal, err := scanNFCBalance(db.SQL.QueryRowContext(ctx, q, strings.ToLower(strings.TrimSpace(wallet)), normalizeNFCNetwork(network)))
	if err == sql.ErrNoRows {
		return &NFCBalance{Wallet: strings.ToLower(strings.TrimSpace(wallet)), Network: normalizeNFCNetwork(network), Asset: "USDT"}, nil
	}
	return bal, err
}

func (db *DB) AuthorizeNFCPayment(ctx context.Context, in NFCAuthorizeInput) (*NFCAuthorization, bool, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("nfc: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if existing, err := txGetNFCAuthorizationByIdempotency(ctx, tx, in.IdempotencyKey); err != nil {
		return nil, false, err
	} else if existing != nil {
		existing.Idempotent = true
		return existing, true, tx.Commit()
	}

	status := NFCStatusDeclined
	responseCode := "05"
	reason := "invalid_token"
	var holdExpires any

	var dbWallet, dbNetwork, tokenStatus string
	var tokenExpires time.Time
	err = tx.QueryRowContext(ctx, `
SELECT wallet_address, network, status, expires_at
FROM nfc_tokens
WHERE token_id = $1 AND token_hash = $2
FOR UPDATE`, in.TokenID, in.TokenHash).Scan(&dbWallet, &dbNetwork, &tokenStatus, &tokenExpires)
	if err == sql.ErrNoRows {
		return txInsertNFCAuthorization(ctx, tx, in, status, responseCode, reason, holdExpires)
	}
	if err != nil {
		return nil, false, fmt.Errorf("nfc: token lookup: %w", err)
	}
	if tokenStatus != "active" || !time.Now().UTC().Before(tokenExpires.UTC()) {
		reason = "token_expired_or_revoked"
		return txInsertNFCAuthorization(ctx, tx, in, status, responseCode, reason, holdExpires)
	}
	if strings.ToLower(dbWallet) != strings.ToLower(in.Wallet) || normalizeNFCNetwork(dbNetwork) != normalizeNFCNetwork(in.Network) {
		reason = "token_wallet_mismatch"
		return txInsertNFCAuthorization(ctx, tx, in, status, responseCode, reason, holdExpires)
	}

	var available, locked int64
	err = tx.QueryRowContext(ctx, `
SELECT available_usdt_micro, locked_usdt_micro
FROM nfc_wallet_balances
WHERE wallet_address = $1 AND network = $2 AND asset = 'USDT'
FOR UPDATE`, strings.ToLower(in.Wallet), normalizeNFCNetwork(in.Network)).Scan(&available, &locked)
	if err == sql.ErrNoRows || available < in.RequiredUSDTMic {
		status = NFCStatusRequiresFunding
		responseCode = "51"
		reason = "insufficient_usdt"
		return txInsertNFCAuthorization(ctx, tx, in, status, responseCode, reason, holdExpires)
	}
	if err != nil {
		return nil, false, fmt.Errorf("nfc: balance lookup: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
UPDATE nfc_wallet_balances
SET available_usdt_micro = available_usdt_micro - $3,
    locked_usdt_micro = locked_usdt_micro + $3,
    updated_at = NOW()
WHERE wallet_address = $1 AND network = $2 AND asset = 'USDT'`,
		strings.ToLower(in.Wallet), normalizeNFCNetwork(in.Network), in.RequiredUSDTMic)
	if err != nil {
		return nil, false, fmt.Errorf("nfc: lock balance: %w", err)
	}
	_, _ = tx.ExecContext(ctx, `UPDATE nfc_tokens SET last_used_at = NOW() WHERE token_id = $1`, in.TokenID)

	status = NFCStatusApproved
	responseCode = "00"
	reason = "approved"
	holdExpires = in.HoldExpiresAt.UTC()
	return txInsertNFCAuthorization(ctx, tx, in, status, responseCode, reason, holdExpires)
}

func (db *DB) GetNFCAuthorization(ctx context.Context, id string) (*NFCAuthorization, error) {
	const q = `
SELECT id, idempotency_key, token_id, wallet_address, network, merchant_id, terminal_id, COALESCE(external_ref,''),
       amount_brl_minor, usdt_rate::float8, required_usdt_micro, status, response_code, COALESCE(reason,''),
       hold_expires_at, created_at, updated_at
FROM nfc_authorizations
WHERE id = $1`
	auth, err := scanNFCAuthorization(db.SQL.QueryRowContext(ctx, q, strings.TrimSpace(id)))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return auth, err
}

func txInsertNFCAuthorization(ctx context.Context, tx *sql.Tx, in NFCAuthorizeInput, status, responseCode, reason string, holdExpires any) (*NFCAuthorization, bool, error) {
	if in.ID == "" {
		in.ID = NewID()
	}
	const q = `
INSERT INTO nfc_authorizations
  (id, idempotency_key, token_id, token_hash, wallet_address, network, merchant_id, terminal_id, external_ref,
   amount_brl_minor, usdt_rate, required_usdt_micro, status, response_code, reason, hold_expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
RETURNING id, idempotency_key, token_id, wallet_address, network, merchant_id, terminal_id, COALESCE(external_ref,''),
          amount_brl_minor, usdt_rate::float8, required_usdt_micro, status, response_code, COALESCE(reason,''),
          hold_expires_at, created_at, updated_at`
	auth, err := scanNFCAuthorization(tx.QueryRowContext(ctx, q,
		in.ID, strings.TrimSpace(in.IdempotencyKey), in.TokenID, in.TokenHash,
		strings.ToLower(strings.TrimSpace(in.Wallet)), normalizeNFCNetwork(in.Network),
		strings.TrimSpace(in.MerchantID), strings.TrimSpace(in.TerminalID), nullableString(strings.TrimSpace(in.ExternalRef)),
		in.AmountBRLMinor, in.USDTRate, in.RequiredUSDTMic, status, responseCode, reason, holdExpires,
	))
	if err != nil {
		return nil, false, fmt.Errorf("nfc: insert authorization: %w", err)
	}
	return auth, false, tx.Commit()
}

func txGetNFCAuthorizationByIdempotency(ctx context.Context, tx *sql.Tx, key string) (*NFCAuthorization, error) {
	const q = `
SELECT id, idempotency_key, token_id, wallet_address, network, merchant_id, terminal_id, COALESCE(external_ref,''),
       amount_brl_minor, usdt_rate::float8, required_usdt_micro, status, response_code, COALESCE(reason,''),
       hold_expires_at, created_at, updated_at
FROM nfc_authorizations
WHERE idempotency_key = $1`
	auth, err := scanNFCAuthorization(tx.QueryRowContext(ctx, q, strings.TrimSpace(key)))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return auth, err
}

func scanNFCAuthorization(row scanner) (*NFCAuthorization, error) {
	var a NFCAuthorization
	var hold sql.NullTime
	err := row.Scan(&a.ID, &a.IdempotencyKey, &a.TokenID, &a.Wallet, &a.Network, &a.MerchantID, &a.TerminalID, &a.ExternalRef,
		&a.AmountBRLMinor, &a.USDTRate, &a.RequiredUSDTMic, &a.Status, &a.ResponseCode, &a.Reason, &hold, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if hold.Valid {
		a.HoldExpiresAt = &hold.Time
	}
	return &a, nil
}

func scanNFCBalance(row scanner) (*NFCBalance, error) {
	var b NFCBalance
	err := row.Scan(&b.Wallet, &b.Network, &b.Asset, &b.AvailableMicro, &b.LockedMicro, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func normalizeNFCNetwork(network string) string {
	network = strings.ToUpper(strings.TrimSpace(network))
	if network == "" {
		return "BSC"
	}
	return network
}
