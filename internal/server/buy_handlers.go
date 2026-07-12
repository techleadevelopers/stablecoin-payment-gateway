package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/workers"
)

func (s *Server) handleCreateBuy(w http.ResponseWriter, r *http.Request) {
	markLegacyRoute(w, r, "/buy")
	if !s.limiter.Allow("buy:" + clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "limite de criaÃƒÂ§ÃƒÂ£o de compras excedido"})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invÃƒÂ¡lido"})
		return
	}
	asset := strings.ToUpper(defaultString(req.Asset, "USDT"))
	if asset != "USDT" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "asset nÃƒÂ£o suportado nesta fase (apenas USDT)"})
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
	if fiatCurrency == "BRL" && (amountFiat < s.buyMinBRL() || amountFiat > s.cfg.OrderMaxBrl) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("valor fora dos limites (%.2f - %.2f BRL)", s.buyMinBRL(), s.cfg.OrderMaxBrl)})
		return
	}
	marketRate := s.workers.PriceWorker.GetPrice(fiatCurrency)
	if marketRate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cotacao ainda nao carregada"})
		return
	}
	rate := s.buyRate(marketRate)
	fee := s.transactionFee(amountFiat, fiatCurrency, rate)
	payout := amountFiat
	if payout <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "valor insuficiente apÃƒÂ³s taxa"})
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
		slog.Error("Erro ao criar cobranca PIX", "buyId", buyID, "provider", "efi", "error", err)
		writeAPIError(w, r, http.StatusBadGateway, "PAYMENT_PROVIDER_ERROR", "Payment provider unavailable.")
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
		CustomerEmail:     customerInput.Email,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	_ = s.db.AddBuyEvent(r.Context(), buy.ID, "buy.meta", map[string]any{"requestId": requestID(r), "ip": clientIP(r), "userAgent": r.UserAgent(), "customer": customerAudit})
	s.workers.Bus.Publish(workers.Event{Type: "buy.created", OrderID: buy.ID, Payload: map[string]any{"requestId": requestID(r), "amountFiat": totalFiat, "fiatCurrency": fiatCurrency, "paymentMethod": paymentMethod}})
	writeJSON(w, http.StatusCreated, map[string]any{
		"buyId": buy.ID, "id": buy.ID, "accessToken": buy.AccessToken, "status": buy.Status, "amountFiat": totalFiat, "subtotalFiat": amountFiat, "fiatCurrency": fiatCurrency, "paymentMethod": paymentMethod, "feeFiat": fee, "totalFiat": totalFiat, "payoutFiat": payout,
		"rate": rate, "marketRate": roundRate(marketRate), "cryptoAmount": cryptoAmount, "asset": asset, "network": deliveryNetwork, "destAddress": buy.DestAddress,
		"feePolicy": s.feePolicy(fiatCurrency, rate), "feeBreakdown": s.buyFeeBreakdown(amountFiat),
		"pixKey": paymentPayload["pixKey"], "qrCodeUrl": paymentPayload["qrCodeUrl"], "payment": paymentPayload,
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
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "compra nÃƒÂ£o encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, buy)
}

func (s *Server) handleBuyStream(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeBuyRead(w, r, r.PathValue("id")) {
		return
	}
	id := r.PathValue("id")
	streamSSE(w, r, func(ctx context.Context) (sseUpdate, bool) {
		buy, _ := s.db.GetBuyOrder(ctx, id)
		if buy == nil {
			return sseUpdate{}, false
		}
		return sseUpdate{
			Key:     buy.Status,
			Payload: map[string]any{"status": buy.Status, "txHash": buy.TxHashOut},
			Final:   isFinalBuyStatus(buy.Status),
		}, true
	})
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
