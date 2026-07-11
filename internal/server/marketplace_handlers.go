package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/database"

	"github.com/ethereum/go-ethereum/common"
)

const marketplacePurchaseTTL = 15 * time.Minute

func (s *Server) handleMarketplaceProducts(w http.ResponseWriter, r *http.Request) {
	products, err := s.db.ListMarketplaceProducts(r.Context(), database.MarketplaceProductFilters{
		Category:     r.URL.Query().Get("category"),
		Provider:     r.URL.Query().Get("provider"),
		Capability:   r.URL.Query().Get("capability"),
		PaymentAsset: r.URL.Query().Get("paymentAsset"),
		Status:       r.URL.Query().Get("status"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": marketplaceProductsResponse(products)})
}

func (s *Server) handleMarketplaceCapabilities(w http.ResponseWriter, r *http.Request) {
	capabilities, err := s.db.ListMarketplaceCapabilities(r.Context(), database.MarketplaceProductFilters{
		Category:     r.URL.Query().Get("category"),
		Capability:   r.URL.Query().Get("capability"),
		PaymentAsset: r.URL.Query().Get("paymentAsset"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"capabilities": capabilities,
		"model":        "capability_network",
		"positioning":  "economic infrastructure for autonomous agents to discover, execute, meter, bill and settle digital capabilities",
		"routing":      "providers are abstracted behind policy routing; execution is hybrid real provider when configured with provider fallback and mock/dev fallback",
	})
}

func (s *Server) handleMarketplaceCapability(w http.ResponseWriter, r *http.Request) {
	capability, err := s.db.GetMarketplaceCapability(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	if capability == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "capability nao encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, capability)
}

func (s *Server) handleMarketplaceCapabilityContract(w http.ResponseWriter, r *http.Request) {
	contract, err := s.db.GetMarketplaceCapabilityContract(r.Context(), strings.TrimSpace(r.PathValue("id")), r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	if contract == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "contrato de capability nao encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contract": contract})
}

func (s *Server) handleAgentCapabilityContract(w http.ResponseWriter, r *http.Request) {
	contract, err := s.db.GetMarketplaceCapabilityContract(r.Context(), strings.TrimSpace(r.PathValue("capability")), r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	if contract == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "contrato de capability nao encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"network":  "chainfx_capability_network",
		"contract": contract,
	})
}

func (s *Server) handleMarketplaceProduct(w http.ResponseWriter, r *http.Request) {
	product, err := s.db.GetMarketplaceProduct(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	if product == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "produto nao encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, marketplaceProductResponse(product))
}

func (s *Server) handleMarketplacePurchaseCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req struct {
		PlanID         string `json:"planId"`
		AgentWallet    string `json:"agentWallet"`
		PayerWallet    string `json:"payerWallet"`
		IdempotencyKey string `json:"idempotencyKey"`
		Nonce          string `json:"nonce"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	req.PlanID = strings.TrimSpace(req.PlanID)
	req.AgentWallet = strings.ToLower(strings.TrimSpace(req.AgentWallet))
	req.PayerWallet = strings.ToLower(strings.TrimSpace(req.PayerWallet))
	req.IdempotencyKey = firstNonEmpty(req.IdempotencyKey, r.Header.Get("X-Idempotency-Key"))
	req.Nonce = strings.TrimSpace(req.Nonce)
	if req.PlanID == "" || req.IdempotencyKey == "" || req.Nonce == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "planId, idempotencyKey e nonce sao obrigatorios"})
		return
	}
	if !common.IsHexAddress(req.AgentWallet) || !common.IsHexAddress(req.PayerWallet) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agentWallet e payerWallet EVM validos sao obrigatorios"})
		return
	}
	if !strings.EqualFold(req.AgentWallet, req.PayerWallet) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agentWallet deve ser igual a payerWallet neste corte"})
		return
	}
	paymentAddress := s.accessPaymentAddress()
	if !common.IsHexAddress(paymentAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "TREASURY_HOT ou SELL_WALLET_ADDRESS precisa ser um endereco EVM valido"})
		return
	}
	paymentContract, err := s.marketplacePaymentContract(r, "USDT")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	purchase, product, plan, err := s.db.CreateMarketplacePurchase(r.Context(), database.MarketplacePurchaseInput{
		PlanID:          req.PlanID,
		AgentWallet:     req.AgentWallet,
		PayerWallet:     req.PayerWallet,
		PaymentAddress:  paymentAddress,
		PaymentContract: paymentContract,
		Nonce:           req.Nonce,
		IdempotencyKey:  req.IdempotencyKey,
		ExpiresAt:       time.Now().UTC().Add(marketplacePurchaseTTL),
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, marketplacePurchaseIntentResponse(purchase, product, plan))
}

func (s *Server) handleMarketplaceCapabilityPurchase(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	capabilityID := strings.TrimSpace(r.PathValue("id"))
	var req struct {
		PlanID         string `json:"planId"`
		AgentWallet    string `json:"agentWallet"`
		PayerWallet    string `json:"payerWallet"`
		PaymentAsset   string `json:"paymentAsset"`
		IdempotencyKey string `json:"idempotencyKey"`
		Nonce          string `json:"nonce"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	capability, err := s.db.GetMarketplaceCapability(r.Context(), capabilityID)
	if err != nil {
		writeError(w, err)
		return
	}
	if capability == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "capability nao encontrada"})
		return
	}
	_, plan, err := s.db.ResolveMarketplaceCapabilityPlan(r.Context(), capability.ID, req.PlanID, req.PaymentAsset)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	req.AgentWallet = strings.ToLower(strings.TrimSpace(req.AgentWallet))
	req.PayerWallet = strings.ToLower(strings.TrimSpace(req.PayerWallet))
	req.IdempotencyKey = firstNonEmpty(req.IdempotencyKey, r.Header.Get("X-Idempotency-Key"))
	req.Nonce = strings.TrimSpace(req.Nonce)
	if req.IdempotencyKey == "" || req.Nonce == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "idempotencyKey e nonce sao obrigatorios"})
		return
	}
	if !common.IsHexAddress(req.AgentWallet) || !common.IsHexAddress(req.PayerWallet) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agentWallet e payerWallet EVM validos sao obrigatorios"})
		return
	}
	if !strings.EqualFold(req.AgentWallet, req.PayerWallet) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agentWallet deve ser igual a payerWallet neste corte"})
		return
	}
	paymentAddress := s.accessPaymentAddress()
	if !common.IsHexAddress(paymentAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "TREASURY_HOT ou SELL_WALLET_ADDRESS precisa ser um endereco EVM valido"})
		return
	}
	paymentContract, err := s.marketplacePaymentContract(r, plan.PaymentAsset)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	purchase, product, plan, err := s.db.CreateMarketplacePurchase(r.Context(), database.MarketplacePurchaseInput{
		PlanID:          plan.ID,
		AgentWallet:     req.AgentWallet,
		PayerWallet:     req.PayerWallet,
		PaymentAddress:  paymentAddress,
		PaymentContract: paymentContract,
		Nonce:           req.Nonce,
		IdempotencyKey:  req.IdempotencyKey,
		ExpiresAt:       time.Now().UTC().Add(marketplacePurchaseTTL),
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	resp := marketplacePurchaseIntentResponse(purchase, product, plan)
	resp["capability"] = map[string]any{
		"id":          capability.ID,
		"displayName": capability.DisplayName,
		"routingMode": capability.RoutingMode,
		"providers":   capability.Providers,
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleMarketplacePurchaseGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	purchase, err := s.db.GetMarketplacePurchase(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	if purchase == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "purchase nao encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purchase": purchase})
}

func (s *Server) handleMarketplacePurchaseExecute(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req struct {
		TxHash   string `json:"txHash"`
		LogIndex int    `json:"logIndex"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	req.TxHash = strings.ToLower(strings.TrimSpace(req.TxHash))
	if req.TxHash == "" || !strings.HasPrefix(req.TxHash, "0x") || req.LogIndex < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "txHash e logIndex validos sao obrigatorios"})
		return
	}
	purchase, err := s.db.GetMarketplacePurchase(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	if purchase == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "purchase nao encontrada"})
		return
	}
	if time.Now().UTC().After(purchase.ExpiresAt) {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": "purchase expirada", "status": database.MarketplacePurchaseExpired})
		return
	}
	amount, err := strconv.ParseFloat(purchase.GrossAmount, 64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "amount interno invalido"})
		return
	}
	asset, err := s.db.GetAgentSupportedAsset(r.Context(), purchase.PaymentAsset, purchase.Network)
	if err != nil {
		writeError(w, err)
		return
	}
	if asset == nil || !asset.Enabled || asset.Status != "active" || !strings.EqualFold(asset.ContractAddress, purchase.PaymentContract) {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": "contrato de pagamento nao esta na allowlist"})
		return
	}
	expectedLogIndex := req.LogIndex
	receipt, err := s.verifyERC20TransferTx(r.Context(), req.TxHash, purchase.PaymentContract, purchase.PayerWallet, purchase.PaymentAddress, amount, purchase.PaymentAsset, asset.Decimals, &expectedLogIndex)
	if err != nil {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": err.Error(), "status": database.MarketplacePurchasePaymentInvalid})
		return
	}
	result, err := s.db.ActivateMarketplacePurchase(r.Context(), purchase.ID, receipt)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, marketplaceActivationResponse(result))
}

func (s *Server) handleMarketplaceUsage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestID      string `json:"requestId"`
		Units          int    `json:"units"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	token := accessBearerToken(r)
	req.IdempotencyKey = firstNonEmpty(req.IdempotencyKey, r.Header.Get("X-Idempotency-Key"))
	if token == "" || strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Bearer token, requestId e idempotencyKey sao obrigatorios"})
		return
	}
	grant, duplicate, err := s.db.ConsumeMarketplaceUsage(r.Context(), token, req.Units, req.RequestID, req.IdempotencyKey)
	if err != nil {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"duplicate":      duplicate,
		"quotaRemaining": grant.QuotaRemaining,
		"grant":          grant,
	})
}

func (s *Server) handleMarketplaceCapabilityUsage(w http.ResponseWriter, r *http.Request) {
	capabilityID := strings.TrimSpace(r.PathValue("id"))
	var req struct {
		RequestID      string `json:"requestId"`
		Units          int    `json:"units"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	token := accessBearerToken(r)
	req.IdempotencyKey = firstNonEmpty(req.IdempotencyKey, r.Header.Get("X-Idempotency-Key"))
	if token == "" || strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Bearer token, requestId e idempotencyKey sao obrigatorios"})
		return
	}
	capability, err := s.db.GetMarketplaceCapability(r.Context(), capabilityID)
	if err != nil {
		writeError(w, err)
		return
	}
	if capability == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "capability nao encontrada"})
		return
	}
	grant, duplicate, err := s.db.ConsumeMarketplaceCapabilityUsage(r.Context(), token, req.Units, req.RequestID, req.IdempotencyKey, capability.ID)
	if err != nil {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"duplicate":      duplicate,
		"capability":     capability.ID,
		"quotaRemaining": grant.QuotaRemaining,
		"grant":          grant,
	})
}

func (s *Server) handleAgentCapabilityRoute(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	capabilityID := strings.TrimSpace(r.PathValue("capability"))
	var req struct {
		Provider     string `json:"provider"`
		RoutingMode  string `json:"routingMode"`
		Region       string `json:"region"`
		MaxLatencyMS int    `json:"maxLatencyMs"`
		MaxCostScore int    `json:"maxCostScore"`
		RequireReal  bool   `json:"requireReal"`
		Units        int    `json:"units"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	candidates, err := s.db.ListMarketplaceRouteCandidates(r.Context(), database.MarketplaceCapabilityExecuteInput{
		CapabilityID:      capabilityID,
		RequestedProvider: req.Provider,
		RoutingMode:       req.RoutingMode,
		Region:            req.Region,
		MaxLatencyMS:      req.MaxLatencyMS,
		MaxCostScore:      req.MaxCostScore,
		RequireReal:       req.RequireReal,
		Units:             req.Units,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(candidates) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "nenhuma rota encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"capability":  capabilityID,
		"routingMode": firstNonEmpty(req.RoutingMode, "best_available"),
		"selected":    candidates[0],
		"candidates":  candidates,
	})
}

func (s *Server) handleAgentCapabilityExecute(w http.ResponseWriter, r *http.Request) {
	capabilityID := strings.TrimSpace(r.PathValue("capability"))
	var req struct {
		Operation      string          `json:"operation"`
		Input          json.RawMessage `json:"input"`
		RequestID      string          `json:"requestId"`
		Units          int             `json:"units"`
		IdempotencyKey string          `json:"idempotencyKey"`
		Provider       string          `json:"provider"`
		RoutingMode    string          `json:"routingMode"`
		Region         string          `json:"region"`
		MaxLatencyMS   int             `json:"maxLatencyMs"`
		MaxCostScore   int             `json:"maxCostScore"`
		RequireReal    bool            `json:"requireReal"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	token := accessBearerToken(r)
	req.IdempotencyKey = firstNonEmpty(req.IdempotencyKey, r.Header.Get("X-Idempotency-Key"))
	if token == "" || strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Bearer access token, requestId e idempotencyKey sao obrigatorios"})
		return
	}
	result, err := s.db.ExecuteMarketplaceCapabilityMock(r.Context(), database.MarketplaceCapabilityExecuteInput{
		Token:             token,
		CapabilityID:      capabilityID,
		Operation:         req.Operation,
		RequestID:         req.RequestID,
		IdempotencyKey:    req.IdempotencyKey,
		RequestedProvider: req.Provider,
		RoutingMode:       req.RoutingMode,
		Region:            req.Region,
		MaxLatencyMS:      req.MaxLatencyMS,
		MaxCostScore:      req.MaxCostScore,
		RequireReal:       req.RequireReal,
		Units:             req.Units,
		Input:             req.Input,
	})
	if err != nil {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": err.Error()})
		return
	}
	if !result.Duplicate {
		s.promoteRealCapabilityExecution(r.Context(), result.Event)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"duplicate":      result.Duplicate,
		"capability":     result.Event.CapabilityID,
		"operation":      result.Event.Operation,
		"provider":       result.Event.ProviderSlug,
		"providerName":   result.Event.ProviderName,
		"routingMode":    result.Event.RoutingMode,
		"status":         result.Event.Status,
		"output":         json.RawMessage(result.Event.Output),
		"quotaRemaining": result.Event.QuotaRemaining,
		"execution":      result.Event,
	})
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

func (s *Server) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req struct {
		Name        string `json:"name"`
		AgentWallet string `json:"agentWallet"`
		Wallet      string `json:"wallet"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	wallet := strings.ToLower(strings.TrimSpace(firstNonEmpty(req.AgentWallet, req.Wallet)))
	if wallet != "" && !common.IsHexAddress(wallet) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet EVM invalida"})
		return
	}
	identity, err := s.db.CreateMarketplaceAgentIdentity(r.Context(), wallet, req.Name)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"agentId":      identity.AgentID,
		"wallet":       identity.Wallet,
		"apiKey":       identity.APIKey,
		"capabilities": identity.Capabilities,
		"status":       identity.Status,
		"walletProvisioning": map[string]any{
			"status": "bring_your_own_wallet",
			"note":   "custodial wallet creation is not enabled in this cut",
		},
	})
}

func (s *Server) marketplacePaymentContract(r *http.Request, asset string) (string, error) {
	symbol := strings.ToUpper(strings.TrimSpace(asset))
	if symbol == "" {
		return "", fmt.Errorf("payment asset invalido")
	}
	if s.db != nil {
		registered, err := s.db.GetAgentSupportedAsset(r.Context(), symbol, "BSC")
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

func marketplaceProductsResponse(products []*database.MarketplaceProduct) []map[string]any {
	out := make([]map[string]any, 0, len(products))
	for _, product := range products {
		out = append(out, marketplaceProductResponse(product))
	}
	return out
}

func marketplaceProductResponse(product *database.MarketplaceProduct) map[string]any {
	provider := map[string]any{}
	if product.Provider != nil {
		provider = map[string]any{"id": product.Provider.ID, "name": product.Provider.Name}
	}
	plans := make([]map[string]any, 0, len(product.Plans))
	for _, plan := range product.Plans {
		plans = append(plans, map[string]any{
			"id":              plan.ID,
			"name":            plan.Name,
			"price":           plan.PriceAmount,
			"asset":           plan.PaymentAsset,
			"network":         plan.Network,
			"takeRateBps":     plan.TakeRateBps,
			"quota":           plan.Quota,
			"validitySeconds": plan.ValiditySeconds,
		})
	}
	return map[string]any{
		"id":          product.ID,
		"name":        product.Name,
		"category":    product.Category,
		"capability":  product.Capability,
		"description": product.Description,
		"provider":    provider,
		"plans":       plans,
	}
}

func marketplacePurchaseIntentResponse(p *database.MarketplacePurchase, product *database.MarketplaceProduct, plan *database.MarketplacePlan) map[string]any {
	productName := p.ProductID
	if product != nil {
		productName = product.Name
	}
	if plan == nil {
		plan = &database.MarketplacePlan{}
	}
	return map[string]any{
		"purchaseId": p.ID,
		"status":     p.Status,
		"product":    productName,
		"payment": map[string]any{
			"asset":           p.PaymentAsset,
			"network":         p.Network,
			"chainId":         p.ChainID,
			"contractAddress": p.PaymentContract,
			"paymentAddress":  p.PaymentAddress,
			"amount":          p.GrossAmount,
			"expiresAt":       p.ExpiresAt,
		},
		"fees": map[string]any{
			"takeRateBps":    p.TakeRateBps,
			"chainfxAmount":  p.ChainFXAmount,
			"providerAmount": p.ProviderAmount,
		},
		"plan":        plan.ID,
		"requestHash": p.RequestHash,
	}
}

func marketplaceActivationResponse(result *database.MarketplaceActivationResult) map[string]any {
	resp := map[string]any{
		"purchaseId": result.Purchase.ID,
		"status":     result.Purchase.Status,
		"purchase":   result.Purchase,
	}
	if result.AccessToken != "" && result.Grant != nil {
		resp["access"] = map[string]any{
			"token":     result.AccessToken,
			"quota":     result.Grant.QuotaTotal,
			"expiresAt": result.Grant.ExpiresAt,
		}
	}
	return resp
}
