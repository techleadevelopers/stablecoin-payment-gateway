package server

import (
	"strings"

	"payment-gateway/internal/bitcoin"
	"payment-gateway/internal/liquidity"
)

func normalizeBuyDeliveryNetwork(network string) string {
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "", "BSC", "BINANCE", "BEP20", "BEP-20":
		return "BSC"
	case "POL", "POLYGON", "MATIC":
		return "POLYGON"
	case "BTC", "BITCOIN":
		return "BITCOIN"
	default:
		return strings.ToUpper(strings.TrimSpace(network))
	}
}

func (s *Server) buyLiquidityPairSupported(asset, network string) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	asset = strings.ToUpper(strings.TrimSpace(asset))
	network = normalizeBuyDeliveryNetwork(network)
	if asset == "" || network == "" {
		return false
	}
	policy := liquidity.NewPairPolicy(s.cfg.LiquidityAllowedPairs)
	if !policy.Empty() {
		return policy.Allows(asset, network)
	}
	return containsCSVFoldServer(s.cfg.LiquidityAllowedAssets, asset) &&
		containsCSVFoldServer(s.cfg.LiquidityAllowedNetworks, network)
}

func validBuyDeliveryAddress(network, address string) bool {
	address = strings.TrimSpace(address)
	switch normalizeBuyDeliveryNetwork(network) {
	case "BSC", "POLYGON":
		return isEVMDeliveryAddress(address)
	case "BITCOIN":
		return bitcoin.ValidateAddress(address, "bc") == nil ||
			bitcoin.ValidateAddress(address, "tb") == nil ||
			bitcoin.ValidateAddress(address, "bcrt") == nil
	default:
		return false
	}
}

func containsCSVFoldServer(raw, value string) bool {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		if strings.ToUpper(strings.TrimSpace(item)) == value {
			return true
		}
	}
	return false
}
