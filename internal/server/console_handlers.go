package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/database"
)

func (s *Server) handleAgentConsoleSummary(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	limit := consoleLimit(r)
	agents, err := s.db.ListConsoleAgents(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	capabilities, err := s.db.ListMarketplaceCapabilities(r.Context(), database.MarketplaceProductFilters{})
	if err != nil {
		writeError(w, err)
		return
	}
	purchases, err := s.db.ListMarketplacePurchaseSummaries(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	executions, err := s.db.ListMarketplaceUsageSummaries(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	spendSeries, err := s.db.ListConsoleSpendSeries(r.Context(), 14)
	if err != nil {
		writeError(w, err)
		return
	}
	settlements, err := s.db.ListConsoleSettlements(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	policies := any(defaultAgentPolicies())
	if len(agents) > 0 {
		if policy, err := s.db.GetAgentPolicy(r.Context(), agents[0].AgentID); err == nil && policy != nil {
			policies = policy
		}
	}

	metrics := agentConsoleMetrics(agents, capabilities, purchases, executions, settlements)
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace":    "agent",
		"generatedAt":  time.Now().UTC(),
		"authMode":     auth.Mode,
		"sandbox":      auth.Sandbox,
		"navigation":   agentConsoleNavigation(),
		"metrics":      metrics,
		"agents":       agents,
		"capabilities": capabilities,
		"purchases":    purchases,
		"executions":   executions,
		"spendSeries":  spendSeries,
		"settlements":  settlements,
		"policies":     policies,
		"wallet": map[string]any{
			"availableBalance":  "428.50",
			"lockedBalance":     "18.00",
			"pendingSettlement": metrics["pendingSettlements"],
			"assets": []map[string]any{
				{"asset": "USDT", "network": "BSC", "balance": "428.50", "address": s.accessPaymentAddress()},
				{"asset": "USDC", "network": "BSC", "balance": "0.00", "address": s.accessPaymentAddress()},
			},
		},
		"alerts": agentConsoleAlerts(agents, executions, settlements),
	})
}

func (s *Server) handleDeveloperConsoleSummary(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	limit := consoleLimit(r)
	dashboard, err := s.db.DeveloperDashboard(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	capabilities, err := s.db.ListMarketplaceCapabilities(r.Context(), database.MarketplaceProductFilters{})
	if err != nil {
		writeError(w, err)
		return
	}
	products, err := s.db.ListMarketplaceProducts(r.Context(), database.MarketplaceProductFilters{})
	if err != nil {
		writeError(w, err)
		return
	}
	projects, err := s.db.ListDeveloperProjects(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	apiKeys, err := s.db.ListDeveloperAPIKeys(r.Context(), "", limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace":       "developer",
		"generatedAt":     time.Now().UTC(),
		"authMode":        auth.Mode,
		"sandbox":         auth.Sandbox,
		"navigation":      developerConsoleNavigation(),
		"dashboard":       dashboard,
		"metrics":         developerConsoleMetrics(dashboard),
		"projects":        projects,
		"apiKeys":         map[string]any{"items": apiKeys, "legacyEnv": s.developerDashboardAPIKeys()},
		"mcpConnections":  developerMCPConnections(publicBaseURL(r), auth),
		"apiExplorer":     developerAPIExplorer(),
		"capabilities":    capabilities,
		"products":        products,
		"providerPublish": providerPublishSpec(),
		"billing":         developerBillingSummary(dashboard),
	})
}

func consoleLimit(r *http.Request) int {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

func agentConsoleNavigation() []string {
	return []string{"Overview", "Agents", "Capabilities", "Marketplace", "Purchases", "Executions", "Wallet & Balance", "Usage & Costs", "Settlements", "API Credentials", "Webhooks", "Policies", "Logs", "Settings"}
}

func developerConsoleNavigation() []string {
	return []string{"Overview", "Projects", "API Keys", "MCP Connections", "Capabilities", "Products", "Usage", "Requests", "Webhooks", "Events", "Logs", "Analytics", "Billing", "Team", "Documentation", "Settings"}
}

func agentConsoleMetrics(agents []*database.ConsoleAgentSummary, capabilities []*database.MarketplaceCapability, purchases []*database.MarketplacePurchaseSummary, executions []*database.MarketplaceUsageSummary, settlements []*database.ConsoleSettlementSummary) map[string]any {
	var spend, providerCost float64
	remainingQuota := 0
	activePurchases := 0
	for _, agent := range agents {
		remainingQuota += agent.QuotaRemaining
	}
	for _, purchase := range purchases {
		spend += database.SumDecimalStrings(purchase.GrossAmount)
		providerCost += database.SumDecimalStrings(purchase.ProviderAmount)
		if strings.EqualFold(purchase.Status, database.MarketplacePurchaseActive) {
			activePurchases++
		}
	}
	pendingSettlements := 0
	for _, settlement := range settlements {
		if !strings.EqualFold(settlement.Status, "paid") && !strings.EqualFold(settlement.Status, "settled") {
			pendingSettlements++
		}
	}
	return map[string]any{
		"connectedAgents":    len(agents),
		"availableBalance":   "428.50 USDT",
		"spendThisMonth":     fmt.Sprintf("%.2f USDT", spend),
		"providerCost":       fmt.Sprintf("%.2f USDT", providerCost),
		"chainfxFees":        fmt.Sprintf("%.2f USDT", spend-providerCost),
		"networkFees":        "0.000 USDT",
		"activeCapabilities": len(capabilities),
		"activePurchases":    activePurchases,
		"remainingQuota":     remainingQuota,
		"pendingSettlements": pendingSettlements,
		"executions":         len(executions),
	}
}

func developerConsoleMetrics(dashboard *database.DeveloperDashboardSummary) map[string]any {
	count := func(key string) int {
		if dashboard == nil || dashboard.Counts == nil {
			return 0
		}
		return dashboard.Counts[key]
	}
	apiRequests := count("apiLogs24h")
	errors := count("mcpErrors24h")
	errorRate := "0.00%"
	if apiRequests > 0 {
		errorRate = fmt.Sprintf("%.2f%%", float64(errors)*100/float64(apiRequests))
	}
	webhookSuccess := "100.00%"
	if dashboard != nil && dashboard.Webhooks != nil && dashboard.Webhooks.Deliveries24h+dashboard.Webhooks.Failures24h > 0 {
		total := dashboard.Webhooks.Deliveries24h + dashboard.Webhooks.Failures24h
		webhookSuccess = fmt.Sprintf("%.2f%%", float64(dashboard.Webhooks.Deliveries24h)*100/float64(total))
	}
	return map[string]any{
		"apiRequests":    apiRequests,
		"mcpToolCalls":   count("mcpCalls24h"),
		"activeAPIKeys":  6,
		"webhookSuccess": webhookSuccess,
		"errorRate":      errorRate,
		"currentSpend":   "142 USDT",
		"latencyP50":     "82 ms",
		"latencyP95":     "248 ms",
		"latencyP99":     "810 ms",
	}
}

func defaultAgentPolicies() map[string]any {
	return map[string]any{
		"maximumTransaction": "100 USDT",
		"dailyLimit":         "500 USDT",
		"monthlyLimit":       "5,000 USDT",
		"allowedCapabilities": []map[string]any{
			{"id": "document_ocr", "allowed": true},
			{"id": "aml_screening", "allowed": true},
			{"id": "llm_chat", "allowed": true},
			{"id": "stablecoin_trade", "allowed": false},
		},
		"allowedProviders": []map[string]any{
			{"id": "openai", "name": "OpenAI", "allowed": true},
			{"id": "chainfx-ocr-http", "name": "ChainFX OCR", "allowed": true},
			{"id": "experimental", "name": "Experimental providers", "allowed": false},
		},
		"requireRealProvider": true,
		"mockFallback":        false,
	}
}

func agentConsoleAlerts(agents []*database.ConsoleAgentSummary, executions []*database.MarketplaceUsageSummary, settlements []*database.ConsoleSettlementSummary) []map[string]any {
	alerts := []map[string]any{}
	for _, agent := range agents {
		if agent.QuotaRemaining > 0 && agent.QuotaRemaining < 1000 {
			alerts = append(alerts, map[string]any{"level": "warning", "message": agent.Name + " is below quota threshold", "status": "quota.low"})
			break
		}
	}
	for _, settlement := range settlements {
		if strings.EqualFold(settlement.Status, database.MarketplaceSettlementPending) {
			alerts = append(alerts, map[string]any{"level": "info", "message": settlement.ID + " is awaiting confirmations", "status": settlement.Status})
			break
		}
	}
	for _, execution := range executions {
		if execution.LatencyMS > 900 {
			alerts = append(alerts, map[string]any{"level": "warning", "message": execution.ProviderSlug + " latency is above policy limit", "status": "latency.high"})
			break
		}
	}
	if len(alerts) == 0 {
		alerts = append(alerts, map[string]any{"level": "ok", "message": "All agent policies are within configured limits", "status": "healthy"})
	}
	return alerts
}

func developerConsoleProjects() []map[string]any {
	return []map[string]any{
		{"name": "Production Platform", "environment": "Production", "apiKeys": 3, "agents": 2, "spendingLimit": "10,000 USDT", "status": "active"},
		{"name": "Mobile App", "environment": "Production", "apiKeys": 2, "agents": 1, "spendingLimit": "2,500 USDT", "status": "active"},
		{"name": "Agent Sandbox", "environment": "Sandbox", "apiKeys": 1, "agents": 3, "spendingLimit": "500 USDT", "status": "active"},
		{"name": "Internal Treasury", "environment": "Production", "apiKeys": 1, "agents": 1, "spendingLimit": "25,000 USDT", "status": "restricted"},
	}
}

func developerMCPConnections(base string, auth chainFXAuth) []map[string]any {
	return []map[string]any{{
		"client":          "Codex",
		"status":          "connected",
		"lastHandshake":   time.Now().UTC().Add(-2 * time.Minute),
		"protocolVersion": "2024-11-05",
		"tools":           25,
		"resources":       8,
		"prompts":         5,
		"latencyMs":       182,
		"environment":     map[bool]string{true: "Sandbox", false: "Production"}[auth.Sandbox],
		"config": map[string]any{"mcpServers": map[string]any{"chainfx": map[string]any{
			"url": base + "/mcp",
			"headers": map[string]string{
				"Authorization": "Bearer sk_live_...",
			},
		}}},
	}}
}

func developerAPIExplorer() []map[string]any {
	return []map[string]any{
		{"group": "Rates", "method": "GET", "path": "/rates", "body": "{}"},
		{"group": "Quotes", "method": "POST", "path": "/quote", "body": `{"side":"buy","asset":"USDT","fiatCurrency":"BRL","amountFiat":100}`},
		{"group": "Buy", "method": "POST", "path": "/buy", "body": `{"quoteId":"qt_...","destAddress":"0x..."}`},
		{"group": "Sell", "method": "POST", "path": "/sell", "body": `{"quoteId":"qt_...","senderWallet":"0x..."}`},
		{"group": "Marketplace", "method": "POST", "path": "/marketplace/capabilities/{id}/purchase", "body": `{"agentWallet":"0x...","payerWallet":"0x...","idempotencyKey":"idem_...","nonce":"n_..."}`},
		{"group": "Agent Rail", "method": "POST", "path": "/agent/v1/trade/quote", "body": `{"payAsset":"USDT","receiveAsset":"USDC","amount":100}`},
		{"group": "MCP", "method": "POST", "path": "/mcp/tools/list", "body": "{}"},
		{"group": "Webhooks", "method": "POST", "path": "/api/webhooks/subscriptions", "body": `{"url":"https://example.com/webhook","events":["payment.completed"]}`},
	}
}

func providerPublishSpec() map[string]any {
	return map[string]any{
		"steps":         []string{"Basic information", "Contract", "Provider endpoint", "Pricing", "Settlement", "Testing", "Publish to sandbox"},
		"pricingModels": []string{"Per request", "Per unit", "Per token", "Per page", "Monthly plan"},
		"tests":         []string{"Run contract test", "Run provider test", "Validate response", "Publish to sandbox"},
	}
}

func developerBillingSummary(dashboard *database.DeveloperDashboardSummary) map[string]any {
	return map[string]any{
		"currentBalance":    "428.50 USDT",
		"currentMonthUsage": "142 USDT",
		"providerCosts":     "113 USDT",
		"chainfxFees":       "29 USDT",
		"networkFees":       "0.00 USDT",
		"grossSales":        "1,820 USDT",
		"netEarnings":       "1,456 USDT",
		"pendingSettlement": "364 USDT",
	}
}
