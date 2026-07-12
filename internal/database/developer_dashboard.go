package database

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type APIRequestLogInput struct {
	RequestID   string
	Method      string
	Path        string
	RouteClass  string
	StatusCode  int
	DurationMS  int64
	APIKeyHash  string
	APIKeyScope string
	AuthMode    string
	ClientIP    string
	UserAgent   string
}

type APIRequestLog struct {
	ID          string    `json:"id"`
	RequestID   string    `json:"requestId"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	RouteClass  string    `json:"routeClass"`
	StatusCode  int       `json:"statusCode"`
	DurationMS  int64     `json:"durationMs"`
	APIKeyHash  string    `json:"apiKeyHash,omitempty"`
	APIKeyScope string    `json:"apiKeyScope,omitempty"`
	AuthMode    string    `json:"authMode,omitempty"`
	ClientIP    string    `json:"clientIp,omitempty"`
	UserAgent   string    `json:"userAgent,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type MCPToolLogInput struct {
	RequestID    string
	ToolName     string
	Status       string
	ErrorMessage string
	DurationMS   int64
	APIKeyHash   string
	AuthMode     string
}

type MCPToolLog struct {
	ID           string    `json:"id"`
	RequestID    string    `json:"requestId,omitempty"`
	ToolName     string    `json:"toolName"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
	DurationMS   int64     `json:"durationMs"`
	APIKeyHash   string    `json:"apiKeyHash,omitempty"`
	AuthMode     string    `json:"authMode,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

type MarketplacePurchaseSummary struct {
	ID             string    `json:"id"`
	ProductID      string    `json:"productId"`
	PlanID         string    `json:"planId"`
	AgentWallet    string    `json:"agentWallet"`
	PaymentAsset   string    `json:"paymentAsset"`
	Network        string    `json:"network"`
	GrossAmount    string    `json:"grossAmount"`
	ProviderAmount string    `json:"providerAmount"`
	Status         string    `json:"status"`
	TxHash         string    `json:"txHash,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

type MarketplaceUsageSummary struct {
	ID             string    `json:"id"`
	CapabilityID   string    `json:"capability"`
	ProviderSlug   string    `json:"provider"`
	RoutingMode    string    `json:"routingMode"`
	Operation      string    `json:"operation"`
	Status         string    `json:"status"`
	UnitsConsumed  int       `json:"unitsConsumed"`
	QuotaRemaining int       `json:"quotaRemaining"`
	LatencyMS      int       `json:"latencyMs"`
	RequestID      string    `json:"requestId"`
	CreatedAt      time.Time `json:"createdAt"`
}

type DeveloperDashboardSummary struct {
	GeneratedAt time.Time                     `json:"generatedAt"`
	Counts      map[string]int                `json:"counts"`
	Health      map[string]any                `json:"health"`
	APILogs     []*APIRequestLog              `json:"apiLogs"`
	MCPLogs     []*MCPToolLog                 `json:"mcpLogs"`
	Purchases   []*MarketplacePurchaseSummary `json:"purchases"`
	Usage       []*MarketplaceUsageSummary    `json:"usage"`
	Webhooks    *WebhookDeliveryStats         `json:"webhooks,omitempty"`
}

func (db *DB) RecordAPIRequestLog(ctx context.Context, in APIRequestLogInput) error {
	if db == nil || db.SQL == nil {
		return nil
	}
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO api_request_logs (
		  id, request_id, method, path, route_class, status_code, duration_ms,
		  api_key_hash, api_key_scope, auth_mode, client_ip, user_agent
		) VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),NULLIF($11,''),NULLIF($12,''))`,
		NewID(), strings.TrimSpace(in.RequestID), strings.ToUpper(strings.TrimSpace(in.Method)),
		strings.TrimSpace(in.Path), strings.TrimSpace(in.RouteClass), in.StatusCode, in.DurationMS,
		strings.TrimSpace(in.APIKeyHash), strings.TrimSpace(in.APIKeyScope), strings.TrimSpace(in.AuthMode),
		strings.TrimSpace(in.ClientIP), strings.TrimSpace(in.UserAgent))
	return err
}

func (db *DB) RecordMCPToolLog(ctx context.Context, in MCPToolLogInput) error {
	if db == nil || db.SQL == nil {
		return nil
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "ok"
	}
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO mcp_tool_logs (
		  id, request_id, tool_name, status, error_message, duration_ms, api_key_hash, auth_mode
		) VALUES ($1,NULLIF($2,''),$3,$4,NULLIF($5,''),$6,NULLIF($7,''),NULLIF($8,''))`,
		NewID(), strings.TrimSpace(in.RequestID), strings.TrimSpace(in.ToolName), status,
		strings.TrimSpace(in.ErrorMessage), in.DurationMS, strings.TrimSpace(in.APIKeyHash), strings.TrimSpace(in.AuthMode))
	return err
}

func (db *DB) DeveloperDashboard(ctx context.Context, limit int) (*DeveloperDashboardSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	apiLogs, err := db.ListAPIRequestLogs(ctx, limit)
	if err != nil {
		return nil, err
	}
	mcpLogs, err := db.ListMCPToolLogs(ctx, limit)
	if err != nil {
		return nil, err
	}
	purchases, err := db.ListMarketplacePurchaseSummaries(ctx, limit)
	if err != nil {
		return nil, err
	}
	usage, err := db.ListMarketplaceUsageSummaries(ctx, limit)
	if err != nil {
		return nil, err
	}
	webhookStats, _ := db.WebhookDashboardStats(ctx)
	counts, err := db.developerDashboardCounts(ctx)
	if err != nil {
		return nil, err
	}
	return &DeveloperDashboardSummary{
		GeneratedAt: time.Now().UTC(),
		Counts:      counts,
		Health: map[string]any{
			"apiRequestLogging": true,
			"mcpToolLogging":    true,
			"payloadStorage":    "redacted",
		},
		APILogs:   apiLogs,
		MCPLogs:   mcpLogs,
		Purchases: purchases,
		Usage:     usage,
		Webhooks:  webhookStats,
	}, nil
}

func (db *DB) developerDashboardCounts(ctx context.Context) (map[string]int, error) {
	counts := map[string]int{}
	queries := map[string]string{
		"apiLogs24h":         `SELECT COUNT(*) FROM api_request_logs WHERE created_at > now() - interval '24 hours'`,
		"mcpCalls24h":        `SELECT COUNT(*) FROM mcp_tool_logs WHERE created_at > now() - interval '24 hours'`,
		"mcpErrors24h":       `SELECT COUNT(*) FROM mcp_tool_logs WHERE created_at > now() - interval '24 hours' AND status <> 'ok'`,
		"purchases24h":       `SELECT COUNT(*) FROM marketplace_purchases WHERE created_at > now() - interval '24 hours'`,
		"activePurchases":    `SELECT COUNT(*) FROM marketplace_purchases WHERE status = 'active'`,
		"capabilityUsage24h": `SELECT COUNT(*) FROM marketplace_execution_events WHERE created_at > now() - interval '24 hours'`,
	}
	for key, query := range queries {
		var n int
		if err := db.SQL.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return nil, err
		}
		counts[key] = n
	}
	return counts, nil
}

func (db *DB) ListAPIRequestLogs(ctx context.Context, limit int) ([]*APIRequestLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id::text, request_id, method, path, route_class, status_code, duration_ms,
		       COALESCE(api_key_hash,''), COALESCE(api_key_scope,''), COALESCE(auth_mode,''),
		       COALESCE(client_ip,''), COALESCE(user_agent,''), created_at
		FROM api_request_logs
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*APIRequestLog{}
	for rows.Next() {
		item := &APIRequestLog{}
		if err := rows.Scan(&item.ID, &item.RequestID, &item.Method, &item.Path, &item.RouteClass,
			&item.StatusCode, &item.DurationMS, &item.APIKeyHash, &item.APIKeyScope, &item.AuthMode,
			&item.ClientIP, &item.UserAgent, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListMCPToolLogs(ctx context.Context, limit int) ([]*MCPToolLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id::text, COALESCE(request_id,''), tool_name, status, COALESCE(error_message,''),
		       duration_ms, COALESCE(api_key_hash,''), COALESCE(auth_mode,''), created_at
		FROM mcp_tool_logs
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MCPToolLog{}
	for rows.Next() {
		item := &MCPToolLog{}
		if err := rows.Scan(&item.ID, &item.RequestID, &item.ToolName, &item.Status, &item.ErrorMessage,
			&item.DurationMS, &item.APIKeyHash, &item.AuthMode, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListMarketplacePurchaseSummaries(ctx context.Context, limit int) ([]*MarketplacePurchaseSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, product_id, plan_id, agent_wallet, payment_asset, network,
		       gross_amount::text, provider_amount::text, status, COALESCE(tx_hash,''), created_at
		FROM marketplace_purchases
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MarketplacePurchaseSummary{}
	for rows.Next() {
		item := &MarketplacePurchaseSummary{}
		if err := rows.Scan(&item.ID, &item.ProductID, &item.PlanID, &item.AgentWallet, &item.PaymentAsset,
			&item.Network, &item.GrossAmount, &item.ProviderAmount, &item.Status, &item.TxHash, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListMarketplaceUsageSummaries(ctx context.Context, limit int) ([]*MarketplaceUsageSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, capability_id, provider_slug, routing_mode, operation, status,
		       units_consumed, quota_remaining, COALESCE(latency_ms,0), request_id, created_at
		FROM marketplace_execution_events
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MarketplaceUsageSummary{}
	for rows.Next() {
		item := &MarketplaceUsageSummary{}
		if err := rows.Scan(&item.ID, &item.CapabilityID, &item.ProviderSlug, &item.RoutingMode,
			&item.Operation, &item.Status, &item.UnitsConsumed, &item.QuotaRemaining,
			&item.LatencyMS, &item.RequestID, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func ScanNullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}
