package mobile

import (
	"testing"

	"payment-gateway/internal/config"
)

func TestMobileLiquiditySupportedPairsRejectsInvalidCartesianFallbackPairs(t *testing.T) {
	s := &Server{cfg: &config.Config{
		LiquidityRouterEnabled:   true,
		LiquidityAllowedPairs:    "",
		LiquidityAllowedAssets:   "USDT,BTC,SOL,USDC,ETH",
		LiquidityAllowedNetworks: "BSC,BITCOIN,SOLANA,BASE",
		SupportedNetworks:        "BSC,BITCOIN,SOLANA,BASE",
		LiquidityProviderURLs:    "mock=https://liquidity-provider.local",
		BscUsdtContract:          "0x55d398326f99059fF775485246999027B3197955",
		BaseUsdcContract:         "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
		BingXEnabled:             true,
		BingXAPIKey:              "key",
		BingXAPISecret:           "secret",
		BingXTradeEnabled:        true,
		BingXWithdrawEnabled:     true,
		BingXAllowedAssets:       "BTC,SOL,ETH",
		BingXAllowedNetworks:     "BITCOIN,SOLANA,BASE",
	}}

	if !s.mobileLiquidityPairSupported("BTC", "BITCOIN") {
		t.Fatalf("expected native BTC:BITCOIN to be supported")
	}
	if !s.mobileLiquidityPairSupported("SOL", "SOLANA") {
		t.Fatalf("expected native SOL:SOLANA to be supported")
	}
	if !s.mobileLiquidityPairSupported("USDT", "BSC") {
		t.Fatalf("expected configured USDT:BSC to be supported")
	}
	if !s.mobileLiquidityPairSupported("USDC", "BASE") {
		t.Fatalf("expected configured USDC:BASE to be supported")
	}
	if !s.mobileLiquidityPairSupported("ETH", "BASE") {
		t.Fatalf("expected native ETH:BASE to be supported")
	}

	for _, tc := range []struct {
		asset   string
		network string
	}{
		{"BTC", "BSC"},
		{"SOL", "BSC"},
		{"USDT", "BITCOIN"},
		{"USDT", "SOLANA"},
		{"USDT", "BASE"},
	} {
		if s.mobileLiquidityPairSupported(tc.asset, tc.network) {
			t.Fatalf("expected %s:%s to be rejected", tc.asset, tc.network)
		}
	}

	pairs := s.mobileLiquiditySupportedPairs()
	if len(pairs) != 5 {
		t.Fatalf("expected 5 executable pairs, got %d: %+v", len(pairs), pairs)
	}
}

func TestMobileLiquiditySupportedPairsRequiresRealRouterProviderForNonHotWalletPairs(t *testing.T) {
	s := &Server{cfg: &config.Config{
		LiquidityRouterEnabled:   true,
		LiquidityAllowedPairs:    "USDT:BSC:0x55d398326f99059fF775485246999027B3197955:18,BTC:BITCOIN::8,SOL:SOLANA::9,ETH:BASE::18",
		LiquidityAllowedAssets:   "USDT,BTC,SOL,ETH",
		LiquidityAllowedNetworks: "BSC,BITCOIN,SOLANA,BASE",
		SupportedNetworks:        "BSC,BITCOIN,SOLANA,BASE",
		BscUsdtContract:          "0x55d398326f99059fF775485246999027B3197955",
	}}

	if !s.mobileLiquidityPairSupported("USDT", "BSC") {
		t.Fatalf("expected hot-wallet USDT:BSC to remain supported")
	}
	for _, tc := range []struct {
		asset   string
		network string
	}{
		{"BTC", "BITCOIN"},
		{"SOL", "SOLANA"},
		{"ETH", "BASE"},
	} {
		if !s.mobileLiquidityPairSupported(tc.asset, tc.network) {
			t.Fatalf("expected %s:%s to remain declared for receive/catalog", tc.asset, tc.network)
		}
		if s.mobileBuyLiquidityPairSupported(tc.asset, tc.network) {
			t.Fatalf("expected %s:%s to require an executable router provider for buy", tc.asset, tc.network)
		}
	}
	pairs := s.mobileLiquiditySupportedPairs()
	if len(pairs) != 3 {
		t.Fatalf("expected all declared catalog pairs, got %+v", pairs)
	}
	for _, pair := range pairs {
		if pair["asset"] == "USDT" && pair["network"] == "BSC" {
			if pair["buy_enabled"] != true {
				t.Fatalf("expected USDT:BSC buy_enabled=true, got %+v", pair)
			}
			continue
		}
		if pair["buy_enabled"] == true {
			t.Fatalf("expected non-hot pairs to have buy_enabled=false without provider, got %+v", pair)
		}
	}
}
