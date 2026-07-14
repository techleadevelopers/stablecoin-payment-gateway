// Package mobile implements all /api/mobile/... endpoints for the React Native app.
// It registers routes on its own ServeMux and wraps the existing HTTP handler,
// so existing routes are untouched.
package mobile

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/workers"
)

// MobileConfig holds mobile-specific env vars (separate from main Config).
type MobileConfig struct {
	JWTSecret            string
	JWTExpiresMin        int
	RefreshSecret        string
	RefreshExpiresDays   int
	FCMServerKey         string
	MobileAllowedOrigins string
}

func loadMobileConfig() *MobileConfig {
	const (
		defaultJWTSecret     = "change_me_at_least_32_chars_secret"
		defaultRefreshSecret = "change_me_refresh_secret_32chars"
	)
	jwtSecret := envOr("MOBILE_JWT_SECRET", envOr("JWT_SECRET", defaultJWTSecret))
	refreshSecret := envOr("MOBILE_REFRESH_SECRET", envOr("JWT_SECRET", defaultRefreshSecret))

	// SECURITY: refuse to start with insecure default secrets in production.
	appEnv := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	isProduction := appEnv == "production" || appEnv == "prod"
	if isProduction && (jwtSecret == defaultJWTSecret || refreshSecret == defaultRefreshSecret) {
		panic("FATAL: MOBILE_JWT_SECRET e MOBILE_REFRESH_SECRET devem ser definidos em producao. " +
			"Nunca use os valores padrao em ambiente real.")
	}
	if jwtSecret == defaultJWTSecret || refreshSecret == defaultRefreshSecret {
		_, _ = os.Stderr.WriteString("[SECURITY WARNING] MOBILE_JWT_SECRET/MOBILE_REFRESH_SECRET estao usando " +
			"valores padrao inseguros. Defina as variaveis de ambiente antes de ir para producao.\n")
	}
	if len(jwtSecret) < 32 {
		panic("FATAL: MOBILE_JWT_SECRET deve ter pelo menos 32 caracteres.")
	}

	return &MobileConfig{
		JWTSecret:            jwtSecret,
		JWTExpiresMin:        envInt("MOBILE_JWT_EXPIRES_MIN", 15),
		RefreshSecret:        refreshSecret,
		RefreshExpiresDays:   envInt("MOBILE_REFRESH_EXPIRES_DAYS", 7),
		FCMServerKey:         envOr("FCM_SERVER_KEY", ""),
		MobileAllowedOrigins: envOr("ALLOWED_ORIGINS", "*"),
	}
}

// Server is the mobile API server.
type Server struct {
	cfg     *config.Config
	mcfg    *MobileConfig
	db      *database.DB
	workers *workers.WorkerManager
	hub     *wsHub
	cacheMu sync.RWMutex
	cache   map[string]mobileCacheEntry
}

// New creates a new mobile Server.
func New(cfg *config.Config, db *database.DB, wm *workers.WorkerManager) *Server {
	s := &Server{
		cfg:     cfg,
		mcfg:    loadMobileConfig(),
		db:      db,
		workers: wm,
		hub:     newWsHub(),
		cache:   make(map[string]mobileCacheEntry),
	}
	go s.hub.run()
	return s
}

// Wrap returns an http.Handler that handles /api/mobile/... and delegates
// everything else to the provided existing handler — zero changes to existing routes.
func (s *Server) Wrap(existing http.Handler) http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/mobile/") {
			s.applyCORS(w, r)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			mux.ServeHTTP(w, r)
			return
		}
		existing.ServeHTTP(w, r)
	})
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	allowed := "*"
	if s != nil && s.mcfg != nil {
		allowed = strings.TrimSpace(s.mcfg.MobileAllowedOrigins)
	}
	if allowed == "" {
		allowed = "*"
	}
	w.Header().Add("Vary", "Origin")
	if allowed == "*" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		if isMobileAllowedOrigin(origin, allowed) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
	}
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Idempotency-Key, X-Request-Id, X-Correlation-Id, X-Trace-Id")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id, Idempotency-Key, Idempotency-Key-Source, Idempotent-Replayed")
	w.Header().Set("Access-Control-Max-Age", "600")
}

func isMobileAllowedOrigin(origin, configured string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return false
	}
	for _, item := range strings.Split(configured, ",") {
		if strings.TrimSpace(item) == origin {
			return true
		}
	}
	normalized := strings.TrimSuffix(strings.ToLower(origin), "/")
	if normalized == "https://chainfx.store" || normalized == "https://www.chainfx.store" {
		return true
	}
	return strings.HasPrefix(normalized, "https://swapped-cryptocurrensy-") &&
		strings.HasSuffix(normalized, "-d3v-techle4ds-projects.vercel.app")
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// ── Auth ──────────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/mobile/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/mobile/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/mobile/auth/refresh", s.handleRefresh)
	mux.HandleFunc("POST /api/mobile/auth/logout", s.requireAuth(s.handleLogout))

	// ── User / KYC ────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/mobile/user/profile", s.requireAuth(s.handleGetProfile))
	mux.HandleFunc("PUT /api/mobile/user/profile", s.requireAuth(s.handleUpdateProfile))
	mux.HandleFunc("POST /api/mobile/user/kyc", s.requireAuth(s.handleSubmitKYC))
	mux.HandleFunc("GET /api/mobile/user/kyc/status", s.requireAuth(s.handleKYCStatus))

	// ── Wallet ────────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/mobile/wallet/balance", s.requireAuth(s.handleWalletBalance))
	mux.HandleFunc("GET /api/mobile/wallet/tokens", s.requireAuth(s.handleWalletTokens))
	mux.HandleFunc("GET /api/mobile/wallet/address", s.requireAuth(s.handleWalletAddress))
	mux.HandleFunc("POST /api/mobile/wallet/generate", s.requireAuth(s.handleWalletGenerate))
	mux.HandleFunc("POST /api/mobile/wallet/transfer", s.requireAuth(s.handleWalletTransfer))
	mux.HandleFunc("GET /api/mobile/wallet/history", s.requireAuth(s.handleWalletHistory))

	// ── Orders ────────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/mobile/order/buy", s.requireAuth(s.requireIdempotency("mobile.order.buy", s.handleMobileBuy)))
	mux.HandleFunc("POST /api/mobile/order/sell", s.requireAuth(s.requireIdempotency("mobile.order.sell", s.handleMobileSell)))
	mux.HandleFunc("POST /api/mobile/order/swap", s.requireAuth(s.requireIdempotency("mobile.order.swap", s.handleMobileSwap)))
	mux.HandleFunc("GET /api/mobile/order/{id}", s.requireAuth(s.handleMobileGetOrder))
	mux.HandleFunc("GET /api/mobile/orders", s.requireAuth(s.handleMobileListOrders))
	mux.HandleFunc("POST /api/mobile/order/cancel", s.requireAuth(s.handleMobileCancelOrder))

	// ── PIX ───────────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/mobile/pix/generate", s.requireAuth(s.requireIdempotency("mobile.pix.generate", s.handlePixGenerate)))
	mux.HandleFunc("POST /api/mobile/pix/confirm", s.handlePixConfirm)
	mux.HandleFunc("GET /api/mobile/pix/status/{id}", s.requireAuth(s.handlePixStatus))
	mux.HandleFunc("POST /api/mobile/pix/copy", s.requireAuth(s.handlePixCopy))

	// ── DCA ───────────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/mobile/dca/create", s.requireAuth(s.requireIdempotency("mobile.dca.create", s.handleDCACreate)))
	mux.HandleFunc("GET /api/mobile/dca/strategies", s.requireAuth(s.handleDCAList))
	mux.HandleFunc("PUT /api/mobile/dca/{id}", s.requireAuth(s.handleDCAUpdate))
	mux.HandleFunc("DELETE /api/mobile/dca/{id}", s.requireAuth(s.handleDCADelete))
	mux.HandleFunc("GET /api/mobile/dca/{id}/status", s.requireAuth(s.handleDCAStatus))

	// ── Security ──────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/mobile/security/pin", s.requireAuth(s.handleSetPIN))
	mux.HandleFunc("POST /api/mobile/security/biometry", s.requireAuth(s.handleSetBiometry))
	mux.HandleFunc("POST /api/mobile/security/2fa", s.requireAuth(s.handleSet2FA))
	mux.HandleFunc("GET /api/mobile/security/devices", s.requireAuth(s.handleListDevices))
	mux.HandleFunc("DELETE /api/mobile/security/device", s.requireAuth(s.handleRemoveDevice))

	// ── Contracts ─────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/mobile/contracts/payout", s.requireAuth(s.handleContractPayout))
	mux.HandleFunc("GET /api/mobile/contracts/vault", s.requireAuth(s.handleContractVault))
	mux.HandleFunc("GET /api/mobile/contracts/delegate", s.requireAuth(s.handleContractDelegate))
	mux.HandleFunc("POST /api/mobile/contracts/pause", s.requireAuth(s.handleContractPause))
	mux.HandleFunc("POST /api/mobile/contracts/unpause", s.requireAuth(s.handleContractUnpause))

	// ── Notifications ─────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/mobile/notifications", s.requireAuth(s.handleListNotifications))
	mux.HandleFunc("PUT /api/mobile/notifications/read", s.requireAuth(s.handleMarkNotificationsRead))
	mux.HandleFunc("DELETE /api/mobile/notifications/{id}", s.requireAuth(s.handleDeleteNotification))
	mux.HandleFunc("POST /api/mobile/notifications/token", s.requireAuth(s.handleRegisterPushToken))

	// ── WebSocket ─────────────────────────────────────────────────────────────
	// WebSocket routes: price feed is public; order/notification feeds require a valid JWT.
	mux.HandleFunc("/api/mobile/ws/orders", s.requireAuth(s.handleWSOrders))
	mux.HandleFunc("/api/mobile/ws/price", s.handleWSPrice)
	mux.HandleFunc("/api/mobile/ws/notifications", s.requireAuth(s.handleWSNotifications))

	// ── Settings ──────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/mobile/settings", s.requireAuth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/mobile/settings", s.requireAuth(s.handleUpdateSettings))
	mux.HandleFunc("GET /api/mobile/settings/limits", s.requireAuth(s.handleGetLimits))

	// ── Health ────────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/mobile/health", s.handleHealth)

	// ── Phase 5: Multi-Asset ──────────────────────────────────────────────────
	mux.HandleFunc("GET /api/mobile/assets", s.handleListAssets)
	mux.HandleFunc("GET /api/mobile/assets/{symbol}", s.handleGetAsset)
	mux.HandleFunc("GET /api/mobile/assets/{symbol}/rate", s.handleGetAssetRate)

	// ── Phase 5: Multi-Country + Multi-Rail ───────────────────────────────────
	mux.HandleFunc("GET /api/mobile/countries", s.handleListCountries)
	mux.HandleFunc("GET /api/mobile/countries/detect", s.handleDetectCountry)
	mux.HandleFunc("GET /api/mobile/countries/{code}", s.handleGetCountry)
	mux.HandleFunc("GET /api/mobile/countries/{code}/rails", s.handleListCountryRails)

	// ── Phase 5: KYC async (non-blocking) ────────────────────────────────────
	mux.HandleFunc("POST /api/mobile/kyc/submit", s.requireAuth(s.handleKYCSubmit))
	mux.HandleFunc("GET /api/mobile/kyc/status", s.requireAuth(s.handleKYCStatusV2))
	mux.HandleFunc("GET /api/mobile/kyc/history", s.requireAuth(s.handleKYCHistory))
	mux.HandleFunc("GET /api/mobile/kyc/limits", s.requireAuth(s.handleKYCLimits))

	// ── Phase 5: Swap (crypto→crypto) ────────────────────────────────────────
	mux.HandleFunc("POST /api/mobile/swap/quote", s.requireAuth(s.handleSwapQuote))
	mux.HandleFunc("POST /api/mobile/swap/execute", s.requireAuth(s.requireIdempotency("mobile.swap.execute", s.handleSwapExecute)))
	mux.HandleFunc("GET /api/mobile/swap/{id}", s.requireAuth(s.handleGetSwap))
	mux.HandleFunc("GET /api/mobile/swaps", s.requireAuth(s.handleListSwaps))

	// ── Phase 5: Webhooks (n8n / Zapier / Make) ───────────────────────────────
	mux.HandleFunc("GET /api/mobile/webhooks/events", s.handleListWebhookEvents)
	mux.HandleFunc("POST /api/mobile/webhooks/subscribe", s.requireAuth(s.handleWebhookSubscribe))
	mux.HandleFunc("GET /api/mobile/webhooks", s.requireAuth(s.handleListWebhooks))
	mux.HandleFunc("DELETE /api/mobile/webhooks/{id}", s.requireAuth(s.handleDeleteWebhook))
	mux.HandleFunc("PUT /api/mobile/webhooks/{id}/toggle", s.requireAuth(s.handleToggleWebhook))
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

// ============================================
// 🔷 MÉTODOS ADICIONAIS DO SERVER
// ============================================

// PriceCache returns the PriceWorker for price queries
// Usado pelo orders.go para pegar preços em tempo real
func (s *Server) PriceCache() *workers.PriceWorker {
	if s.workers == nil {
		return nil
	}
	return s.workers.PriceWorker
}

// GetWorkerManager returns the full WorkerManager
func (s *Server) GetWorkerManager() *workers.WorkerManager {
	return s.workers
}

// GetDB returns the database connection
func (s *Server) GetDB() *database.DB {
	return s.db
}

// GetConfig returns the main config
func (s *Server) GetConfig() *config.Config {
	return s.cfg
}

// GetMobileConfig returns the mobile config
func (s *Server) GetMobileConfig() *MobileConfig {
	return s.mcfg
}
