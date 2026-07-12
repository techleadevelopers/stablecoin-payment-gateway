package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
)

func (s *Server) handleChainFXRates(w http.ResponseWriter, r *http.Request) {
	price := s.workers.PriceWorker.GetCurrentPrice()
	if price <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "rates are not loaded yet"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"brand":        "ChainFX",
		"category":     "Digital FX Payments Infrastructure",
		"description":  "Accept PIX. Deliver digital dollars. Receive stablecoins. Pay out PIX.",
		"base":         "USDT",
		"sellWallet":   s.cfg.SellWalletAddress,
		"sellNetwork":  "BEP20",
		"sellNetworks": s.supportedSellNetworks(),
		"rates": map[string]float64{
			"USDT_BRL":      s.workers.PriceWorker.GetPrice("BRL"),
			"SELL_USDT_BRL": s.sellRate(price),
			"USDT_USD":      s.workers.PriceWorker.GetPrice("USD"),
			"USDT_EUR":      s.workers.PriceWorker.GetPrice("EUR"),
			"BTC_USDT":      s.workers.PriceWorker.GetPrice("BTCUSDT"),
			"EUR_USD":       s.workers.PriceWorker.GetPrice("EURUSD"),
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
	if side != "sell" && fiatCurrency == "BRL" && (amountFiat < s.buyMinBRL() || amountFiat > s.cfg.OrderMaxBrl) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("amount outside limits (%.2f - %.2f BRL)", s.buyMinBRL(), s.cfg.OrderMaxBrl)})
		return
	}
	marketRate := s.workers.PriceWorker.GetPrice(fiatCurrency)
	if marketRate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "rates are not loaded yet"})
		return
	}
	expiresAt := time.Now().Add(time.Duration(s.cfg.RateLockSec) * time.Second).UTC()
	if side == "sell" {
		amountUSDT := amountFiat
		rate, payoutBRL, spreadBRL := s.sellQuote(amountUSDT, marketRate)
		if payoutBRL < s.cfg.OrderMinBrl || payoutBRL > s.cfg.OrderMaxBrl {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("payout outside limits (%.2f - %.2f BRL)", s.cfg.OrderMinBrl, s.cfg.OrderMaxBrl)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"quoteId":      "qt_" + strings.ReplaceAll(database.NewID(), "-", ""),
			"side":         side,
			"fiat":         "BRL",
			"asset":        asset,
			"rate":         rate,
			"marketRate":   roundRate(marketRate),
			"cryptoAmount": amountUSDT,
			"fiatAmount":   payoutBRL,
			"feeFiat":      spreadBRL,
			"spreadFiat":   spreadBRL,
			"payoutFiat":   payoutBRL,
			"totalFiat":    payoutBRL,
			"paymentRail":  paymentMethod,
			"sellPolicy":   s.sellPolicy(marketRate, rate),
			"expiresAt":    expiresAt,
			"sandbox":      s.chainFXAuthContext(r).Sandbox,
		})
		return
	}
	rate := s.buyRate(marketRate)
	fee := s.transactionFee(amountFiat, fiatCurrency, rate)
	writeJSON(w, http.StatusOK, map[string]any{
		"quoteId":      "qt_" + strings.ReplaceAll(database.NewID(), "-", ""),
		"side":         side,
		"fiat":         fiatCurrency,
		"asset":        asset,
		"rate":         rate,
		"marketRate":   roundRate(marketRate),
		"fiatAmount":   amountFiat,
		"feeFiat":      fee,
		"feeBreakdown": s.buyFeeBreakdown(amountFiat),
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
	marketRate := s.workers.PriceWorker.GetCurrentPrice()
	if marketRate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "rates are not loaded yet"})
		return
	}
	amountUSDT := req.Amount
	if amountUSDT <= 0 && req.AmountBRL > 0 {
		amountUSDT = req.AmountBRL / s.sellRate(marketRate)
	}
	depositAddress := firstNonEmpty(s.cfg.SellWalletAddress, req.DepositAddress, req.Wallet)
	if depositAddress == "" && auth.Sandbox {
		depositAddress = chainFXFakeWallet()
	}
	payload := map[string]any{
		"amountUSDT": amountUSDT,
		"address":    depositAddress,
		"network":    defaultString(req.Network, "BSC"),
		"asset":      defaultString(req.Asset, "USDT"),
		"pixCpf":     firstNonEmpty(req.PixCPF, req.Pix.CPF),
		"pixPhone":   firstNonEmpty(req.PixPhone, req.Pix.Phone),
	}
	s.handleCreateOrder(w, cloneJSONRequest(r, payload))
}
