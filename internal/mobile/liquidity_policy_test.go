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
		BscUsdtContract:          "0x55d398326f99059fF775485246999027B3197955",
		BaseUsdcContract:         "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
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
