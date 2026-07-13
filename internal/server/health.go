package server

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
)

func (s *Server) handleWebAvailability(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"surface":   "web",
		"path":      r.URL.Path,
		"ready":     "/readyz",
		"health":    "/healthz",
		"dashboard": "/developers/dashboard",
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "db": false, "error": err.Error()})
		return
	}
	certOK, certErr := s.efiCertificateReady()
	gaps := s.operationalGapsWithCertificate(certOK)
	status := http.StatusOK
	if s.cfg.IsProduction() && (len(gaps) > 0 || !certOK) {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{
		"ok":              len(gaps) == 0 && certOK,
		"db":              true,
		"network":         s.deliveryNetwork(),
		"bsc":             s.cfg.BscRpcUrls != "" && s.cfg.BscUsdtContract != "",
		"pix":             s.efiPixConfigured() && certOK && defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret) != "",
		"efi_card":        s.efiChargesConfigured() && certOK,
		"efi_certificate": certOK,
		"efi_cert_source": s.efiCertificateSource(),
		"efi_cert_path":   s.cfg.EfiCertificatePath,
		"efi_cert_error":  certErr,
		"signer":          s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		"mode":            s.cfg.Environment,
		"warnings":        gaps,
	})
}

func (s *Server) operationalGaps() []string {
	certOK, _ := s.efiCertificateReady()
	return s.operationalGapsWithCertificate(certOK)
}

func (s *Server) operationalGapsWithCertificate(certOK bool) []string {
	checks := map[string]bool{
		"pix_provider":    s.efiPixConfigured(),
		"efi_certificate": certOK,
		"pix_webhook":     defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret) != "",
		"efi_card":        s.efiChargesConfigured(),
		"signer":          s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		"signer_private":  !strings.Contains(strings.ToLower(strings.TrimSpace(s.cfg.SignerUrl)), "up.railway.app"),
		"lgpd_secret":     s.cfg.LGPDSecret != "",
		"no_simulations":  !s.cfg.AllowSimulations,
		"sweep_not_stub":  !s.cfg.EnableSweepStub,
		"treasury_hot":    s.cfg.TreasuryHot != "",
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
	Valid         bool
	Sandbox       bool
	Mode          string
	ProjectID     string
	APIKeyID      string
	APIKeyLogHash string
	Scopes        []string
}

func (s *Server) authorizeAdmin(w http.ResponseWriter, r *http.Request) (*database.AdminUser, chainFXAuth, bool) {
	if !s.authorizeAdminConsoleKey(w, r) {
		return nil, chainFXAuth{}, false
	}
	token := chainFXAPIKey(r)
	user, err := s.db.ValidateAdminSession(r.Context(), token)
	if err != nil {
		writeError(w, err)
		return nil, chainFXAuth{}, false
	}
	if user != nil {
		return user, chainFXAuth{Valid: true, Mode: "admin"}, true
	}
	auth := s.chainFXAuthContext(r)
	if auth.Valid {
		if auth.Sandbox && s.cfg.IsProduction() {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "sandbox API keys cannot create live orders",
				"hint":  "use an admin account or a live server API key",
			})
			return nil, chainFXAuth{}, false
		}
		return &database.AdminUser{Email: "api-key", Role: auth.Mode}, auth, true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": "admin login required",
		"hint":  "POST /api/admin/login with email and password, then send Authorization: Bearer <token>",
	})
	return nil, chainFXAuth{}, false
}

func (s *Server) authorizeAdminConsoleKey(w http.ResponseWriter, r *http.Request) bool {
	expected := ""
	if s != nil && s.cfg != nil {
		expected = strings.TrimSpace(s.cfg.AdminConsoleKey)
	}
	if expected == "" {
		return true
	}
	got := strings.TrimSpace(r.Header.Get("X-Admin-Console-Key"))
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "palavra-chave administrativa invalida",
			"hint":  "send X-Admin-Console-Key with the console keyword configured in ADMIN_CONSOLE_KEY",
		})
		return false
	}
	return true
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
