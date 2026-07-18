package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/money"
	"payment-gateway/internal/nfc"

	"github.com/ethereum/go-ethereum/common"
)

type nfcProvisionRequest struct {
	WalletAddress string `json:"wallet_address"`
	DeviceID      string `json:"device_id"`
	Network       string `json:"network"`
	TTLSeconds    int    `json:"ttl_seconds"`
}

type nfcAuthorizeRequest struct {
	Token          string `json:"token"`
	AmountBRL      string `json:"amount_brl"`
	Currency       string `json:"currency"`
	MerchantID     string `json:"merchant_id"`
	TerminalID     string `json:"terminal_id"`
	ExternalRef    string `json:"external_ref"`
	IdempotencyKey string `json:"idempotency_key"`
}

type nfcSandboxFundRequest struct {
	WalletAddress string `json:"wallet_address"`
	Network       string `json:"network"`
	AmountUSDT    string `json:"amount_usdt"`
}

func (s *Server) handleNFCProvision(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req nfcProvisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	req.WalletAddress = strings.ToLower(strings.TrimSpace(req.WalletAddress))
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Network = normalizeStablecoinNetwork(req.Network)
	if req.Network == "" {
		req.Network = "BSC"
	}
	if !common.IsHexAddress(req.WalletAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet_address must be a valid EVM address"})
		return
	}
	if req.DeviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "device_id is required"})
		return
	}
	if req.Network != "BSC" && req.Network != "POLYGON" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network must be BSC or POLYGON"})
		return
	}
	ttl := time.Duration(s.cfg.NFCTokenTTLSeconds) * time.Second
	if req.TTLSeconds > 0 && req.TTLSeconds <= 300 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	now := time.Now().UTC()
	token, claims, err := nfc.IssueToken(s.cfg.NFCTokenSecret, req.WalletAddress, req.DeviceID, req.Network, ttl, now)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
		return
	}
	if err := s.db.StoreNFCToken(r.Context(), database.NFCTokenInput{
		TokenID:   claims.TokenID,
		TokenHash: nfc.TokenHash(token),
		Wallet:    claims.Wallet,
		DeviceID:  claims.DeviceID,
		Network:   claims.Network,
		ExpiresAt: time.Unix(claims.ExpiresAtUnix, 0),
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"token_id":   claims.TokenID,
		"expires_at": time.Unix(claims.ExpiresAtUnix, 0).UTC(),
		"network":    claims.Network,
		"next_step":  "Send this opaque token through the Android HCE APDU response; the ChainFX terminal submits it to POST /api/nfc/authorize.",
	})
}

func (s *Server) handleNFCAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req nfcAuthorizeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	req.Currency = strings.ToUpper(strings.TrimSpace(defaultString(req.Currency, "BRL")))
	req.MerchantID = strings.TrimSpace(req.MerchantID)
	req.TerminalID = strings.TrimSpace(req.TerminalID)
	req.ExternalRef = strings.TrimSpace(req.ExternalRef)
	req.IdempotencyKey = firstNonEmpty(strings.TrimSpace(req.IdempotencyKey), strings.TrimSpace(r.Header.Get("Idempotency-Key")), strings.TrimSpace(r.Header.Get("X-Idempotency-Key")))
	if req.Token == "" || req.MerchantID == "" || req.TerminalID == "" || req.IdempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "token, merchant_id, terminal_id and idempotency_key are required"})
		return
	}
	if req.Currency != "BRL" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "only BRL NFC authorizations are currently supported"})
		return
	}
	amount, err := money.ParseMoney(req.AmountBRL)
	if err != nil || amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_brl must be a positive decimal amount"})
		return
	}
	if s.cfg.NFCMaxAmountBRL > 0 && amount.Float64() > s.cfg.NFCMaxAmountBRL {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "amount exceeds NFC_MAX_AMOUNT_BRL", "code": "NFC_AMOUNT_LIMIT"})
		return
	}

	claims, err := nfc.VerifyToken(s.cfg.NFCTokenSecret, req.Token, time.Now().UTC())
	if err != nil {
		status := http.StatusUnauthorized
		code := "NFC_INVALID_TOKEN"
		if errors.Is(err, nfc.ErrExpiredToken) {
			code = "NFC_TOKEN_EXPIRED"
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "code": code, "response_code": "05"})
		return
	}
	usdtRate := s.workers.PriceWorker.GetPrice("BRL")
	if usdtRate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "USDT/BRL rate unavailable"})
		return
	}
	required := money.TokensFromFiat(amount, money.RateFromFloat(usdtRate))
	if required <= 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "invalid required USDT amount"})
		return
	}

	auth, idempotent, err := s.db.AuthorizeNFCPayment(r.Context(), database.NFCAuthorizeInput{
		ID:              "nfc_auth_" + database.NewAccessToken()[:24],
		IdempotencyKey:  req.IdempotencyKey,
		TokenID:         claims.TokenID,
		TokenHash:       nfc.TokenHash(req.Token),
		Wallet:          claims.Wallet,
		Network:         claims.Network,
		MerchantID:      req.MerchantID,
		TerminalID:      req.TerminalID,
		ExternalRef:     req.ExternalRef,
		AmountBRLMinor:  int64(amount),
		USDTRate:        usdtRate,
		RequiredUSDTMic: int64(required),
		HoldExpiresAt:   time.Now().UTC().Add(time.Duration(s.cfg.NFCHoldTTLSeconds) * time.Second),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	auth.Idempotent = idempotent
	statusCode := http.StatusOK
	if auth.Status == database.NFCStatusApproved {
		statusCode = http.StatusAccepted
	}
	writeJSON(w, statusCode, nfcAuthorizationView(auth))
}

func (s *Server) handleNFCGetAuthorization(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	auth, err := s.db.GetNFCAuthorization(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	if auth == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "authorization not found"})
		return
	}
	writeJSON(w, http.StatusOK, nfcAuthorizationView(auth))
}

func (s *Server) handleNFCCaptureAuthorization(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	auth, err := s.db.CaptureNFCAuthorization(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "NFC_CAPTURE_FAILED"})
		return
	}
	if auth == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "authorization not found"})
		return
	}
	writeJSON(w, http.StatusOK, nfcAuthorizationView(auth))
}

func (s *Server) handleNFCReverseAuthorization(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	auth, err := s.db.ReverseNFCAuthorization(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "NFC_REVERSAL_FAILED"})
		return
	}
	if auth == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "authorization not found"})
		return
	}
	writeJSON(w, http.StatusOK, nfcAuthorizationView(auth))
}

func (s *Server) handleNFCBalance(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	wallet := strings.ToLower(strings.TrimSpace(r.PathValue("wallet")))
	if !common.IsHexAddress(wallet) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet must be a valid EVM address"})
		return
	}
	network := normalizeStablecoinNetwork(r.URL.Query().Get("network"))
	if network == "" {
		network = "BSC"
	}
	bal, err := s.db.GetNFCBalance(r.Context(), wallet, network)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nfcBalanceView(bal))
}

func (s *Server) handleNFCSandboxFund(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	if !s.cfg.AllowSimulations {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "sandbox funding is disabled when ALLOW_SIMULATIONS=false"})
		return
	}
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req nfcSandboxFundRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	req.WalletAddress = strings.ToLower(strings.TrimSpace(req.WalletAddress))
	req.Network = normalizeStablecoinNetwork(req.Network)
	if req.Network == "" {
		req.Network = "BSC"
	}
	if !common.IsHexAddress(req.WalletAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet_address must be a valid EVM address"})
		return
	}
	amount, err := money.ParseToken(req.AmountUSDT)
	if err != nil || amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_usdt must be a positive decimal amount"})
		return
	}
	bal, err := s.db.AddNFCBalance(r.Context(), database.NFCFundingInput{
		Wallet:     req.WalletAddress,
		Network:    req.Network,
		Asset:      "USDT",
		DeltaMicro: int64(amount),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nfcBalanceView(bal))
}

func (s *Server) nfcReady(w http.ResponseWriter) bool {
	if s == nil || s.cfg == nil || !s.cfg.NFCEnabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "NFC rail is disabled", "code": "NFC_DISABLED"})
		return false
	}
	if strings.TrimSpace(s.cfg.NFCTokenSecret) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "NFC_TOKEN_SECRET is not configured", "code": "NFC_SECRET_MISSING"})
		return false
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database unavailable", "code": "DB_UNAVAILABLE"})
		return false
	}
	return true
}

func nfcAuthorizationView(a *database.NFCAuthorization) map[string]any {
	out := map[string]any{
		"authorization_id": a.ID,
		"token_id":         a.TokenID,
		"wallet_address":   a.Wallet,
		"network":          a.Network,
		"merchant_id":      a.MerchantID,
		"terminal_id":      a.TerminalID,
		"amount_brl":       fmt.Sprintf("%.2f", float64(a.AmountBRLMinor)/100),
		"required_usdt":    fmt.Sprintf("%.6f", float64(a.RequiredUSDTMic)/1_000_000),
		"usdt_rate":        fmt.Sprintf("%.4f", a.USDTRate),
		"status":           a.Status,
		"response_code":    a.ResponseCode,
		"reason":           a.Reason,
		"idempotent":       a.Idempotent,
		"created_at":       a.CreatedAt,
	}
	if a.ExternalRef != "" {
		out["external_ref"] = a.ExternalRef
	}
	if a.HoldExpiresAt != nil {
		out["hold_expires_at"] = *a.HoldExpiresAt
	}
	return out
}

func nfcBalanceView(b *database.NFCBalance) map[string]any {
	return map[string]any{
		"wallet_address": b.Wallet,
		"network":        b.Network,
		"asset":          b.Asset,
		"available_usdt": fmt.Sprintf("%.6f", float64(b.AvailableMicro)/1_000_000),
		"locked_usdt":    fmt.Sprintf("%.6f", float64(b.LockedMicro)/1_000_000),
		"updated_at":     b.UpdatedAt,
	}
}
