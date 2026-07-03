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

func productionReadyConfig() *Config {
	return &Config{
		Environment:       "production",
		AllowSimulations:  false,
		DatabaseURL:       "postgres://user:pass@localhost/db",
		LGPDSecret:        "lgpd-secret",
		WebhookSecret:     "webhook-secret",
		PixWebhookSecret:  "pix-secret",
		SignerUrl:         "http://signer:4010",
		SignerNetwork:     "tron",
		SignerHmacSecret:  "signer-secret",
		TronXPub:          "xpub",
		TronUsdtContract:  "contract",
		TronFullNodeURL:   "https://api.trongrid.io",
		PagSeguroApiToken: "pagbank-token",
		TreasuryHot:       "TXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
		EnableSweepStub:   false,
	}
}
