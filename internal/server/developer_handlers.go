package server

import (
	"fmt"
	"html"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/database"

	"gopkg.in/yaml.v3"
)

func (s *Server) handleDeveloperAPIKeys(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":              auth.Mode,
		"sandbox":           auth.Sandbox,
		"requireApiKey":     s.cfg.ChainFXRequireAPIKey,
		"livePublicKeys":    maskCSVKeys(s.cfg.ChainFXLivePublicKeys),
		"liveSecretKeys":    maskCSVKeys(s.cfg.ChainFXLiveSecretKeys),
		"testPublicKeys":    maskCSVKeys(s.cfg.ChainFXTestPublicKeys),
		"testSecretKeys":    maskCSVKeys(s.cfg.ChainFXTestSecretKeys),
		"authentication":    "Authorization: Bearer sk_live_xxx",
		"productionWarning": "Do not use sk_test keys on the production host.",
	})
}

func (s *Server) handleDeveloperLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.db.ListDeveloperEvents(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"count":  len(events),
	})
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeAdminConsoleKey(w, r) {
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	user, token, err := s.db.AuthenticateAdmin(r.Context(), req.Email, req.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "credenciais invalidas"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":            token,
		"user":             user,
		"expiresInSeconds": 43200,
	})
}

func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	adminUser, auth, ok := s.authorizeAdmin(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	transactions, err := s.db.ListAdminTransactions(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	events, err := s.db.ListDeveloperEvents(r.Context(), minInt(limit, 200))
	if err != nil {
		writeError(w, err)
		return
	}
	certOK, certErr := s.efiCertificateReady()
	gaps := s.operationalGaps()
	ready := map[string]any{
		"ok":              len(gaps) == 0 && certOK,
		"db":              true,
		"network":         s.deliveryNetwork(),
		"bsc":             s.cfg.BscRpcUrls != "" && s.cfg.BscUsdtContract != "",
		"pix":             s.efiPixConfigured() && certOK && defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret) != "",
		"efi_card":        s.efiChargesConfigured() && certOK,
		"efi_certificate": certOK,
		"efi_cert_source": s.efiCertificateSource(),
		"signer":          s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		"mode":            s.cfg.Environment,
		"warnings":        gaps,
	}
	if certErr != "" {
		ready["efi_cert_error"] = certErr
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt":     time.Now().UTC().Format(time.RFC3339Nano),
		"authMode":        auth.Mode,
		"sandbox":         auth.Sandbox,
		"adminUser":       adminUser,
		"readiness":       ready,
		"rates":           s.adminRates(),
		"metrics":         summarizeAdminTransactions(transactions),
		"transactions":    transactions,
		"events":          events,
		"operationalGaps": gaps,
	})
}

func (s *Server) handleAdminTransactions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	transactions, err := s.db.ListAdminTransactions(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"transactions": transactions,
		"count":        len(transactions),
	})
}

func (s *Server) adminRates() map[string]any {
	return map[string]any{
		"USDT_BRL": s.workers.PriceWorker.GetPrice("BRL"),
		"USDT_USD": s.workers.PriceWorker.GetPrice("USD"),
		"USDT_EUR": s.workers.PriceWorker.GetPrice("EUR"),
		"BTC_USDT": s.workers.PriceWorker.GetPrice("BTCUSDT"),
		"source":   "price_worker",
	}
}

func summarizeAdminTransactions(transactions []database.AdminTransaction) map[string]any {
	byStatus := map[string]int{}
	bySource := map[string]int{}
	var grossBRL, feesBRL, netBRL, cryptoAmount float64
	var sent, failed, pending int
	for _, tx := range transactions {
		byStatus[tx.Status]++
		bySource[tx.Source]++
		grossBRL += tx.AmountBRL
		feesBRL += tx.FeeBRL
		netBRL += tx.PayoutBRL
		cryptoAmount += tx.CryptoAmount
		status := strings.ToLower(tx.Status)
		switch {
		case strings.Contains(status, "erro") || strings.Contains(status, "failed") || strings.Contains(status, "rejeit"):
			failed++
		case strings.Contains(status, "enviado") || strings.Contains(status, "delivered") || strings.Contains(status, "confirmado") || strings.Contains(status, "conclu"):
			sent++
		case strings.Contains(status, "aguardando") || strings.Contains(status, "pending"):
			pending++
		}
	}
	return map[string]any{
		"count":        len(transactions),
		"byStatus":     byStatus,
		"bySource":     bySource,
		"grossBRL":     math.Round(grossBRL*100) / 100,
		"feesBRL":      math.Round(feesBRL*100) / 100,
		"netBRL":       math.Round(netBRL*100) / 100,
		"cryptoAmount": cryptoAmount,
		"sent":         sent,
		"failed":       failed,
		"pending":      pending,
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *Server) handleDevelopersDashboard(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	dashboard, err := s.db.DeveloperDashboard(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	if strings.EqualFold(r.URL.Query().Get("format"), "json") || strings.HasSuffix(r.URL.Path, ".json") || strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{
			"authMode":        auth.Mode,
			"sandbox":         auth.Sandbox,
			"dashboard":       dashboard,
			"apiKeys":         s.developerDashboardAPIKeys(),
			"productionNotes": s.developerDashboardProductionNotes(),
		})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(s.renderDeveloperDashboardHTML(r, dashboard, auth)))
}

func (s *Server) developerDashboardAPIKeys() map[string]any {
	return map[string]any{
		"livePublic": maskCSVKeys(s.cfg.ChainFXLivePublicKeys),
		"liveSecret": maskCSVKeys(s.cfg.ChainFXLiveSecretKeys),
		"testPublic": maskCSVKeys(s.cfg.ChainFXTestPublicKeys),
		"testSecret": maskCSVKeys(s.cfg.ChainFXTestSecretKeys),
	}
}

func (s *Server) developerDashboardProductionNotes() []string {
	return []string{
		"API keys are never stored in plaintext in request/tool logs; only short hashes are recorded.",
		"MCP mock_dev fallback and seed fixtures are not production providers.",
		"Redis-backed rate limiting remains optional until multi-instance deployment is enabled.",
	}
}

func (s *Server) renderDeveloperDashboardHTML(r *http.Request, dashboard *database.DeveloperDashboardSummary, auth chainFXAuth) string {
	keys := s.developerDashboardAPIKeys()
	card := func(title string, value any, note string) string {
		return fmt.Sprintf(`<article class="card"><span>%s</span><strong>%v</strong><small>%s</small></article>`, html.EscapeString(title), value, html.EscapeString(note))
	}
	apiRows := strings.Builder{}
	for _, item := range dashboard.APILogs {
		apiRows.WriteString(fmt.Sprintf(`<tr><td>%s</td><td><code>%s</code></td><td>%s</td><td>%s</td><td>%d</td><td>%dms</td><td><code>%s</code></td></tr>`,
			html.EscapeString(item.CreatedAt.Format(time.RFC3339)),
			html.EscapeString(item.RequestID),
			html.EscapeString(item.Method),
			html.EscapeString(item.Path),
			item.StatusCode,
			item.DurationMS,
			html.EscapeString(item.APIKeyHash),
		))
	}
	mcpRows := strings.Builder{}
	for _, item := range dashboard.MCPLogs {
		mcpRows.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td>%s</td><td>%dms</td><td><code>%s</code></td><td>%s</td></tr>`,
			html.EscapeString(item.CreatedAt.Format(time.RFC3339)),
			html.EscapeString(item.ToolName),
			html.EscapeString(item.Status),
			item.DurationMS,
			html.EscapeString(item.APIKeyHash),
			html.EscapeString(item.ErrorMessage),
		))
	}
	purchaseRows := strings.Builder{}
	for _, item := range dashboard.Purchases {
		purchaseRows.WriteString(fmt.Sprintf(`<tr><td>%s</td><td><code>%s</code></td><td>%s</td><td>%s</td><td>%s %s</td><td>%s</td></tr>`,
			html.EscapeString(item.CreatedAt.Format(time.RFC3339)),
			html.EscapeString(item.ID),
			html.EscapeString(item.ProductID),
			html.EscapeString(item.Status),
			html.EscapeString(item.GrossAmount),
			html.EscapeString(item.PaymentAsset),
			html.EscapeString(item.Network),
		))
	}
	usageRows := strings.Builder{}
	for _, item := range dashboard.Usage {
		usageRows.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%d</td><td>%d</td><td>%dms</td></tr>`,
			html.EscapeString(item.CreatedAt.Format(time.RFC3339)),
			html.EscapeString(item.CapabilityID),
			html.EscapeString(item.ProviderSlug),
			html.EscapeString(item.Status),
			item.UnitsConsumed,
			item.QuotaRemaining,
			item.LatencyMS,
		))
	}
	webhookDeliveries, webhookFailures := 0, 0
	webhookActive := 0
	if dashboard.Webhooks != nil {
		webhookDeliveries = dashboard.Webhooks.Deliveries24h
		webhookFailures = dashboard.Webhooks.Failures24h
		webhookActive = dashboard.Webhooks.ActiveSubscriptions
	}
	notes := strings.Builder{}
	for _, note := range s.developerDashboardProductionNotes() {
		notes.WriteString("<li>" + html.EscapeString(note) + "</li>")
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>ChainFX Developer Dashboard</title>
  <style>
    :root{--bg:#eef4fa;--panel:#fff;--ink:#102a43;--muted:#64748b;--line:#d8e5f2;--blue:#1266d6;--cyan:#12b7d8;--ok:#0f8a5f;--warn:#b7791f}
    *{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.5 Inter,system-ui,Segoe UI,sans-serif}
    header{padding:34px 28px;background:linear-gradient(135deg,#fff,#e8f8ff);border-bottom:1px solid var(--line)}
    main{padding:24px 28px;max-width:1320px;margin:auto}.grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:14px}.card{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:16px;box-shadow:0 16px 40px rgba(16,42,67,.08)}
    .card span{display:block;color:var(--muted);font-size:12px;text-transform:uppercase}.card strong{display:block;font-size:28px;margin:3px 0}.card small{color:var(--muted)}
    h1{margin:0 0 6px;font-size:34px}h2{margin:26px 0 12px;font-size:18px}p{margin:0;color:var(--muted)}code{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}
    table{width:100%%;border-collapse:collapse;background:#fff;border:1px solid var(--line);border-radius:8px;overflow:hidden}th,td{padding:10px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}th{background:#f8fbff;color:#475569}td code{white-space:pre-wrap;word-break:break-word;font-size:12px}
    .actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}a{color:#fff;background:linear-gradient(135deg,var(--blue),var(--cyan));padding:10px 12px;border-radius:8px;text-decoration:none;font-weight:700}.muted{color:var(--muted)}.split{display:grid;grid-template-columns:1fr 1fr;gap:18px}.panel{background:#fff;border:1px solid var(--line);border-radius:8px;padding:16px}.notes li{margin:5px 0;color:var(--muted)}
    @media(max-width:900px){.grid{grid-template-columns:1fr}main,header{padding-left:18px;padding-right:18px}}
  </style>
</head>
<body>
  <header>
    <h1>ChainFX Developer Dashboard</h1>
    <p>Production operations for APIs, MCP, Capability Marketplace, usage, billing and webhooks.</p>
    <div class="actions"><a href="/developers/dashboard.json">Dashboard JSON</a><a href="/developers/api-keys">API Keys JSON</a><a href="/openapi.json">OpenAPI</a><a href="/.well-known/ai-services.json">Agent Discovery</a></div>
  </header>
  <main>
    <section class="grid">
      %s%s%s%s
      %s%s%s%s
    </section>
    <section class="split" style="margin-top:18px">
      <div class="panel"><h2>API Keys</h2><p>Live public <code>%v</code></p><p>Live secret <code>%v</code></p><p>Test public <code>%v</code></p><p>Test secret <code>%v</code></p></div>
      <div class="panel"><h2>Production Notes</h2><ul class="notes">%s</ul></div>
    </section>
    <h2>API Logs</h2>
    <table><thead><tr><th>At</th><th>Request</th><th>Method</th><th>Path</th><th>Status</th><th>Latency</th><th>Key hash</th></tr></thead><tbody>%s</tbody></table>
    <h2>MCP Tool Calls</h2>
    <table><thead><tr><th>At</th><th>Tool</th><th>Status</th><th>Latency</th><th>Key hash</th><th>Error</th></tr></thead><tbody>%s</tbody></table>
    <h2>Marketplace Purchases</h2>
    <table><thead><tr><th>At</th><th>Purchase</th><th>Product</th><th>Status</th><th>Gross</th><th>Network</th></tr></thead><tbody>%s</tbody></table>
    <h2>Capability Usage</h2>
    <table><thead><tr><th>At</th><th>Capability</th><th>Provider</th><th>Status</th><th>Units</th><th>Quota</th><th>Latency</th></tr></thead><tbody>%s</tbody></table>
  </main>
</body>
</html>`,
		card("API logs 24h", dashboard.Counts["apiLogs24h"], "HTTP requests"),
		card("MCP calls 24h", dashboard.Counts["mcpCalls24h"], "tool calls"),
		card("Capability usage 24h", dashboard.Counts["capabilityUsage24h"], "executions"),
		card("Purchases 24h", dashboard.Counts["purchases24h"], "marketplace intents"),
		card("Active purchases", dashboard.Counts["activePurchases"], "granted access"),
		card("Webhook deliveries 24h", webhookDeliveries, "outbound callbacks"),
		card("Webhook failures 24h", webhookFailures, "delivery errors"),
		card("Active webhooks", webhookActive, "subscriptions"),
		keys["livePublic"], keys["liveSecret"], keys["testPublic"], keys["testSecret"],
		notes.String(), apiRows.String(), mcpRows.String(), purchaseRows.String(), usageRows.String(),
	)
}

func (s *Server) handleDevelopers(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>ChainFX Developers</title>
  <style>
    :root{color-scheme:light;--ink:#102a43;--muted:#5f6c7b;--line:#dbe7f3;--blue:#0b72d9;--cyan:#0fb7d4;--bg:#f6f9fc}
    *{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.55 Inter,system-ui,-apple-system,Segoe UI,sans-serif}
    header{padding:72px 24px 44px;background:linear-gradient(135deg,#fff,#edf8ff);border-bottom:1px solid var(--line)}
    main{max-width:1120px;margin:auto;padding:32px 24px 64px}.hero,.grid{max-width:1120px;margin:auto}
    h1{font-size:clamp(36px,5vw,68px);line-height:1;margin:0 0 18px}h2{margin:0 0 12px;font-size:22px}p{color:var(--muted);margin:0 0 18px}
    code,pre{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}pre{overflow:auto;background:#0b1726;color:#dff7ff;padding:18px;border-radius:8px}
    .pill{display:inline-flex;margin:0 8px 8px 0;padding:7px 10px;border:1px solid var(--line);border-radius:999px;background:#fff;color:var(--muted)}
    .grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}.card{background:#fff;border:1px solid var(--line);border-radius:8px;padding:20px;box-shadow:0 16px 42px rgba(16,42,67,.08)}
    a{color:var(--blue);font-weight:700;text-decoration:none}.cta{display:inline-flex;padding:12px 16px;border-radius:8px;color:#fff;background:linear-gradient(135deg,var(--blue),var(--cyan))}
    @media(max-width:820px){.grid{grid-template-columns:1fr}header{padding-top:44px}}
  </style>
</head>
<body>
  <header><section class="hero">
    <span class="pill">Digital FX Payments Infrastructure</span>
    <h1>ChainFX Developers</h1>
    <p>Accept PIX. Deliver digital dollars. Receive stablecoins. Pay out PIX.</p>
    <a class="cta" href="/openapi.json">OpenAPI JSON</a>
  </section></header>
  <main>
    <section class="grid">
      <article class="card"><h2>REST API</h2><p>GET /rates, POST /quote, POST /buy, POST /sell, GET /order/:id.</p></article>
      <article class="card"><h2>Webhooks</h2><p>payment.created, payment.completed, payment.failed, order.confirmed, crypto.sent, crypto.confirmed, order.failed.</p></article>
      <article class="card"><h2>Sandbox</h2><p>Use <code>sk_test_chainfx_local</code> with fake PIX, fake QR, fake wallet and simulated webhook payloads.</p></article>
      <article class="card"><h2>API Keys</h2><p><code>Authorization: Bearer sk_live_xxx</code> or <code>sk_test_xxx</code>. Configure live keys with <code>CHAINFX_LIVE_SECRET_KEYS</code>.</p></article>
      <article class="card"><h2>SDKs</h2><p>Phase 3 includes Node and Python SDKs in the repository. Go and PHP stay on the roadmap.</p></article>
      <article class="card"><h2>Status</h2><p>Use <a href="/readyz">/readyz</a> for backend readiness and <a href="/rates">/rates</a> for rate availability.</p></article>
      <article class="card"><h2>Dashboard</h2><p><code>GET /developers/dashboard</code> uses <code>Authorization: Bearer sk_live_xxx</code>. Do not put secret keys in URLs.</p></article>
      <article class="card"><h2>Logs</h2><p><code>GET /developers/logs</code> reads recent buy/sell events from the gateway audit tables.</p></article>
      <article class="card"><h2>Retry</h2><p><code>POST /webhooks/retry</code> rebuilds a webhook payload from an order and optionally posts it to a target URL.</p></article>
    </section>
    <h2 style="margin-top:32px">Quote Example</h2>
    <pre>POST /quote
{
  "side": "buy",
  "fiat": "BRL",
  "asset": "USDT",
  "amount": 500
}</pre>
    <h2>Node Example</h2>
    <pre>const order = await chainfx.buy({
  fiat: "BRL",
  asset: "USDT",
  amount: 500,
  wallet: "0x..."
});</pre>
  </main>
</body>
</html>`))
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	if raw, err := os.ReadFile("docs/openapi/chainfx.openapi.yaml"); err == nil {
		var doc any
		if err := yaml.Unmarshal(raw, &doc); err == nil {
			writeJSON(w, http.StatusOK, doc)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "ChainFX API",
			"version":     "1.0.0-phase3",
			"description": "Digital FX Payments API for PIX <> stablecoin flows. Phase 3 includes SDK Node/Python, OpenAPI and examples.",
		},
		"servers": []map[string]string{
			{"url": "https://api.chainfx.com", "description": "Production"},
			{"url": "https://sandbox-api.chainfx.com", "description": "Sandbox"},
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]string{"type": "http", "scheme": "bearer"},
			},
		},
		"paths": map[string]any{
			"/rates":                                       map[string]any{"get": map[string]any{"summary": "Current FX and crypto rates"}},
			"/quote":                                       map[string]any{"post": map[string]any{"summary": "Create a rate-locked quote"}},
			"/buy":                                         map[string]any{"post": map[string]any{"summary": "Create a PIX/card to USDT order", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/sell":                                        map[string]any{"post": map[string]any{"summary": "Create a USDT to PIX BRL order", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/order/{id}":                                  map[string]any{"get": map[string]any{"summary": "Read an order by ID"}},
			"/webhooks/test":                               map[string]any{"post": map[string]any{"summary": "Generate a simulated webhook payload", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/webhooks/retry":                              map[string]any{"post": map[string]any{"summary": "Retry a webhook for an existing order", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/developers/api-keys":                         map[string]any{"get": map[string]any{"summary": "List configured API keys masked", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/developers/logs":                             map[string]any{"get": map[string]any{"summary": "List recent developer logs", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/mcp/initialize":                              map[string]any{"post": map[string]any{"summary": "Initialize MCP HTTP session for autonomous agents", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/mcp/tools/list":                              map[string]any{"post": map[string]any{"summary": "List MCP tools exposed by ChainFX", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/mcp/tools/call":                              map[string]any{"post": map[string]any{"summary": "Call an MCP tool exposed by ChainFX", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/api/agents/market-analysis":                  map[string]any{"post": map[string]any{"summary": "Generate AI market analysis from current rates", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/api/agents/recommend":                        map[string]any{"post": map[string]any{"summary": "Generate AI buy/sell/hold recommendation", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/api/agents/anomalies":                        map[string]any{"post": map[string]any{"summary": "Detect anomalies in transaction payloads", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/api/agents/predict":                          map[string]any{"post": map[string]any{"summary": "Generate short-horizon price prediction", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/api/agents/summary":                          map[string]any{"post": map[string]any{"summary": "Summarize transaction activity", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/api/webhooks/events":                         map[string]any{"get": map[string]any{"summary": "List automation webhook event names"}},
			"/api/webhooks/subscriptions":                  map[string]any{"get": map[string]any{"summary": "List automation webhook subscriptions", "security": []map[string][]string{{"bearerAuth": []string{}}}}, "post": map[string]any{"summary": "Create automation webhook subscription", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/api/webhooks/dashboard":                      map[string]any{"get": map[string]any{"summary": "Webhook delivery health dashboard", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/.well-known/ai-services.json":                map[string]any{"get": map[string]any{"summary": "Agent service discovery document"}},
			"/.well-known/x402.json":                       map[string]any{"get": map[string]any{"summary": "x402-compatible payment discovery document"}},
			"/llms.txt":                                    map[string]any{"get": map[string]any{"summary": "LLM-readable service description"}},
			"/marketplace/apis":                            map[string]any{"get": map[string]any{"summary": "List API products agents can buy with stablecoins"}},
			"/marketplace/apis/{id}":                       map[string]any{"get": map[string]any{"summary": "Read one API product"}},
			"/marketplace/capabilities":                    map[string]any{"get": map[string]any{"summary": "List capability-first marketplace entries for agents"}},
			"/marketplace/capabilities/{id}":               map[string]any{"get": map[string]any{"summary": "Read one marketplace capability and its active plans"}},
			"/marketplace/capabilities/{id}/contract":      map[string]any{"get": map[string]any{"summary": "Read a versioned capability input/output contract"}},
			"/marketplace/capabilities/{id}/purchase":      map[string]any{"post": map[string]any{"summary": "Create a payment intent for a capability-selected active plan", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/marketplace/capabilities/{id}/usage":         map[string]any{"post": map[string]any{"summary": "Debit usage for a capability-bound access grant", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/marketplace/products":                        map[string]any{"get": map[string]any{"summary": "List premium marketplace API products and active plans"}},
			"/marketplace/products/{id}":                   map[string]any{"get": map[string]any{"summary": "Read one premium marketplace product"}},
			"/marketplace/purchase":                        map[string]any{"post": map[string]any{"summary": "Create a BSC stablecoin marketplace payment intent", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/marketplace/purchase/{id}":                   map[string]any{"get": map[string]any{"summary": "Read marketplace purchase status", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/marketplace/purchase/{id}/execute":           map[string]any{"post": map[string]any{"summary": "Verify ERC20 receipt and issue marketplace access grant", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/marketplace/usage":                           map[string]any{"post": map[string]any{"summary": "Debit marketplace API usage quota with access token", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/v1/access/quote":                             map[string]any{"post": map[string]any{"summary": "Create a BSC USDT quote for temporary API access"}},
			"/v1/access/purchase":                          map[string]any{"post": map[string]any{"summary": "Verify BSC USDT payment and issue temporary API access token"}},
			"/v1/access/{id}":                              map[string]any{"get": map[string]any{"summary": "Read access quote or grant status"}},
			"/v1/meter/usage":                              map[string]any{"post": map[string]any{"summary": "Debit API usage quota before executing paid work", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/agent/v1/capabilities":                       map[string]any{"get": map[string]any{"summary": "Machine-readable capabilities, lifecycle, fees and security model for autonomous agents"}},
			"/agent/connect":                               map[string]any{"post": map[string]any{"summary": "Create a ChainFX agent identity and API credential", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/agent/v1/capabilities/{capability}/contract": map[string]any{"get": map[string]any{"summary": "Read a capability execution contract for agent/provider interoperability"}},
			"/agent/v1/capabilities/{capability}/route":    map[string]any{"post": map[string]any{"summary": "Preview provider route candidates by price, latency, quality and enterprise policy", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/agent/v1/capabilities/{capability}/execute":  map[string]any{"post": map[string]any{"summary": "Execute a capability through the hybrid Capability Router with real metering and mock fallback", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/agent/v1/assets":                             map[string]any{"get": map[string]any{"summary": "List stablecoin symbols enabled for machine-to-machine liquidity trades"}},
			"/agent/v1/trade/quote":                        map[string]any{"post": map[string]any{"summary": "Create a machine-to-machine liquidity quote between enabled BSC stablecoin symbols"}},
			"/agent/v1/trade/execute":                      map[string]any{"post": map[string]any{"summary": "Verify on-chain stablecoin payment and settle receiveAsset to agent wallet"}},
			"/agent/v1/trade/{id}":                         map[string]any{"get": map[string]any{"summary": "Read machine trade intent status"}},
			"/agent/v1/pay":                                map[string]any{"post": map[string]any{"summary": "Create an agent-funded M2M PIX or credit-card payment intent settled by ChainFX", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/agent/v1/pay/{id}":                           map[string]any{"get": map[string]any{"summary": "Read M2M payment intent status", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/api/admin/overview":                          map[string]any{"get": map[string]any{"summary": "Owner operational overview with readiness, metrics, recent transactions and audit events", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/api/admin/transactions":                      map[string]any{"get": map[string]any{"summary": "Owner transaction ledger for buy and sell reconciliation", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
		},
		"x-chainfx": map[string]any{
			"category":           "Digital FX Payments Infrastructure",
			"phase":              "3",
			"supportedAssets":    []string{"USDT"},
			"agentMarketplace":   []string{"Capability-first catalog", "USDT/USDC on BSC premium API capability purchases", "20% default ChainFX take rate", "request-bound payment intents", "chain_id + tx_hash + log_index receipt verification", "quota metering", "provider settlement ledger pending manual payout"},
			"agentLiquidityRail": []string{"Enabled BSC stablecoin pairs from /agent/v1/assets", "USDT/USDC active by default", "BUSD registered as legacy disabled asset", "6% ChainFX execution fee", "on-chain receipt verification", "signer-backed treasury settlement", "idempotency and nonce-bound intents"},
			"agentPayments":      []string{"M2M PIX and credit-card payment intents", "agent deposits required USDT to unique payment address", "address-based deposit matching", "per-agent fee policy", "no automatic refund on overpayment"},
			"phase2":             []string{"Developer Dashboard", "API Keys", "Logs", "Webhook Retry"},
			"phase3":             []string{"Node SDK", "Python SDK", "OpenAPI", "Examples"},
			"notNow":             []string{"bridge", "pool", "AMM", "yield", "DEX", "LP"},
		},
	})
}
