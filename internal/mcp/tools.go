package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"payment-gateway/internal/webhooks"
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
	var req toolCallRequest
	if err := decodeJSON(r, &req); err != nil {
		writeMCPError(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	result, err := s.callTool(r.Context(), req.Name, req.Arguments)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"isError": false,
		"content": []map[string]any{{"type": "json", "json": result}},
	})
}

func (s *Server) callTool(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "get_rates":
		return s.toolGetRates(), nil
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
	return s.dispatch.Emit(ctx, event, payload), nil
}
