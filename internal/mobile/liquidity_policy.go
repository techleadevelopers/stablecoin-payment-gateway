package mobile

import (
	"strings"

	"payment-gateway/internal/liquidity"
)

func normalizeMobileBuyNetwork(network string) string {
	network = strings.TrimSpace(network)
	if network == "" {
		return "BSC"
	}
	normalized := liquidity.NormalizeNetwork(network)
	if _, ok := liquidity.NetworkMetadata(normalized); !ok {
		return ""
	}
	return normalized
}

func (s *Server) mobileLiquidityPairSupported(asset, network string) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	asset = strings.ToUpper(strings.TrimSpace(asset))
	network = normalizeMobileBuyNetwork(network)
	if asset == "" || network == "" {
		return false
	}
	if !s.mobileNetworkEnabled(network) {
		return false
	}
	if !s.mobileBuyPairExecutableWithoutRouter(asset, network) && !s.cfg.LiquidityRouterEnabled {
		return false
	}
	policy := liquidity.NewPairPolicy(s.cfg.LiquidityAllowedPairs)
	if !policy.Empty() {
		return policy.Allows(asset, network)
	}
	return containsCSVFoldMobile(s.cfg.LiquidityAllowedAssets, asset) &&
		containsCSVFoldMobile(s.cfg.LiquidityAllowedNetworks, network)
}

func (s *Server) mobileLiquiditySupportedPairs() []map[string]any {
	if s == nil || s.cfg == nil {
		return nil
	}
	policy := liquidity.NewPairPolicy(s.cfg.LiquidityAllowedPairs)
	pairs := policy.Pairs()
	out := make([]map[string]any, 0, len(pairs))
	for _, pair := range pairs {
		pair = liquidity.EnrichPair(pair)
		if !s.mobileNetworkEnabled(pair.Network) {
			continue
		}
		hotWalletEnabled := s.mobileBuyPairExecutableWithoutRouter(pair.Asset, pair.Network)
		routerEnabled := s.cfg.LiquidityRouterEnabled
		buyEnabled := hotWalletEnabled || routerEnabled
		sendEnabled := s.mobilePairSendEnabled(pair)
		networkMeta, _ := liquidity.NetworkMetadata(pair.Network)
		receiveEnabled := networkMeta.ReceiveEnabled
		if pair.Network == "SOLANA" {
			receiveEnabled = s.solSvc != nil
		}
		if !buyEnabled && !sendEnabled && !receiveEnabled {
			continue
		}
		out = append(out, map[string]any{
			"asset":                    pair.Asset,
			"network":                  pair.Network,
			"family":                   pair.Family,
			"contract_address":         pair.ContractAddress,
			"decimals":                 pair.Decimals,
			"token_standard":           pair.TokenStandard,
			"receive_enabled":          receiveEnabled,
			"send_enabled":             networkMeta.SendEnabled && sendEnabled,
			"buy_enabled":              networkMeta.BuyEnabled && buyEnabled,
			"dca_enabled":              networkMeta.DCAEnabled && buyEnabled,
			"hot_wallet_enabled":       hotWalletEnabled,
			"liquidity_router_enabled": routerEnabled,
		})
	}
	return out
}

func (s *Server) mobileBuyPairExecutableWithoutRouter(asset, network string) bool {
	return strings.EqualFold(asset, "USDT") && normalizeMobileBuyNetwork(network) == "BSC"
}

func (s *Server) mobilePairSendEnabled(pair liquidity.Pair) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	pair = liquidity.EnrichPair(pair)
	if !liquidity.IsEVMNetwork(pair.Network) || pair.TokenStandard != "ERC20" {
		return pair.Asset == "SOL" && pair.Network == "SOLANA" && s.solSvc != nil && s.cfg.SolanaWithdrawalsEnabled
	}
	_, _, _, err := s.mobileTransferToken(pair.Asset, pair.Network)
	return err == nil
}

func (s *Server) mobileSupportedNetworks() []liquidity.NetworkMeta {
	if s == nil || s.cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []liquidity.NetworkMeta
	raw := s.cfg.SupportedNetworks
	if strings.TrimSpace(raw) == "" {
		raw = s.cfg.LiquidityAllowedNetworks
	}
	for _, item := range splitMobilePolicyItems(raw) {
		network := normalizeMobileBuyNetwork(item)
		if network == "" || seen[network] {
			continue
		}
		meta, ok := liquidity.NetworkMetadata(network)
		if !ok {
			continue
		}
		seen[network] = true
		out = append(out, meta)
	}
	return out
}

func (s *Server) mobileNetworkEnabled(network string) bool {
	network = normalizeMobileBuyNetwork(network)
	if network == "" {
		return false
	}
	for _, meta := range s.mobileSupportedNetworks() {
		if meta.Network == network && meta.Enabled {
			return true
		}
	}
	return false
}

func (s *Server) mobileLiquiditySupportedTokens() []string {
	seen := map[string]bool{}
	var out []string
	for _, pair := range s.mobileLiquiditySupportedPairs() {
		asset, _ := pair["asset"].(string)
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if asset != "" && !seen[asset] {
			seen[asset] = true
			out = append(out, asset)
		}
	}
	return out
}

func (s *Server) mobileLiquiditySupportedNetworks() []string {
	seen := map[string]bool{}
	var out []string
	for _, pair := range s.mobileLiquiditySupportedPairs() {
		network, _ := pair["network"].(string)
		network = strings.ToUpper(strings.TrimSpace(network))
		if network != "" && !seen[network] {
			seen[network] = true
			out = append(out, network)
		}
	}
	return out
}

func containsCSVFoldMobile(raw, value string) bool {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, item := range splitMobilePolicyItems(raw) {
		if strings.ToUpper(strings.TrimSpace(item)) == value {
			return true
		}
	}
	return false
}

func splitMobilePolicyItems(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}
