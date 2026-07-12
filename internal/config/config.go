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
        RateLimitBackend       string
        RedisURL               string
        OrderRateLimitWindowMs int
        OrderRateLimitMax      int
        FeeBps                 int
        FeeFixedUsd            float64
        FeePerUsdtUsd          float64
        FeeMinBrl              float64
        BuyTier1MinBrl         float64
        BuyTier1MaxBrl         float64
        BuyTier1Bps            int
        BuyTier2MaxBrl         float64
        BuyTier2Bps            int
        BuyTier3Bps            int
        BuyNetworkFeeBrl       float64
        BuyMinFeeBrl           float64
        BuyRateSpreadBps       int
        SellRateBps            int
        SellSpreadMinBps       int
        SellSpreadMaxBps       int
        SellSpreadHighValueBrl float64
        SellUsdtBrlRate        float64
        SellWalletAddress      string
        BuyHotDerivationIndex  int
        ChainFXLiveSecretKeys  string
        ChainFXTestSecretKeys  string
        ChainFXLivePublicKeys  string
        ChainFXTestPublicKeys  string
        ChainFXRequireAPIKey   bool
        InternalAllowedCIDRs   string
        AdminBootstrapEmail    string
        AdminBootstrapPassword string

        // Regras de Limite e Fraude
        PixMaxOrdersPer24h     int
        PixMaxBrlPer24h        float64
        OrderHoldSecForNewDest int
        BscDepositTolerancePct float64

        // Efí Bank Pix
        EfiClientID        string
        EfiClientSecret    string
        EfiPixKey          string
        EfiApiBaseURL      string
        EfiCertificatePath string
        EfiCertificateKey  string
        EfiCertificatePass string
        EfiCertificateP12  string
        EfiPixFeeBps       int
        PixWebhookSecret   string

        // Tesouraria / signer / sweep
        TreasuryHot         string
        TreasuryCold        string
        SignerUrl           string
        SignerNetwork       string
        SignerHmacSecret    string
        BscRpcUrls          string
        BscUsdtContract     string
        PolygonRpcUrls      string
        PolygonUsdtContract string
        EnableSweepWorker   bool
        EnableSweepStub     bool
        SweepBatchUsdtMin   float64
        SweepBatchUsdtMax   float64
        SweepFrequencyMs    int
        BscGasReserveBNB    float64

        // SMTP / mensagens
        SMTPHost       string
        SMTPPort       int
        SMTPUser       string
        SMTPPass       string
        SMTPSecure     bool
        SMTPFromEmail  string
        SMTPFromName   string
        OpsEmail       string
        EmailBrandName string
        EmailLogoURL   string
        EmailSiteURL   string
        EmailAddress   string
        SupportEmail   string

        // LGPD / auditoria
        LGPDSecret string

        // Webhooks
        WebhooksEnabled    bool
        WebhooksMaxRetries int

        // OpenAI / AI Agents
        OpenAIAPIKey  string
        OpenAIModel   string
        OpenAIBaseURL string

        // Capability provider adapters
        CapabilityOCRURL    string
        CapabilityOCRAPIKey string

        // M2M Agent Payments (PIX + credit-card on behalf of AI agents)
        M2MPixFeeBps           int     // default 1000 = 10%
        M2MCreditFeeBps        int     // default 1900 = 19%
        M2MDepositTolerancePct float64 // fraction tolerance for on-chain amount match (default 0.005)
        M2MMaxDailyOutflowBRL  float64 // max BRL settled per 24 h (0 = unlimited)

        // On-chain reorg protection — minimum block confirmations before accepting a deposit.
        // BSC is finalistic with low reorg depth; Polygon can have deep reorgs.
        BSCMinConfirmations     uint64 // default 6
        PolygonMinConfirmations uint64 // default 128

        // Gas Station (Paymaster) — subsidise gas on behalf of AI agents.
        // Agents submit EIP-712 permits; ChainFX pays BNB/POL gas and deducts
        // the equivalent USDT fee from the user's on-chain balance.
        GasStationEnabled          bool
        GasStationSurchargeBps     int     // extra fee margin on top of raw gas cost (default 1000 = 10%)
        GasStationMaxFeeUsdt       float64 // per-relay USDT fee ceiling (default 2.00)
        GasStationMinFeeUsdt       float64 // per-relay USDT fee floor   (default 0.05)
        PaymasterMulticallContract string  // Multicall3 address (empty → 0xcA11bde…CA11)
        // TokenRelayer spread — hidden fee retained from the token amount before delivery.
        // The user pays "Taxa Zero"; ChainFX earns via the embedded spread.
        // Default 100 bps = 1 % (e.g. user moves 100 USDT, 1 USDT stays in TreasuryHot).
        PaymasterPrivKey        string // hex ECDSA key for the paymaster hot wallet (optional)
        PaymasterRelaySpreadBps int    // spread in BPS captured per relay (default 100 = 1 %)

        // Auto-Sweeper — moves hot wallet excess USDT to cold wallet / Gnosis Safe.
        AutoSweeperEnabled     bool
        AutoSweeperIntervalSec int     // polling interval in seconds (default 60)
        AutoSweeperHotMaxUsdt  float64 // sweep threshold — trigger when balance exceeds this (default 5000)
        AutoSweeperHotMinUsdt  float64 // minimum reserve to keep in hot wallet after sweep (default 500)
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
                AllowedOrigins:         getEnv("ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173,https://swapped-cryptocurrensy.vercel.app,https://www.chainfx.store,https://chainfx.store"),
                WebhookSecret:          getEnv("WEBHOOK_SECRET", ""),
                StripeWebhookSecret:    getEnv("STRIPE_WEBHOOK_SECRET", ""),
                Port:                   getEnv("PORT", "8080"),
                OrderMinBrl:            getEnvAsFloat("ORDER_MIN_BRL", 10.0),
                OrderMaxBrl:            getEnvAsFloat("ORDER_MAX_BRL", 10000.0),
                RateLockSec:            getEnvAsInt("RATE_LOCK_SEC", 600),
                RateLimitWindowMs:      getEnvAsInt("RATE_LIMIT_WINDOW_MS", 60000),
                RateLimitMax:           getEnvAsInt("RATE_LIMIT_MAX", 100),
                RateLimitBackend:       strings.ToLower(getEnv("RATE_LIMIT_BACKEND", "memory")),
                RedisURL:               getEnv("REDIS_URL", ""),
                OrderRateLimitWindowMs: getEnvAsInt("ORDER_RATE_LIMIT_WINDOW_MS", 60000),
                OrderRateLimitMax:      getEnvAsInt("ORDER_RATE_LIMIT_MAX", 20),
                FeeBps:                 getEnvAsInt("FEE_BPS", getEnvAsInt("TRANSACTION_FEE_BPS", 200)),
                FeeFixedUsd:            getEnvAsFloat("FEE_FIXED_USD", getEnvAsFloat("TRANSACTION_FEE_FIXED_USD", 0)),
                FeePerUsdtUsd:          getEnvAsFloat("FEE_PER_USDT_USD", 0.03),
                FeeMinBrl:              getEnvAsFloat("FEE_MIN_BRL", 0),
                BuyTier1MinBrl:         getEnvAsFloat("FEE_BUY_TIER_1_MIN", 20),
                BuyTier1MaxBrl:         getEnvAsFloat("FEE_BUY_TIER_1_MAX", 100),
                BuyTier1Bps:            getEnvPercentAsBps("FEE_BUY_TIER_1_PERCENT", getEnvAsInt("FEE_BUY_TIER_1_BPS", 750)),
                BuyTier2MaxBrl:         getEnvAsFloat("FEE_BUY_TIER_2_MAX", 500),
                BuyTier2Bps:            getEnvPercentAsBps("FEE_BUY_TIER_2_PERCENT", getEnvAsInt("FEE_BUY_TIER_2_BPS", 550)),
                BuyTier3Bps:            getEnvPercentAsBps("FEE_BUY_TIER_3_PERCENT", getEnvAsInt("FEE_BUY_TIER_3_BPS", 450)),
                BuyNetworkFeeBrl:       getEnvAsFloat("FEE_BUY_NETWORK_BRL", getEnvAsFloat("FEE_BUY_TIER_1_FIXED", getEnvAsFloat("FEE_BUY_TIER_FIXED", 1.99))),
                BuyMinFeeBrl:           getEnvAsFloat("FEE_BUY_MIN_TOTAL", getEnvAsFloat("FEE_MIN_BRL", 4.99)),
                BuyRateSpreadBps:       getEnvPercentAsBps("FEE_BUY_SPREAD_PERCENT", getEnvAsInt("FEE_BUY_SPREAD_BPS", 100)),
                SellRateBps:            getEnvAsInt("SELL_RATE_BPS", 0),
                SellSpreadMinBps:       getEnvPercentAsBps("FEE_SELL_SPREAD_MIN", getEnvAsInt("FEE_SELL_SPREAD_MIN_BPS", 800)),
                SellSpreadMaxBps:       getEnvPercentAsBps("FEE_SELL_SPREAD_MAX", getEnvAsInt("FEE_SELL_SPREAD_MAX_BPS", 1000)),
                SellSpreadHighValueBrl: getEnvAsFloat("FEE_SELL_HIGH_VALUE_BRL", 1000),
                SellUsdtBrlRate:        getEnvAsFloat("SELL_USDT_BRL_RATE", 0),
                SellWalletAddress:      getEnv("SELL_WALLET_ADDRESS", "0x7e3BF3FDfeF16040CE3ec60A663381766d3dB375"),
                BuyHotDerivationIndex:  getEnvAsInt("BUY_HOT_DERIVATION_INDEX", 0),
                ChainFXLiveSecretKeys:  getEnv("CHAINFX_LIVE_SECRET_KEYS", ""),
                ChainFXTestSecretKeys:  getEnv("CHAINFX_TEST_SECRET_KEYS", "sk_test_chainfx_local"),
                ChainFXLivePublicKeys:  getEnv("CHAINFX_LIVE_PUBLIC_KEYS", ""),
                ChainFXTestPublicKeys:  getEnv("CHAINFX_TEST_PUBLIC_KEYS", "pk_test_chainfx_local"),
                ChainFXRequireAPIKey:   getEnvAsBool("CHAINFX_REQUIRE_API_KEY", false),
                InternalAllowedCIDRs:   getEnv("INTERNAL_ALLOWED_CIDRS", "127.0.0.1/32,::1/128"),
                AdminBootstrapEmail:    getEnv("ADMIN_BOOTSTRAP_EMAIL", ""),
                AdminBootstrapPassword: getEnv("ADMIN_BOOTSTRAP_PASSWORD", ""),

                PixMaxOrdersPer24h:     getEnvAsInt("PIX_MAX_ORDERS_PER_24H", 5),
                PixMaxBrlPer24h:        getEnvAsFloat("PIX_MAX_BRL_PER_24H", 20000.0),
                OrderHoldSecForNewDest: getEnvAsInt("ORDER_HOLD_SEC_FOR_NEW_DEST", 180),
                BscDepositTolerancePct: getEnvAsFloat("BSC_DEPOSIT_TOLERANCE_PCT", 0.02),

                EfiClientID:        getEnv("EFI_CLIENT_ID", ""),
                EfiClientSecret:    getEnv("EFI_CLIENT_SECRET", ""),
                EfiPixKey:          getEnv("EFI_PIX_KEY", ""),
                EfiApiBaseURL:      getEnv("EFI_API_BASE_URL", "https://pix.api.efipay.com.br"),
                EfiCertificatePath: getEnv("EFI_CERTIFICATE_PATH", ""),
                EfiCertificateKey:  getEnv("EFI_CERTIFICATE_KEY_PATH", ""),
                EfiCertificatePass: getEnv("EFI_CERTIFICATE_PASSWORD", ""),
                EfiCertificateP12:  getEnv("EFI_CERTIFICATE_P12_BASE64", ""),
                EfiPixFeeBps:       getEnvAsInt("EFI_PIX_FEE_BPS", 119),
                PixWebhookSecret:   getEnv("PIX_WEBHOOK_SECRET", ""),

                TreasuryHot:         getEnv("TREASURY_HOT", ""),
                TreasuryCold:        getEnv("TREASURY_COLD", ""),
                SignerUrl:           getEnv("SIGNER_URL", ""),
                SignerNetwork:       strings.ToLower(getEnv("SIGNER_NETWORK", "bsc")),
                SignerHmacSecret:    getEnv("SIGNER_HMAC_SECRET", ""),
                BscRpcUrls:          getBscRpcUrls(),
                BscUsdtContract:     getEnv("BSC_USDT_CONTRACT", getEnv("BSC_TOKEN_CONTRACT", "")),
                PolygonRpcUrls:      getPolygonRpcUrls(),
                PolygonUsdtContract: getEnv("POLYGON_USDT_CONTRACT", getEnv("POLYGON_TOKEN_CONTRACT", "")),
                EnableSweepWorker:   getEnvAsBool("ENABLE_SWEEP_WORKER", false),
                EnableSweepStub:     getEnvAsBool("ENABLE_SWEEP_STUB", false),
                SweepBatchUsdtMin:   getEnvAsFloat("SWEEP_BATCH_USDT_MIN", 0),
                SweepBatchUsdtMax:   getEnvAsFloat("SWEEP_BATCH_USDT_MAX", 1_000_000),
                SweepFrequencyMs:    getEnvAsInt("SWEEP_FREQUENCY_MS", 80800),
                BscGasReserveBNB:    getEnvAsFloat("BSC_GAS_RESERVE_BNB", 0.003),

                SMTPHost:            getEnv("SMTP_HOST", ""),
                SMTPPort:            getEnvAsInt("SMTP_PORT", 587),
                SMTPUser:            getEnv("SMTP_USER", ""),
                SMTPPass:            getEnv("SMTP_PASS", ""),
                SMTPSecure:          getEnvAsBool("SMTP_SECURE", false),
                SMTPFromEmail:       getEnv("SMTP_FROM_EMAIL", ""),
                SMTPFromName:        getEnv("SMTP_FROM_NAME", "ChainFX"),
                OpsEmail:            getEnv("OPS_EMAIL", getEnv("SMTP_FROM_EMAIL", "")),
                EmailBrandName:      getEnv("EMAIL_BRAND_NAME", "ChainFX"),
                EmailLogoURL:        getEnv("EMAIL_LOGO_URL", "https://res.cloudinary.com/limpeja/image/upload/v1783623705/Green_Modern_Marketing_Logo-removebg-preview_1_yivrrc.png"),
                EmailSiteURL:        strings.TrimRight(getEnv("EMAIL_SITE_URL", "https://www.chainfx.store"), "/"),
                EmailAddress:        getEnv("EMAIL_COMPANY_ADDRESS", "ChainFX Payments"),
                SupportEmail:        getEnv("SUPPORT_EMAIL", getEnv("SMTP_FROM_EMAIL", "")),
                LGPDSecret:          getEnv("LGPD_SECRET", ""),
                WebhooksEnabled:     getEnvAsBool("WEBHOOKS_ENABLED", true),
                WebhooksMaxRetries:  getEnvAsInt("WEBHOOKS_MAX_RETRIES", 5),
                OpenAIAPIKey:        getEnv("OPENAI_API_KEY", ""),
                OpenAIModel:         getEnv("OPENAI_MODEL", "gpt-5.5"),
                OpenAIBaseURL:       strings.TrimRight(getEnv("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
                CapabilityOCRURL:    strings.TrimRight(getEnv("CAPABILITY_OCR_URL", ""), "/"),
                CapabilityOCRAPIKey: getEnv("CAPABILITY_OCR_API_KEY", ""),

                M2MPixFeeBps:            getEnvAsInt("M2M_PIX_FEE_BPS", 1000),
                M2MCreditFeeBps:         getEnvAsInt("M2M_CREDIT_FEE_BPS", 1900),
                M2MDepositTolerancePct:  getEnvAsFloat("M2M_DEPOSIT_TOLERANCE_PCT", 0.005),
                M2MMaxDailyOutflowBRL:   getEnvAsFloat("M2M_MAX_DAILY_OUTFLOW_BRL", 50000),
                BSCMinConfirmations:     getEnvAsUint64("BSC_MIN_CONFIRMATIONS", 6),
                PolygonMinConfirmations: getEnvAsUint64("POLYGON_MIN_CONFIRMATIONS", 128),

                GasStationEnabled:          getEnvAsBool("GAS_STATION_ENABLED", false),
                GasStationSurchargeBps:     getEnvAsInt("GAS_STATION_SURCHARGE_BPS", 1000),
                GasStationMaxFeeUsdt:       getEnvAsFloat("GAS_STATION_MAX_FEE_USDT", 2.00),
                GasStationMinFeeUsdt:       getEnvAsFloat("GAS_STATION_MIN_FEE_USDT", 0.05),
                PaymasterMulticallContract: getEnv("PAYMASTER_MULTICALL_CONTRACT", ""),
                PaymasterPrivKey:           getEnv("PAYMASTER_PRIV_KEY", ""),
                PaymasterRelaySpreadBps:    getEnvAsInt("PAYMASTER_RELAY_SPREAD_BPS", 100),

                AutoSweeperEnabled:     getEnvAsBool("AUTO_SWEEPER_ENABLED", false),
                AutoSweeperIntervalSec: getEnvAsInt("AUTO_SWEEPER_INTERVAL_SEC", 60),
                AutoSweeperHotMaxUsdt:  getEnvAsFloat("AUTO_SWEEPER_HOT_MAX_USDT", 5000),
                AutoSweeperHotMinUsdt:  getEnvAsFloat("AUTO_SWEEPER_HOT_MIN_USDT", 500),
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
                "DATABASE_URL":       c.DatabaseURL,
                "LGPD_SECRET":        c.LGPDSecret,
                "WEBHOOK_SECRET":     c.WebhookSecret,
                "PIX_WEBHOOK_SECRET": c.PixWebhookSecret,
                "SIGNER_URL":         c.SignerUrl,
                "SIGNER_HMAC_SECRET": c.SignerHmacSecret,
                "EFI_CLIENT_ID":      c.EfiClientID,
                "EFI_CLIENT_SECRET":  c.EfiClientSecret,
                "EFI_PIX_KEY":        c.EfiPixKey,
                "TREASURY_HOT":       c.TreasuryHot,
        }
        if strings.TrimSpace(c.EfiCertificatePath) == "" && strings.TrimSpace(c.EfiCertificateP12) == "" {
                required["EFI_CERTIFICATE_PATH or EFI_CERTIFICATE_P12_BASE64"] = ""
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
        signerURL := strings.ToLower(strings.TrimSpace(c.SignerUrl))
        if strings.Contains(signerURL, "up.railway.app") {
                return fmt.Errorf("SIGNER_URL deve usar rede privada em producao, nao dominio publico Railway; exemplo: http://signer.railway.internal:4010")
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

func getPolygonRpcUrls() string {
        if raw := strings.TrimSpace(getEnv("POLYGON_RPC_URLS", getEnv("POLYGON_RPC_URL", ""))); raw != "" {
                return raw
        }
        var urls []string
        for _, key := range []string{"ALCHEMY_POLYGON_RPC_URL_1", "ALCHEMY_POLYGON_RPC_URL_2", "ALCHEMY_POLYGON_RPC_URL", "ALCHEMY_POLYGON_FALLBACK_RPC_URL"} {
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

func getEnvPercentAsBps(key string, defaultValue int) int {
        valueStr := getEnv(key, "")
        if value, err := strconv.ParseFloat(valueStr, 64); err == nil {
                return int(value*100 + 0.5)
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

func getEnvAsUint64(key string, defaultValue uint64) uint64 {
        valueStr := getEnv(key, "")
        if valueStr == "" {
                return defaultValue
        }
        value, err := strconv.ParseUint(valueStr, 10, 64)
        if err != nil || value == 0 {
                return defaultValue
        }
        return value
}
