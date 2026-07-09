package config

import "testing"

func TestValidateProductionRejectsMissingCriticalConfig(t *testing.T) {
	cfg := &Config{Environment: "production", AllowSimulations: false}

	if err := cfg.ValidateProduction(); err == nil {
		t.Fatal("expected production config validation to fail")
	}
}

func TestValidateProductionRejectsSimulations(t *testing.T) {
	cfg := productionReadyConfig()
	cfg.AllowSimulations = true

	if err := cfg.ValidateProduction(); err == nil {
		t.Fatal("expected production config validation to reject simulations")
	}
}

func TestValidateProductionAcceptsRequiredConfig(t *testing.T) {
	cfg := productionReadyConfig()

	if err := cfg.ValidateProduction(); err != nil {
		t.Fatalf("expected production config to pass, got %v", err)
	}
}

func TestValidateProductionRejectsPublicRailwaySignerURL(t *testing.T) {
	cfg := productionReadyConfig()
	cfg.SignerUrl = "https://signer-production.up.railway.app"

	if err := cfg.ValidateProduction(); err == nil {
		t.Fatal("expected public Railway signer URL to be rejected")
	}
}

func productionReadyConfig() *Config {
	return &Config{
		Environment:       "production",
		AllowSimulations:  false,
		DatabaseURL:       "postgres://user:pass@localhost/db",
		LGPDSecret:        "lgpd-secret",
		WebhookSecret:     "webhook-secret",
		PixWebhookSecret:  "pix-secret",
		SignerUrl:         "http://signer:4010",
		SignerNetwork:     "bsc",
		SignerHmacSecret:  "signer-secret",
		BscRpcUrls:        "https://bnb-mainnet.g.alchemy.com/v2/key-1,https://bnb-mainnet.g.alchemy.com/v2/key-2",
		BscUsdtContract:   "0x55d398326f99059fF775485246999027B3197955",
		EfiClientID:        "efi-client",
		EfiClientSecret:    "efi-secret",
		EfiPixKey:          "efi-pix-key",
		EfiCertificatePath: "efi-cert.pem",
		TreasuryHot:       "0x1111111111111111111111111111111111111111",
		EnableSweepStub:   false,
	}
}
