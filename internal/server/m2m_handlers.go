package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"

	"github.com/ethereum/go-ethereum/common"
)

// ─── Request / Response types ────────────────────────────────────────────────

type m2mCreateRequest struct {
	Type           string `json:"type"`            // "pix" | "credit_card"
	AmountBRL      string `json:"amount_brl"`      // BRL amount the recipient will receive
	PixKey         string `json:"pix_key"`         // required when type == "pix"
	IdempotencyKey string `json:"idempotency_key"` // caller-generated, immutable
	AgentWallet    string `json:"agent_wallet"`    // EVM address of paying agent (audit)
}

type m2mCreateResponse struct {
	IntentID       string    `json:"intent_id"`
	Status         string    `json:"status"`
	PaymentType    string    `json:"payment_type"`
	AmountBRL      string    `json:"amount_brl"`
	GrossUSDT      string    `json:"gross_usdt"`
	FeeUSDT        string    `json:"fee_usdt"`
	RequiredUSDT   string    `json:"required_usdt"`
	FeeBps         int       `json:"fee_bps"`
	USDTRate       string    `json:"usdt_rate"`
	PaymentAddress string    `json:"payment_address"`
	ExpiresAt      time.Time `json:"expires_at"`
	Idempotent     bool      `json:"idempotent,omitempty"` // true when returning cached response
	NextStep       string    `json:"next_step"`
}

// ─── POST /agent/v1/pay ──────────────────────────────────────────────────────

// handleM2MCreateIntent creates a new agent-initiated payment intent.
// The agent deposits RequiredUSDT on-chain; our system then pays the fiat recipient.
func (s *Server) handleM2MCreateIntent(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req m2mCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}

	// ── Validation ────────────────────────────────────────────────────────────
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.AgentWallet = strings.ToLower(strings.TrimSpace(req.AgentWallet))
	req.PixKey = strings.TrimSpace(req.PixKey)

	if req.IdempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "idempotency_key is required"})
		return
	}
	if req.AgentWallet == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_wallet is required"})
		return
	}
	if !common.IsHexAddress(req.AgentWallet) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_wallet must be a valid EVM address"})
		return
	}

	var feeBps int
	switch req.Type {
	case "pix":
		if req.PixKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pix_key is required for type=pix"})
			return
		}
		feeBps = s.cfg.M2MPixFeeBps
	case "credit_card":
		feeBps = s.cfg.M2MCreditFeeBps
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "type must be 'pix' or 'credit_card'"})
		return
	}

	amountBRL, err := parsePositiveFloat(req.AmountBRL)
	if err != nil || amountBRL <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_brl must be a positive number"})
		return
	}

	// ── Rate ──────────────────────────────────────────────────────────────────
	usdtRate := s.workers.PriceWorker.GetPrice("BRL")
	if usdtRate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "USDT/BRL rate unavailable; retry in a few seconds",
		})
		return
	}

	// ── Fee calculation ───────────────────────────────────────────────────────
	// grossUSDT = amountBRL / usdtRate   (base: how much the pix recipient costs in USDT)
	// feeUSDT   = grossUSDT * (feeBps / 10_000)
	// required  = grossUSDT + feeUSDT
	grossUSDT := amountBRL / usdtRate
	feeUSDT := grossUSDT * (float64(feeBps) / 10_000.0)
	requiredUSDT := grossUSDT + feeUSDT

	_, decision, policyErr := s.db.ValidateAgentPaymentPolicy(r.Context(), req.AgentWallet, "USDT", fmt.Sprintf("%.6f", requiredUSDT))
	if policyErr != nil {
		writeError(w, policyErr)
		return
	}
	if !decision.Allowed {
		writeAPIError(w, r, http.StatusForbidden, decision.Code, decision.Message)
		return
	}

	// ── Intent ID (deterministic from idempotency key + wallet) ──────────────
	intentID := m2mIntentID(req.IdempotencyKey, req.AgentWallet)

	// ── Audit hash (canonical request fingerprint) ────────────────────────────
	requestHash := database.CanonicalRequestHash(
		req.Type, req.AmountBRL, req.PixKey,
		req.IdempotencyKey, req.AgentWallet,
	)

	paymentAddress := strings.ToLower(strings.TrimSpace(s.cfg.TreasuryHot))
	if !common.IsHexAddress(paymentAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "TREASURY_HOT must be a valid EVM payment address"})
		return
	}

	in := database.M2MCreateInput{
		ID:             intentID,
		IdempotencyKey: req.IdempotencyKey,
		AgentWallet:    req.AgentWallet,
		PaymentType:    database.M2MPaymentType(req.Type),
		PixKey:         req.PixKey,
		AmountBRL:      amountBRL,
		FeeBps:         feeBps,
		FeeUSDT:        feeUSDT,
		GrossUSDT:      grossUSDT,
		RequiredUSDT:   requiredUSDT,
		USDTRate:       usdtRate,
		PaymentAddress: paymentAddress,
		RequestHash:    requestHash,
		ExpiresAt:      time.Now().UTC().Add(15 * time.Minute),
	}

	intent, isIdempotent, err := s.db.CreateAgentPaymentIntent(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to create intent"})
		return
	}

	statusCode := http.StatusCreated
	if isIdempotent {
		statusCode = http.StatusOK
	}

	writeJSON(w, statusCode, m2mIntentToResponse(intent, isIdempotent))
}

// ─── GET /agent/v1/pay/{id} ──────────────────────────────────────────────────

func (s *Server) handleM2MGetIntent(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "intent id is required"})
		return
	}

	intent, err := s.db.GetAgentPaymentIntent(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to fetch intent"})
		return
	}
	if intent == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "intent not found"})
		return
	}

	writeJSON(w, http.StatusOK, intentFullView(intent))
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func m2mIntentToResponse(i *database.AgentPaymentIntent, idempotent bool) m2mCreateResponse {
	nextStep := "deposit RequiredUSDT on-chain to PaymentAddress, then poll GET /agent/v1/pay/{intent_id}"
	if i.Status != "pending_deposit" {
		nextStep = fmt.Sprintf("intent is %s — no deposit required", i.Status)
	}
	return m2mCreateResponse{
		IntentID:       i.ID,
		Status:         string(i.Status),
		PaymentType:    string(i.PaymentType),
		AmountBRL:      fmt.Sprintf("%.2f", i.AmountBRL),
		GrossUSDT:      fmt.Sprintf("%.6f", i.GrossUSDT),
		FeeUSDT:        fmt.Sprintf("%.6f", i.FeeUSDT),
		RequiredUSDT:   fmt.Sprintf("%.6f", i.RequiredUSDT),
		FeeBps:         i.FeeBps,
		USDTRate:       fmt.Sprintf("%.4f", i.USDTRate),
		PaymentAddress: i.PaymentAddress,
		ExpiresAt:      i.ExpiresAt,
		Idempotent:     idempotent,
		NextStep:       nextStep,
	}
}

func intentFullView(i *database.AgentPaymentIntent) map[string]any {
	out := map[string]any{
		"id":              i.ID,
		"status":          string(i.Status),
		"payment_type":    string(i.PaymentType),
		"amount_brl":      fmt.Sprintf("%.2f", i.AmountBRL),
		"gross_usdt":      fmt.Sprintf("%.6f", i.GrossUSDT),
		"fee_usdt":        fmt.Sprintf("%.6f", i.FeeUSDT),
		"required_usdt":   fmt.Sprintf("%.6f", i.RequiredUSDT),
		"fee_bps":         i.FeeBps,
		"usdt_rate":       fmt.Sprintf("%.4f", i.USDTRate),
		"payment_address": i.PaymentAddress,
		"expires_at":      i.ExpiresAt,
		"attempts":        i.Attempts,
		"created_at":      i.CreatedAt,
		"updated_at":      i.UpdatedAt,
	}
	if i.PixKey != "" {
		out["pix_key"] = i.PixKey
	}
	if i.DepositTx != nil {
		out["deposit_tx"] = *i.DepositTx
	}
	if i.DepositAmountUSDT != nil {
		out["deposit_amount_usdt"] = fmt.Sprintf("%.6f", *i.DepositAmountUSDT)
	}
	if i.EfiEndToEndID != nil {
		out["efi_end_to_end_id"] = *i.EfiEndToEndID
	}
	if i.EfiStatus != nil {
		out["efi_status"] = *i.EfiStatus
	}
	if i.ErrorMessage != nil {
		out["error_message"] = *i.ErrorMessage
	}
	if i.SettledAt != nil {
		out["settled_at"] = *i.SettledAt
	}
	return out
}

func m2mIntentID(idempotencyKey, agentWallet string) string {
	hash := database.CanonicalRequestHash(idempotencyKey, strings.ToLower(agentWallet))
	if len(hash) > 24 {
		hash = hash[:24]
	}
	return "int_m2m_" + hash
}

func parsePositiveFloat(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}
