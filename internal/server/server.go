package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/email"
	"payment-gateway/internal/models"
	"payment-gateway/internal/privacy"
	"payment-gateway/internal/settlement"
	"payment-gateway/internal/workers"

	"github.com/ethereum/go-ethereum/common"
)

type Server struct {
	cfg     *config.Config
	db      *database.DB
	workers *workers.WorkerManager
	email   *email.Service
	limiter *rateLimiter
}

type requestIDContextKey struct{}

func New(cfg *config.Config, db *database.DB, workerMgr *workers.WorkerManager, mailer *email.Service) *Server {
	return &Server{
		cfg:     cfg,
		db:      db,
		workers: workerMgr,
		email:   mailer,
		limiter: newRateLimiter(cfg.OrderRateLimitWindowMs, cfg.OrderRateLimitMax),
	}
}

func (s *Server) transactionFee(amountFiat float64, fiatCurrency string, rate float64) float64 {
	percentFee := amountFiat * (float64(s.cfg.FeeBps) / 10000)
	fixedFee := s.cfg.FeeFixedUsd
	perUsdtFee := s.cfg.FeePerUsdtUsd * (amountFiat / rate)
	if strings.EqualFold(fiatCurrency, "BRL") {
		fixedFee = s.cfg.FeeFixedUsd * rate
		perUsdtFee = s.cfg.FeePerUsdtUsd * amountFiat
	}
	fee := percentFee + fixedFee + perUsdtFee
	if strings.EqualFold(fiatCurrency, "BRL") && s.cfg.FeeMinBrl > fee {
		fee = s.cfg.FeeMinBrl
	}
	return fee
}

func (s *Server) feePolicy(fiatCurrency string, rate float64) map[string]any {
	fixedFiat := s.cfg.FeeFixedUsd
	perUsdtFiat := s.cfg.FeePerUsdtUsd
	if strings.EqualFold(fiatCurrency, "BRL") {
		fixedFiat = s.cfg.FeeFixedUsd * rate
		perUsdtFiat = s.cfg.FeePerUsdtUsd * rate
	}
	return map[string]any{
		"bps":             s.cfg.FeeBps,
		"percent":         float64(s.cfg.FeeBps) / 100,
		"fixedUsd":        s.cfg.FeeFixedUsd,
		"fixedFiat":       fixedFiat,
		"perUsdtUsd":      s.cfg.FeePerUsdtUsd,
		"perUsdtFiat":     perUsdtFiat,
		"fiatCurrency":    strings.ToUpper(fiatCurrency),
		"description":     "2% + US$0.03 por USDT",
		"backendEnforced": true,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/price", s.handlePrice)
	mux.HandleFunc("GET /api/quote", s.handleQuote)
	mux.HandleFunc("POST /api/quote", s.handleQuote)
	mux.HandleFunc("POST /api/buy", s.handleCreateBuy)
	mux.HandleFunc("GET /api/buy/{id}", s.handleGetBuy)
	mux.HandleFunc("GET /api/buy/{id}/stream", s.handleBuyStream)
	mux.HandleFunc("POST /api/order", s.handleCreateOrder)
	mux.HandleFunc("GET /api/order/{id}", s.handleGetOrder)
	mux.HandleFunc("GET /api/order/{id}/stream", s.handleOrderStream)
	mux.HandleFunc("POST /api/order/{id}/deposit", s.handleDeposit)
	mux.HandleFunc("POST /api/order/{id}/payout", s.handlePayout)
	mux.HandleFunc("POST /api/pix/webhook", s.handlePixWebhook)
	mux.HandleFunc("POST /api/pix/webhook/buy", s.handlePixWebhookBuy)
	mux.HandleFunc("POST /api/stripe/webhook/buy", s.handleStripeWebhookBuy)
	mux.HandleFunc("POST /internal/sweep", s.handleInternalSweep)
	mux.HandleFunc("POST /internal/email/test", s.handleEmailTest)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /readyz", s.handleReady)
	return securityHeaders(cors(s.cfg, withRequestID(logRequests(mux))))
}

func (s *Server) handleCreateBuy(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.Allow("buy:" + clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "limite de criaÃ§Ã£o de compras excedido"})
		return
	}
	var req struct {
		AmountBRL         float64 `json:"amountBRL"`
		AmountUSD         float64 `json:"amountUSD"`
		AmountFiat        float64 `json:"amountFiat"`
		FiatCurrency      string  `json:"fiatCurrency"`
		PaymentMethod     string  `json:"paymentMethod"`
		ProviderPaymentID string  `json:"providerPaymentId"`
		Asset             string  `json:"asset"`
		Address           string  `json:"address"`
		PixCpf            string  `json:"pixCpf"`
		PixPhone          string  `json:"pixPhone"`
		CPF               string  `json:"cpf"`
		Phone             string  `json:"phone"`
		Email             string  `json:"email"`
		CustomerEmail     string  `json:"customerEmail"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invÃ¡lido"})
		return
	}
	asset := strings.ToUpper(defaultString(req.Asset, "USDT"))
	if asset != "USDT" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "asset nÃ£o suportado nesta fase (apenas USDT)"})
		return
	}
	deliveryNetwork := s.deliveryNetwork()
	if !s.isDeliveryAddress(req.Address) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("endereco %s invalido", deliveryNetwork)})
		return
	}
	fiatCurrency, paymentMethod, amountFiat := normalizePaymentRail(req.FiatCurrency, req.PaymentMethod, req.AmountFiat, req.AmountBRL, req.AmountUSD)
	if fiatCurrency == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rail de pagamento nao suportado"})
		return
	}
	if amountFiat <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amountFiat deve ser maior que zero"})
		return
	}
	if fiatCurrency == "BRL" && (amountFiat < s.cfg.OrderMinBrl || amountFiat > s.cfg.OrderMaxBrl) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("valor fora dos limites (%.2f - %.2f BRL)", s.cfg.OrderMinBrl, s.cfg.OrderMaxBrl)})
		return
	}
	rate := s.workers.PriceWorker.GetPrice(fiatCurrency)
	if rate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cotacao ainda nao carregada"})
		return
	}
	fee := s.transactionFee(amountFiat, fiatCurrency, rate)
	payout := amountFiat
	if payout <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "valor insuficiente apÃ³s taxa"})
		return
	}
	totalFiat := amountFiat + fee
	cryptoAmount := payout / rate
	buyID := database.NewID()
	paymentPayload, err := s.createPaymentIntent(r.Context(), buyID, totalFiat, fiatCurrency, paymentMethod)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	customerAudit := buildCustomerAudit(s.cfg.LGPDSecret,
		firstNonEmpty(req.PixCpf, req.CPF),
		firstNonEmpty(req.PixPhone, req.Phone),
		firstNonEmpty(req.Email, req.CustomerEmail),
	)
	if len(customerAudit) > 0 {
		paymentPayload["customer"] = customerAudit
	}
	amountBRL := totalFiat
	status := "aguardando_" + paymentMethod
	buy, err := s.db.CreateBuyOrder(r.Context(), database.BuyOrderInput{
		ID:                buyID,
		Status:            status,
		AmountBRL:         amountBRL,
		AmountFiat:        totalFiat,
		FiatCurrency:      fiatCurrency,
		PaymentMethod:     paymentMethod,
		ProviderPaymentID: req.ProviderPaymentID,
		RequestID:         requestID(r),
		FeeBRL:            fee,
		PayoutBRL:         payout,
		CryptoAmount:      cryptoAmount,
		Asset:             asset,
		DestAddress:       strings.TrimSpace(req.Address),
		RateLocked:        rate,
		RateLockExpiresAt: time.Now().Add(time.Duration(s.cfg.RateLockSec) * time.Second),
		PixPayload:        paymentPayload,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	_ = s.db.AddBuyEvent(r.Context(), buy.ID, "buy.meta", map[string]any{"requestId": requestID(r), "ip": clientIP(r), "userAgent": r.UserAgent(), "customer": customerAudit})
	s.workers.Bus.Publish(workers.Event{Type: "buy.created", OrderID: buy.ID, Payload: map[string]any{"requestId": requestID(r), "amountFiat": totalFiat, "fiatCurrency": fiatCurrency, "paymentMethod": paymentMethod}})
	writeJSON(w, http.StatusCreated, map[string]any{
		"buyId": buy.ID, "id": buy.ID, "accessToken": buy.AccessToken, "status": buy.Status, "amountFiat": totalFiat, "subtotalFiat": amountFiat, "fiatCurrency": fiatCurrency, "paymentMethod": paymentMethod, "feeFiat": fee, "totalFiat": totalFiat, "payoutFiat": payout,
		"rate": rate, "cryptoAmount": cryptoAmount, "asset": asset, "network": deliveryNetwork, "destAddress": buy.DestAddress,
		"feePolicy": s.feePolicy(fiatCurrency, rate),
		"pixKey": paymentPayload["pixKey"], "qrCodeUrl": paymentPayload["qrCodeUrl"], "payment": paymentPayload,
		"statusUrl": fmt.Sprintf("/api/buy/%s?accessToken=%s", buy.ID, buy.AccessToken),
		"streamUrl": fmt.Sprintf("/api/buy/%s/stream?accessToken=%s", buy.ID, buy.AccessToken),
	})
}

func (s *Server) handleGetBuy(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeBuyRead(w, r, r.PathValue("id")) {
		return
	}
	buy, err := s.db.GetBuyOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if buy == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "compra nÃ£o encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, buy)
}

func (s *Server) handleBuyStream(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeBuyRead(w, r, r.PathValue("id")) {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var last string
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			buy, _ := s.db.GetBuyOrder(r.Context(), r.PathValue("id"))
			if buy == nil {
				continue
			}
			if buy.Status != last {
				last = buy.Status
				raw, _ := json.Marshal(map[string]any{"status": buy.Status, "txHash": buy.TxHashOut})
				_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}
}

func (s *Server) authorizeBuyRead(w http.ResponseWriter, r *http.Request, id string) bool {
	ok, err := s.db.ValidateBuyAccess(r.Context(), id, customerAccessToken(r))
	if err != nil {
		writeError(w, err)
		return false
	}
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token de acesso invalido"})
		return false
	}
	return true
}

func (s *Server) handlePrice(w http.ResponseWriter, r *http.Request) {
	price := s.workers.PriceWorker.GetCurrentPrice()
	if price <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "preço ainda não carregado"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"brl":     price,
		"usd":     s.workers.PriceWorker.GetPrice("USD"),
		"eur":     s.workers.PriceWorker.GetPrice("EUR"),
		"usdtbrl": s.workers.PriceWorker.GetPrice("USDTBRL"),
		"eurusd":  s.workers.PriceWorker.GetPrice("EURUSD"),
		"btcusdt": s.workers.PriceWorker.GetPrice("BTCUSDT"),
	})
}

func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	var amountBRL, amountUSD, amountFiat float64
	mode := "buy"
	asset := "USDT"
	fiatCurrency := "BRL"
	paymentMethod := "pix"
	if r.Method == http.MethodGet {
		amountBRL, _ = strconv.ParseFloat(r.URL.Query().Get("amountBRL"), 64)
		amountUSD, _ = strconv.ParseFloat(r.URL.Query().Get("amountUSD"), 64)
		amountFiat, _ = strconv.ParseFloat(r.URL.Query().Get("amountFiat"), 64)
		mode = defaultString(r.URL.Query().Get("mode"), mode)
		asset = defaultString(r.URL.Query().Get("asset"), asset)
		fiatCurrency = defaultString(r.URL.Query().Get("fiatCurrency"), fiatCurrency)
		paymentMethod = defaultString(r.URL.Query().Get("paymentMethod"), paymentMethod)
	} else {
		var req struct {
			AmountBRL     float64 `json:"amountBRL"`
			AmountUSD     float64 `json:"amountUSD"`
			AmountFiat    float64 `json:"amountFiat"`
			FiatCurrency  string  `json:"fiatCurrency"`
			PaymentMethod string  `json:"paymentMethod"`
			Mode          string  `json:"mode"`
			Asset         string  `json:"asset"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
			return
		}
		amountBRL = req.AmountBRL
		amountUSD = req.AmountUSD
		amountFiat = req.AmountFiat
		mode = defaultString(req.Mode, mode)
		asset = defaultString(req.Asset, asset)
		fiatCurrency = defaultString(req.FiatCurrency, fiatCurrency)
		paymentMethod = defaultString(req.PaymentMethod, paymentMethod)
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if mode != "buy" && mode != "sell" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "modo invalido"})
		return
	}
	if asset != "USDT" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "asset nao suportado nesta fase"})
		return
	}
	fiatCurrency, paymentMethod, amountFiat = normalizePaymentRail(fiatCurrency, paymentMethod, amountFiat, amountBRL, amountUSD)
	if fiatCurrency == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rail de pagamento nao suportado"})
		return
	}
	if amountFiat <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amountFiat deve ser maior que zero"})
		return
	}
	if fiatCurrency == "BRL" && (amountFiat < s.cfg.OrderMinBrl || amountFiat > s.cfg.OrderMaxBrl) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("valor fora dos limites (%.2f - %.2f BRL)", s.cfg.OrderMinBrl, s.cfg.OrderMaxBrl)})
		return
	}
	rate := s.workers.PriceWorker.GetPrice(fiatCurrency)
	if rate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cotacao ainda nao carregada"})
		return
	}
	fee := s.transactionFee(amountFiat, fiatCurrency, rate)
	payout := amountFiat
	if payout <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "valor insuficiente apos taxa"})
		return
	}
	totalFiat := amountFiat + fee
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":              mode,
		"asset":             asset,
		"amountFiat":        totalFiat,
		"subtotalFiat":      amountFiat,
		"fiatCurrency":      fiatCurrency,
		"paymentMethod":     paymentMethod,
		"feeFiat":           fee,
		"totalFiat":         totalFiat,
		"payoutFiat":        payout,
		"feePolicy":         s.feePolicy(fiatCurrency, rate),
		"rate":              rate,
		"cryptoAmount":      payout / rate,
		"rateLockExpiresAt": time.Now().Add(time.Duration(s.cfg.RateLockSec) * time.Second),
	})
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.Allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "limite de criação de ordens excedido"})
		return
	}
	var req struct {
		AmountBRL float64 `json:"amountBRL"`
		Address   string  `json:"address"`
		Network   string  `json:"network"`
		Asset     string  `json:"asset"`
		PixCpf    string  `json:"pixCpf"`
		PixPhone  string  `json:"pixPhone"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON inválido"})
		return
	}
	if req.PixCpf == "" && req.PixPhone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pixCpf ou pixPhone e obrigatorio"})
		return
	}
	if req.AmountBRL < s.cfg.OrderMinBrl || req.AmountBRL > s.cfg.OrderMaxBrl {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("valor fora dos limites (%.2f - %.2f BRL)", s.cfg.OrderMinBrl, s.cfg.OrderMaxBrl)})
		return
	}
	network := strings.ToUpper(defaultString(req.Network, "BSC"))
	asset := strings.ToUpper(defaultString(req.Asset, "USDT"))
	if network != "BSC" || asset != "USDT" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "somente pedidos BSC/USDT sao suportados"})
		return
	}
	ctx := r.Context()
	stats, err := s.db.StatsPixLast24h(ctx, req.PixCpf, req.PixPhone)
	if err != nil {
		writeError(w, err)
		return
	}
	if stats.Count >= s.cfg.PixMaxOrdersPer24h || stats.Total+req.AmountBRL > s.cfg.PixMaxBrlPer24h {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "limite diario por chave PIX excedido"})
		return
	}

	var idx *int
	depositAddress := strings.TrimSpace(req.Address)
	if depositAddress == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endereco BSC de deposito obrigatorio"})
		return
	}
	if !common.IsHexAddress(depositAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endereco BSC invalido"})
		return
	}

	rate := s.workers.PriceWorker.GetCurrentPrice()
	if rate <= 0 {
		rate = 5.0
	}
	fee := s.transactionFee(req.AmountBRL, "BRL", rate)
	payout := req.AmountBRL
	if payout <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "valor insuficiente após taxa"})
		return
	}
	totalBRL := req.AmountBRL + fee
	amountUSDT := payout / rate
	order, err := s.db.CreateOrder(ctx, database.OrderInput{
		Status:            string(models.StatusAguardandoDeposito),
		AmountBRL:         totalBRL,
		AmountUSDT:        amountUSDT,
		FeeBRL:            fee,
		PayoutBRL:         payout,
		Address:           depositAddress,
		Asset:             asset,
		Network:           network,
		RateLocked:        rate,
		RateLockExpiresAt: time.Now().Add(time.Duration(s.cfg.RateLockSec) * time.Second),
		RequestID:         requestID(r),
		PixCpf:            req.PixCpf,
		PixPhone:          req.PixPhone,
		DerivationIndex:   idx,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	_ = s.db.AddEvent(ctx, order.ID, "order.meta", map[string]any{"requestId": requestID(r), "ip": clientIP(r), "userAgent": r.UserAgent()})
	s.workers.Bus.Publish(workers.Event{Type: "order.created", OrderID: order.ID, Payload: map[string]any{"requestId": requestID(r), "amountBRL": totalBRL}})
	s.email.NotifyOps("Swappy: nova ordem criada", fmt.Sprintf("Ordem %s criada para %.2f BRL. Endereço: %s", order.ID, totalBRL, depositAddress))
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": order.ID, "orderId": order.ID, "accessToken": order.AccessToken, "status": order.Status, "address": depositAddress, "depositAddress": depositAddress,
		"amountBRL": totalBRL, "subtotalBRL": req.AmountBRL, "amountUSDT": amountUSDT, "btcAmount": amountUSDT, "feeBRL": fee, "totalBRL": totalBRL, "payoutBRL": payout,
		"rate": rate, "network": network, "feePolicy": s.feePolicy("BRL", rate),
		"statusUrl": fmt.Sprintf("/api/order/%s?accessToken=%s", order.ID, order.AccessToken),
		"streamUrl": fmt.Sprintf("/api/order/%s/stream?accessToken=%s", order.ID, order.AccessToken),
	})
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeOrderRead(w, r, r.PathValue("id")) {
		return
	}
	order, err := s.db.GetOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if order == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "ordem não encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (s *Server) handleOrderStream(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeOrderRead(w, r, r.PathValue("id")) {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var last string
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			order, _ := s.db.GetOrder(r.Context(), r.PathValue("id"))
			if order == nil {
				continue
			}
			status := string(order.Status)
			if status != last {
				last = status
				raw, _ := json.Marshal(map[string]any{"status": status, "txHash": order.TxHash})
				_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}
}

func (s *Server) authorizeOrderRead(w http.ResponseWriter, r *http.Request, id string) bool {
	ok, err := s.db.ValidateOrderAccess(r.Context(), id, customerAccessToken(r))
	if err != nil {
		writeError(w, err)
		return false
	}
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token de acesso invalido"})
		return false
	}
	return true
}

func (s *Server) handleDeposit(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura inválida"})
		return
	}
	var req struct {
		TxHash string  `json:"txHash"`
		Amount float64 `json:"amount"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.TxHash == "" || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	id := r.PathValue("id")
	if idem := r.Header.Get("x-idempotency-key"); idem != "" {
		exists, _ := s.db.HasEvent(r.Context(), id, "idempotency", "key", idem)
		if exists {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
			return
		}
		_ = s.db.AddEvent(r.Context(), id, "idempotency", map[string]any{"requestId": requestID(r), "key": idem, "endpoint": "deposit"})
	}
	order, err := s.db.GetOrder(r.Context(), id)
	if err != nil || order == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "ordem não encontrada"})
		return
	}
	if order.Status != models.StatusAguardandoDeposito {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "status atual não permite depósito"})
		return
	}
	if err := s.db.UpdateOrderStatus(r.Context(), id, "pago", map[string]any{"requestId": requestID(r), "depositTx": req.TxHash, "depositAmount": req.Amount}); err != nil {
		writeError(w, err)
		return
	}
	s.workers.Bus.Publish(workers.Event{Type: "onchain.detected", OrderID: id, Payload: map[string]any{"tx_hash": req.TxHash, "amount_usdt": req.Amount}})
	s.workers.Bus.Publish(workers.Event{Type: "payout.requested", OrderID: id})
	s.email.NotifyOps("Swappy: depósito detectado", fmt.Sprintf("Ordem %s recebeu depósito %s no valor %.8f USDT.", id, req.TxHash, req.Amount))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePayout(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura inválida"})
		return
	}
	var req struct {
		ProviderID string `json:"providerId"`
		Status     string `json:"status"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.ProviderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	status := "erro"
	extra := map[string]any{"requestId": requestID(r), "error": req.Error}
	if strings.HasPrefix(strings.ToLower(req.Status), "conclu") {
		status = "concluida"
		extra = map[string]any{"requestId": requestID(r), "txHash": req.ProviderID}
	}
	if err := s.db.UpdateOrderStatus(r.Context(), r.PathValue("id"), status, extra); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePixWebhook(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	secret := defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret)
	if !validHMAC(secret, raw, r.Header.Get("x-pagbank-signature")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura inválida"})
		return
	}
	var req struct {
		OrderID    string `json:"orderId"`
		Status     string `json:"status"`
		ProviderID string `json:"providerId"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.OrderID == "" || req.ProviderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	status := "erro"
	extra := map[string]any{"requestId": requestID(r), "error": req.Error}
	if strings.HasPrefix(strings.ToLower(req.Status), "conclu") {
		status = "concluida"
		extra = map[string]any{"requestId": requestID(r), "txHash": req.ProviderID}
	}
	if err := s.db.UpdateOrderStatus(r.Context(), req.OrderID, status, extra); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePixWebhookBuy(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	secret := defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret)
	signature := r.Header.Get("x-pagbank-signature")
	if !validHMAC(secret, raw, signature) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invÃ¡lida"})
		return
	}
	var req struct {
		BuyID      string `json:"buyId"`
		Status     string `json:"status"`
		ProviderID string `json:"providerId"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.BuyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invÃ¡lido"})
		return
	}
	status := settlement.PixWebhookStatus(req.Status)
	extra := map[string]any{"requestId": requestID(r), "error": req.Error}
	if settlement.ShouldPublishBuyPaid(status) {
		extra = map[string]any{"requestId": requestID(r), "providerPaymentId": req.ProviderID}
	}
	duplicate, err := s.db.ApplyBuyProviderWebhook(r.Context(), req.BuyID, req.ProviderID, req.Status, status, extra)
	if err != nil {
		writeError(w, err)
		return
	}
	if duplicate {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
		return
	}
	if settlement.ShouldPublishBuyPaid(status) {
		s.workers.Bus.Publish(workers.Event{Type: "buy.paid", OrderID: req.BuyID, Payload: map[string]any{"providerId": req.ProviderID}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStripeWebhookBuy(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	secret := defaultString(s.cfg.StripeWebhookSecret, s.cfg.WebhookSecret)
	if !validStripeSignature(secret, raw, r.Header.Get("Stripe-Signature"), 5*time.Minute) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invalida"})
		return
	}
	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object map[string]any `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &event); err != nil || event.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	buyID := nestedString(event.Data.Object, "metadata", "buyId")
	if buyID == "" {
		buyID = nestedString(event.Data.Object, "metadata", "buy_id")
	}
	if buyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "metadata.buyId obrigatorio"})
		return
	}
	status := settlement.StripeWebhookStatus(event.Type)
	extra := map[string]any{"requestId": requestID(r), "providerPaymentId": event.ID, "stripeEventType": event.Type}
	if !settlement.ShouldPublishBuyPaid(status) {
		extra["error"] = "stripe event nao liquidado: " + event.Type
	}
	duplicate, err := s.db.ApplyBuyProviderWebhook(r.Context(), buyID, event.ID, event.Type, status, extra)
	if err != nil {
		writeError(w, err)
		return
	}
	if duplicate {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
		return
	}
	if settlement.ShouldPublishBuyPaid(status) {
		s.workers.Bus.Publish(workers.Event{Type: "buy.paid", OrderID: buyID, Payload: map[string]any{"providerId": event.ID, "rail": "stripe"}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleInternalSweep(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura inválida"})
		return
	}
	var req struct {
		ChildIndex int     `json:"childIndex"`
		ToAddr     string  `json:"toAddr"`
		Amount     float64 `json:"amount"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.Amount <= 0 || req.ToAddr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	sweep, err := s.db.CreateSweep(r.Context(), req.ChildIndex, s.cfg.TreasuryHot, req.ToAddr, req.Amount, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "sweepId": sweep.ID})
}

func (s *Server) handleEmailTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON inválido"})
		return
	}
	if req.Subject == "" {
		req.Subject = "Swappy Financial - teste SMTP"
	}
	if req.Body == "" {
		req.Body = "Serviço de email operacional ativo."
	}
	if err := s.email.Send(email.Message{To: req.To, Subject: req.Subject, Body: req.Body}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "db": false, "error": err.Error()})
		return
	}
	gaps := s.operationalGaps()
	status := http.StatusOK
	if s.cfg.IsProduction() && len(gaps) > 0 {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{
		"ok":       len(gaps) == 0,
		"db":       true,
		"network":  s.deliveryNetwork(),
		"bsc":      s.cfg.BscRpcUrls != "" && s.cfg.BscUsdtContract != "",
		"pix":      s.cfg.PagSeguroApiToken != "" && defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret) != "",
		"stripe":   defaultString(s.cfg.StripeWebhookSecret, s.cfg.WebhookSecret) != "",
		"signer":   s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		"mode":     s.cfg.Environment,
		"warnings": gaps,
	})
}

func (s *Server) operationalGaps() []string {
	checks := map[string]bool{
		"pix_provider":   s.cfg.PagSeguroApiToken != "",
		"pix_webhook":    defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret) != "",
		"stripe_webhook": defaultString(s.cfg.StripeWebhookSecret, s.cfg.WebhookSecret) != "",
		"signer":         s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		"lgpd_secret":    s.cfg.LGPDSecret != "",
		"no_simulations": !s.cfg.AllowSimulations,
		"sweep_not_stub": !s.cfg.EnableSweepStub,
		"treasury_hot":   s.cfg.TreasuryHot != "",
	}
	if strings.EqualFold(s.cfg.SignerNetwork, "bsc") || strings.EqualFold(s.cfg.SignerNetwork, "evm") {
		checks["signer_bsc"] = true
		checks["bsc_contract"] = s.cfg.BscUsdtContract != ""
		checks["bsc_rpc_urls"] = s.cfg.BscRpcUrls != ""
	}
	var gaps []string
	for name, ok := range checks {
		if !ok {
			gaps = append(gaps, name)
		}
	}
	return gaps
}

func decodeJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dest)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, err error) {
	slog.Error("Erro HTTP", "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
}

func validHMAC(secret string, raw []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature), []byte(expected))
}

func validStripeSignature(secret string, raw []byte, header string, tolerance time.Duration) bool {
	if secret == "" || header == "" {
		return false
	}
	var timestamp, signature string
	for _, part := range strings.Split(header, ",") {
		keyValue := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(keyValue) != 2 {
			continue
		}
		switch keyValue[0] {
		case "t":
			timestamp = keyValue[1]
		case "v1":
			signature = keyValue[1]
		}
	}
	if timestamp == "" || signature == "" {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if tolerance > 0 {
		diff := time.Since(time.Unix(ts, 0))
		if diff < 0 {
			diff = -diff
		}
		if diff > tolerance {
			return false
		}
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(raw)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature), []byte(expected))
}

func cors(cfg *config.Config, next http.Handler) http.Handler {
	allowed := strings.Split(cfg.AllowedOrigins, ",")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		for _, item := range allowed {
			item = strings.TrimSpace(item)
			if item == "*" || item == origin || (origin == "" && item != "") {
				w.Header().Set("Access-Control-Allow-Origin", defaultString(origin, item))
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, x-internal-hmac, x-idempotency-key, x-pagbank-signature, Stripe-Signature")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				break
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("http_request", "request_id", requestID(r), "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	})
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = database.NewID()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestID(r *http.Request) string {
	if value, ok := r.Context().Value(requestIDContextKey{}).(string); ok {
		return value
	}
	return ""
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func customerAccessToken(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-Customer-Access-Token")); token != "" {
		return token
	}
	if token := strings.TrimSpace(r.URL.Query().Get("accessToken")); token != "" {
		return token
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

func (s *Server) deliveryNetwork() string {
	network := strings.ToUpper(strings.TrimSpace(s.cfg.SignerNetwork))
	switch network {
	case "", "EVM", "BINANCE", "BEP20":
		return "BSC"
	default:
		return network
	}
}

func (s *Server) isDeliveryAddress(address string) bool {
	address = strings.TrimSpace(address)
	switch s.deliveryNetwork() {
	case "BSC", "EVM":
		return common.IsHexAddress(address)
	default:
		return false
	}
}

func normalizePaymentRail(currency, method string, amountFiat, amountBRL, amountUSD float64) (string, string, float64) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	method = strings.ToLower(strings.TrimSpace(method))
	if currency == "" {
		if amountUSD > 0 {
			currency = "USD"
		} else {
			currency = "BRL"
		}
	}
	if method == "" {
		if currency == "USD" {
			method = "stripe"
		} else {
			method = "pix"
		}
	}
	if amountFiat <= 0 {
		if currency == "USD" {
			amountFiat = amountUSD
		} else {
			amountFiat = amountBRL
		}
	}
	switch {
	case currency == "BRL" && method == "pix":
		return currency, method, amountFiat
	case currency == "USD" && method == "stripe":
		return currency, method, amountFiat
	default:
		return "", "", 0
	}
}

func (s *Server) createPaymentIntent(ctx context.Context, buyID string, amountFiat float64, fiatCurrency, paymentMethod string) (map[string]any, error) {
	if paymentMethod == "stripe" {
		return map[string]any{
			"provider":     "stripe",
			"status":       "requires_provider_checkout",
			"buyId":        buyID,
			"amount":       amountFiat,
			"currency":     fiatCurrency,
			"instructions": "create Stripe Checkout/PaymentIntent client-side or upstream and include metadata.buyId",
		}, nil
	}
	if paymentMethod != "pix" {
		return nil, fmt.Errorf("rail de pagamento nao suportado")
	}
	if strings.TrimSpace(s.cfg.PagSeguroApiToken) == "" {
		if s.cfg.AllowSimulations && !s.cfg.IsProduction() {
			return map[string]any{"provider": "pix", "mode": "simulation", "pixKey": "chavepix@nexswap.com", "qrCodeUrl": "/images/qrcode.png", "buyId": buyID}, nil
		}
		return nil, fmt.Errorf("PAGSEGURO_API_TOKEN nao configurado")
	}
	payload := map[string]any{
		"reference_id": buyID,
		"customer":     map[string]any{"name": "Swappy Customer"},
		"qr_codes": []map[string]any{{
			"amount": map[string]any{"value": int64(math.Round(amountFiat * 100))},
		}},
		"notification_urls": []string{},
	}
	raw, _ := json.Marshal(payload)
	endpoint := strings.TrimRight(s.cfg.PagSeguroApiBaseUrl, "/") + "/" + strings.TrimLeft(defaultString(s.cfg.PixChargeEndpoint, "/orders"), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.PagSeguroApiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("PagBank rejeitou cobranca PIX: status %d", resp.StatusCode)
	}
	var provider map[string]any
	if err := json.Unmarshal(body, &provider); err != nil {
		return nil, fmt.Errorf("PagBank respondeu JSON invalido")
	}
	provider["provider"] = "pix"
	provider["buyId"] = buyID
	provider["providerStatus"] = resp.StatusCode
	return provider, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func nestedString(root map[string]any, keys ...string) string {
	var current any = root
	for _, key := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = obj[key]
	}
	if value, ok := current.(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func buildCustomerAudit(secret, cpf, phone, email string) map[string]any {
	customer := make(map[string]any)
	if hash := privacy.Hash(cpf, secret); hash != "" {
		customer["cpfHash"] = hash
	}
	if hash := privacy.Hash(phone, secret); hash != "" {
		customer["phoneHash"] = hash
	}
	if hash := privacy.Hash(email, secret); hash != "" {
		customer["emailHash"] = hash
	}
	return customer
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	return r.RemoteAddr
}

type rateLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	max      int
	counters map[string]rateBucket
}

type rateBucket struct {
	ResetAt time.Time
	Count   int
}

func newRateLimiter(windowMs, max int) *rateLimiter {
	if windowMs <= 0 {
		windowMs = 60000
	}
	if max <= 0 {
		max = 20
	}
	return &rateLimiter{window: time.Duration(windowMs) * time.Millisecond, max: max, counters: make(map[string]rateBucket)}
}

func (l *rateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.counters[key]
	if b.ResetAt.IsZero() || now.After(b.ResetAt) {
		b = rateBucket{ResetAt: now.Add(l.window)}
	}
	b.Count++
	l.counters[key] = b
	return b.Count <= l.max
}
