package mobile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/models"
)

// mobileDB wraps the existing DB to add mobile-specific queries.
// Uses DB.SQL directly so we don't touch the existing database package.
type mobileQueries struct {
	sql *sql.DB
}

func mobileDB(db *database.DB) *mobileQueries {
	return &mobileQueries{sql: db.SQL}
}

// ─── Users ────────────────────────────────────────────────────────────────────

func (q *mobileQueries) CreateUser(ctx context.Context, email, passwordHash, fullName, phone string) (*models.User, error) {
	var id string
	err := q.sql.QueryRowContext(ctx, `
                INSERT INTO users (email, password_hash, full_name, phone)
                VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''))
                RETURNING id`, email, passwordHash, fullName, phone).Scan(&id)
	if err != nil {
		return nil, err
	}
	return q.GetUserByID(ctx, id)
}

func (q *mobileQueries) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return q.scanUser(q.sql.QueryRowContext(ctx, `
                SELECT id,email,phone,full_name,password_hash,wallet_address,pix_key,
                       kyc_status,kyc_documents,pin_hash,biometry_enabled,two_factor_enabled,
                       two_factor_secret,refresh_token_hash,created_at,updated_at
                FROM users WHERE email=$1`, email))
}

func (q *mobileQueries) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	return q.scanUser(q.sql.QueryRowContext(ctx, `
                SELECT id,email,phone,full_name,password_hash,wallet_address,pix_key,
                       kyc_status,kyc_documents,pin_hash,biometry_enabled,two_factor_enabled,
                       two_factor_secret,refresh_token_hash,created_at,updated_at
                FROM users WHERE id=$1`, id))
}

func (q *mobileQueries) scanUser(row *sql.Row) (*models.User, error) {
	u := &models.User{}
	err := row.Scan(
		&u.ID, &u.Email, &u.Phone, &u.FullName, &u.PasswordHash,
		&u.WalletAddress, &u.PixKey, &u.KYCStatus, &u.KYCDocuments,
		&u.PinHash, &u.BiometryEnabled, &u.TwoFactorEnabled,
		&u.TwoFactorSecret, &u.RefreshTokenHash,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (q *mobileQueries) UpdateUser(ctx context.Context, id string, fields map[string]any) error {
	set := ""
	args := []any{}
	i := 1
	for k, v := range fields {
		if set != "" {
			set += ", "
		}
		set += k + "=$" + itoa(i)
		args = append(args, v)
		i++
	}
	args = append(args, id)
	_, err := q.sql.ExecContext(ctx, "UPDATE users SET "+set+" WHERE id=$"+itoa(i), args...)
	return err
}

func (q *mobileQueries) SaveRefreshToken(ctx context.Context, userID, token string) error {
	hash := refreshTokenDigest(token)
	_, err := q.sql.ExecContext(ctx,
		"UPDATE users SET refresh_token_hash=$1 WHERE id=$2", hash, userID)
	return err
}

func refreshTokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (q *mobileQueries) ClearRefreshToken(ctx context.Context, userID string) error {
	_, err := q.sql.ExecContext(ctx,
		"UPDATE users SET refresh_token_hash=NULL WHERE id=$1", userID)
	return err
}

// ─── Devices ──────────────────────────────────────────────────────────────────

func (q *mobileQueries) UpsertDevice(ctx context.Context, userID, deviceName, deviceType, fcmToken, apnsToken string) error {
	_, err := q.sql.ExecContext(ctx, `
                INSERT INTO devices (user_id, device_name, device_type, fcm_token, apns_token, last_active)
                VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NOW())
                ON CONFLICT (user_id, COALESCE(fcm_token,''), COALESCE(apns_token,''))
                DO UPDATE SET last_active=NOW(), device_name=EXCLUDED.device_name, device_type=EXCLUDED.device_type`,
		userID, deviceName, deviceType, fcmToken, apnsToken)
	if err != nil {
		// fallback: plain insert ignoring conflict
		_, err = q.sql.ExecContext(ctx, `
                        INSERT INTO devices (user_id, device_name, device_type, fcm_token, apns_token, last_active)
                        VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NOW())
                        ON CONFLICT DO NOTHING`,
			userID, deviceName, deviceType, fcmToken, apnsToken)
	}
	return err
}

func (q *mobileQueries) ListDevices(ctx context.Context, userID string) ([]models.Device, error) {
	rows, err := q.sql.QueryContext(ctx, `
                SELECT id,user_id,device_name,device_type,fcm_token,apns_token,last_active,created_at
                FROM devices WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Device
	for rows.Next() {
		d := models.Device{}
		_ = rows.Scan(&d.ID, &d.UserID, &d.DeviceName, &d.DeviceType, &d.FCMToken, &d.APNSToken, &d.LastActive, &d.CreatedAt)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (q *mobileQueries) DeleteDevice(ctx context.Context, userID, deviceID string) error {
	_, err := q.sql.ExecContext(ctx, "DELETE FROM devices WHERE id=$1 AND user_id=$2", deviceID, userID)
	return err
}

// ─── DCA ─────────────────────────────────────────────────────────────────────

func (q *mobileQueries) CreateDCA(ctx context.Context, userID, symbol string, amount float64, freq models.DCAFrequency) (*models.DCAStrategy, error) {
	next := nextDCAExecution(freq)
	d := &models.DCAStrategy{}
	err := q.sql.QueryRowContext(ctx, `
                INSERT INTO dca_strategies (user_id, token_symbol, amount_brl, frequency, next_execution)
                VALUES ($1,$2,$3,$4,$5)
                RETURNING id,user_id,token_symbol,amount_brl,frequency,active,
                          total_invested,total_tokens,next_execution,created_at`,
		userID, symbol, amount, string(freq), next).Scan(
		&d.ID, &d.UserID, &d.TokenSymbol, &d.AmountBRL, &d.Frequency,
		&d.Active, &d.TotalInvested, &d.TotalTokens, &d.NextExecution, &d.CreatedAt)
	return d, err
}

func (q *mobileQueries) GetDCA(ctx context.Context, id string) (*models.DCAStrategy, error) {
	row := q.sql.QueryRowContext(ctx, `
                SELECT id,user_id,token_symbol,amount_brl,frequency,active,
                       total_invested,total_tokens,next_execution,created_at
                FROM dca_strategies WHERE id=$1`, id)
	return q.scanDCA(row)
}

func (q *mobileQueries) ListDCA(ctx context.Context, userID string) ([]models.DCAStrategy, error) {
	rows, err := q.sql.QueryContext(ctx, `
                SELECT id,user_id,token_symbol,amount_brl,frequency,active,
                       total_invested,total_tokens,next_execution,created_at
                FROM dca_strategies WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.DCAStrategy
	for rows.Next() {
		d := models.DCAStrategy{}
		_ = rows.Scan(&d.ID, &d.UserID, &d.TokenSymbol, &d.AmountBRL, &d.Frequency,
			&d.Active, &d.TotalInvested, &d.TotalTokens, &d.NextExecution, &d.CreatedAt)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (q *mobileQueries) UpdateDCA(ctx context.Context, id, userID string, active *bool, amount *float64, freq *models.DCAFrequency) error {
	if active != nil {
		_, err := q.sql.ExecContext(ctx, "UPDATE dca_strategies SET active=$1 WHERE id=$2 AND user_id=$3", *active, id, userID)
		if err != nil {
			return err
		}
	}
	if amount != nil {
		_, err := q.sql.ExecContext(ctx, "UPDATE dca_strategies SET amount_brl=$1 WHERE id=$2 AND user_id=$3", *amount, id, userID)
		if err != nil {
			return err
		}
	}
	if freq != nil {
		_, err := q.sql.ExecContext(ctx, "UPDATE dca_strategies SET frequency=$1, next_execution=$2 WHERE id=$3 AND user_id=$4",
			string(*freq), nextDCAExecution(*freq), id, userID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (q *mobileQueries) DeleteDCA(ctx context.Context, id, userID string) error {
	_, err := q.sql.ExecContext(ctx, "DELETE FROM dca_strategies WHERE id=$1 AND user_id=$2", id, userID)
	return err
}

func (q *mobileQueries) scanDCA(row *sql.Row) (*models.DCAStrategy, error) {
	d := &models.DCAStrategy{}
	err := row.Scan(&d.ID, &d.UserID, &d.TokenSymbol, &d.AmountBRL, &d.Frequency,
		&d.Active, &d.TotalInvested, &d.TotalTokens, &d.NextExecution, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func nextDCAExecution(freq models.DCAFrequency) time.Time {
	switch freq {
	case models.DCAWeekly:
		return time.Now().Add(7 * 24 * time.Hour)
	case models.DCAMonthly:
		return time.Now().AddDate(0, 1, 0)
	default:
		return time.Now().Add(24 * time.Hour)
	}
}

// ─── Notifications ────────────────────────────────────────────────────────────

func (q *mobileQueries) CreateNotification(ctx context.Context, userID, title, body, ntype string, data map[string]any) error {
	var dataStr *string
	if data != nil {
		if b, err := marshalJSON(data); err == nil {
			s := string(b)
			dataStr = &s
		}
	}
	_, err := q.sql.ExecContext(ctx, `
                INSERT INTO notifications (user_id, title, body, type, data)
                VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5)`,
		userID, title, body, ntype, dataStr)
	return err
}

func (q *mobileQueries) ListNotifications(ctx context.Context, userID string, limit int) ([]models.Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.sql.QueryContext(ctx, `
                SELECT id,user_id,title,body,type,read,data,created_at
                FROM notifications WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Notification
	for rows.Next() {
		n := models.Notification{}
		_ = rows.Scan(&n.ID, &n.UserID, &n.Title, &n.Body, &n.Type, &n.Read, &n.Data, &n.CreatedAt)
		out = append(out, n)
	}
	return out, rows.Err()
}

func (q *mobileQueries) MarkNotificationsRead(ctx context.Context, userID string, ids []string) error {
	if len(ids) == 0 {
		_, err := q.sql.ExecContext(ctx, "UPDATE notifications SET read=true WHERE user_id=$1", userID)
		return err
	}
	placeholders := ""
	args := []any{userID}
	for i, id := range ids {
		if placeholders != "" {
			placeholders += ","
		}
		placeholders += "$" + itoa(i+2)
		args = append(args, id)
	}
	_, err := q.sql.ExecContext(ctx, "UPDATE notifications SET read=true WHERE user_id=$1 AND id IN ("+placeholders+")", args...)
	return err
}

func (q *mobileQueries) DeleteNotification(ctx context.Context, userID, id string) error {
	_, err := q.sql.ExecContext(ctx, "DELETE FROM notifications WHERE id=$1 AND user_id=$2", id, userID)
	return err
}

// ─── Settings ─────────────────────────────────────────────────────────────────

func (q *mobileQueries) GetSettings(ctx context.Context, userID string) (*models.UserSettings, error) {
	s := &models.UserSettings{UserID: userID, DarkMode: true, Language: "pt-BR", Currency: "BRL", NotificationsEnabled: true, DailyLimit: 10000}
	err := q.sql.QueryRowContext(ctx, `
                SELECT user_id,dark_mode,language,currency,notifications_enabled,daily_limit
                FROM settings WHERE user_id=$1`, userID).Scan(
		&s.UserID, &s.DarkMode, &s.Language, &s.Currency, &s.NotificationsEnabled, &s.DailyLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil // return defaults
	}
	return s, err
}

func (q *mobileQueries) UpsertSettings(ctx context.Context, s *models.UserSettings) error {
	_, err := q.sql.ExecContext(ctx, `
                INSERT INTO settings (user_id, dark_mode, language, currency, notifications_enabled, daily_limit)
                VALUES ($1,$2,$3,$4,$5,$6)
                ON CONFLICT (user_id) DO UPDATE SET
                        dark_mode=$2, language=$3, currency=$4,
                        notifications_enabled=$5, daily_limit=$6`,
		s.UserID, s.DarkMode, s.Language, s.Currency, s.NotificationsEnabled, s.DailyLimit)
	return err
}

// ─── Orders (read-only — write goes through existing server) ──────────────────

func (q *mobileQueries) ListOrdersByUser(ctx context.Context, userID string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := q.sql.QueryContext(ctx, `
                SELECT id, kind, amount_brl, crypto_amount, fee_brl, payout_brl,
                       status, asset, network, rate_locked, tx_hash, created_at, updated_at
                FROM (
                        SELECT id::text,
                               'sell'::text AS kind,
                               amount_brl::float8,
                               btc_amount::float8 AS crypto_amount,
                               COALESCE(fee_brl, 0)::float8 AS fee_brl,
                               COALESCE(payout_brl, 0)::float8 AS payout_brl,
                               status,
                               asset,
                               network,
                               rate_locked::float8,
                               tx_hash,
                               created_at,
                               updated_at
                        FROM orders
                        WHERE user_id=$1::uuid
                        UNION ALL
                        SELECT id::text,
                               'buy'::text AS kind,
                               amount_brl::float8,
                               crypto_amount::float8,
                               COALESCE(fee_brl, 0)::float8 AS fee_brl,
                               COALESCE(payout_brl, 0)::float8 AS payout_brl,
                               status,
                               asset,
                               'BSC'::text AS network,
                               rate_locked::float8,
                               tx_hash_out AS tx_hash,
                               created_at,
                               updated_at
                        FROM buy_orders
                        WHERE user_id=$1::uuid
                ) mobile_orders
                ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, kind, status, asset, network string
		var amtBRL, amtUSDT, feeBRL, payoutBRL, rateLocked float64
		var txHash *string
		var createdAt, updatedAt interface{}
		_ = rows.Scan(&id, &kind, &amtBRL, &amtUSDT, &feeBRL, &payoutBRL, &status, &asset, &network, &rateLocked, &txHash, &createdAt, &updatedAt)
		out = append(out, map[string]any{
			"id": id, "kind": kind, "status": status, "asset": asset, "network": network,
			"amountBrl": amtBRL, "amountCrypto": amtUSDT, "feeBrl": feeBRL,
			"payoutBrl": payoutBRL, "rateLocked": rateLocked,
			"txHash": txHash, "createdAt": createdAt, "updatedAt": updatedAt,
		})
	}
	return out, rows.Err()
}

// TagOrderUser sets user_id on an order (used after anonymous order creation).
func (q *mobileQueries) TagOrderUser(ctx context.Context, orderID, userID string) error {
	_, err := q.sql.ExecContext(ctx,
		"UPDATE orders SET user_id=$1::uuid WHERE id=$2::uuid AND user_id IS NULL", userID, orderID)
	return err
}

// TagBuyOrderUser sets user_id on a buy order created through the mobile API.
func (q *mobileQueries) TagBuyOrderUser(ctx context.Context, orderID, userID string) error {
	_, err := q.sql.ExecContext(ctx,
		"UPDATE buy_orders SET user_id=$1::uuid WHERE id=$2::uuid AND user_id IS NULL", userID, orderID)
	return err
}

func (q *mobileQueries) CancelOrder(ctx context.Context, orderID, userID string) error {
	res, err := q.sql.ExecContext(ctx, `
                UPDATE orders SET status='cancelada', updated_at=NOW()
                WHERE id=$1 AND user_id=$2 AND status='aguardando_deposito'`, orderID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("ordem não encontrada ou não pode ser cancelada")
	}
	return nil
}
