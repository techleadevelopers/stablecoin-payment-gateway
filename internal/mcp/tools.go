package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/webhooks"

	"github.com/ethereum/go-ethereum/common"
)

// Tool describes an MCP tool: an action an agent can invoke.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (s *Server) tools() []Tool {
	return []Tool{
		{
			Name:        "get_rates",
			Description: "Retorna as cotações atuais da ChainFX (USDT/BRL, USDT/USD, taxa de venda, etc).",
			InputSchema: schema(nil),
		},
		{
			Name:        "searchCapabilities",
			Description: "Busca capacidades digitais compraveis/executaveis via ChainFX Capability Marketplace.",
			InputSchema: schema(map[string]string{"query": "string opcional", "category": "string opcional", "paymentAsset": "string opcional"}),
		},
		{
			Name:        "listCapabilities",
			Description: "Lista o catalogo capability-first da ChainFX, incluindo planos ativos e providers abstratos.",
			InputSchema: schema(map[string]string{"category": "string opcional", "paymentAsset": "string opcional"}),
		},
		{
			Name:        "getCapability",
			Description: "Le uma capability especifica pelo id/slug, com providers, routing mode e planos ativos.",
			InputSchema: schema(map[string]string{"capability": "string obrigatorio, ex: document_ocr"}),
		},
		{
			Name:        "getCapabilityContract",
			Description: "Le o contrato versionado de input/output de uma capability para interoperabilidade entre agentes e providers.",
			InputSchema: schema(map[string]string{"capability": "string obrigatorio", "version": "string opcional, default v1"}),
		},
		{
			Name:        "purchaseCapability",
			Description: "Cria payment intent stablecoin para uma capability. O agente paga on-chain e depois submete receipt via API.",
			InputSchema: schema(map[string]string{"capability": "string obrigatorio", "planId": "string opcional", "agentWallet": "string obrigatorio", "payerWallet": "string obrigatorio", "idempotencyKey": "string obrigatorio", "nonce": "string obrigatorio", "paymentAsset": "string opcional"}),
		},
		{
			Name:        "getPurchase",
			Description: "Consulta status de uma purchase do marketplace/capability exchange.",
			InputSchema: schema(map[string]string{"purchaseId": "string obrigatorio"}),
		},
		{
			Name:        "executeCapability",
			Description: "Executa uma capability via Capability Router com metering real, provider real quando configurado e fallback mock/dev.",
			InputSchema: schema(map[string]string{"capability": "string obrigatorio", "accessToken": "string obrigatorio", "operation": "string opcional", "requestId": "string obrigatorio", "idempotencyKey": "string obrigatorio", "units": "number opcional", "provider": "string opcional", "routingMode": "best_available|cheapest|lowest_latency|highest_quality", "region": "string opcional", "maxLatencyMs": "number opcional", "maxCostScore": "number opcional", "requireReal": "boolean opcional", "input": "object opcional"}),
		},
		{
			Name:        "chooseRoute",
			Description: "Estima a melhor rota/provider para uma capability por preco, latencia, qualidade, regiao e politica empresarial, sem debitar quota.",
			InputSchema: schema(map[string]string{"capability": "string obrigatorio", "provider": "string opcional", "routingMode": "best_available|cheapest|lowest_latency|highest_quality", "region": "string opcional", "maxLatencyMs": "number opcional", "maxCostScore": "number opcional", "requireReal": "boolean opcional", "units": "number opcional"}),
		},
		{
			Name:        "getUsage",
			Description: "Consulta status de grant ou purchase para acompanhar quota/usage.",
			InputSchema: schema(map[string]string{"grantId": "string opcional", "purchaseId": "string opcional"}),
		},
		{
			Name:        "listAssets",
			Description: "Lista assets BSC habilitados para Agent Rail e pagamentos marketplace.",
			InputSchema: schema(nil),
		},
		{
			Name:        "quote",
			Description: "Calcula estimativa simples do Agent Rail para troca stablecoin 1:1 antes de criar intent HTTP.",
			InputSchema: schema(map[string]string{"payAsset": "string obrigatorio", "receiveAsset": "string obrigatorio", "amount": "number obrigatorio", "amountType": "string opcional: pay|receive"}),
		},
		{
			Name:        "trade",
			Description: "Retorna instrucoes estruturadas para executar Agent Rail via endpoints seguros com receipt on-chain.",
			InputSchema: schema(map[string]string{"payAsset": "string obrigatorio", "receiveAsset": "string obrigatorio", "amount": "number obrigatorio", "agentWallet": "string obrigatorio"}),
		},
		{
			Name:        "settlementStatus",
			Description: "Consulta status de settlement/purchase ou trade intent.",
			InputSchema: schema(map[string]string{"purchaseId": "string opcional", "tradeIntentId": "string opcional"}),
		},
		{
			Name:        "get_order_status",
			Description: "Consulta o status de uma ordem de compra ou venda pelo id.",
			InputSchema: schema(map[string]string{"orderId": "string (obrigatório)", "side": "string opcional: buy|sell"}),
		},
		{
			Name:        "market_analysis",
			Description: "Gera uma análise de mercado com IA a partir das cotações atuais.",
			InputSchema: schema(nil),
		},
		{
			Name:        "trade_recommendation",
			Description: "Recomenda comprar, vender ou aguardar com base no contexto informado.",
			InputSchema: schema(map[string]string{"intendedAmount": "number opcional", "notes": "string opcional"}),
		},
		{
			Name:        "price_prediction",
			Description: "Projeta uma faixa de preço de curtíssimo prazo para USDT/BRL.",
			InputSchema: schema(map[string]string{"horizon": "string opcional, ex: '1h', '24h'"}),
		},
		{
			Name:        "detect_anomalies",
			Description: "Analisa uma lista de transações e aponta anomalias/possível fraude.",
			InputSchema: schema(map[string]string{"transactions": "array de objetos (obrigatório)"}),
		},
		{
			Name:        "summarize_transactions",
			Description: "Resume a atividade financeira de um conjunto de transações.",
			InputSchema: schema(map[string]string{"transactions": "array de objetos (obrigatório)", "period": "string opcional"}),
		},
		{
			Name:        "list_webhook_events",
			Description: "Lista os eventos de automação disponíveis para n8n/Zapier/Make (order.created, payment.received, etc).",
			InputSchema: schema(nil),
		},
		{
			Name:        "create_webhook_subscription",
			Description: "Cria uma assinatura de webhook de automação (n8n, Zapier, Make ou genérico).",
			InputSchema: schema(map[string]string{
				"targetUrl":   "string (obrigatório)",
				"events":      "array de strings (obrigatório)",
				"provider":    "string opcional: n8n|zapier|make|generic",
				"secret":      "string opcional, usado para assinar o payload (HMAC SHA-256)",
				"description": "string opcional",
			}),
		},
		{
			Name:        "list_webhook_subscriptions",
			Description: "Lista as assinaturas de webhook de automação configuradas.",
			InputSchema: schema(nil),
		},
		{
			Name:        "trigger_test_webhook",
			Description: "Dispara um evento de automação sintético para testar integrações n8n/Zapier/Make.",
			InputSchema: schema(map[string]string{"event": "string (obrigatório)", "payload": "objeto opcional"}),
		},
		{
			Name: "createPaymentIntent",
			Description: "Cria uma intent de pagamento M2M para que o agente pague PIX ou cartão de crédito em nome de terceiros. " +
				"O agente deposita o valor em USDT (incluindo taxa) na PaymentAddress; o sistema liquida o fiat ao destinatário. " +
				"Taxa PIX: 10% | Taxa Cartão: 19%. Validade: 15 minutos (proteção cambial).",
			InputSchema: schema(map[string]string{
				"type":            "string obrigatorio: 'pix' ou 'credit_card'",
				"amount_brl":      "string obrigatorio: valor em BRL que o destinatario final recebera",
				"pix_key":         "string obrigatorio quando type=pix: chave PIX destino",
				"idempotency_key": "string obrigatorio: chave unica gerada pelo agente para evitar duplicatas",
				"agent_wallet":    "string obrigatorio: endereco EVM do agente pagador (audit trail)",
			}),
		},
		{
			Name:        "getPaymentIntent",
			Description: "Consulta o status de uma intent de pagamento M2M pelo ID retornado em createPaymentIntent.",
			InputSchema: schema(map[string]string{
				"intent_id": "string obrigatorio: ID da intent retornado por createPaymentIntent",
			}),
		},
	}
}

func schema(props map[string]string) map[string]any {
	properties := map[string]any{}
	for name, desc := range props {
		properties[name] = map[string]any{"type": "string", "description": desc}
	}
	return map[string]any{"type": "object", "properties": properties}
}

func (s *Server) handleToolsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": s.tools()})
}

type toolCallRequest struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req toolCallRequest
	if err := decodeJSON(r, &req); err != nil {
		s.recordMCPToolLog(r, "", "error", "invalid_json", time.Since(start))
		writeMCPError(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	result, err := s.callTool(r.Context(), req.Name, req.Arguments)
	if err != nil {
		s.recordMCPToolLog(r, req.Name, "error", err.Error(), time.Since(start))
		writeJSON(w, http.StatusOK, map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
		})
		return
	}
	s.recordMCPToolLog(r, req.Name, "ok", "", time.Since(start))
	writeJSON(w, http.StatusOK, map[string]any{
		"isError": false,
		"content": []map[string]any{{"type": "json", "json": result}},
	})
}

func (s *Server) recordMCPToolLog(r *http.Request, toolName, status, errorMessage string, duration time.Duration) {
	if s == nil || s.db == nil {
		return
	}
	apiKey := mcpAPIKey(r)
	authMode := "anonymous"
	if apiKey != "" {
		authMode = "api_key"
	}
	_ = s.db.RecordMCPToolLog(r.Context(), database.MCPToolLogInput{
		RequestID:    strings.TrimSpace(r.Header.Get("X-Request-Id")),
		ToolName:     toolName,
		Status:       status,
		ErrorMessage: errorMessage,
		DurationMS:   duration.Milliseconds(),
		APIKeyHash:   shortMCPSecretHash(apiKey),
		AuthMode:     authMode,
	})
}

func mcpAPIKey(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if key := strings.TrimSpace(r.Header.Get("X-Api-Key")); key != "" {
		return key
	}
	return strings.TrimSpace(r.URL.Query().Get("apiKey"))
}

func shortMCPSecretHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func (s *Server) callTool(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "get_rates":
		return s.toolGetRates(), nil
	case "searchCapabilities":
		return s.toolListCapabilities(ctx, args)
	case "listCapabilities":
		return s.toolListCapabilities(ctx, args)
	case "getCapability":
		return s.toolGetCapability(ctx, args)
	case "getCapabilityContract":
		return s.toolGetCapabilityContract(ctx, args)
	case "purchaseCapability":
		return s.toolPurchaseCapability(ctx, args)
	case "getPurchase":
		return s.toolGetPurchase(ctx, args)
	case "executeCapability":
		return s.toolExecuteCapability(ctx, args)
	case "chooseRoute":
		return s.toolChooseRoute(ctx, args)
	case "getUsage":
		return s.toolGetUsage(ctx, args)
	case "listAssets":
		return s.toolListAssets(ctx)
	case "quote":
		return s.toolQuote(ctx, args)
	case "trade":
		return s.toolTrade(args)
	case "settlementStatus":
		return s.toolSettlementStatus(ctx, args)
	case "get_order_status":
		return s.toolGetOrderStatus(ctx, args)
	case "market_analysis":
		return s.toolMarketAnalysis(ctx)
	case "trade_recommendation":
		return s.toolTradeRecommendation(ctx, args)
	case "price_prediction":
		return s.toolPricePrediction(ctx, args)
	case "detect_anomalies":
		return s.toolDetectAnomalies(ctx, args)
	case "summarize_transactions":
		return s.toolSummarizeTransactions(ctx, args)
	case "list_webhook_events":
		return webhooks.AllEvents(), nil
	case "create_webhook_subscription":
		return s.toolCreateWebhookSubscription(ctx, args)
	case "list_webhook_subscriptions":
		return s.db.ListWebhookSubscriptions(ctx)
	case "trigger_test_webhook":
		return s.toolTriggerTestWebhook(ctx, args)
	case "createPaymentIntent":
		return s.toolCreateM2MPaymentIntent(ctx, args)
	case "getPaymentIntent":
		return s.toolGetM2MPaymentIntent(ctx, args)
	default:
		return nil, fmt.Errorf("ferramenta desconhecida: %s", name)
	}
}

func (s *Server) toolGetRates() map[string]any {
	price := s.prices.GetPrice("BRL")
	return map[string]any{
		"USDT_BRL": price,
		"USDT_USD": s.prices.GetPrice("USD"),
		"USDT_EUR": s.prices.GetPrice("EUR"),
		"BTC_USDT": s.prices.GetPrice("BTCUSDT"),
		"EUR_USD":  s.prices.GetPrice("EURUSD"),
	}
}

func (s *Server) toolListCapabilities(ctx context.Context, args map[string]any) (any, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database indisponivel")
	}
	query := strings.TrimSpace(stringArg(args, "query"))
	filter := database.MarketplaceProductFilters{
		Category:     stringArg(args, "category"),
		PaymentAsset: stringArg(args, "paymentAsset"),
	}
	if query != "" {
		filter.Capability = query
	}
	return s.db.ListMarketplaceCapabilities(ctx, filter)
}

func (s *Server) toolGetCapability(ctx context.Context, args map[string]any) (any, error) {
	id := firstNonEmptyMCP(stringArg(args, "capability"), stringArg(args, "id"))
	if id == "" {
		return nil, fmt.Errorf("capability e obrigatoria")
	}
	capability, err := s.db.GetMarketplaceCapability(ctx, id)
	if err != nil {
		return nil, err
	}
	if capability == nil {
		return nil, fmt.Errorf("capability nao encontrada: %s", id)
	}
	return capability, nil
}

func (s *Server) toolGetCapabilityContract(ctx context.Context, args map[string]any) (any, error) {
	id := firstNonEmptyMCP(stringArg(args, "capability"), stringArg(args, "id"))
	if id == "" {
		return nil, fmt.Errorf("capability e obrigatoria")
	}
	contract, err := s.db.GetMarketplaceCapabilityContract(ctx, id, stringArg(args, "version"))
	if err != nil {
		return nil, err
	}
	if contract == nil {
		return nil, fmt.Errorf("contrato de capability nao encontrado: %s", id)
	}
	return contract, nil
}

func (s *Server) toolPurchaseCapability(ctx context.Context, args map[string]any) (any, error) {
	capabilityID := firstNonEmptyMCP(stringArg(args, "capability"), stringArg(args, "id"))
	if capabilityID == "" {
		return nil, fmt.Errorf("capability e obrigatoria")
	}
	agentWallet := strings.ToLower(strings.TrimSpace(stringArg(args, "agentWallet")))
	payerWallet := strings.ToLower(strings.TrimSpace(stringArg(args, "payerWallet")))
	if !common.IsHexAddress(agentWallet) || !common.IsHexAddress(payerWallet) {
		return nil, fmt.Errorf("agentWallet e payerWallet EVM validos sao obrigatorios")
	}
	if !strings.EqualFold(agentWallet, payerWallet) {
		return nil, fmt.Errorf("agentWallet deve ser igual a payerWallet neste corte")
	}
	idempotencyKey := strings.TrimSpace(stringArg(args, "idempotencyKey"))
	nonce := strings.TrimSpace(stringArg(args, "nonce"))
	if idempotencyKey == "" || nonce == "" {
		return nil, fmt.Errorf("idempotencyKey e nonce sao obrigatorios")
	}
	capability, err := s.db.GetMarketplaceCapability(ctx, capabilityID)
	if err != nil {
		return nil, err
	}
	if capability == nil {
		return nil, fmt.Errorf("capability nao encontrada")
	}
	_, plan, err := s.db.ResolveMarketplaceCapabilityPlan(ctx, capability.ID, stringArg(args, "planId"), stringArg(args, "paymentAsset"))
	if err != nil {
		return nil, err
	}
	paymentAddress := s.mcpPaymentAddress()
	if !common.IsHexAddress(paymentAddress) {
		return nil, fmt.Errorf("TREASURY_HOT ou SELL_WALLET_ADDRESS precisa ser um endereco EVM valido")
	}
	contract, err := s.mcpPaymentContract(ctx, plan.PaymentAsset)
	if err != nil {
		return nil, err
	}
	purchase, product, plan, err := s.db.CreateMarketplacePurchase(ctx, database.MarketplacePurchaseInput{
		PlanID:          plan.ID,
		AgentWallet:     agentWallet,
		PayerWallet:     payerWallet,
		PaymentAddress:  paymentAddress,
		PaymentContract: contract,
		Nonce:           nonce,
		IdempotencyKey:  idempotencyKey,
		ExpiresAt:       time.Now().UTC().Add(15 * time.Minute),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"purchaseId": purchase.ID,
		"status":     purchase.Status,
		"capability": map[string]any{"id": capability.ID, "displayName": capability.DisplayName, "routingMode": capability.RoutingMode, "providers": capability.Providers},
		"product":    product.Name,
		"plan":       plan.ID,
		"payment": map[string]any{
			"asset":           purchase.PaymentAsset,
			"network":         purchase.Network,
			"chainId":         purchase.ChainID,
			"contractAddress": purchase.PaymentContract,
			"paymentAddress":  purchase.PaymentAddress,
			"amount":          purchase.GrossAmount,
			"expiresAt":       purchase.ExpiresAt,
		},
		"fees": map[string]any{
			"takeRateBps":    purchase.TakeRateBps,
			"chainfxAmount":  purchase.ChainFXAmount,
			"providerAmount": purchase.ProviderAmount,
		},
		"requestHash": purchase.RequestHash,
		"nextStep":    "pay on-chain, then submit txHash/logIndex to POST /marketplace/purchase/{id}/execute",
	}, nil
}

func (s *Server) toolGetPurchase(ctx context.Context, args map[string]any) (any, error) {
	id := strings.TrimSpace(stringArg(args, "purchaseId"))
	if id == "" {
		return nil, fmt.Errorf("purchaseId e obrigatorio")
	}
	purchase, err := s.db.GetMarketplacePurchase(ctx, id)
	if err != nil {
		return nil, err
	}
	if purchase == nil {
		return nil, fmt.Errorf("purchase nao encontrada: %s", id)
	}
	return purchase, nil
}

func (s *Server) toolExecuteCapability(ctx context.Context, args map[string]any) (any, error) {
	capability := firstNonEmptyMCP(stringArg(args, "capability"), stringArg(args, "id"))
	token := firstNonEmptyMCP(stringArg(args, "accessToken"), stringArg(args, "token"))
	requestID := strings.TrimSpace(stringArg(args, "requestId"))
	idempotencyKey := strings.TrimSpace(stringArg(args, "idempotencyKey"))
	if capability == "" || token == "" || requestID == "" || idempotencyKey == "" {
		return nil, fmt.Errorf("capability, accessToken, requestId e idempotencyKey sao obrigatorios")
	}
	rawInput, _ := json.Marshal(args["input"])
	if args["input"] == nil {
		rawInput = json.RawMessage(`{}`)
	}
	result, err := s.db.ExecuteMarketplaceCapabilityMock(ctx, database.MarketplaceCapabilityExecuteInput{
		Token:             token,
		CapabilityID:      capability,
		Operation:         stringArg(args, "operation"),
		RequestID:         requestID,
		IdempotencyKey:    idempotencyKey,
		RequestedProvider: stringArg(args, "provider"),
		RoutingMode:       stringArg(args, "routingMode"),
		Region:            stringArg(args, "region"),
		MaxLatencyMS:      intArg(args, "maxLatencyMs"),
		MaxCostScore:      intArg(args, "maxCostScore"),
		RequireReal:       boolArg(args, "requireReal"),
		Units:             intArg(args, "units"),
		Input:             rawInput,
	})
	if err != nil {
		return nil, err
	}
	if !result.Duplicate {
		s.promoteRealCapabilityExecution(ctx, result.Event)
	}
	return result, nil
}

func (s *Server) toolChooseRoute(ctx context.Context, args map[string]any) (any, error) {
	capability := firstNonEmptyMCP(stringArg(args, "capability"), stringArg(args, "id"))
	if capability == "" {
		return nil, fmt.Errorf("capability e obrigatoria")
	}
	candidates, err := s.db.ListMarketplaceRouteCandidates(ctx, database.MarketplaceCapabilityExecuteInput{
		CapabilityID:      capability,
		RequestedProvider: stringArg(args, "provider"),
		RoutingMode:       stringArg(args, "routingMode"),
		Region:            stringArg(args, "region"),
		MaxLatencyMS:      intArg(args, "maxLatencyMs"),
		MaxCostScore:      intArg(args, "maxCostScore"),
		RequireReal:       boolArg(args, "requireReal"),
		Units:             intArg(args, "units"),
	})
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("nenhuma rota encontrada")
	}
	return map[string]any{
		"capability":  capability,
		"routingMode": firstNonEmptyMCP(stringArg(args, "routingMode"), "best_available"),
		"selected":    candidates[0],
		"candidates":  candidates,
	}, nil
}

func (s *Server) promoteRealCapabilityExecution(ctx context.Context, event *database.MarketplaceCapabilityExecution) {
	if event == nil {
		return
	}
	start := time.Now()
	if output, err := s.executeCapabilityProvider(ctx, event, nil); err == nil {
		latencyMS := int(time.Since(start).Milliseconds())
		_ = s.db.CompleteMarketplaceExecutionMetrics(ctx, event.ID, "real_completed", output, latencyMS, "", "")
		_ = s.db.RecordMarketplaceProviderMetric(ctx, event.CapabilityID, event.ProviderSlug, "real_completed", latencyMS)
		event.Output = output
		event.Status = "real_completed"
		event.LatencyMS = latencyMS
		return
	} else if !s.executionFallbackEnabled(event) {
		latencyMS := int(time.Since(start).Milliseconds())
		output = capabilityFallbackOutput(event, err)
		_ = s.db.CompleteMarketplaceExecutionMetrics(ctx, event.ID, "mock_fallback", output, latencyMS, "provider_failed", err.Error())
		_ = s.db.RecordMarketplaceProviderMetric(ctx, event.CapabilityID, event.ProviderSlug, "mock_fallback", latencyMS)
		event.Output = output
		event.Status = "mock_fallback"
		event.LatencyMS = latencyMS
		event.ErrorCode = "provider_failed"
		event.ErrorMessage = err.Error()
		return
	}

	var lastErr error
	candidates, err := s.db.ListMarketplaceRouteCandidates(ctx, database.MarketplaceCapabilityExecuteInput{
		CapabilityID: event.CapabilityID,
		RoutingMode:  event.RoutingMode,
		RequireReal:  true,
		Units:        event.UnitsConsumed,
	})
	if err != nil {
		lastErr = err
	}
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.ProviderSlug, event.ProviderSlug) {
			continue
		}
		attemptStart := time.Now()
		output, err := s.executeCapabilityProvider(ctx, event, candidate)
		if err != nil {
			_ = s.db.RecordMarketplaceProviderMetric(ctx, event.CapabilityID, candidate.ProviderSlug, "real_failed", int(time.Since(attemptStart).Milliseconds()))
			lastErr = err
			continue
		}
		latencyMS := int(time.Since(attemptStart).Milliseconds())
		_ = s.db.ReassignMarketplaceExecutionProvider(ctx, event.ID, candidate)
		_ = s.db.CompleteMarketplaceExecutionMetrics(ctx, event.ID, "real_completed", output, latencyMS, "", "")
		_ = s.db.RecordMarketplaceProviderMetric(ctx, event.CapabilityID, candidate.ProviderSlug, "real_completed", latencyMS)
		event.ProviderSlug = candidate.ProviderSlug
		event.ProviderName = candidate.ProviderName
		event.RouteName = candidate.RouteName
		event.RoutingMode = candidate.RoutingMode
		event.Output = output
		event.Status = "real_completed"
		event.LatencyMS = latencyMS
		return
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nenhum provider real disponivel para fallback")
	}
	latencyMS := int(time.Since(start).Milliseconds())
	output := capabilityFallbackOutput(event, lastErr)
	_ = s.db.CompleteMarketplaceExecutionMetrics(ctx, event.ID, "mock_fallback", output, latencyMS, "provider_fallback_exhausted", lastErr.Error())
	_ = s.db.RecordMarketplaceProviderMetric(ctx, event.CapabilityID, event.ProviderSlug, "mock_fallback", latencyMS)
	event.Output = output
	event.Status = "mock_fallback"
	event.LatencyMS = latencyMS
	event.ErrorCode = "provider_fallback_exhausted"
	event.ErrorMessage = lastErr.Error()
}

func (s *Server) executeCapabilityProvider(ctx context.Context, event *database.MarketplaceCapabilityExecution, candidate *database.MarketplaceRouteCandidate) (json.RawMessage, error) {
	if candidate != nil {
		event = cloneExecutionForProvider(event, candidate)
	}
	switch event.CapabilityID {
	case "semantic_memory":
		return s.db.ApplyMarketplaceMemoryOperation(ctx, event)
	case "llm_chat":
		if !strings.EqualFold(event.ProviderSlug, "openai") {
			return nil, fmt.Errorf("provider %s ainda nao possui adapter real para llm_chat", event.ProviderSlug)
		}
		return s.executeLLMCapability(ctx, event)
	case "document_ocr":
		if !strings.EqualFold(event.ProviderSlug, "chainfx-ocr-http") {
			return nil, fmt.Errorf("provider %s ainda nao possui adapter real para document_ocr", event.ProviderSlug)
		}
		return s.executeOCRCapability(ctx, event)
	default:
		return nil, fmt.Errorf("capability %s ainda nao possui provider real", event.CapabilityID)
	}
}

func cloneExecutionForProvider(event *database.MarketplaceCapabilityExecution, candidate *database.MarketplaceRouteCandidate) *database.MarketplaceCapabilityExecution {
	cloned := *event
	cloned.ProviderSlug = candidate.ProviderSlug
	cloned.ProviderName = candidate.ProviderName
	cloned.RouteName = candidate.RouteName
	cloned.RoutingMode = candidate.RoutingMode
	return &cloned
}

func (s *Server) executionFallbackEnabled(event *database.MarketplaceCapabilityExecution) bool {
	return event != nil
}

func (s *Server) executeLLMCapability(ctx context.Context, event *database.MarketplaceCapabilityExecution) (json.RawMessage, error) {
	if s.agents == nil || !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY nao configurado")
	}
	input := map[string]any{}
	_ = json.Unmarshal(event.Input, &input)
	out, err := s.agents.GenerateText(ctx, event.Operation, input)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(out)
	return raw, nil
}

func (s *Server) executeOCRCapability(ctx context.Context, event *database.MarketplaceCapabilityExecution) (json.RawMessage, error) {
	if s.cfg == nil || strings.TrimSpace(s.cfg.CapabilityOCRURL) == "" {
		return nil, fmt.Errorf("CAPABILITY_OCR_URL nao configurado")
	}
	body := map[string]any{}
	_ = json.Unmarshal(event.Input, &body)
	body["operation"] = event.Operation
	body["capability"] = event.CapabilityID
	rawBody, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.CapabilityOCRURL, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(s.cfg.CapabilityOCRAPIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(s.cfg.CapabilityOCRAPIKey))
	}
	client := &http.Client{Timeout: 40 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OCR adapter retornou status %d", resp.StatusCode)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("OCR adapter retornou JSON invalido: %w", err)
	}
	parsed["mode"] = "real"
	parsed["provider"] = "chainfx-ocr-http"
	parsed["operation"] = event.Operation
	out, _ := json.Marshal(parsed)
	return out, nil
}

func capabilityFallbackOutput(event *database.MarketplaceCapabilityExecution, cause error) json.RawMessage {
	payload := map[string]any{
		"mode":       "mock",
		"fallback":   true,
		"capability": event.CapabilityID,
		"operation":  event.Operation,
		"provider":   event.ProviderSlug,
		"status":     "completed",
		"reason":     cause.Error(),
	}
	if len(event.Output) > 0 && json.Valid(event.Output) {
		var existing map[string]any
		if json.Unmarshal(event.Output, &existing) == nil {
			for k, v := range existing {
				payload[k] = v
			}
			payload["fallback"] = true
			payload["reason"] = cause.Error()
		}
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func (s *Server) toolGetUsage(ctx context.Context, args map[string]any) (any, error) {
	if grantID := strings.TrimSpace(stringArg(args, "grantId")); grantID != "" {
		grant, err := s.db.GetAccessGrant(ctx, grantID)
		if err != nil {
			return nil, err
		}
		if grant == nil {
			return nil, fmt.Errorf("grant nao encontrado: %s", grantID)
		}
		return grant, nil
	}
	if purchaseID := strings.TrimSpace(stringArg(args, "purchaseId")); purchaseID != "" {
		return s.toolGetPurchase(ctx, map[string]any{"purchaseId": purchaseID})
	}
	return nil, fmt.Errorf("grantId ou purchaseId e obrigatorio")
}

func (s *Server) toolListAssets(ctx context.Context) (any, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database indisponivel")
	}
	return s.db.ListAgentSupportedAssets(ctx)
}

func (s *Server) toolQuote(ctx context.Context, args map[string]any) (any, error) {
	payAsset := strings.ToUpper(strings.TrimSpace(stringArg(args, "payAsset")))
	receiveAsset := strings.ToUpper(strings.TrimSpace(stringArg(args, "receiveAsset")))
	amount := floatArg(args, "amount")
	amountType := strings.ToLower(firstNonEmptyMCP(stringArg(args, "amountType"), "receive"))
	if payAsset == "" || receiveAsset == "" || amount <= 0 {
		return nil, fmt.Errorf("payAsset, receiveAsset e amount sao obrigatorios")
	}
	if payAsset == receiveAsset {
		return nil, fmt.Errorf("payAsset e receiveAsset devem ser diferentes")
	}
	pay, err := s.db.GetAgentSupportedAsset(ctx, payAsset, "BSC")
	if err != nil {
		return nil, err
	}
	receive, err := s.db.GetAgentSupportedAsset(ctx, receiveAsset, "BSC")
	if err != nil {
		return nil, err
	}
	if pay == nil || receive == nil {
		return nil, fmt.Errorf("asset nao habilitado")
	}
	feeBps := maxIntMCP(600, maxIntMCP(pay.FeeBps, receive.FeeBps))
	payAmount, receiveAmount := amount, amount
	if amountType == "receive" {
		payAmount = amount / (1 - float64(feeBps)/10000)
		receiveAmount = amount
	} else {
		payAmount = amount
		receiveAmount = amount * (1 - float64(feeBps)/10000)
	}
	return map[string]any{
		"payAsset":         payAsset,
		"receiveAsset":     receiveAsset,
		"payAmount":        round6MCP(payAmount),
		"receiveAmount":    round6MCP(receiveAmount),
		"chainfxFeeAmount": round6MCP(payAmount - receiveAmount),
		"feeBps":           feeBps,
		"network":          "BSC",
		"nextStep":         "create intent with POST /agent/v1/trade/quote, pay on-chain, then POST /agent/v1/trade/execute",
	}, nil
}

func (s *Server) toolTrade(args map[string]any) (any, error) {
	return map[string]any{
		"status":  "requires_http_intent",
		"quote":   "/agent/v1/trade/quote",
		"execute": "/agent/v1/trade/execute",
		"note":    "Agent Rail trade uses receipt verification and signer settlement; create the intent through the HTTP endpoint before paying on-chain.",
		"input":   args,
	}, nil
}

func (s *Server) toolSettlementStatus(ctx context.Context, args map[string]any) (any, error) {
	if purchaseID := strings.TrimSpace(stringArg(args, "purchaseId")); purchaseID != "" {
		return s.toolGetPurchase(ctx, map[string]any{"purchaseId": purchaseID})
	}
	if tradeID := strings.TrimSpace(stringArg(args, "tradeIntentId")); tradeID != "" {
		intent, err := s.db.GetAgentTradeIntent(ctx, tradeID)
		if err != nil {
			return nil, err
		}
		if intent == nil {
			return nil, fmt.Errorf("trade intent nao encontrado: %s", tradeID)
		}
		return intent, nil
	}
	return nil, fmt.Errorf("purchaseId ou tradeIntentId e obrigatorio")
}

func (s *Server) toolGetOrderStatus(ctx context.Context, args map[string]any) (any, error) {
	orderID, _ := args["orderId"].(string)
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return nil, fmt.Errorf("orderId é obrigatório")
	}
	side, _ := args["side"].(string)
	side = strings.ToLower(strings.TrimSpace(side))

	if side == "" || side == "buy" {
		if buy, err := s.db.GetBuyOrder(ctx, orderID); err == nil && buy != nil {
			return buy, nil
		}
	}
	if side == "" || side == "sell" {
		if order, err := s.db.GetOrder(ctx, orderID); err == nil && order != nil {
			return order, nil
		}
	}
	return nil, fmt.Errorf("ordem não encontrada: %s", orderID)
}

func (s *Server) toolMarketAnalysis(ctx context.Context) (any, error) {
	if !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurado; recurso de IA indisponível")
	}
	return s.agents.AnalyzeMarket(ctx, s.toolGetRates())
}

func (s *Server) toolTradeRecommendation(ctx context.Context, args map[string]any) (any, error) {
	if !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurado; recurso de IA indisponível")
	}
	tradeContext := s.toolGetRates()
	for k, v := range args {
		tradeContext[k] = v
	}
	return s.agents.Recommend(ctx, tradeContext)
}

func (s *Server) toolPricePrediction(ctx context.Context, args map[string]any) (any, error) {
	if !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurado; recurso de IA indisponível")
	}
	horizon, _ := args["horizon"].(string)
	if horizon == "" {
		horizon = "24h"
	}
	history, _ := args["history"].([]any)
	histMaps := make([]map[string]any, 0, len(history))
	for _, item := range history {
		if m, ok := item.(map[string]any); ok {
			histMaps = append(histMaps, m)
		}
	}
	if len(histMaps) == 0 {
		histMaps = append(histMaps, s.toolGetRates())
	}
	return s.agents.PredictPrice(ctx, histMaps, horizon)
}

func (s *Server) toolDetectAnomalies(ctx context.Context, args map[string]any) (any, error) {
	if !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurado; recurso de IA indisponível")
	}
	transactions, err := toMapSlice(args["transactions"])
	if err != nil {
		return nil, err
	}
	return s.agents.DetectAnomalies(ctx, transactions)
}

func (s *Server) toolSummarizeTransactions(ctx context.Context, args map[string]any) (any, error) {
	if !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurado; recurso de IA indisponível")
	}
	transactions, err := toMapSlice(args["transactions"])
	if err != nil {
		return nil, err
	}
	period, _ := args["period"].(string)
	return s.agents.SummarizeTransactions(ctx, transactions, period)
}

func toMapSlice(raw any) ([]map[string]any, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("transactions deve ser um array")
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Server) toolCreateWebhookSubscription(ctx context.Context, args map[string]any) (any, error) {
	targetURL, _ := args["targetUrl"].(string)
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return nil, fmt.Errorf("targetUrl é obrigatório")
	}
	provider, _ := args["provider"].(string)
	if provider == "" {
		provider = webhooks.ProviderGeneric
	}
	secret, _ := args["secret"].(string)
	description, _ := args["description"].(string)

	if err := webhooks.ValidateTargetURL(targetURL); err != nil {
		return nil, err
	}

	var events []string
	if raw, ok := args["events"].([]any); ok {
		for _, e := range raw {
			if str, ok := e.(string); ok && webhooks.IsKnownEvent(str) {
				events = append(events, str)
			}
		}
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("events deve conter pelo menos um evento válido: %v", webhooks.AllEvents())
	}
	return s.db.CreateWebhookSubscription(ctx, provider, targetURL, secret, description, events)
}

func (s *Server) toolTriggerTestWebhook(ctx context.Context, args map[string]any) (any, error) {
	event, _ := args["event"].(string)
	if !webhooks.IsKnownEvent(event) {
		return nil, fmt.Errorf("evento desconhecido: %s (válidos: %v)", event, webhooks.AllEvents())
	}
	payload, _ := args["payload"].(map[string]any)
	if payload == nil {
		payload = map[string]any{"test": true}
	}
	return s.dispatch.EmitSync(ctx, event, payload), nil
}

func (s *Server) mcpPaymentAddress() string {
	if s.cfg == nil {
		return ""
	}
	return strings.ToLower(firstNonEmptyMCP(s.cfg.TreasuryHot, s.cfg.SellWalletAddress))
}

func (s *Server) mcpPaymentContract(ctx context.Context, asset string) (string, error) {
	symbol := strings.ToUpper(strings.TrimSpace(asset))
	if symbol == "" {
		return "", fmt.Errorf("payment asset invalido")
	}
	if s.db != nil {
		registered, err := s.db.GetAgentSupportedAsset(ctx, symbol, "BSC")
		if err != nil {
			return "", err
		}
		if registered != nil && registered.Enabled && registered.Status == "active" && common.IsHexAddress(registered.ContractAddress) {
			return strings.ToLower(registered.ContractAddress), nil
		}
	}
	if symbol == "USDT" && s.cfg != nil && common.IsHexAddress(s.cfg.BscUsdtContract) {
		return strings.ToLower(s.cfg.BscUsdtContract), nil
	}
	return "", fmt.Errorf("%s BSC nao configurado na allowlist", symbol)
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}

func intArg(args map[string]any, key string) int {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := strconv.Atoi(v.String())
		return n
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

func boolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(v))
		return parsed
	default:
		return false
	}
}

func floatArg(args map[string]any, key string) float64 {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := strconv.ParseFloat(v.String(), 64)
		return n
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n
	default:
		return 0
	}
}

// ─── M2M Payment Intent MCP tools ────────────────────────────────────────────

// toolCreateM2MPaymentIntent exposes M2M payment intent creation via MCP.
// This is the canonical entry-point for AI agents that need to pay PIX or
// credit-card bills on behalf of humans using their USDT balance.
func (s *Server) toolCreateM2MPaymentIntent(ctx context.Context, args map[string]any) (any, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database indisponivel")
	}
	if s.cfg == nil {
		return nil, fmt.Errorf("configuracao indisponivel")
	}

	paymentType := strings.ToLower(strings.TrimSpace(stringArg(args, "type")))
	amountBRLStr := strings.TrimSpace(stringArg(args, "amount_brl"))
	pixKey := strings.TrimSpace(stringArg(args, "pix_key"))
	idempotencyKey := strings.TrimSpace(stringArg(args, "idempotency_key"))
	agentWallet := strings.ToLower(strings.TrimSpace(stringArg(args, "agent_wallet")))

	// ── Validation ────────────────────────────────────────────────────────────
	if idempotencyKey == "" {
		return nil, fmt.Errorf("idempotency_key e obrigatorio")
	}
	if agentWallet == "" {
		return nil, fmt.Errorf("agent_wallet e obrigatorio")
	}

	var feeBps int
	switch paymentType {
	case "pix":
		if pixKey == "" {
			return nil, fmt.Errorf("pix_key e obrigatorio para type=pix")
		}
		feeBps = s.cfg.M2MPixFeeBps
	case "credit_card":
		feeBps = s.cfg.M2MCreditFeeBps
	default:
		return nil, fmt.Errorf("type deve ser 'pix' ou 'credit_card'")
	}

	var amountBRL float64
	if _, err := fmt.Sscanf(amountBRLStr, "%f", &amountBRL); err != nil || amountBRL <= 0 {
		return nil, fmt.Errorf("amount_brl deve ser um numero positivo")
	}

	// ── Rate ──────────────────────────────────────────────────────────────────
	usdtRate := s.prices.GetPrice("BRL")
	if usdtRate <= 0 {
		return nil, fmt.Errorf("cotacao USDT/BRL indisponivel; tente novamente em instantes")
	}

	// ── Fee calculation ───────────────────────────────────────────────────────
	grossUSDT := amountBRL / usdtRate
	feeUSDT := grossUSDT * (float64(feeBps) / 10_000.0)
	requiredUSDT := grossUSDT + feeUSDT

	// ── Intent ID ─────────────────────────────────────────────────────────────
	reqHash := database.CanonicalRequestHash(paymentType, amountBRLStr, pixKey, idempotencyKey, agentWallet)
	hashShort := reqHash
	if len(hashShort) > 24 {
		hashShort = hashShort[:24]
	}
	intentID := "int_m2m_" + hashShort

	paymentAddress := strings.ToLower(strings.TrimSpace(s.cfg.TreasuryHot))

	in := database.M2MCreateInput{
		ID:             intentID,
		IdempotencyKey: idempotencyKey,
		AgentWallet:    agentWallet,
		PaymentType:    database.M2MPaymentType(paymentType),
		PixKey:         pixKey,
		AmountBRL:      amountBRL,
		FeeBps:         feeBps,
		FeeUSDT:        feeUSDT,
		GrossUSDT:      grossUSDT,
		RequiredUSDT:   requiredUSDT,
		USDTRate:       usdtRate,
		PaymentAddress: paymentAddress,
		RequestHash:    reqHash,
		ExpiresAt:      time.Now().UTC().Add(15 * time.Minute),
	}

	intent, isIdempotent, err := s.db.CreateAgentPaymentIntent(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("erro ao criar intent: %w", err)
	}

	feeLabel := fmt.Sprintf("%d%% (%d bps)", feeBps/100, feeBps)
	resp := map[string]any{
		"intent_id":       intent.ID,
		"status":          string(intent.Status),
		"payment_type":    string(intent.PaymentType),
		"amount_brl":      fmt.Sprintf("%.2f", intent.AmountBRL),
		"gross_usdt":      fmt.Sprintf("%.6f", intent.GrossUSDT),
		"fee_usdt":        fmt.Sprintf("%.6f", intent.FeeUSDT),
		"required_usdt":   fmt.Sprintf("%.6f", intent.RequiredUSDT),
		"fee_applied":     feeLabel,
		"usdt_rate":       fmt.Sprintf("%.4f", intent.USDTRate),
		"payment_address": intent.PaymentAddress,
		"expires_at":      intent.ExpiresAt,
		"idempotent":      isIdempotent,
		"next_step":       "Deposite exatamente required_usdt em USDT (BEP-20 ou Polygon) para payment_address. O sistema paga o PIX automaticamente apos a confirmacao on-chain.",
	}
	if intent.PixKey != "" {
		resp["pix_key"] = intent.PixKey
	}
	return resp, nil
}

// toolGetM2MPaymentIntent returns the current status of an M2M payment intent.
func (s *Server) toolGetM2MPaymentIntent(ctx context.Context, args map[string]any) (any, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database indisponivel")
	}
	intentID := strings.TrimSpace(stringArg(args, "intent_id"))
	if intentID == "" {
		return nil, fmt.Errorf("intent_id e obrigatorio")
	}
	intent, err := s.db.GetAgentPaymentIntent(ctx, intentID)
	if err != nil {
		return nil, fmt.Errorf("erro ao buscar intent: %w", err)
	}
	if intent == nil {
		return nil, fmt.Errorf("intent nao encontrada: %s", intentID)
	}
	out := map[string]any{
		"intent_id":       intent.ID,
		"status":          string(intent.Status),
		"payment_type":    string(intent.PaymentType),
		"amount_brl":      fmt.Sprintf("%.2f", intent.AmountBRL),
		"required_usdt":   fmt.Sprintf("%.6f", intent.RequiredUSDT),
		"fee_usdt":        fmt.Sprintf("%.6f", intent.FeeUSDT),
		"fee_bps":         intent.FeeBps,
		"payment_address": intent.PaymentAddress,
		"expires_at":      intent.ExpiresAt,
		"attempts":        intent.Attempts,
		"created_at":      intent.CreatedAt,
	}
	if intent.PixKey != "" {
		out["pix_key"] = intent.PixKey
	}
	if intent.DepositTx != nil {
		out["deposit_tx"] = *intent.DepositTx
	}
	if intent.DepositAmountUSDT != nil {
		out["deposit_amount_usdt"] = fmt.Sprintf("%.6f", *intent.DepositAmountUSDT)
	}
	if intent.EfiEndToEndID != nil {
		out["efi_end_to_end_id"] = *intent.EfiEndToEndID
	}
	if intent.EfiStatus != nil {
		out["efi_status"] = *intent.EfiStatus
	}
	if intent.ErrorMessage != nil {
		out["error_message"] = *intent.ErrorMessage
	}
	if intent.SettledAt != nil {
		out["settled_at"] = *intent.SettledAt
	}
	return out, nil
}

func firstNonEmptyMCP(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func maxIntMCP(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func round6MCP(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}
