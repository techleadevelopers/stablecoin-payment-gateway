package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
)

// handleMobileBuy — POST /api/mobile/order/buy
// Delegates to the existing POST /api/buy handler internally.
func (s *Server) handleMobileBuy(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		AmountBRL     float64 `json:"amount_brl"`
		Asset         string  `json:"asset"`
		DestAddress   string  `json:"dest_address"`
		PaymentMethod string  `json:"payment_method"` // "pix" | "card"
		CustomerName  string  `json:"customer_name"`
		CustomerEmail string  `json:"customer_email"`
		CustomerCPF   string  `json:"customer_cpf"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AmountBRL <= 0 || req.DestAddress == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_brl e dest_address obrigatórios"})
		return
	}
	if req.Asset == "" {
		req.Asset = "USDT"
	}
	if req.PaymentMethod == "" {
		req.PaymentMethod = "pix"
	}

	// Forward to existing /api/buy
	payload := map[string]any{
		"amountBRL":     req.AmountBRL,
		"asset":         req.Asset,
		"address":       req.DestAddress,
		"paymentMethod": req.PaymentMethod,
		"customer": map[string]any{
			"name":  req.CustomerName,
			"email": req.CustomerEmail,
			"cpf":   req.CustomerCPF,
		},
	}
	ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
	defer cancel()
	internalReq := r.Clone(ctx)
	resp, err := forwardToInternal(internalReq, "POST", s.internalBase(r)+"/api/buy", payload, s.internalAPIKey())
	if err != nil {
		if strings.EqualFold(req.PaymentMethod, "pix") {
			if s.writeDegradedMobileBuy(w, r, req.AmountBRL, req.Asset, req.DestAddress, req.PaymentMethod, req.CustomerEmail, "internal_request_failed") {
				return
			}
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "erro ao criar ordem: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 && strings.EqualFold(req.PaymentMethod, "pix") {
		if s.writeDegradedMobileBuy(w, r, req.AmountBRL, req.Asset, req.DestAddress, req.PaymentMethod, req.CustomerEmail, "payment_provider_unavailable") {
			return
		}
	}

	// Tag order with user_id if we got an id back
	var result map[string]any
	if json.Unmarshal(body, &result) == nil {
		if id, ok := result["id"].(string); ok && id != "" {
			_ = mobileDB(s.db).TagBuyOrderUser(r.Context(), id, uid)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (s *Server) writeDegradedMobileBuy(w http.ResponseWriter, r *http.Request, amountBRL float64, asset, destAddress, paymentMethod, customerEmail, reason string) bool {
	uid := userIDFromCtx(r)
	asset = strings.ToUpper(strings.TrimSpace(firstNonEmptyStr(asset, "USDT")))
	paymentMethod = strings.ToLower(strings.TrimSpace(firstNonEmptyStr(paymentMethod, "pix")))
	destAddress = strings.TrimSpace(destAddress)
	if paymentMethod != "pix" || amountBRL <= 0 || !looksLikeEVMAddress(destAddress) {
		return false
	}
	if min := s.mobileBuyMinBRL(); min > 0 && amountBRL < min {
		return false
	}
	if s != nil && s.cfg != nil && s.cfg.OrderMaxBrl > 0 && amountBRL > s.cfg.OrderMaxBrl {
		return false
	}
	marketRate := 0.0
	if pw := s.PriceCache(); pw != nil {
		marketRate = pw.GetPrice("BRL")
	}
	if marketRate <= 0 {
		return false
	}
	rate := s.mobileBuyRate(marketRate)
	fee, feeBreakdown := s.mobileBuyFee(amountBRL)
	totalFiat := roundMoney(amountBRL + fee)
	cryptoAmount := roundCrypto(amountBRL / rate)
	buy, err := s.db.CreateBuyOrder(r.Context(), database.BuyOrderInput{
		Status:            "payment_provider_pending",
		AmountBRL:         totalFiat,
		AmountFiat:        totalFiat,
		FiatCurrency:      "BRL",
		PaymentMethod:     "pix",
		RequestID:         mobileRequestID(r),
		FeeBRL:            fee,
		PayoutBRL:         amountBRL,
		CryptoAmount:      cryptoAmount,
		Asset:             asset,
		DestAddress:       destAddress,
		RateLocked:        rate,
		RateLockExpiresAt: time.Now().Add(time.Duration(s.mobileRateLockSec()) * time.Second),
		PixPayload: map[string]any{
			"provider":             "degraded",
			"providerUnavailable":  true,
			"paymentAvailable":     false,
			"requiresPaymentRetry": true,
			"reason":               reason,
			"message":              "Provedor de pagamento indisponivel; ordem criada para retentativa de cobranca.",
		},
		CustomerEmail: customerEmail,
	})
	if err != nil {
		slog.Warn("mobile_buy_degraded_create_failed", "err", err, "reason", reason)
		return false
	}
	_ = mobileDB(s.db).TagBuyOrderUser(r.Context(), buy.ID, uid)
	if s.workers != nil && s.workers.Bus != nil {
		s.workers.Bus.Publish(workerEvent("buy.payment_provider_pending", map[string]any{
			"orderId":       buy.ID,
			"requestId":     mobileRequestID(r),
			"amountFiat":    totalFiat,
			"fiatCurrency":  "BRL",
			"paymentMethod": "pix",
			"reason":        reason,
		}))
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"buyId":                buy.ID,
		"id":                   buy.ID,
		"accessToken":          buy.AccessToken,
		"status":               buy.Status,
		"paymentAvailable":     false,
		"requiresPaymentRetry": true,
		"amountFiat":           totalFiat,
		"subtotalFiat":         amountBRL,
		"fiatCurrency":         "BRL",
		"paymentMethod":        "pix",
		"feeFiat":              fee,
		"totalFiat":            totalFiat,
		"payoutFiat":           amountBRL,
		"rate":                 rate,
		"marketRate":           roundRateLocal(marketRate),
		"cryptoAmount":         cryptoAmount,
		"asset":                asset,
		"network":              "BSC",
		"destAddress":          destAddress,
		"feeBreakdown":         feeBreakdown,
		"payment": map[string]any{
			"provider":             "degraded",
			"paymentAvailable":     false,
			"requiresPaymentRetry": true,
			"reason":               reason,
		},
		"orderUrl":  fmt.Sprintf("/order/%s?accessToken=%s", buy.ID, buy.AccessToken),
		"statusUrl": fmt.Sprintf("/api/buy/%s?accessToken=%s", buy.ID, buy.AccessToken),
	})
	return true
}

// handleMobileSell — POST /api/mobile/order/sell
// Delegates to existing POST /api/order handler.
func (s *Server) handleMobileSell(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		AmountUSDT float64 `json:"amount_usdt"`
		PixKey     string  `json:"pix_key"`
		PixCpf     string  `json:"pix_cpf"`
		PixPhone   string  `json:"pix_phone"`
		Asset      string  `json:"asset"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AmountUSDT <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_usdt obrigatório"})
		return
	}
	pixKey := req.PixKey
	if pixKey == "" && req.PixPhone != "" {
		pixKey = req.PixPhone
	}
	if pixKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pix_key ou pix_phone obrigatório"})
		return
	}
	payload := map[string]any{
		"amountUSDT": req.AmountUSDT,
		"pixPhone":   pixKey,
		"pixCpf":     req.PixCpf,
		"asset":      firstNonEmptyStr(req.Asset, "USDT"),
	}
	resp, err := forwardToInternal(r, "POST", s.internalBase(r)+"/api/order", payload, s.internalAPIKey())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if json.Unmarshal(body, &result) == nil {
		if id, ok := result["id"].(string); ok && id != "" {
			_ = mobileDB(s.db).TagOrderUser(r.Context(), id, uid)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// handleMobileSwap — POST /api/mobile/order/swap
// Stub: swap = sell → buy. Returns instructions for two-leg swap.
func (s *Server) handleMobileSwap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FromAsset string  `json:"from_asset"`
		ToAsset   string  `json:"to_asset"`
		Amount    float64 `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "from_asset, to_asset e amount obrigatórios"})
		return
	}
	price := mobileAssetPriceBRL(s.PriceCache(), strings.ToUpper(firstNonEmptyStr(req.FromAsset, "USDT")))
	writeJSON(w, http.StatusAccepted, map[string]any{
		"type":       "swap",
		"from_asset": req.FromAsset,
		"to_asset":   req.ToAsset,
		"amount":     req.Amount,
		"rate":       price,
		"status":     "quote_only",
		"hint":       "Swap direto em andamento. Use sell + buy para executar agora.",
	})
}

// handleMobileGetOrder — GET /api/mobile/order/{id}
func (s *Server) handleMobileGetOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := forwardToInternal(r, "GET", s.internalBase(r)+"/api/order/"+id, nil, s.internalAPIKey())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// handleMobileListOrders — GET /api/mobile/orders
func (s *Server) handleMobileListOrders(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	orders, err := mobileDB(s.db).ListOrdersByUser(r.Context(), uid, 20)
	if err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": orders, "count": len(orders)})
}

// handleMobileCancelOrder — POST /api/mobile/order/cancel
func (s *Server) handleMobileCancelOrder(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		OrderID string `json:"order_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.OrderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "order_id obrigatório"})
		return
	}
	if err := mobileDB(s.db).CancelOrder(r.Context(), req.OrderID, uid); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func (s *Server) internalBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.Split(forwarded, ",")[0]
	}
	host := r.Host
	if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
		host = strings.Split(forwardedHost, ",")[0]
	}
	if host == "" {
		host = fmt.Sprintf("localhost:%s", s.cfg.Port)
	}
	return scheme + "://" + host
}

func (s *Server) internalAPIKey() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	if key := strings.TrimSpace(s.cfg.ChainFXLiveSecretKeys); key != "" {
		return key
	}
	return strings.TrimSpace(s.cfg.ChainFXTestSecretKeys)
}

func forwardToInternal(r *http.Request, method, url string, payload any, apiKey string) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		first := strings.Split(apiKey, ",")[0]
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(first))
	}
	return http.DefaultClient.Do(req)
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func looksLikeEVMAddress(address string) bool {
	address = strings.TrimSpace(address)
	if len(address) != 42 || !strings.HasPrefix(address, "0x") {
		return false
	}
	for _, ch := range address[2:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

func (s *Server) mobileBuyMinBRL() float64 {
	if s == nil || s.cfg == nil {
		return 0
	}
	if s.cfg.BuyTier1MinBrl > s.cfg.OrderMinBrl {
		return s.cfg.BuyTier1MinBrl
	}
	return s.cfg.OrderMinBrl
}

func (s *Server) mobileRateLockSec() int {
	if s == nil || s.cfg == nil || s.cfg.RateLockSec <= 0 {
		return 600
	}
	return s.cfg.RateLockSec
}

func (s *Server) mobileBuyRate(marketRate float64) float64 {
	if marketRate <= 0 {
		return 0
	}
	spreadBPS := 0
	if s != nil && s.cfg != nil && s.cfg.BuyRateSpreadBps > 0 {
		spreadBPS = s.cfg.BuyRateSpreadBps
	}
	return roundRateLocal(marketRate * (1 + float64(spreadBPS)/10000))
}

func (s *Server) mobileBuyFee(amountBRL float64) (float64, map[string]any) {
	if s == nil || s.cfg == nil {
		return 0, map[string]any{"tier": "none", "service_bps": 0, "network_fee_brl": 0, "min_fee_brl": 0}
	}
	bps := s.cfg.BuyTier3Bps
	tier := "tier3"
	if amountBRL < s.cfg.BuyTier1MaxBrl {
		bps = s.cfg.BuyTier1Bps
		tier = "tier1"
	} else if amountBRL < s.cfg.BuyTier2MaxBrl {
		bps = s.cfg.BuyTier2Bps
		tier = "tier2"
	}
	if bps == 0 {
		bps = s.cfg.FeeBps
		tier = "default"
	}
	serviceFee := roundMoney(amountBRL * float64(bps) / 10000)
	networkFee := roundMoney(s.cfg.BuyNetworkFeeBrl)
	totalFee := roundMoney(serviceFee + networkFee)
	minFee := roundMoney(s.cfg.BuyMinFeeBrl)
	if minFee <= 0 {
		minFee = roundMoney(s.cfg.FeeMinBrl)
	}
	if totalFee < minFee {
		totalFee = minFee
	}
	return totalFee, map[string]any{
		"tier":            tier,
		"service_bps":     bps,
		"service_fee_brl": serviceFee,
		"network_fee_brl": networkFee,
		"min_fee_brl":     minFee,
		"total_fee_brl":   totalFee,
	}
}

func mobileRequestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, header := range []string{"X-Request-Id", "X-Correlation-Id", "X-Trace-Id"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			return value
		}
	}
	return ""
}

func roundMoney(value float64) float64 {
	return math.Round(value*100) / 100
}

func roundCrypto(value float64) float64 {
	return math.Round(value*1e8) / 1e8
}

func roundRateLocal(value float64) float64 {
	return math.Round(value*1e6) / 1e6
}
