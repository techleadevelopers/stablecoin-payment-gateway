package mobile

// swap.go — Phase 5: Direct crypto-to-crypto Swap endpoints (mobile-only)
//
//	POST /api/mobile/swap/quote    — get a real-time swap quote
//	POST /api/mobile/swap/execute  — submit a swap order
//	GET  /api/mobile/swap/{id}     — get swap status
//	GET  /api/mobile/swaps         — list user's swaps

import (
	"log/slog"
	"net/http"
	"strings"

	"payment-gateway/internal/models"
)

const defaultSwapFeeBPS = 50  // 0.50 %
const defaultSlippage = 0.005 // 0.5 %

// handleSwapQuote — POST /api/mobile/swap/quote
// Returns a real-time price estimate. No DB write, no commitment.
func (s *Server) handleSwapQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FromAsset string  `json:"from_asset"`
		ToAsset   string  `json:"to_asset"`
		Amount    float64 `json:"amount"`
		Slippage  float64 `json:"slippage"` // e.g. 0.005 = 0.5 %
	}
	if err := decodeJSON(r, &req); err != nil || req.Amount <= 0 || req.FromAsset == "" || req.ToAsset == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "from_asset, to_asset e amount obrigatórios"})
		return
	}
	req.FromAsset = strings.ToUpper(req.FromAsset)
	req.ToAsset = strings.ToUpper(req.ToAsset)
	if req.FromAsset == req.ToAsset {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "from_asset e to_asset devem ser diferentes"})
		return
	}
	if req.Slippage <= 0 {
		req.Slippage = defaultSlippage
	}

	pw := s.PriceCache()
	if false && pw == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "serviço de cotação indisponível"})
		return
	}
	fromBRL := mobileAssetPriceBRL(pw, req.FromAsset)
	toBRL := mobileAssetPriceBRL(pw, req.ToAsset)

	if fromBRL <= 0 || toBRL <= 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "cotação indisponível para um dos ativos",
		})
		return
	}

	rate := fromBRL / toBRL
	fee := req.Amount * float64(defaultSwapFeeBPS) / 10_000
	netFrom := req.Amount - fee
	estimatedTo := netFrom * rate
	minReceived := estimatedTo * (1 - req.Slippage)

	writeJSON(w, http.StatusOK, map[string]any{
		"from_asset":     req.FromAsset,
		"to_asset":       req.ToAsset,
		"from_amount":    req.Amount,
		"estimated_to":   estimatedTo,
		"min_received":   minReceived,
		"rate":           rate,
		"fee_bps":        defaultSwapFeeBPS,
		"fee_amount":     fee,
		"slippage":       req.Slippage,
		"from_price_brl": fromBRL,
		"to_price_brl":   toBRL,
	})
}

// handleSwapExecute — POST /api/mobile/swap/execute
// Creates a swap order and enqueues it for the SwapWorker.
func (s *Server) handleSwapExecute(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		FromAsset string  `json:"from_asset"`
		ToAsset   string  `json:"to_asset"`
		Amount    float64 `json:"amount"`
		Slippage  float64 `json:"slippage"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Amount <= 0 || req.FromAsset == "" || req.ToAsset == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "from_asset, to_asset e amount obrigatórios"})
		return
	}
	req.FromAsset = strings.ToUpper(req.FromAsset)
	req.ToAsset = strings.ToUpper(req.ToAsset)
	if req.FromAsset == req.ToAsset {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "from_asset e to_asset devem ser diferentes"})
		return
	}
	if req.Slippage <= 0 {
		req.Slippage = defaultSlippage
	}

	// Validate assets exist and are active
	db := mobileDB(s.db)
	fromAsset, _, err := s.mobileAssetBySymbol(r.Context(), req.FromAsset)
	if err != nil && fromAsset == nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if fromAsset == nil || !fromAsset.Active {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ativo de origem inválido ou inativo"})
		return
	}
	toAsset, _, err := s.mobileAssetBySymbol(r.Context(), req.ToAsset)
	if err != nil && toAsset == nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if toAsset == nil || !toAsset.Active {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ativo de destino inválido ou inativo"})
		return
	}

	swap, err := db.CreateSwap(r.Context(), uid, req.FromAsset, req.ToAsset,
		req.Amount, req.Slippage, defaultSwapFeeBPS)
	if err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}

	// Publish event for SwapWorker to pick up
	if s.workers != nil {
		s.workers.Bus.Publish(workerEvent("swap.created", map[string]any{
			"swap_id":    swap.ID,
			"user_id":    uid,
			"from_asset": req.FromAsset,
			"to_asset":   req.ToAsset,
			"amount":     req.Amount,
		}))
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"swap":    swap,
		"message": "Swap em processamento. Acompanhe o status pelo ID.",
	})
}

// handleGetSwap — GET /api/mobile/swap/{id}
func (s *Server) handleGetSwap(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	id := r.PathValue("id")
	swap, err := mobileDB(s.db).GetSwap(r.Context(), id)
	if err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if swap == nil || swap.UserID != uid {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "swap não encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, swap)
}

// handleListSwaps — GET /api/mobile/swaps
func (s *Server) handleListSwaps(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	swaps, err := mobileDB(s.db).ListSwapsByUser(r.Context(), uid, 20)
	if err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if swaps == nil {
		swaps = make([]models.Swap, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{"swaps": swaps, "count": len(swaps)})
}
