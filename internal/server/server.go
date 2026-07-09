package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
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
	"golang.org/x/crypto/pkcs12"
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
	mux.HandleFunc("GET /developers", s.handleDevelopers)
	mux.HandleFunc("GET /developers/dashboard", s.handleDevelopersDashboard)
	mux.HandleFunc("GET /developers/api-keys", s.handleDeveloperAPIKeys)
	mux.HandleFunc("GET /developers/logs", s.handleDeveloperLogs)
	mux.HandleFunc("GET /openapi.json", s.handleOpenAPI)
	mux.HandleFunc("GET /rates", s.handleChainFXRates)
	mux.HandleFunc("POST /quote", s.handleChainFXQuote)
	mux.HandleFunc("POST /buy", s.handleChainFXBuy)
	mux.HandleFunc("POST /sell", s.handleChainFXSell)
	mux.HandleFunc("GET /order/{id}", s.handleChainFXOrder)
	mux.HandleFunc("POST /webhooks/test", s.handleChainFXWebhookTest)
	mux.HandleFunc("POST /webhooks/retry", s.handleChainFXWebhookRetry)
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
		CustomerName      string  `json:"customerName"`
		Name              string  `json:"name"`
		BirthDate         string  `json:"birthDate"`
		Customer          struct {
			Name      string         `json:"name"`
			Email     string         `json:"email"`
			CPF       string         `json:"cpf"`
			Phone     string         `json:"phone"`
			BirthDate string         `json:"birthDate"`
			Address   map[string]any `json:"address"`
		} `json:"customer"`
		AddressPayload map[string]any `json:"addressPayload"`
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
	customerInput := paymentCustomerInput{
		Name:      firstNonEmpty(req.Customer.Name, req.CustomerName, req.Name),
		Email:     firstNonEmpty(req.Customer.Email, req.Email, req.CustomerEmail),
		CPF:       firstNonEmpty(req.Customer.CPF, req.PixCpf, req.CPF),
		Phone:     firstNonEmpty(req.Customer.Phone, req.PixPhone, req.Phone),
		BirthDate: firstNonEmpty(req.Customer.BirthDate, req.BirthDate),
		Address:   firstNonNilMap(req.Customer.Address, req.AddressPayload),
	}
	paymentPayload, err := s.createPaymentIntent(r.Context(), buyID, totalFiat, fiatCurrency, paymentMethod, customerInput)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	customerAudit := buildCustomerAudit(s.cfg.LGPDSecret,
		customerInput.CPF,
		customerInput.Phone,
		customerInput.Email,
		customerInput.Name,
		customerInput.BirthDate,
		customerInput.Address,
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
		ProviderPaymentID: firstNonEmpty(req.ProviderPaymentID, mapString(paymentPayload, "providerPaymentId"), mapString(paymentPayload, "txid")),
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
		"pixKey":    paymentPayload["pixKey"], "qrCodeUrl": paymentPayload["qrCodeUrl"], "payment": paymentPayload,
		"orderUrl":  fmt.Sprintf("/order/%s?accessToken=%s", buy.ID, buy.AccessToken),
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

// ChainFX Phase 1 exposes the infrastructure API without changing the legacy /api surface.
func (s *Server) handleChainFXRates(w http.ResponseWriter, r *http.Request) {
	price := s.workers.PriceWorker.GetCurrentPrice()
	if price <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "rates are not loaded yet"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"brand":       "ChainFX",
		"category":    "Digital FX Payments Infrastructure",
		"description": "Accept PIX. Deliver digital dollars. Receive stablecoins. Pay out PIX.",
		"base":        "USDT",
		"rates": map[string]float64{
			"USDT_BRL": s.workers.PriceWorker.GetPrice("BRL"),
			"USDT_USD": s.workers.PriceWorker.GetPrice("USD"),
			"USDT_EUR": s.workers.PriceWorker.GetPrice("EUR"),
			"BTC_USDT": s.workers.PriceWorker.GetPrice("BTCUSDT"),
			"EUR_USD":  s.workers.PriceWorker.GetPrice("EURUSD"),
		},
		"supportedAssets": []string{"USDT"},
		"roadmapAssets":   []string{"EURUSDT", "BTC"},
		"supportedFiat":   []string{"BRL", "USD"},
		"rails": map[string][]string{
			"buy":  {"pix", "stripe"},
			"sell": {"pix"},
		},
		"sandbox": map[string]any{
			"baseUrl":        "https://sandbox-api.chainfx.com",
			"defaultTestKey": "sk_test_chainfx_local",
			"features":       []string{"fake PIX", "fake QR", "fake wallet", "simulated webhooks", "test orders"},
		},
		"timestamp": time.Now().UTC(),
	})
}

func (s *Server) handleChainFXQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Side          string  `json:"side"`
		Fiat          string  `json:"fiat"`
		Asset         string  `json:"asset"`
		Amount        float64 `json:"amount"`
		PaymentMethod string  `json:"paymentMethod"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	side := strings.ToLower(defaultString(req.Side, "buy"))
	asset := strings.ToUpper(defaultString(req.Asset, "USDT"))
	if side != "buy" && side != "sell" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "side must be buy or sell"})
		return
	}
	if asset != "USDT" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "asset not supported in phase 1", "supportedAssets": []string{"USDT"}})
		return
	}
	fiatCurrency, paymentMethod, amountFiat := normalizePaymentRail(req.Fiat, req.PaymentMethod, req.Amount, 0, 0)
	if side == "sell" {
		fiatCurrency, paymentMethod = "BRL", "pix"
	}
	if fiatCurrency == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payment rail not supported"})
		return
	}
	if amountFiat <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount must be greater than zero"})
		return
	}
	if fiatCurrency == "BRL" && (amountFiat < s.cfg.OrderMinBrl || amountFiat > s.cfg.OrderMaxBrl) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("amount outside limits (%.2f - %.2f BRL)", s.cfg.OrderMinBrl, s.cfg.OrderMaxBrl)})
		return
	}
	rate := s.workers.PriceWorker.GetPrice(fiatCurrency)
	if rate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "rates are not loaded yet"})
		return
	}
	fee := s.transactionFee(amountFiat, fiatCurrency, rate)
	expiresAt := time.Now().Add(time.Duration(s.cfg.RateLockSec) * time.Second).UTC()
	writeJSON(w, http.StatusOK, map[string]any{
		"quoteId":      "qt_" + strings.ReplaceAll(database.NewID(), "-", ""),
		"side":         side,
		"fiat":         fiatCurrency,
		"asset":        asset,
		"rate":         rate,
		"fiatAmount":   amountFiat,
		"feeFiat":      fee,
		"totalFiat":    amountFiat + fee,
		"cryptoAmount": amountFiat / rate,
		"paymentRail":  paymentMethod,
		"expiresAt":    expiresAt,
		"sandbox":      s.chainFXAuthContext(r).Sandbox,
	})
}

func (s *Server) handleChainFXBuy(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	var req struct {
		QuoteID       string  `json:"quoteId"`
		Fiat          string  `json:"fiat"`
		Asset         string  `json:"asset"`
		Amount        float64 `json:"amount"`
		Wallet        string  `json:"wallet"`
		PaymentMethod string  `json:"paymentMethod"`
		Customer      struct {
			Name      string         `json:"name"`
			CPF       string         `json:"cpf"`
			Phone     string         `json:"phone"`
			Email     string         `json:"email"`
			BirthDate string         `json:"birthDate"`
			Address   map[string]any `json:"address"`
		} `json:"customer"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	wallet := strings.TrimSpace(req.Wallet)
	if wallet == "" && auth.Sandbox {
		wallet = chainFXFakeWallet()
	}
	payload := map[string]any{
		"amountFiat":     req.Amount,
		"fiatCurrency":   defaultString(req.Fiat, "BRL"),
		"paymentMethod":  defaultString(req.PaymentMethod, "pix"),
		"asset":          defaultString(req.Asset, "USDT"),
		"address":        wallet,
		"pixCpf":         req.Customer.CPF,
		"pixPhone":       req.Customer.Phone,
		"email":          req.Customer.Email,
		"customerName":   req.Customer.Name,
		"birthDate":      req.Customer.BirthDate,
		"addressPayload": req.Customer.Address,
		"customer": map[string]any{
			"name":      req.Customer.Name,
			"email":     req.Customer.Email,
			"cpf":       req.Customer.CPF,
			"phone":     req.Customer.Phone,
			"birthDate": req.Customer.BirthDate,
			"address":   req.Customer.Address,
		},
	}
	s.handleCreateBuy(w, cloneJSONRequest(r, payload))
}

func (s *Server) handleChainFXSell(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	var req struct {
		QuoteID        string  `json:"quoteId"`
		Asset          string  `json:"asset"`
		Network        string  `json:"network"`
		Amount         float64 `json:"amount"`
		AmountBRL      float64 `json:"amountBRL"`
		DepositAddress string  `json:"depositAddress"`
		Wallet         string  `json:"wallet"`
		PixCPF         string  `json:"pixCpf"`
		PixPhone       string  `json:"pixPhone"`
		Pix            struct {
			CPF   string `json:"cpf"`
			Phone string `json:"phone"`
		} `json:"pix"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	amountBRL := req.AmountBRL
	if amountBRL <= 0 {
		amountBRL = req.Amount
	}
	depositAddress := firstNonEmpty(req.DepositAddress, req.Wallet)
	if depositAddress == "" && auth.Sandbox {
		depositAddress = chainFXFakeWallet()
	}
	payload := map[string]any{
		"amountBRL": amountBRL,
		"address":   depositAddress,
		"network":   defaultString(req.Network, "BSC"),
		"asset":     defaultString(req.Asset, "USDT"),
		"pixCpf":    firstNonEmpty(req.PixCPF, req.Pix.CPF),
		"pixPhone":  firstNonEmpty(req.PixPhone, req.Pix.Phone),
	}
	s.handleCreateOrder(w, cloneJSONRequest(r, payload))
}

func (s *Server) handleChainFXOrder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "order id is required"})
		return
	}
	auth := s.chainFXAuthContext(r)
	token := customerAccessToken(r)
	if buy, ok := s.readChainFXBuy(r.Context(), id, token, auth.Valid); ok {
		writeJSON(w, http.StatusOK, buy)
		return
	}
	if order, ok := s.readChainFXSell(r.Context(), id, token, auth.Valid); ok {
		writeJSON(w, http.StatusOK, order)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "order not found"})
}

func (s *Server) handleChainFXWebhookTest(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	var req struct {
		Event     string `json:"event"`
		OrderID   string `json:"orderId"`
		Asset     string `json:"asset"`
		Amount    string `json:"amount"`
		TargetURL string `json:"targetUrl"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	event := defaultString(req.Event, "payment.completed")
	if !validChainFXEvent(event) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported event", "events": chainFXWebhookEvents()})
		return
	}
	payload := map[string]any{
		"event":     event,
		"orderId":   defaultString(req.OrderID, "ord_test_123"),
		"status":    chainFXEventStatus(event),
		"asset":     defaultString(req.Asset, "USDT"),
		"amount":    defaultString(req.Amount, "96.52"),
		"timestamp": time.Now().UTC(),
		"sandbox":   auth.Sandbox,
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"delivered":   false,
		"simulated":   true,
		"targetUrl":   req.TargetURL,
		"payload":     payload,
		"retryPolicy": "Phase 2: dashboard logs and webhook retry",
	})
}

func (s *Server) handleChainFXWebhookRetry(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	var req struct {
		Event     string `json:"event"`
		OrderID   string `json:"orderId"`
		Side      string `json:"side"`
		TargetURL string `json:"targetUrl"`
		Asset     string `json:"asset"`
		Amount    string `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	req.OrderID = strings.TrimSpace(req.OrderID)
	if req.OrderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "orderId is required"})
		return
	}
	event := defaultString(req.Event, "payment.completed")
	if !validChainFXEvent(event) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported event", "events": chainFXWebhookEvents()})
		return
	}
	source, payload, found := s.chainFXWebhookPayloadFromOrder(r.Context(), req.OrderID, req.Side, event, req.Asset, req.Amount, auth.Sandbox)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "order not found"})
		return
	}
	delivery := map[string]any{"attempted": false}
	if strings.TrimSpace(req.TargetURL) != "" {
		result := s.deliverChainFXWebhook(r.Context(), req.TargetURL, event, payload)
		delivery = result
	}
	logPayload := map[string]any{
		"requestId": requestID(r),
		"event":     event,
		"targetUrl": req.TargetURL,
		"delivery":  delivery,
		"sandbox":   auth.Sandbox,
	}
	if source == "buy" {
		_ = s.db.AddBuyEvent(r.Context(), req.OrderID, "developer.webhook_retry", logPayload)
	} else {
		_ = s.db.AddEvent(r.Context(), req.OrderID, "developer.webhook_retry", logPayload)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"source":   source,
		"payload":  payload,
		"delivery": delivery,
	})
}

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

func (s *Server) handleDevelopersDashboard(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	limit := 25
	events, _ := s.db.ListDeveloperEvents(r.Context(), limit)
	keys := map[string]any{
		"livePublic": maskCSVKeys(s.cfg.ChainFXLivePublicKeys),
		"liveSecret": maskCSVKeys(s.cfg.ChainFXLiveSecretKeys),
		"testPublic": maskCSVKeys(s.cfg.ChainFXTestPublicKeys),
		"testSecret": maskCSVKeys(s.cfg.ChainFXTestSecretKeys),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	var rows strings.Builder
	for _, event := range events {
		rows.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td><code>%s</code></td></tr>`,
			html.EscapeString(event.CreatedAt.Format(time.RFC3339)),
			html.EscapeString(event.Source),
			html.EscapeString(event.Type),
			html.EscapeString(event.OrderID),
			html.EscapeString(strings.TrimSpace(string(event.Payload))),
		))
	}
	apiKey := html.EscapeString(chainFXAPIKey(r))
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>ChainFX Developer Dashboard</title>
  <style>
    :root{--bg:#eef4fa;--panel:#fff;--ink:#102a43;--muted:#64748b;--line:#d8e5f2;--blue:#1266d6;--cyan:#12b7d8}
    *{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.5 Inter,system-ui,Segoe UI,sans-serif}
    header{padding:34px 28px;background:linear-gradient(135deg,#fff,#e8f8ff);border-bottom:1px solid var(--line)}
    main{padding:24px 28px;max-width:1240px;margin:auto}.grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:14px}.card{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:18px;box-shadow:0 16px 40px rgba(16,42,67,.08)}
    h1{margin:0 0 6px;font-size:34px}h2{margin:0 0 12px;font-size:18px}p{margin:0;color:var(--muted)}code{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}
    table{width:100%%;border-collapse:collapse;background:#fff;border:1px solid var(--line);border-radius:8px;overflow:hidden}th,td{padding:10px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}th{background:#f8fbff;color:#475569}td code{white-space:pre-wrap;word-break:break-word;font-size:12px}
    .actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}a{color:#fff;background:linear-gradient(135deg,var(--blue),var(--cyan));padding:10px 12px;border-radius:8px;text-decoration:none;font-weight:700}.muted{color:var(--muted)}
    @media(max-width:900px){.grid{grid-template-columns:1fr}main,header{padding-left:18px;padding-right:18px}}
  </style>
</head>
<body>
  <header>
    <h1>ChainFX Developer Dashboard</h1>
    <p>API keys, webhook logs and retry operations for Digital FX Payments.</p>
    <div class="actions"><a href="/developers/api-keys?apiKey=%s">API Keys JSON</a><a href="/developers/logs?apiKey=%s">Logs JSON</a><a href="/openapi.json">OpenAPI</a></div>
  </header>
  <main>
    <section class="grid">
      <article class="card"><h2>Live Public</h2><p><code>%v</code></p></article>
      <article class="card"><h2>Live Secret</h2><p><code>%v</code></p></article>
      <article class="card"><h2>Test Public</h2><p><code>%v</code></p></article>
      <article class="card"><h2>Test Secret</h2><p><code>%v</code></p></article>
    </section>
    <section style="margin-top:22px">
      <h2>Recent Logs</h2>
      <table><thead><tr><th>At</th><th>Source</th><th>Type</th><th>Order</th><th>Payload</th></tr></thead><tbody>%s</tbody></table>
      <p class="muted" style="margin-top:14px">Retry endpoint: POST /webhooks/retry with Bearer API key, orderId, event and optional targetUrl.</p>
    </section>
  </main>
</body>
</html>`, apiKey, apiKey, keys["livePublic"], keys["liveSecret"], keys["testPublic"], keys["testSecret"], rows.String())
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
      <article class="card"><h2>Dashboard</h2><p>Phase 2: <code>/developers/dashboard?apiKey=sk_live_xxx</code> with API keys, logs and webhook retry operations.</p></article>
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
			"/rates":               map[string]any{"get": map[string]any{"summary": "Current FX and crypto rates"}},
			"/quote":               map[string]any{"post": map[string]any{"summary": "Create a rate-locked quote"}},
			"/buy":                 map[string]any{"post": map[string]any{"summary": "Create a PIX/card to USDT order", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/sell":                map[string]any{"post": map[string]any{"summary": "Create a USDT to PIX BRL order", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/order/{id}":          map[string]any{"get": map[string]any{"summary": "Read an order by ID"}},
			"/webhooks/test":       map[string]any{"post": map[string]any{"summary": "Generate a simulated webhook payload", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/webhooks/retry":      map[string]any{"post": map[string]any{"summary": "Retry a webhook for an existing order", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/developers/api-keys": map[string]any{"get": map[string]any{"summary": "List configured API keys masked", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
			"/developers/logs":     map[string]any{"get": map[string]any{"summary": "List recent developer logs", "security": []map[string][]string{{"bearerAuth": []string{}}}}},
		},
		"x-chainfx": map[string]any{
			"category":        "Digital FX Payments Infrastructure",
			"phase":           "3",
			"supportedAssets": []string{"USDT"},
			"phase2":          []string{"Developer Dashboard", "API Keys", "Logs", "Webhook Retry"},
			"phase3":          []string{"Node SDK", "Python SDK", "OpenAPI", "Examples"},
			"notNow":          []string{"bridge", "pool", "AMM", "yield", "DEX", "LP"},
		},
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cotacao ainda nao carregada"})
		return
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
	if !validHMAC(secret, raw, r.Header.Get("x-efi-signature")) {
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
	signature := firstNonEmpty(r.Header.Get("x-efi-signature"), r.Header.Get("x-chainfx-signature"))
	queryHMAC := r.URL.Query().Get("hmac")
	if secret != "" && signature != "" && !validHMAC(secret, raw, signature) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invalida"})
		return
	}
	if secret != "" && signature == "" && queryHMAC != "" && !hmac.Equal([]byte(queryHMAC), []byte(secret)) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "hmac invalido"})
		return
	}
	if s.cfg.IsProduction() && secret != "" && signature == "" && queryHMAC == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "webhook sem autenticacao adicional"})
		return
	}
	var req struct {
		BuyID      string `json:"buyId"`
		Status     string `json:"status"`
		ProviderID string `json:"providerId"`
		Error      string `json:"error"`
		Pix        []struct {
			EndToEndID string `json:"endToEndId"`
			TxID       string `json:"txid"`
			Valor      string `json:"valor"`
			Horario    string `json:"horario"`
		} `json:"pix"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	if len(req.Pix) > 0 {
		req.Status = firstNonEmpty(req.Status, "CONCLUIDA")
		req.ProviderID = firstNonEmpty(req.ProviderID, req.Pix[0].EndToEndID, req.Pix[0].TxID)
		req.BuyID = firstNonEmpty(req.BuyID, buyIDFromEfiTxID(req.Pix[0].TxID))
	}
	if req.BuyID == "" || req.ProviderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload sem buyId/txid"})
		return
	}
	status := settlement.PixWebhookStatus(firstNonEmpty(req.Status, "CONCLUIDA"))
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
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invalida"})
		return
	}
	var req struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
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
		"pix":      s.efiPixConfigured() && defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret) != "",
		"stripe":   defaultString(s.cfg.StripeWebhookSecret, s.cfg.WebhookSecret) != "",
		"signer":   s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		"mode":     s.cfg.Environment,
		"warnings": gaps,
	})
}

func (s *Server) operationalGaps() []string {
	checks := map[string]bool{
		"pix_provider":   s.efiPixConfigured(),
		"pix_webhook":    defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret) != "",
		"stripe_webhook": defaultString(s.cfg.StripeWebhookSecret, s.cfg.WebhookSecret) != "",
		"signer":         s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		"signer_private": !strings.Contains(strings.ToLower(strings.TrimSpace(s.cfg.SignerUrl)), "up.railway.app"),
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

type chainFXAuth struct {
	Valid   bool
	Sandbox bool
	Mode    string
}

func (s *Server) authorizeChainFX(w http.ResponseWriter, r *http.Request) (chainFXAuth, bool) {
	auth := s.chainFXAuthContext(r)
	if auth.Valid {
		if auth.Sandbox && s.cfg.IsProduction() {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "sandbox API keys cannot create live orders",
				"hint":  "use https://sandbox-api.chainfx.com for sk_test_xxx keys",
			})
			return chainFXAuth{}, false
		}
		return auth, true
	}
	if !s.cfg.ChainFXRequireAPIKey && !s.cfg.IsProduction() {
		return chainFXAuth{Valid: true, Sandbox: true, Mode: "development"}, true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": "API key required",
		"hint":  "send Authorization: Bearer sk_test_xxx or sk_live_xxx",
	})
	return chainFXAuth{}, false
}

func (s *Server) chainFXAuthContext(r *http.Request) chainFXAuth {
	key := chainFXAPIKey(r)
	if key == "" {
		return chainFXAuth{}
	}
	if strings.HasPrefix(key, "sk_test_") || csvContains(s.cfg.ChainFXTestSecretKeys, key) {
		return chainFXAuth{Valid: true, Sandbox: true, Mode: "test"}
	}
	if csvContains(s.cfg.ChainFXLiveSecretKeys, key) {
		return chainFXAuth{Valid: true, Mode: "live"}
	}
	if strings.HasPrefix(key, "sk_live_") && !s.cfg.ChainFXRequireAPIKey && !s.cfg.IsProduction() {
		return chainFXAuth{Valid: true, Mode: "live-dev"}
	}
	return chainFXAuth{}
}

func chainFXAPIKey(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if key := strings.TrimSpace(r.Header.Get("X-Api-Key")); key != "" {
		return key
	}
	return strings.TrimSpace(r.URL.Query().Get("apiKey"))
}

func csvContains(csv, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, item := range strings.Split(csv, ",") {
		if strings.TrimSpace(item) == value {
			return true
		}
	}
	return false
}

func cloneJSONRequest(r *http.Request, payload any) *http.Request {
	raw, _ := json.Marshal(payload)
	clone := r.Clone(r.Context())
	clone.Body = io.NopCloser(bytes.NewReader(raw))
	clone.ContentLength = int64(len(raw))
	clone.Header = r.Header.Clone()
	clone.Header.Set("Content-Type", "application/json")
	return clone
}

func chainFXFakeWallet() string {
	return "0x000000000000000000000000000000000000dEaD"
}

func chainFXWebhookEvents() []string {
	return []string{"payment.created", "payment.completed", "payment.failed", "order.confirmed", "crypto.sent", "crypto.confirmed", "order.failed"}
}

func validChainFXEvent(event string) bool {
	for _, allowed := range chainFXWebhookEvents() {
		if event == allowed {
			return true
		}
	}
	return false
}

func chainFXEventStatus(event string) string {
	switch event {
	case "payment.created":
		return "created"
	case "payment.completed":
		return "paid"
	case "payment.failed", "order.failed":
		return "failed"
	case "order.confirmed", "crypto.confirmed":
		return "confirmed"
	case "crypto.sent":
		return "sent"
	default:
		return "unknown"
	}
}

func (s *Server) chainFXWebhookPayloadFromOrder(ctx context.Context, orderID, side, event, asset, amount string, sandbox bool) (string, map[string]any, bool) {
	side = strings.ToLower(strings.TrimSpace(side))
	if side == "" || side == "buy" {
		if buy, err := s.db.GetBuyOrder(ctx, orderID); err == nil && buy != nil {
			return "buy", map[string]any{
				"event":     event,
				"orderId":   buy.ID,
				"status":    chainFXEventStatus(event),
				"side":      "buy",
				"asset":     defaultString(asset, buy.Asset),
				"amount":    defaultString(amount, fmt.Sprintf("%.8f", buy.CryptoAmount)),
				"timestamp": time.Now().UTC(),
				"sandbox":   sandbox,
			}, true
		}
	}
	if side == "" || side == "sell" {
		if order, err := s.db.GetOrder(ctx, orderID); err == nil && order != nil {
			return "sell", map[string]any{
				"event":     event,
				"orderId":   order.ID,
				"status":    chainFXEventStatus(event),
				"side":      "sell",
				"asset":     defaultString(asset, order.Asset),
				"amount":    defaultString(amount, fmt.Sprintf("%.8f", order.AmountUSDT)),
				"timestamp": time.Now().UTC(),
				"sandbox":   sandbox,
			}, true
		}
	}
	return "", nil, false
}

func (s *Server) deliverChainFXWebhook(ctx context.Context, targetURL, event string, payload map[string]any) map[string]any {
	targetURL = strings.TrimSpace(targetURL)
	if !strings.HasPrefix(strings.ToLower(targetURL), "https://") && !strings.HasPrefix(strings.ToLower(targetURL), "http://") {
		return map[string]any{"attempted": false, "error": "targetUrl must be http or https"}
	}
	raw, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, targetURL, bytes.NewReader(raw))
	if err != nil {
		return map[string]any{"attempted": false, "error": err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ChainFX-Webhooks/1.0")
	req.Header.Set("X-ChainFX-Event", event)
	req.Header.Set("X-ChainFX-Signature", signChainFXWebhook(defaultString(s.cfg.WebhookSecret, s.cfg.PixWebhookSecret), raw))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return map[string]any{"attempted": true, "ok": false, "error": err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return map[string]any{
		"attempted":  true,
		"ok":         resp.StatusCode >= 200 && resp.StatusCode < 300,
		"statusCode": resp.StatusCode,
		"body":       string(body),
	}
}

func signChainFXWebhook(secret string, raw []byte) string {
	if strings.TrimSpace(secret) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func maskCSVKeys(csv string) []string {
	var out []string
	for _, item := range strings.Split(csv, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, maskAPIKey(item))
	}
	return out
}

func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 12 {
		return key
	}
	return key[:8] + "..." + key[len(key)-4:]
}

func (s *Server) readChainFXBuy(ctx context.Context, id, accessToken string, apiKeyOK bool) (map[string]any, bool) {
	if !apiKeyOK {
		ok, err := s.db.ValidateBuyAccess(ctx, id, accessToken)
		if err != nil || !ok {
			return nil, false
		}
	}
	buy, err := s.db.GetBuyOrder(ctx, id)
	if err != nil || buy == nil {
		return nil, false
	}
	return map[string]any{
		"id":           buy.ID,
		"side":         "buy",
		"status":       buy.Status,
		"fiat":         buy.FiatCurrency,
		"asset":        buy.Asset,
		"amountFiat":   buy.AmountFiat,
		"feeFiat":      buy.FeeBRL,
		"totalFiat":    buy.AmountFiat,
		"payoutFiat":   buy.PayoutBRL,
		"cryptoAmount": buy.CryptoAmount,
		"rate":         buy.RateLocked,
		"wallet":       buy.DestAddress,
		"paymentRail":  buy.PaymentMethod,
		"payment":      jsonRawToMap(buy.PixPayload),
		"txHash":       buy.TxHashOut,
		"error":        buy.Error,
		"createdAt":    buy.CreatedAt,
		"updatedAt":    buy.UpdatedAt,
		"expiresAt":    buy.RateLockExpiresAt,
	}, true
}

func (s *Server) readChainFXSell(ctx context.Context, id, accessToken string, apiKeyOK bool) (map[string]any, bool) {
	if !apiKeyOK {
		ok, err := s.db.ValidateOrderAccess(ctx, id, accessToken)
		if err != nil || !ok {
			return nil, false
		}
	}
	order, err := s.db.GetOrder(ctx, id)
	if err != nil || order == nil {
		return nil, false
	}
	return map[string]any{
		"id":             order.ID,
		"side":           "sell",
		"status":         order.Status,
		"fiat":           "BRL",
		"asset":          order.Asset,
		"network":        order.Network,
		"amountFiat":     order.AmountBRL,
		"feeFiat":        order.FeeBRL,
		"payoutFiat":     order.PayoutBRL,
		"cryptoAmount":   order.AmountUSDT,
		"rate":           order.RateLocked,
		"depositAddress": order.Address,
		"pixKey":         order.PixKey,
		"txHash":         order.TxHash,
		"depositTx":      order.DepositTx,
		"depositAmount":  order.DepositAmount,
		"error":          order.Error,
		"createdAt":      order.CreatedAt,
		"updatedAt":      order.UpdatedAt,
		"expiresAt":      order.RateLockExpiresAt,
	}, true
}

func jsonRawToMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
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
	allowed = append(allowed,
		"http://localhost:5173",
		"http://127.0.0.1:5173",
		"https://swapped-cryptocurrensy.vercel.app",
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		w.Header().Add("Vary", "Origin")
		for _, item := range allowed {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if item == "*" {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Api-Key, x-internal-hmac, x-idempotency-key, x-efi-signature, x-chainfx-signature, Stripe-Signature")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				break
			}
			if origin != "" && item == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Api-Key, x-internal-hmac, x-idempotency-key, x-efi-signature, x-chainfx-signature, Stripe-Signature")
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

type paymentCustomerInput struct {
	Name      string
	Email     string
	CPF       string
	Phone     string
	BirthDate string
	Address   map[string]any
}

func (s *Server) createPaymentIntent(ctx context.Context, buyID string, amountFiat float64, fiatCurrency, paymentMethod string, customer paymentCustomerInput) (map[string]any, error) {
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
	if !s.efiPixConfigured() {
		if s.cfg.AllowSimulations && !s.cfg.IsProduction() {
			return map[string]any{"provider": "efi", "mode": "simulation", "pixKey": "chavepix@nexswap.com", "qrCodeUrl": "/images/qrcode.png", "buyId": buyID}, nil
		}
		return nil, fmt.Errorf("credenciais Efí Pix nao configuradas")
	}
	return s.createEfiPixCharge(ctx, buyID, amountFiat, customer)
}

func (s *Server) efiPixConfigured() bool {
	return strings.TrimSpace(s.cfg.EfiClientID) != "" &&
		strings.TrimSpace(s.cfg.EfiClientSecret) != "" &&
		strings.TrimSpace(s.cfg.EfiPixKey) != "" &&
		strings.TrimSpace(s.cfg.EfiCertificatePath) != ""
}

func (s *Server) createEfiPixCharge(ctx context.Context, buyID string, amountFiat float64, customer paymentCustomerInput) (map[string]any, error) {
	client, err := s.efiHTTPClient()
	if err != nil {
		return nil, err
	}
	token, err := s.efiAccessToken(ctx, client)
	if err != nil {
		return nil, err
	}
	txid := efiTxIDFromBuyID(buyID)
	payload := map[string]any{
		"calendario": map[string]any{"expiracao": s.cfg.RateLockSec},
		"valor":      map[string]any{"original": fmt.Sprintf("%.2f", amountFiat)},
		"chave":      s.cfg.EfiPixKey,
		"solicitacaoPagador": fmt.Sprintf(
			"Pedido %s - USDT BSC",
			buyID,
		),
	}
	if debtor := buildEfiDebtor(customer); len(debtor) > 0 {
		payload["devedor"] = debtor
	}

	raw, _ := json.Marshal(payload)
	endpoint := strings.TrimRight(s.cfg.EfiApiBaseURL, "/") + "/v2/cob/" + txid
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Efí rejeitou cobranca PIX: status %d", resp.StatusCode)
	}
	var provider map[string]any
	if err := json.Unmarshal(body, &provider); err != nil {
		return nil, fmt.Errorf("Efí respondeu JSON invalido")
	}
	provider["provider"] = "efi"
	provider["buyId"] = buyID
	provider["txid"] = defaultString(fmt.Sprint(provider["txid"]), txid)
	provider["providerPaymentId"] = txid
	provider["providerStatus"] = resp.StatusCode
	provider["pixKey"] = s.cfg.EfiPixKey
	if qr, err := s.efiQRCode(ctx, client, token, provider); err == nil {
		for key, value := range qr {
			provider[key] = value
		}
	} else {
		return nil, err
	}
	if mapString(provider, "qrCodeUrl") == "" || mapString(provider, "pixCopiaECola") == "" {
		return nil, fmt.Errorf("Efí nao retornou QR Code Pix completo")
	}
	if feeBps := s.cfg.EfiPixFeeBps; feeBps > 0 {
		provider["providerFeeBps"] = feeBps
		provider["providerFeeFiatEstimated"] = math.Round((amountFiat*float64(feeBps)/10000)*100) / 100
	}
	if customer.Name != "" || customer.Email != "" || customer.CPF != "" || customer.Phone != "" || customer.BirthDate != "" || len(customer.Address) > 0 {
		provider["customerSubmitted"] = buildCustomerAudit(s.cfg.LGPDSecret, customer.CPF, customer.Phone, customer.Email, customer.Name, customer.BirthDate, customer.Address)
	}
	return provider, nil
}

func (s *Server) efiQRCode(ctx context.Context, client *http.Client, token string, charge map[string]any) (map[string]any, error) {
	locID := nestedFloat(charge, "loc", "id")
	if locID <= 0 {
		return nil, fmt.Errorf("cobranca Efí sem loc.id")
	}
	endpoint := fmt.Sprintf("%s/v2/loc/%d/qrcode", strings.TrimRight(s.cfg.EfiApiBaseURL, "/"), int64(locID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Efí rejeitou QR Code Pix: status %d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return map[string]any{
		"pixCopiaECola":     mapString(data, "qrcode"),
		"qrCodeUrl":         mapString(data, "imagemQrcode"),
		"linkVisualizacao":  mapString(data, "linkVisualizacao"),
		"qrCodeProviderRaw": data,
	}, nil
}

func (s *Server) efiHTTPClient() (*http.Client, error) {
	certPath := strings.TrimSpace(s.cfg.EfiCertificatePath)
	keyPath := strings.TrimSpace(s.cfg.EfiCertificateKey)
	cert, err := s.loadEfiCertificate(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}},
	}, nil
}

func (s *Server) loadEfiCertificate(certPath, keyPath string) (tls.Certificate, error) {
	if strings.TrimSpace(certPath) == "" {
		return tls.Certificate{}, fmt.Errorf("EFI_CERTIFICATE_PATH nao configurado")
	}
	if strings.HasSuffix(strings.ToLower(certPath), ".p12") || strings.HasSuffix(strings.ToLower(certPath), ".pfx") {
		raw, err := os.ReadFile(certPath)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("nao foi possivel ler certificado Efí P12: %w", err)
		}
		privateKey, cert, err := pkcs12.Decode(raw, s.cfg.EfiCertificatePass)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("certificado Efí P12 invalido; confira EFI_CERTIFICATE_PASSWORD: %w", err)
		}
		return tls.Certificate{
			Certificate: [][]byte{cert.Raw},
			PrivateKey:  privateKey,
			Leaf:        cert,
		}, nil
	}
	if keyPath == "" {
		keyPath = certPath
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certificado Efí PEM/KEY invalido: %w", err)
	}
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	}
	return cert, nil
}

func (s *Server) efiAccessToken(ctx context.Context, client *http.Client) (string, error) {
	raw := []byte(`{"grant_type":"client_credentials"}`)
	endpoint := strings.TrimRight(s.cfg.EfiApiBaseURL, "/") + "/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.cfg.EfiClientID, s.cfg.EfiClientSecret)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Efí OAuth rejeitou credenciais: status %d", resp.StatusCode)
	}
	var data struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &data); err != nil || strings.TrimSpace(data.AccessToken) == "" {
		return "", fmt.Errorf("Efí OAuth respondeu token inválido")
	}
	return data.AccessToken, nil
}

func buildEfiDebtor(customer paymentCustomerInput) map[string]any {
	cpf := onlyDigits(customer.CPF)
	name := strings.TrimSpace(customer.Name)
	if cpf == "" || name == "" {
		return nil
	}
	return map[string]any{
		"cpf":  cpf,
		"nome": name,
	}
}

func efiTxIDFromBuyID(buyID string) string {
	clean := onlyAlphaNum(buyID)
	if len(clean) >= 26 && len(clean) <= 35 {
		return clean
	}
	if len(clean) > 35 {
		return clean[:35]
	}
	return strings.ToUpper(clean + strings.Repeat("0", 26-len(clean)))
}

func buyIDFromEfiTxID(txid string) string {
	clean := onlyAlphaNum(txid)
	if len(clean) == 32 {
		return fmt.Sprintf("%s-%s-%s-%s-%s", clean[0:8], clean[8:12], clean[12:16], clean[16:20], clean[20:32])
	}
	return txid
}

func onlyAlphaNum(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func copyAddressField(out, in map[string]any, target string, keys ...string) {
	if value := firstMapString(in, keys...); value != "" {
		out[target] = value
	}
}

func firstMapString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := values[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func mapString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	if raw, ok := values[key]; ok {
		if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

func nestedFloat(root map[string]any, keys ...string) float64 {
	var current any = root
	for _, key := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			return 0
		}
		current = obj[key]
	}
	switch value := current.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		out, _ := value.Float64()
		return out
	default:
		out, _ := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(value)), 64)
		return out
	}
}

func onlyDigits(value string) string {
	var builder strings.Builder
	for _, ch := range value {
		if ch >= '0' && ch <= '9' {
			builder.WriteRune(ch)
		}
	}
	return builder.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonNilMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
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

func buildCustomerAudit(secret, cpf, phone, email, name, birthDate string, address map[string]any) map[string]any {
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
	if hash := privacy.Hash(name, secret); hash != "" {
		customer["nameHash"] = hash
	}
	if hash := privacy.Hash(birthDate, secret); hash != "" {
		customer["birthDateHash"] = hash
	}
	if len(address) > 0 {
		raw, _ := json.Marshal(address)
		if hash := privacy.Hash(string(raw), secret); hash != "" {
			customer["addressHash"] = hash
		}
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
