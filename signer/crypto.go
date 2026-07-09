package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"payment-gateway/internal/security" // Importe o novo pacote
)

// Configuração do signer
type Config struct {
	Port          string
	HMACSecret    string
	HMACMaxSkew   int64
	NonceTTL      time.Duration
	RateLimit     int
	RateWindow    time.Duration
	MaxBodySize   int64
	SignerNetwork string
	BSCFullNode   string
	BSCUSDT       string
	BSCXPub       string
}

func main() {
	cfg := loadConfig()

	// 1. Configurar Nonce Store (em memória ou Redis)
	nonceStore := security.NewInMemoryNonceStore()

	// 2. Configurar validador de requisições
	validatorConfig := security.RequestValidatorConfig{
		HMACSecret:      cfg.HMACSecret,
		HMACMaxSkew:     cfg.HMACMaxSkew,
		NonceStore:      nonceStore,
		NonceTTL:        cfg.NonceTTL,
		RateLimit:       cfg.RateLimit,
		RateWindow:      cfg.RateWindow,
		RateLimiterType: security.TokenBucketType,
		MaxBodySize:     cfg.MaxBodySize,
	}

	validator := security.NewRequestValidator(validatorConfig)

	// 3. Configurar middleware
	middleware := security.NewMiddleware(validator, security.SecurityOptions{
		Enabled:       true,
		RequireHMAC:   true,
		RequireNonce:  true,
		RequireAPIKey: false,
		ExcludePaths:  []string{"/healthz", "/readyz"},
	})

	// 4. Configurar rotas
	mux := http.NewServeMux()

	// Rotas protegidas
	mux.Handle("/hd/transfer", middleware.Handler(http.HandlerFunc(handleTransfer)))
	mux.Handle("/hd/derive", middleware.Handler(http.HandlerFunc(handleDerive)))
	mux.Handle("/custody/unlock", middleware.Handler(http.HandlerFunc(handleUnlock)))

	// Rotas públicas (excluídas do middleware via ExcludePaths)
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/readyz", handleReadyz)

	// 5. Iniciar servidor
	log.Printf("Signer server running on port %s", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, mux); err != nil {
		log.Fatal(err)
	}
}

// --- Handlers ---

func handleTransfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Recupera contexto da requisição (já validado pelo middleware)
	reqCtx := security.GetRequestContext(r.Context())
	if reqCtx == nil {
		http.Error(w, "Request context not found", http.StatusInternalServerError)
		return
	}

	// Parse do body
	var req struct {
		Destination string `json:"destination"`
		Amount      string `json:"amount"`
		Asset       string `json:"asset"`
		Network     string `json:"network"`
		Idempotency string `json:"idempotencyKey"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Log da transação com RequestID
	log.Printf("[%s] Transfer request: dest=%s, amount=%s, asset=%s",
		reqCtx.RequestID, req.Destination, req.Amount, req.Asset)

	// ... lógica de transferência ...

	response := map[string]interface{}{
		"status":    "ok",
		"txHash":    "0x...",
		"requestId": reqCtx.RequestID,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleDerive(w http.ResponseWriter, r *http.Request) {
	// Similar ao handleTransfer
}

func handleUnlock(w http.ResponseWriter, r *http.Request) {
	// Similar ao handleTransfer
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func handleReadyz(w http.ResponseWriter, r *http.Request) {
	// Verifica se o signer está pronto para operações
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ready"}`))
}

// --- Configuração ---

func loadConfig() Config {
	return Config{
		Port:          getEnv("PORT", "4010"),
		HMACSecret:    getEnv("SIGNER_HMAC_SECRET", ""),
		HMACMaxSkew:   getEnvAsInt64("SIGNER_HMAC_MAX_SKEW", 300),
		NonceTTL:      time.Duration(getEnvAsInt64("SIGNER_NONCE_TTL", 300)) * time.Second,
		RateLimit:     getEnvAsInt("SIGNER_RATE_LIMIT", 100),
		RateWindow:    time.Duration(getEnvAsInt64("SIGNER_RATE_WINDOW", 60)) * time.Second,
		MaxBodySize:   getEnvAsInt64("SIGNER_MAX_BODY_SIZE", 1024*1024), // 1MB
		SignerNetwork: getEnv("SIGNER_NETWORK", "BSC"),
		BSCFullNode:   getEnv("BSC_FULLNODE_URL", ""),
		BSCUSDT:       getEnv("BSC_USDT_CONTRACT", ""),
		BSCXPub:       getEnv("BSC_XPUB", ""),
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvAsInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvAsInt64(key string, fallback int64) int64 {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return i
		}
	}
	return fallback
}
