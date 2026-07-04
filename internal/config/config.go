package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config centraliza todas as variáveis do seu .env de forma tipada e segura
type Config struct {
	Environment            string
	AllowSimulations       bool
	DatabaseURL            string
	AllowedOrigins         string
	WebhookSecret          string
	StripeWebhookSecret    string
	Port                   string
	OrderMinBrl            float64
	OrderMaxBrl            float64
	RateLockSec            int
	RateLimitWindowMs      int
	RateLimitMax           int
	OrderRateLimitWindowMs int
	OrderRateLimitMax      int
	FeeBps                 int
	FeeFixedUsd            float64
	FeePerUsdtUsd          float64
	FeeMinBrl              float64
	BuyHotDerivationIndex  int

	// Regras de Limite e Fraude
	PixMaxOrdersPer24h     int
	PixMaxBrlPer24h        float64
	OrderHoldSecForNewDest int
	BscDepositTolerancePct float64

	// PagBank
	PagSeguroApiToken   string
	PagSeguroApiBaseUrl string
	PixWebhookSecret    string
	PixChargeEndpoint   string

	// Tesouraria / signer / sweep
	TreasuryHot       string
	TreasuryCold      string
	SignerUrl         string
	SignerNetwork     string
	SignerHmacSecret  string
	BscRpcUrls        string
	BscUsdtContract   string
	EnableSweepWorker bool
	EnableSweepStub   bool
	SweepBatchUsdtMin float64
	SweepBatchUsdtMax float64
	SweepFrequencyMs  int
	BscGasReserveBNB  float64

	// SMTP / mensagens
	SMTPHost      string
	SMTPPort      int
	SMTPUser      string
	SMTPPass      string
	SMTPSecure    bool
	SMTPFromEmail string
	SMTPFromName  string
	OpsEmail      string

	// LGPD / auditoria
	LGPDSecret string
}

// LoadConfig é o cara que lê o .env e joga para dentro da estrutura acima
func LoadConfig() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("Aviso: Arquivo .env não encontrado, usando variáveis de ambiente do sistema")
	}

	return &Config{
		Environment:            getEnv("APP_ENV", getEnv("GO_ENV", "development")),
		AllowSimulations:       getEnvAsBool("ALLOW_SIMULATIONS", true),
		DatabaseURL:            getEnv("DATABASE_URL", ""),
		AllowedOrigins:         getEnv("ALLOWED_ORIGINS", "http://localhost:5173"),
		WebhookSecret:          getEnv("WEBHOOK_SECRET", ""),
		StripeWebhookSecret:    getEnv("STRIPE_WEBHOOK_SECRET", ""),
		Port:                   getEnv("PORT", "8080"),
		OrderMinBrl:            getEnvAsFloat("ORDER_MIN_BRL", 10.0),
		OrderMaxBrl:            getEnvAsFloat("ORDER_MAX_BRL", 10000.0),
		RateLockSec:            getEnvAsInt("RATE_LOCK_SEC", 600),
		RateLimitWindowMs:      getEnvAsInt("RATE_LIMIT_WINDOW_MS", 60000),
		RateLimitMax:           getEnvAsInt("RATE_LIMIT_MAX", 100),
		OrderRateLimitWindowMs: getEnvAsInt("ORDER_RATE_LIMIT_WINDOW_MS", 60000),
		OrderRateLimitMax:      getEnvAsInt("ORDER_RATE_LIMIT_MAX", 20),
		FeeBps:                 getEnvAsInt("FEE_BPS", getEnvAsInt("TRANSACTION_FEE_BPS", 200)),
		FeeFixedUsd:            getEnvAsFloat("FEE_FIXED_USD", getEnvAsFloat("TRANSACTION_FEE_FIXED_USD", 0)),
		FeePerUsdtUsd:          getEnvAsFloat("FEE_PER_USDT_USD", 0.03),
		FeeMinBrl:              getEnvAsFloat("FEE_MIN_BRL", 0),
		BuyHotDerivationIndex:  getEnvAsInt("BUY_HOT_DERIVATION_INDEX", 0),

		PixMaxOrdersPer24h:     getEnvAsInt("PIX_MAX_ORDERS_PER_24H", 5),
		PixMaxBrlPer24h:        getEnvAsFloat("PIX_MAX_BRL_PER_24H", 20000.0),
		OrderHoldSecForNewDest: getEnvAsInt("ORDER_HOLD_SEC_FOR_NEW_DEST", 180),
		BscDepositTolerancePct: getEnvAsFloat("BSC_DEPOSIT_TOLERANCE_PCT", 0.02),

		PagSeguroApiToken:   getEnv("PAGSEGURO_API_TOKEN", ""),
		PagSeguroApiBaseUrl: getEnv("PAGSEGURO_API_BASE_URL", "https://api.pagseguro.com"),
		PixWebhookSecret:    getEnv("PIX_WEBHOOK_SECRET", ""),
		PixChargeEndpoint:   getEnv("PIX_CHARGE_ENDPOINT", "/orders"),

		TreasuryHot:       getEnv("TREASURY_HOT", ""),
		TreasuryCold:      getEnv("TREASURY_COLD", ""),
		SignerUrl:         getEnv("SIGNER_URL", ""),
		SignerNetwork:     strings.ToLower(getEnv("SIGNER_NETWORK", "bsc")),
		SignerHmacSecret:  getEnv("SIGNER_HMAC_SECRET", ""),
		BscRpcUrls:        getBscRpcUrls(),
		BscUsdtContract:   getEnv("BSC_USDT_CONTRACT", getEnv("BSC_TOKEN_CONTRACT", "")),
		EnableSweepWorker: getEnvAsBool("ENABLE_SWEEP_WORKER", false),
		EnableSweepStub:   getEnvAsBool("ENABLE_SWEEP_STUB", false),
		SweepBatchUsdtMin: getEnvAsFloat("SWEEP_BATCH_USDT_MIN", 0),
		SweepBatchUsdtMax: getEnvAsFloat("SWEEP_BATCH_USDT_MAX", 1_000_000),
		SweepFrequencyMs:  getEnvAsInt("SWEEP_FREQUENCY_MS", 80800),
		BscGasReserveBNB:  getEnvAsFloat("BSC_GAS_RESERVE_BNB", 0.003),

		SMTPHost:      getEnv("SMTP_HOST", ""),
		SMTPPort:      getEnvAsInt("SMTP_PORT", 587),
		SMTPUser:      getEnv("SMTP_USER", ""),
		SMTPPass:      getEnv("SMTP_PASS", ""),
		SMTPSecure:    getEnvAsBool("SMTP_SECURE", false),
		SMTPFromEmail: getEnv("SMTP_FROM_EMAIL", ""),
		SMTPFromName:  getEnv("SMTP_FROM_NAME", "Swappy Financial"),
		OpsEmail:      getEnv("OPS_EMAIL", getEnv("SMTP_FROM_EMAIL", "")),
		LGPDSecret:    getEnv("LGPD_SECRET", ""),
	}
}

func (c *Config) IsProduction() bool {
	env := strings.ToLower(strings.TrimSpace(c.Environment))
	return env == "production" || env == "prod"
}

func (c *Config) ValidateProduction() error {
	if !c.IsProduction() {
		return nil
	}
	required := map[string]string{
		"DATABASE_URL":        c.DatabaseURL,
		"LGPD_SECRET":         c.LGPDSecret,
		"WEBHOOK_SECRET":      c.WebhookSecret,
		"PIX_WEBHOOK_SECRET":  c.PixWebhookSecret,
		"SIGNER_URL":          c.SignerUrl,
		"SIGNER_HMAC_SECRET":  c.SignerHmacSecret,
		"PAGSEGURO_API_TOKEN": c.PagSeguroApiToken,
		"TREASURY_HOT":        c.TreasuryHot,
	}
	switch strings.ToLower(strings.TrimSpace(c.SignerNetwork)) {
	case "bsc", "evm":
		required["BSC_RPC_URLS"] = c.BscRpcUrls
		required["BSC_USDT_CONTRACT"] = c.BscUsdtContract
	default:
		return fmt.Errorf("SIGNER_NETWORK deve ser bsc em producao")
	}
	var missing []string
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("configuracao de producao incompleta: %s", strings.Join(missing, ", "))
	}
	if c.AllowSimulations {
		return fmt.Errorf("ALLOW_SIMULATIONS deve ser false em producao")
	}
	if c.EnableSweepStub {
		return fmt.Errorf("ENABLE_SWEEP_STUB deve ser false em producao")
	}
	return nil
}

// Auxiliares para leitura e conversão de tipos
func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getBscRpcUrls() string {
	if raw := strings.TrimSpace(getEnv("BSC_RPC_URLS", getEnv("RPC_URLS", getEnv("RPC_URL", "")))); raw != "" {
		return raw
	}
	var urls []string
	for _, key := range []string{"ALCHEMY_BSC_RPC_URL_1", "ALCHEMY_BSC_RPC_URL_2", "ALCHEMY_BSC_RPC_URL", "ALCHEMY_BSC_FALLBACK_RPC_URL"} {
		if url := strings.TrimSpace(getEnv(key, "")); url != "" {
			urls = append(urls, url)
		}
	}
	return strings.Join(urls, ",")
}

func getEnvAsInt(key string, defaultValue int) int {
	valueStr := getEnv(key, "")
	if value, err := strconv.Atoi(valueStr); err == nil {
		return value
	}
	return defaultValue
}

func getEnvAsFloat(key string, defaultValue float64) float64 {
	valueStr := getEnv(key, "")
	if value, err := strconv.ParseFloat(valueStr, 64); err == nil {
		return value
	}
	return defaultValue
}

func getEnvAsBool(key string, defaultValue bool) bool {
	valueStr := getEnv(key, "")
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.ParseBool(valueStr)
	if err != nil {
		return defaultValue
	}
	return value
}
