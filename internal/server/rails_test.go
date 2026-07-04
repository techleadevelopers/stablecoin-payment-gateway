package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/workers"
)

func TestNormalizePaymentRailPixBRL(t *testing.T) {
	currency, method, amount := normalizePaymentRail("", "", 0, 150, 0)
	if currency != "BRL" || method != "pix" || amount != 150 {
		t.Fatalf("unexpected rail: %s %s %.2f", currency, method, amount)
	}
}

func TestNormalizePaymentRailStripeUSD(t *testing.T) {
	currency, method, amount := normalizePaymentRail("USD", "stripe", 0, 0, 25)
	if currency != "USD" || method != "stripe" || amount != 25 {
		t.Fatalf("unexpected rail: %s %s %.2f", currency, method, amount)
	}
}

func TestNormalizePaymentRailRejectsUnsupported(t *testing.T) {
	currency, method, amount := normalizePaymentRail("USD", "pix", 10, 0, 0)
	if currency != "" || method != "" || amount != 0 {
		t.Fatalf("expected unsupported rail to be rejected")
	}
}

func TestValidStripeSignature(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"id":"evt_123"}`)
	ts := fmt.Sprintf("%d", time.Now().Unix())
	header := stripeHeader(secret, ts, body)

	if !validStripeSignature(secret, body, header, 5*time.Minute) {
		t.Fatal("expected valid stripe signature")
	}
	if validStripeSignature(secret, []byte(`{"id":"evt_tampered"}`), header, 5*time.Minute) {
		t.Fatal("expected tampered body to fail signature validation")
	}
}

func TestValidStripeSignatureRejectsExpiredTimestamp(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"id":"evt_123"}`)
	ts := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())

	if validStripeSignature(secret, body, stripeHeader(secret, ts, body), 5*time.Minute) {
		t.Fatal("expected expired stripe signature to be rejected")
	}
}

func TestCustomerAccessTokenPrefersHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/buy/id?accessToken=query-token", nil)
	req.Header.Set("X-Customer-Access-Token", "header-token")

	if got := customerAccessToken(req); got != "header-token" {
		t.Fatalf("expected header token, got %q", got)
	}
}

func TestPixBuyWebhookRequiresProviderID(t *testing.T) {
	secret := "pix-secret"
	body := []byte(`{"buyId":"00000000-0000-4000-8000-000000000001","status":"concluido"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pix/webhook/buy", strings.NewReader(string(body)))
	req.Header.Set("x-pagbank-signature", rawHMAC(secret, body))
	rec := httptest.NewRecorder()

	s := &Server{cfg: &config.Config{PixWebhookSecret: secret}, workers: &workers.WorkerManager{}}
	s.handlePixWebhookBuy(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing providerId to be rejected with 400, got %d", rec.Code)
	}
}

func TestEmailTestRequiresInternalHMAC(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/internal/email/test", strings.NewReader(`{"to":"ops@example.com"}`))
	rec := httptest.NewRecorder()

	s := &Server{cfg: &config.Config{SignerHmacSecret: "internal-secret"}}
	s.handleEmailTest(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unsigned email test to be rejected, got %d", rec.Code)
	}
}

func rawHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func stripeHeader(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
