package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/lib/pq"
)

// WebhookSubscription is an outbound automation subscription (n8n, Zapier,
// Make.com or any generic HTTP receiver) that gets notified when platform
// events happen.
type WebhookSubscription struct {
	ID              string     `json:"id"`
	Provider        string     `json:"provider"`
	TargetURL       string     `json:"targetUrl"`
	Secret          string     `json:"-"`
	HasSecret       bool       `json:"hasSecret"`
	Events          []string   `json:"events"`
	Active          bool       `json:"active"`
	Description     string     `json:"description,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	LastTriggeredAt *time.Time `json:"lastTriggeredAt,omitempty"`
	LastStatusCode  *int       `json:"lastStatusCode,omitempty"`
	LastError       *string    `json:"lastError,omitempty"`
	FailureCount    int        `json:"failureCount"`
}

// CreateWebhookSubscription inserts a new outbound automation subscription.
func (db *DB) CreateWebhookSubscription(ctx context.Context, provider, targetURL, secret, description string, events []string) (*WebhookSubscription, error) {
	id := NewID()
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO webhook_subscriptions (id, provider, target_url, secret, events, description)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, provider, targetURL, secret, pq.Array(events), description)
	if err != nil {
		return nil, err
	}
	return db.GetWebhookSubscription(ctx, id)
}

// GetWebhookSubscription fetches a single subscription by id.
func (db *DB) GetWebhookSubscription(ctx context.Context, id string) (*WebhookSubscription, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT id, provider, target_url, secret, events, active, COALESCE(description, ''),
		       created_at, updated_at, last_triggered_at, last_status_code, last_error, failure_count
		FROM webhook_subscriptions WHERE id = $1`, id)
	return scanWebhookSubscription(row)
}

// ListWebhookSubscriptions returns every configured subscription.
func (db *DB) ListWebhookSubscriptions(ctx context.Context) ([]*WebhookSubscription, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, provider, target_url, secret, events, active, COALESCE(description, ''),
		       created_at, updated_at, last_triggered_at, last_status_code, last_error, failure_count
		FROM webhook_subscriptions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WebhookSubscription
	for rows.Next() {
		sub, err := scanWebhookSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// ListActiveWebhookSubscriptionsForEvent returns active subscriptions listening to eventType.
func (db *DB) ListActiveWebhookSubscriptionsForEvent(ctx context.Context, eventType string) ([]*WebhookSubscription, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, provider, target_url, secret, events, active, COALESCE(description, ''),
		       created_at, updated_at, last_triggered_at, last_status_code, last_error, failure_count
		FROM webhook_subscriptions
		WHERE active = true AND ($1 = ANY(events) OR '*' = ANY(events))`, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WebhookSubscription
	for rows.Next() {
		sub, err := scanWebhookSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// DeleteWebhookSubscription removes a subscription.
func (db *DB) DeleteWebhookSubscription(ctx context.Context, id string) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, id)
	return err
}

// SetWebhookSubscriptionActive toggles a subscription on/off.
func (db *DB) SetWebhookSubscriptionActive(ctx context.Context, id string, active bool) error {
	_, err := db.SQL.ExecContext(ctx, `UPDATE webhook_subscriptions SET active = $2, updated_at = now() WHERE id = $1`, id, active)
	return err
}

// RecordWebhookDelivery stores a delivery attempt and updates subscription health fields.
func (db *DB) RecordWebhookDelivery(ctx context.Context, subscriptionID, event string, payload map[string]any, statusCode int, ok bool, deliveryErr string, attempt int) error {
	raw, _ := json.Marshal(payload)
	id := NewID()
	if _, err := db.SQL.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (id, subscription_id, event, payload, status_code, ok, error, attempt)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, subscriptionID, event, raw, statusCode, ok, nullableString(deliveryErr), attempt); err != nil {
		return err
	}
	failureIncrement := 0
	if !ok {
		failureIncrement = 1
	}
	_, err := db.SQL.ExecContext(ctx, `
		UPDATE webhook_subscriptions
		SET last_triggered_at = now(),
		    last_status_code = $2,
		    last_error = $3,
		    failure_count = CASE WHEN $4 THEN 0 ELSE failure_count + $5 END,
		    updated_at = now()
		WHERE id = $1`, subscriptionID, statusCode, nullableString(deliveryErr), ok, failureIncrement)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWebhookSubscription(row rowScanner) (*WebhookSubscription, error) {
	var sub WebhookSubscription
	var secret sql.NullString
	var lastTriggeredAt sql.NullTime
	var lastStatusCode sql.NullInt64
	var lastError sql.NullString
	if err := row.Scan(
		&sub.ID, &sub.Provider, &sub.TargetURL, &secret, pq.Array(&sub.Events), &sub.Active, &sub.Description,
		&sub.CreatedAt, &sub.UpdatedAt, &lastTriggeredAt, &lastStatusCode, &lastError, &sub.FailureCount,
	); err != nil {
		return nil, err
	}
	sub.Secret = secret.String
	sub.HasSecret = secret.Valid && secret.String != ""
	if lastTriggeredAt.Valid {
		sub.LastTriggeredAt = &lastTriggeredAt.Time
	}
	if lastStatusCode.Valid {
		v := int(lastStatusCode.Int64)
		sub.LastStatusCode = &v
	}
	if lastError.Valid {
		sub.LastError = &lastError.String
	}
	return &sub, nil
}
