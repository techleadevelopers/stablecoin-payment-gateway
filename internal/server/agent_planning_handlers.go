package server

import (
	"net/http"
	"time"
)

func (s *Server) handleAgentPolicyDiscoveryWellKnown(w http.ResponseWriter, r *http.Request) {
	s.handleAgentPolicyDiscovery(w, r)
}

func (s *Server) handleAgentPolicyDiscovery(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	s.writeCachedDiscoveryJSON(w, r, "agent-policy-discovery:"+base, time.Minute, func() (any, error) {
		return s.agentPolicyDiscoveryDocument(base), nil
	})
}

func (s *Server) handleAgentCapabilityGraphWellKnown(w http.ResponseWriter, r *http.Request) {
	s.handleAgentCapabilityGraph(w, r)
}

func (s *Server) handleAgentCapabilityGraph(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	s.writeCachedDiscoveryJSON(w, r, "agent-capability-graph:"+base, time.Minute, func() (any, error) {
		return s.agentCapabilityGraphDocument(base), nil
	})
}

func (s *Server) agentPolicyDiscoveryDocument(base string) map[string]any {
	return map[string]any{
		"agent":      "ChainFX Agent Pay",
		"version":    "1.0.0",
		"updated_at": time.Now().UTC().Format(time.RFC3339),
		"policy_required_for": []string{
			"pay_pix_with_usdt",
			"pay_card_bill_with_usdt",
			"stablecoin_exchange",
			"capability_purchase",
			"capability_execution",
			"x402_capability_execution",
		},
		"required_policy": map[string]any{
			"identity": "agent wallet must be connected through POST /agent/connect or have an active marketplace_agent_policy",
			"status":   "active",
			"assets":   []string{"USDT", "USDC"},
			"network":  []string{"BSC"},
			"permissions": []string{
				"payments:create",
				"capabilities:read",
				"capabilities:purchase",
				"capabilities:execute",
				"trades:create",
				"settlements:read",
			},
			"limits": map[string]any{
				"max_transaction_usdt": "policy.maxTransactionUsdt",
				"daily_limit_usdt":     "policy.dailyLimitUsdt",
				"monthly_limit_usdt":   "policy.monthlyLimitUsdt",
			},
		},
		"supported_policies": []map[string]any{
			{
				"id":                    "default_autonomous_agent",
				"description":           "Default active policy created by /agent/connect for autonomous agents.",
				"wallet_mode":           "existing",
				"mock_fallback":         true,
				"require_real_provider": false,
				"allowed_assets":        []string{"USDT", "USDC"},
				"permissions":           []string{"capabilities:read", "capabilities:purchase", "capabilities:execute", "trades:create", "payments:create", "settlements:read", "webhooks:write"},
			},
		},
		"onboarding": map[string]any{
			"connect": base + "/agent/connect",
			"method":  "POST",
			"auth":    "Authorization: Bearer <ChainFX API key>",
			"example": map[string]any{
				"name":               "Agent QA",
				"environment":        "production",
				"agentType":          "autonomous",
				"walletMode":         "existing",
				"agentWallet":        "0x830000000000000000000000000000000000019a",
				"dailyLimitUsdt":     "500",
				"monthlyLimitUsdt":   "5000",
				"maxTransactionUsdt": "100",
				"allowedAssets":      []string{"USDT", "USDC"},
				"permissions":        []string{"capabilities:read", "capabilities:purchase", "capabilities:execute", "trades:create", "payments:create", "settlements:read"},
			},
		},
		"error_recovery": map[string]any{
			"AGENT_POLICY_REQUIRED": map[string]any{
				"meaning":     "The paying agent wallet has no active policy.",
				"next_action": "Call /agent/connect with the agent wallet or ask the ChainFX admin to create an active policy.",
				"docs":        base + "/.well-known/agent-policy.json",
			},
			"AGENT_POLICY_INACTIVE": map[string]any{
				"meaning":     "The policy exists but is not active.",
				"next_action": "Update policy status to active through PATCH /agent/{id}/policy.",
			},
			"MAX_TRANSACTION_EXCEEDED": map[string]any{
				"meaning":     "The requested amount exceeds maxTransactionUsdt.",
				"next_action": "Lower the amount or update the policy limit.",
			},
		},
	}
}

func (s *Server) agentCapabilityGraphDocument(base string) map[string]any {
	return map[string]any{
		"agent":      "ChainFX Agent Pay",
		"version":    "1.0.0",
		"updated_at": time.Now().UTC().Format(time.RFC3339),
		"nodes": []map[string]any{
			{"id": "agent_identity", "type": "trust", "endpoint": base + "/.well-known/agent-card.json"},
			{"id": "agent_policy", "type": "policy", "endpoint": base + "/.well-known/agent-policy.json"},
			{"id": "payment_methods", "type": "discovery", "a2a_skill": "list_supported_payment_methods"},
			{"id": "quote_required_usdt", "type": "quote", "a2a_skill": "quote_required_usdt"},
			{"id": "pay_pix_with_usdt", "type": "payment", "a2a_skill": "pay_pix_with_usdt"},
			{"id": "pay_card_bill_with_usdt", "type": "payment", "a2a_skill": "pay_card_bill_with_usdt"},
			{"id": "stablecoin_exchange", "type": "settlement", "a2a_skill": "stablecoin_exchange"},
			{"id": "capability_exchange", "type": "marketplace", "a2a_skill": "capability_exchange"},
			{"id": "x402_capability_execution", "type": "payment_protocol", "endpoint": base + "/x402/capabilities/{capability}/execute"},
			{"id": "payment_status", "type": "status", "a2a_skill": "get_payment_status"},
			{"id": "episodes", "type": "observability", "endpoint": base + "/agent/v1/episodes"},
			{"id": "reputation", "type": "trust_metric", "endpoint": base + "/.well-known/agent-reputation.json"},
			{"id": "sla", "type": "trust_metric", "endpoint": base + "/.well-known/agent-sla.json"},
		},
		"edges": []map[string]any{
			{"from": "agent_identity", "to": "agent_policy", "relation": "requires"},
			{"from": "pay_pix_with_usdt", "to": "agent_policy", "relation": "requires"},
			{"from": "pay_card_bill_with_usdt", "to": "agent_policy", "relation": "requires"},
			{"from": "stablecoin_exchange", "to": "agent_policy", "relation": "requires"},
			{"from": "capability_exchange", "to": "agent_policy", "relation": "recommended_for_purchase"},
			{"from": "quote_required_usdt", "to": "payment_methods", "relation": "depends_on"},
			{"from": "pay_pix_with_usdt", "to": "quote_required_usdt", "relation": "depends_on"},
			{"from": "pay_pix_with_usdt", "to": "payment_status", "relation": "follow_up"},
			{"from": "x402_capability_execution", "to": "capability_exchange", "relation": "depends_on"},
			{"from": "x402_capability_execution", "to": "episodes", "relation": "emits"},
			{"from": "pay_pix_with_usdt", "to": "episodes", "relation": "emits"},
			{"from": "episodes", "to": "reputation", "relation": "aggregates_into"},
			{"from": "episodes", "to": "sla", "relation": "measures"},
		},
		"plans": []map[string]any{
			{
				"id":          "pay_pix_with_usdt_happy_path",
				"description": "Policy-aware PIX payment sequence.",
				"steps": []string{
					"fetch_agent_card",
					"fetch_agent_policy_discovery",
					"ensure_agent_policy_or_call_agent_connect",
					"call_quote_required_usdt",
					"call_pay_pix_with_usdt",
					"deposit_exact_required_usdt_on_bsc",
					"poll_get_payment_status",
					"read_episode_and_reputation",
				},
			},
			{
				"id":          "x402_capability_happy_path",
				"description": "Pay-per-call digital capability execution.",
				"steps": []string{
					"fetch_capability_graph",
					"discover_capability_exchange",
					"POST_x402_capability_without_PAYMENT",
					"pay_returned_payment_requirements",
					"replay_with_PAYMENT_header",
					"read_PAYMENT_RESPONSE",
				},
			},
		},
		"semantic_aliases": map[string][]string{
			"pay_pix_with_usdt":         {"pay pix", "pix payment", "send brl", "pay brazil recipient"},
			"quote_required_usdt":       {"price", "quote", "required usdt", "estimate payment"},
			"stablecoin_exchange":       {"swap stablecoin", "exchange usdt", "convert usdc"},
			"document_ocr":              {"extract text", "read document", "ocr", "parse invoice"},
			"llm_chat":                  {"generate text", "chat", "summarize", "classification", "translate"},
			"semantic_memory":           {"remember", "retrieve context", "knowledge lookup", "rag memory"},
			"capability_exchange":       {"find provider", "capability marketplace", "buy tool"},
			"x402_capability_execution": {"pay per call", "http 402", "paid api", "micropayment"},
		},
	}
}
