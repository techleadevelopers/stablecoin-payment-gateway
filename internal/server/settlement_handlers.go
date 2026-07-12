package server

import (
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/models"
	"payment-gateway/internal/settlement"
	"payment-gateway/internal/workers"
)

func (s *Server) handleDeposit(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invÃ¡lida"})
		return
	}
	var req struct {
		TxHash string  `json:"txHash"`
		Amount float64 `json:"amount"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.TxHash == "" || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invÃ¡lido"})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "status atual não permite depÃ³sito"})
		return
	}
	if err := s.db.UpdateOrderStatus(r.Context(), id, "pago", map[string]any{"requestId": requestID(r), "depositTx": req.TxHash, "depositAmount": req.Amount}); err != nil {
		writeError(w, err)
		return
	}
	s.workers.Bus.Publish(workers.Event{Type: "onchain.detected", OrderID: id, Payload: map[string]any{"tx_hash": req.TxHash, "amount_usdt": req.Amount}})
	s.workers.Bus.Publish(workers.Event{Type: "payout.requested", OrderID: id})
	s.email.NotifyOps("Swappy: depÃ³sito detectado", fmt.Sprintf("Ordem %s recebeu depÃ³sito %s no valor %.8f USDT.", id, req.TxHash, req.Amount))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePayout(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invÃ¡lida"})
		return
	}
	var req struct {
		ProviderID string `json:"providerId"`
		Status     string `json:"status"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.ProviderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invÃ¡lido"})
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
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invÃ¡lida"})
		return
	}
	var req struct {
		OrderID    string `json:"orderId"`
		Status     string `json:"status"`
		ProviderID string `json:"providerId"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.OrderID == "" || req.ProviderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invÃ¡lido"})
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
	// SECURITY: once an operator has configured a Pix webhook secret, every
	// request must carry a valid signature or hmac param — in every
	// environment, not only production. Gating this on IsProduction() left
	// staging/dev/test deployments (which still hold real Efí credentials
	// and can move real settlement state) open to a fully unauthenticated
	// forged settlement callback. Leaving the secret unset (local-only,
	// never real credentials) remains the only way to run unauthenticated.
	if secret != "" && signature == "" && queryHMAC == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "webhook sem autenticacao adicional"})
		return
	}
	// PSP abstraction path: when a Router is wired (cmd/api/main.go), normalise
	// the body through the active provider adapter and process every PIX
	// payment event independently — Efí batches multiple events per POST, and
	// each one may settle a different buy order.
	if s.pspRouter != nil {
		statusCode, body := s.handlePixWebhookBuyViaPSP(r, raw, secret)
		writeJSON(w, statusCode, body)
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

// handlePixWebhookBuyViaPSP parses raw through the wired PSP Router and applies
// every normalised PixWebhookPayload as an independent buy-order settlement.
// It never mutates HTTP state itself — callers write the returned status/body —
// so it stays trivially testable without an httptest.Recorder.
func (s *Server) handlePixWebhookBuyViaPSP(r *http.Request, raw []byte, secret string) (statusCode int, body map[string]any) {
	payloads, err := s.pspRouter.ParseWebhookAll(r.Context(), raw, secret)
	if err != nil {
		return http.StatusBadRequest, map[string]any{"error": "payload invalido: " + err.Error()}
	}

	anyApplied := false
	for _, p := range payloads {
		buyID := buyIDFromEfiTxID(p.TXID)
		if strings.TrimSpace(buyID) == "" {
			buyID = p.EndToEndID
		}
		providerID := firstNonEmpty(p.EndToEndID, p.TXID)
		if buyID == "" || providerID == "" {
			continue // no way to correlate this event to an order; skip rather than fail the whole batch
		}

		status := settlement.PixWebhookStatus("CONCLUIDA")
		extra := map[string]any{
			"requestId":         requestID(r),
			"providerPaymentId": providerID,
			"pspProvider":       p.Provider,
			"amountBRL":         p.AmountBRL,
		}
		duplicate, err := s.db.ApplyBuyProviderWebhook(r.Context(), buyID, providerID, "CONCLUIDA", status, extra)
		if err != nil {
			return http.StatusInternalServerError, map[string]any{"error": err.Error()}
		}
		if duplicate {
			continue
		}
		anyApplied = true
		if settlement.ShouldPublishBuyPaid(status) {
			s.workers.Bus.Publish(workers.Event{Type: "buy.paid", OrderID: buyID, Payload: map[string]any{"providerId": providerID}})
		}
	}

	return http.StatusOK, map[string]any{"ok": true, "processed": len(payloads), "applied": anyApplied}
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
