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
	skillContracts := []map[string]any{
		{
			"skill":    "list_supported_payment_methods",
			"category": "discovery",
			"requires": []string{
				"agent_card",
			},
			"produces": []string{
				"payment_methods",
				"quote_skill",
				"status_skill",
				"create_skills",
			},
			"next": []string{
				"quote_required_usdt",
				"pay_pix_with_usdt",
				"pay_card_bill_with_usdt",
			},
			"preconditions": []string{
				"Agent has discovered the A2A URL from /.well-known/agent-card.json.",
			},
			"failure_modes": map[string]string{
				"UNKNOWN_SKILL": "refresh_agent_card_and_retry",
			},
			"recovery_actions": []string{
				"fetch_agent_card",
				"retry_same_skill",
			},
			"estimated_cost": map[string]any{
				"type":     "free",
				"currency": "USDT",
				"amount":   "0",
			},
			"expected_latency_ms": map[string]any{
				"p95": 3000,
			},
			"policy_requirements": []string{},
			"input_schema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
			"output_schema": map[string]any{
				"type":     "object",
				"required": []string{"payment_methods", "quote_skill", "status_skill"},
			},
		},
		{
			"skill":    "quote_required_usdt",
			"category": "quote",
			"requires": []string{
				"payment_methods",
				"agent_wallet",
				"amount_brl",
			},
			"produces": []string{
				"gross_usdt",
				"fee_usdt",
				"required_usdt",
				"fee_bps",
				"usdt_rate",
			},
			"next": []string{
				"pay_pix_with_usdt",
				"pay_card_bill_with_usdt",
			},
			"preconditions": []string{
				"amount_brl is a positive decimal string.",
				"agent_wallet is a valid EVM address.",
				"USDT/BRL rate is available.",
			},
			"failure_modes": map[string]string{
				"INVALID_AMOUNT":       "correct_amount_brl",
				"INVALID_AGENT_WALLET": "provide_valid_evm_wallet",
				"RATE_UNAVAILABLE":     "retry_after_price_worker_updates",
				"UNAUTHORIZED":         "send_bearer_api_key",
			},
			"recovery_actions": []string{
				"fix_payload",
				"fetch_agent_policy_discovery",
				"retry_same_skill",
			},
			"estimated_cost": map[string]any{
				"type":   "dynamic",
				"source": "amount_brl / USDTBRL plus fee_bps",
			},
			"expected_latency_ms": map[string]any{
				"p95": 5000,
			},
			"policy_requirements": []string{
				"bearer_auth_required",
			},
			"input_schema": map[string]any{
				"type": "object",
				"required": []string{
					"type",
					"amount_brl",
					"agent_wallet",
				},
				"properties": map[string]any{
					"type":         map[string]any{"enum": []string{"pix", "credit_card"}},
					"amount_brl":   map[string]any{"type": "string", "pattern": "^[0-9]+(\\.[0-9]{2})?$"},
					"agent_wallet": map[string]any{"type": "string", "pattern": "^0x[a-fA-F0-9]{40}$"},
				},
				"additionalProperties": false,
			},
			"output_schema": map[string]any{
				"type":     "object",
				"required": []string{"required_usdt", "gross_usdt", "fee_usdt", "fee_bps", "funding_asset", "funding_network"},
			},
		},
		{
			"skill":    "pay_pix_with_usdt",
			"category": "payment",
			"requires": []string{
				"agent_policy",
				"quote_required_usdt",
				"agent_wallet",
				"pix_key",
				"idempotency_key",
			},
			"produces": []string{
				"payment_intent",
				"payment_address",
				"required_usdt",
				"expires_at",
			},
			"next": []string{
				"deposit_exact_required_usdt_on_bsc",
				"get_payment_status",
				"read_episode_and_reputation",
			},
			"preconditions": []string{
				"agent wallet has active policy.",
				"policy allows payments:create.",
				"amount is inside maxTransactionUsdt, dailyLimitUsdt and monthlyLimitUsdt.",
				"USDT on BSC is allowed by policy.",
				"idempotency_key is unique for the agent intent.",
			},
			"failure_modes": map[string]string{
				"AGENT_POLICY_REQUIRED":       "call_agent_connect_or_activate_policy",
				"AGENT_POLICY_INACTIVE":       "patch_policy_status_active",
				"AGENT_PERMISSION_DENIED":     "add_payments_create_permission",
				"MAX_TRANSACTION_EXCEEDED":    "lower_amount_or_update_policy",
				"DAILY_LIMIT_EXCEEDED":        "wait_for_budget_window_or_update_policy",
				"MONTHLY_LIMIT_EXCEEDED":      "wait_for_budget_window_or_update_policy",
				"ASSET_NOT_ALLOWED":           "select_supported_asset_or_update_policy",
				"M2M_DEPOSIT_ADDRESS_MISSING": "configure_m2m_deposit_addresses_or_treasury_hot",
				"UNAUTHORIZED":                "send_bearer_api_key",
			},
			"recovery_actions": []string{
				"fetch_policy_discovery",
				"call_agent_connect",
				"update_policy",
				"lower_amount",
				"retry_same_skill_with_same_idempotency_key_only_if_previous_attempt_failed_before_intent_creation",
				"poll_get_payment_status_if_intent_id_was_created",
			},
			"estimated_cost": map[string]any{
				"type":   "dynamic",
				"source": "quote_required_usdt.required_usdt",
			},
			"expected_latency_ms": map[string]any{
				"p95": 15000,
			},
			"policy_requirements": []string{
				"status=active",
				"permissions includes payments:create",
				"allowedAssets includes USDT",
				"maxTransactionUsdt >= quote.required_usdt",
				"dailyLimitUsdt and monthlyLimitUsdt have remaining budget",
			},
			"input_schema": map[string]any{
				"type": "object",
				"required": []string{
					"amount_brl",
					"pix_key",
					"idempotency_key",
					"agent_wallet",
				},
				"properties": map[string]any{
					"amount_brl":       map[string]any{"type": "string", "pattern": "^[0-9]+(\\.[0-9]{2})?$"},
					"pix_key":          map[string]any{"type": "string", "minLength": 3},
					"beneficiary_name": map[string]any{"type": "string"},
					"idempotency_key":  map[string]any{"type": "string", "minLength": 8},
					"agent_wallet":     map[string]any{"type": "string", "pattern": "^0x[a-fA-F0-9]{40}$"},
				},
				"additionalProperties": false,
			},
			"output_schema": map[string]any{
				"type":     "object",
				"required": []string{"payment.intent_id", "payment.payment_address", "payment.required_usdt", "payment.status"},
			},
		},
		{
			"skill":    "pay_card_bill_with_usdt",
			"category": "payment",
			"requires": []string{
				"agent_policy",
				"quote_required_usdt",
				"agent_wallet",
				"payment_link_or_barcode",
				"idempotency_key",
			},
			"produces": []string{
				"payment_intent",
				"payment_address",
				"required_usdt",
				"expires_at",
			},
			"next": []string{
				"deposit_exact_required_usdt_on_bsc",
				"get_payment_status",
			},
			"preconditions": []string{
				"agent wallet has active policy.",
				"policy allows payments:create.",
				"USDT on BSC is allowed by policy.",
				"payment_link or barcode identifies the bill destination.",
			},
			"failure_modes": map[string]string{
				"AGENT_POLICY_REQUIRED":    "call_agent_connect_or_activate_policy",
				"AGENT_PERMISSION_DENIED":  "add_payments_create_permission",
				"MAX_TRANSACTION_EXCEEDED": "lower_amount_or_update_policy",
				"INVALID_BILL_REFERENCE":   "provide_payment_link_or_barcode",
			},
			"recovery_actions": []string{
				"fetch_policy_discovery",
				"update_policy",
				"fix_bill_reference",
				"retry_same_skill",
			},
			"estimated_cost": map[string]any{
				"type":   "dynamic",
				"source": "quote_required_usdt.required_usdt with credit_card fee_bps",
			},
			"expected_latency_ms": map[string]any{
				"p95": 15000,
			},
			"policy_requirements": []string{
				"status=active",
				"permissions includes payments:create",
				"allowedAssets includes USDT",
			},
		},
		{
			"skill":    "get_payment_status",
			"category": "status",
			"requires": []string{
				"payment_intent",
				"intent_id",
			},
			"produces": []string{
				"intent_status",
				"deposit_data",
				"settlement_receipt",
			},
			"next": []string{
				"deposit_exact_required_usdt_on_bsc_if_pending_deposit",
				"read_episode_and_reputation",
			},
			"preconditions": []string{
				"intent_id was returned by pay_pix_with_usdt or pay_card_bill_with_usdt.",
			},
			"failure_modes": map[string]string{
				"NOT_FOUND":      "verify_intent_id",
				"UNAUTHORIZED":   "send_bearer_api_key",
				"INTENT_EXPIRED": "create_new_payment_intent",
			},
			"recovery_actions": []string{
				"retry_poll_until_terminal_status",
				"create_new_intent_if_expired",
			},
			"estimated_cost": map[string]any{
				"type":     "free",
				"currency": "USDT",
				"amount":   "0",
			},
			"expected_latency_ms": map[string]any{
				"p95": 3000,
			},
			"policy_requirements": []string{
				"bearer_auth_required",
			},
		},
		{
			"skill":    "stablecoin_exchange",
			"category": "settlement",
			"requires": []string{
				"agent_policy",
				"agent_wallet",
				"pay_asset",
				"receive_asset",
				"amount",
			},
			"produces": []string{
				"trade_quote",
				"trade_intent",
				"payment_address",
				"request_hash",
			},
			"next": []string{
				"pay_onchain",
				"agent_v1_trade_status",
			},
			"preconditions": []string{
				"policy allows trades:create.",
				"asset pair is enabled on BSC.",
				"agentWallet and payerWallet are valid EVM addresses.",
			},
			"failure_modes": map[string]string{
				"AGENT_POLICY_REQUIRED":    "call_agent_connect_or_activate_policy",
				"ASSET_NOT_ALLOWED":        "select_supported_asset_or_update_policy",
				"MAX_TRANSACTION_EXCEEDED": "lower_amount_or_update_policy",
				"PAIR_NOT_SUPPORTED":       "call_list_assets_and_choose_supported_pair",
			},
			"recovery_actions": []string{
				"fetch_agent_assets",
				"fetch_policy_discovery",
				"update_policy",
				"retry_with_supported_pair",
			},
			"estimated_cost": map[string]any{
				"type":   "dynamic",
				"source": "agent/v1/trade/quote",
			},
			"expected_latency_ms": map[string]any{
				"p95": 12000,
			},
			"policy_requirements": []string{
				"status=active",
				"permissions includes trades:create",
				"allowedAssets includes payAsset",
			},
		},
		{
			"skill":    "capability_exchange",
			"category": "marketplace",
			"requires": []string{
				"agent_policy_recommended",
				"capability_catalog",
			},
			"produces": []string{
				"capability_list",
				"plans",
				"providers",
				"contracts",
			},
			"next": []string{
				"get_capability_contract",
				"purchase_capability",
				"x402_capability_execution",
			},
			"preconditions": []string{
				"Agent can read public marketplace discovery.",
			},
			"failure_modes": map[string]string{
				"CAPABILITY_NOT_FOUND": "select_existing_capability",
				"NO_ACTIVE_PLAN":       "select_capability_with_active_plan",
			},
			"recovery_actions": []string{
				"refresh_capability_catalog",
				"select_alternative_capability",
			},
			"estimated_cost": map[string]any{
				"type":   "catalog",
				"source": "marketplace/capabilities.plans",
			},
			"expected_latency_ms": map[string]any{
				"p95": 5000,
			},
			"policy_requirements": []string{
				"policy required before purchase or execution",
			},
		},
		{
			"skill":    "document_ocr",
			"category": "capability",
			"requires": []string{
				"capability_grant_or_x402_payment",
				"document_input",
			},
			"produces": []string{
				"ocr_text",
				"structured_document_fields",
				"usage_event",
				"episode",
			},
			"next": []string{
				"llm_chat",
				"semantic_memory",
			},
			"preconditions": []string{
				"Agent has a valid grant or completes x402 payment challenge.",
				"documentUrl or document payload is reachable by configured provider.",
			},
			"failure_modes": map[string]string{
				"AGENT_POLICY_REQUIRED": "call_agent_connect_or_activate_policy",
				"GRANT_REQUIRED":        "purchase_capability_or_use_x402",
				"PROVIDER_UNAVAILABLE":  "allow_mock_fallback_or_retry_later",
				"INVALID_INPUT":         "provide_document_url_or_supported_payload",
			},
			"recovery_actions": []string{
				"purchase_capability",
				"POST_x402_capability_without_PAYMENT",
				"retry_with_alternative_provider",
				"fallback_to_mock_if_policy_allows",
			},
			"estimated_cost": map[string]any{
				"type":   "plan_or_x402",
				"source": "marketplace/capabilities/document_ocr.plans",
			},
			"expected_latency_ms": map[string]any{
				"p95": 15000,
			},
			"policy_requirements": []string{
				"permissions includes capabilities:purchase for purchase",
				"permissions includes capabilities:execute for grant execution",
				"allowedCapabilities empty or includes document_ocr",
			},
		},
		{
			"skill":    "llm_chat",
			"category": "capability",
			"requires": []string{
				"capability_grant_or_x402_payment",
				"prompt_or_messages",
			},
			"produces": []string{
				"generated_text",
				"classification",
				"summary",
				"usage_event",
				"episode",
			},
			"next": []string{
				"semantic_memory",
				"pay_pix_with_usdt",
			},
			"preconditions": []string{
				"OpenAI-compatible provider configured or mock fallback allowed.",
			},
			"failure_modes": map[string]string{
				"GRANT_REQUIRED":       "purchase_capability_or_use_x402",
				"PROVIDER_UNAVAILABLE": "configure_openai_api_key_or_allow_fallback",
				"INVALID_INPUT":        "provide_prompt_or_messages",
			},
			"recovery_actions": []string{
				"purchase_capability",
				"retry_with_smaller_input",
				"fallback_to_mock_if_policy_allows",
			},
			"estimated_cost": map[string]any{
				"type":   "plan_or_token_usage",
				"source": "marketplace/capabilities/llm_chat.plans",
			},
			"expected_latency_ms": map[string]any{
				"p95": 20000,
			},
			"policy_requirements": []string{
				"allowedCapabilities empty or includes llm_chat",
			},
		},
		{
			"skill":    "semantic_memory",
			"category": "capability",
			"requires": []string{
				"capability_grant_or_x402_payment",
				"memory_operation",
			},
			"produces": []string{
				"memory_record",
				"retrieved_context",
				"usage_event",
				"episode",
			},
			"next": []string{
				"llm_chat",
				"document_ocr",
			},
			"preconditions": []string{
				"Postgres-backed memory adapter available.",
			},
			"failure_modes": map[string]string{
				"GRANT_REQUIRED": "purchase_capability_or_use_x402",
				"INVALID_INPUT":  "provide_text_or_query",
			},
			"recovery_actions": []string{
				"purchase_capability",
				"fix_operation_payload",
				"retry_same_skill",
			},
			"estimated_cost": map[string]any{
				"type":   "plan_or_x402",
				"source": "marketplace/capabilities/semantic_memory.plans",
			},
			"expected_latency_ms": map[string]any{
				"p95": 8000,
			},
			"policy_requirements": []string{
				"allowedCapabilities empty or includes semantic_memory",
			},
		},
		{
			"skill":    "x402_capability_execution",
			"category": "payment_protocol",
			"requires": []string{
				"capability_exchange",
				"agent_policy",
				"agent_wallet",
				"payer_wallet",
				"PAYMENT_header_after_challenge",
			},
			"produces": []string{
				"payment_requirements",
				"access_grant",
				"capability_result",
				"PAYMENT_RESPONSE",
				"episode",
			},
			"next": []string{
				"read_PAYMENT_RESPONSE",
				"read_episode_and_reputation",
			},
			"preconditions": []string{
				"First call without PAYMENT receives HTTP 402.",
				"Agent pays returned requirements on BSC.",
				"Replay includes base64url PAYMENT header with purchaseId, txHash and logIndex.",
			},
			"failure_modes": map[string]string{
				"HTTP_402_PAYMENT_REQUIRED": "pay_returned_payment_requirements",
				"INVALID_PAYMENT_HEADER":    "fix_payment_header_shape",
				"PAYMENT_NOT_CONFIRMED":     "wait_for_bsc_receipt_confirmations",
				"MAX_TRANSACTION_EXCEEDED":  "lower_units_or_update_policy",
			},
			"recovery_actions": []string{
				"pay_returned_payment_requirements",
				"replay_with_PAYMENT_header",
				"poll_chain_for_receipt",
				"retry_after_confirmation",
			},
			"estimated_cost": map[string]any{
				"type":   "exact",
				"source": "payment_requirements.amount",
			},
			"expected_latency_ms": map[string]any{
				"p95": 20000,
			},
			"policy_requirements": []string{
				"status=active",
				"permissions includes capabilities:purchase",
				"permissions includes capabilities:execute",
				"allowedCapabilities empty or includes requested capability",
			},
		},
	}

	return map[string]any{
		"agent":        "ChainFX Agent Pay",
		"name":         "ChainFX Agent Capability Graph",
		"product_name": "ChainFX Planning Layer for Agent Commerce",
		"version":      "2.0.0",
		"updated_at":   time.Now().UTC().Format(time.RFC3339),
		"objective":    "Let autonomous agents understand how to use each ChainFX skill before execution.",
		"nodes": []map[string]any{
			{"id": "agent_card", "type": "discovery", "endpoint": base + "/.well-known/agent-card.json"},
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
		"skills":          skillContracts,
		"skill_contracts": skillContracts,
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
		"phase_report": map[string]any{
			"id":        "agent_graph_v2_report",
			"phase":     "1",
			"objective": "Discovery becomes understanding: an agent can read the graph and know dependencies, outputs, failures and recovery actions for each skill.",
			"endpoints": []string{
				base + "/.well-known/capability-graph.json",
				base + "/agent/v1/capability-graph",
			},
			"skills_mapped": []string{
				"list_supported_payment_methods",
				"quote_required_usdt",
				"pay_pix_with_usdt",
				"pay_card_bill_with_usdt",
				"get_payment_status",
				"stablecoin_exchange",
				"capability_exchange",
				"document_ocr",
				"llm_chat",
				"semantic_memory",
				"x402_capability_execution",
			},
			"coverage": []string{
				"requires",
				"produces",
				"preconditions",
				"failure_modes",
				"recovery_actions",
				"estimated_cost",
				"expected_latency_ms",
				"policy_requirements",
				"input_schema",
				"output_schema",
			},
			"acceptance_criteria": "An agent can read the graph and infer the correct sequence without human documentation.",
			"qa": map[string]any{
				"tool":           "tools/agent-qa/openai-agent-pay-test",
				"expected_check": "capability_graph_v2_validated",
			},
		},
	}
}
