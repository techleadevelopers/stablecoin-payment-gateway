package server

import (
	"math"
	"strings"

	"payment-gateway/internal/money"
)

func (s *Server) transactionFee(amountFiat float64, fiatCurrency string, rate float64) float64 {
	return s.transactionFeeMinor(money.MoneyFromFloat(amountFiat), fiatCurrency, money.RateFromFloat(rate)).Float64()
}

func (s *Server) transactionFeeMinor(amountFiat money.MoneyMinor, fiatCurrency string, rate money.RateDecimal) money.MoneyMinor {
	if strings.EqualFold(fiatCurrency, "BRL") && s.cfg.BuyTier1Bps+s.cfg.BuyTier2Bps+s.cfg.BuyTier3Bps > 0 {
		_, _, _, _, totalFee, _ := s.buyFeeBreakdownMinor(amountFiat)
		return totalFee
	}
	percentFee := money.FeeBps(amountFiat, s.cfg.FeeBps)
	fixedFee := money.MoneyFromFloat(s.cfg.FeeFixedUsd)
	perUSDTFee := money.FiatFromTokens(money.TokensFromFiat(amountFiat, rate), money.RateFromFloat(s.cfg.FeePerUsdtUsd))
	if strings.EqualFold(fiatCurrency, "BRL") {
		fixedFee = money.FiatFromTokens(money.TokenFromFloat(s.cfg.FeeFixedUsd), rate)
		perUSDTFee = money.MoneyMinor(roundDivInt64(int64(amountFiat)*int64(money.RateFromFloat(s.cfg.FeePerUsdtUsd)), money.RateScale))
	}
	fee := percentFee + fixedFee + perUSDTFee
	minFee := money.MoneyFromFloat(s.cfg.FeeMinBrl)
	if strings.EqualFold(fiatCurrency, "BRL") && minFee > fee {
		fee = minFee
	}
	return fee
}

func (s *Server) buyFeeBreakdown(amountBRL float64) buyFeeBreakdown {
	tier, bps, serviceFee, networkFee, totalFee, minFee := s.buyFeeBreakdownMinor(money.MoneyFromFloat(amountBRL))
	return buyFeeBreakdown{
		Tier:          tier,
		ServiceBps:    bps,
		ServiceFee:    serviceFee.Float64(),
		NetworkFee:    networkFee.Float64(),
		MinFee:        minFee.Float64(),
		TotalFee:      totalFee.Float64(),
		RateSpreadBps: s.cfg.BuyRateSpreadBps,
	}
}

func (s *Server) buyFeeBreakdownMinor(amountBRL money.MoneyMinor) (string, int, money.MoneyMinor, money.MoneyMinor, money.MoneyMinor, money.MoneyMinor) {
	bps := s.cfg.BuyTier3Bps
	tier := "tier3"
	switch {
	case amountBRL < money.MoneyFromFloat(s.cfg.BuyTier1MaxBrl):
		bps = s.cfg.BuyTier1Bps
		tier = "tier1"
	case amountBRL < money.MoneyFromFloat(s.cfg.BuyTier2MaxBrl):
		bps = s.cfg.BuyTier2Bps
		tier = "tier2"
	}
	serviceFee := money.FeeBps(amountBRL, bps)
	networkFee := money.MoneyFromFloat(s.cfg.BuyNetworkFeeBrl)
	totalFee := serviceFee + networkFee
	minFee := money.MoneyFromFloat(s.cfg.BuyMinFeeBrl)
	if totalFee < minFee {
		totalFee = minFee
	}
	return tier, bps, serviceFee, networkFee, totalFee, minFee
}

func (s *Server) buyMinBRL() float64 {
	if s.cfg.BuyTier1MinBrl > s.cfg.OrderMinBrl {
		return s.cfg.BuyTier1MinBrl
	}
	return s.cfg.OrderMinBrl
}

func (s *Server) buyRate(marketRate float64) float64 {
	spreadBps := s.cfg.BuyRateSpreadBps
	if spreadBps < 0 {
		spreadBps = 0
	}
	return roundRate(money.AddBps(money.RateFromFloat(marketRate), spreadBps).Float64())
}

func buyAssetSupported(asset string) bool {
	switch strings.ToUpper(strings.TrimSpace(asset)) {
	case "USDT", "BTC", "BNB", "ETH", "SOL", "LINK", "AVAX":
		return true
	default:
		return false
	}
}

func (s *Server) buyAssetMarketRate(fiatCurrency, asset string) float64 {
	if s == nil || s.workers == nil || s.workers.PriceWorker == nil {
		return 0
	}
	fiatCurrency = strings.ToUpper(strings.TrimSpace(defaultString(fiatCurrency, "BRL")))
	asset = strings.ToUpper(strings.TrimSpace(defaultString(asset, "USDT")))
	if fiatCurrency != "BRL" {
		return s.workers.PriceWorker.GetPrice(fiatCurrency)
	}
	usdtBRL := s.workers.PriceWorker.GetPrice("BRL")
	switch asset {
	case "USDT":
		return usdtBRL
	case "BTC":
		btcUSD := s.workers.PriceWorker.GetPrice("BTCUSDT_SOURCE")
		if btcUSD <= 0 {
			btcUSD = s.workers.PriceWorker.GetPrice("BTCUSDT")
		}
		if btcUSD > 0 && usdtBRL > 0 {
			return btcUSD * usdtBRL
		}
	case "BNB":
		bnbUSD := s.workers.PriceWorker.GetPrice("BNBUSDT_SOURCE")
		if bnbUSD <= 0 {
			bnbUSD = s.workers.PriceWorker.GetPrice("BNBUSDT")
		}
		if bnbUSD > 0 && usdtBRL > 0 {
			return bnbUSD * usdtBRL
		}
	case "ETH":
		ethUSD := s.workers.PriceWorker.GetPrice("ETHUSDT_SOURCE")
		if ethUSD <= 0 {
			ethUSD = s.workers.PriceWorker.GetPrice("ETHUSDT")
		}
		if ethUSD > 0 && usdtBRL > 0 {
			return ethUSD * usdtBRL
		}
	case "SOL":
		solUSD := s.workers.PriceWorker.GetPrice("SOLUSDT_SOURCE")
		if solUSD <= 0 {
			solUSD = s.workers.PriceWorker.GetPrice("SOLUSDT")
		}
		if solUSD > 0 && usdtBRL > 0 {
			return solUSD * usdtBRL
		}
	case "LINK":
		linkUSD := s.workers.PriceWorker.GetPrice("LINKUSDT_SOURCE")
		if linkUSD <= 0 {
			linkUSD = s.workers.PriceWorker.GetPrice("LINKUSDT")
		}
		if linkUSD > 0 && usdtBRL > 0 {
			return linkUSD * usdtBRL
		}
	case "AVAX":
		avaxUSD := s.workers.PriceWorker.GetPrice("AVAXUSDT_SOURCE")
		if avaxUSD <= 0 {
			avaxUSD = s.workers.PriceWorker.GetPrice("AVAXUSDT")
		}
		if avaxUSD > 0 && usdtBRL > 0 {
			return avaxUSD * usdtBRL
		}
	}
	return 0
}

func (s *Server) feePolicy(fiatCurrency string, rate float64) map[string]any {
	fixedFiat := s.cfg.FeeFixedUsd
	perUsdtFiat := s.cfg.FeePerUsdtUsd
	if strings.EqualFold(fiatCurrency, "BRL") {
		fixedFiat = s.cfg.FeeFixedUsd * rate
		perUsdtFiat = s.cfg.FeePerUsdtUsd * rate
	}
	return map[string]any{
		"bps":             s.cfg.FeeBps,
		"percent":         float64(s.cfg.FeeBps) / 100,
		"fixedUsd":        s.cfg.FeeFixedUsd,
		"fixedFiat":       fixedFiat,
		"perUsdtUsd":      s.cfg.FeePerUsdtUsd,
		"perUsdtFiat":     perUsdtFiat,
		"buyMinBRL":       s.buyMinBRL(),
		"buyTier1Bps":     s.cfg.BuyTier1Bps,
		"buyTier1MaxBRL":  s.cfg.BuyTier1MaxBrl,
		"buyTier2Bps":     s.cfg.BuyTier2Bps,
		"buyTier2MaxBRL":  s.cfg.BuyTier2MaxBrl,
		"buyTier3Bps":     s.cfg.BuyTier3Bps,
		"networkFeeBRL":   s.cfg.BuyNetworkFeeBrl,
		"minFeeBRL":       s.cfg.BuyMinFeeBrl,
		"rateSpreadBps":   s.cfg.BuyRateSpreadBps,
		"fiatCurrency":    strings.ToUpper(fiatCurrency),
		"description":     "Tiered BUY fee + network fee + minimum fee + rate spread",
		"backendEnforced": true,
	}
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func roundRate(value float64) float64 {
	return math.Round(value*10000) / 10000
}

func roundDivInt64(num, den int64) int64 {
	if den == 0 {
		return 0
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return (num - den/2) / den
}

func (s *Server) sellRate(marketRate float64) float64 {
	if s.cfg.SellUsdtBrlRate > 0 {
		return roundRate(s.cfg.SellUsdtBrlRate)
	}
	if s.cfg.SellRateBps > 0 {
		bps := s.cfg.SellRateBps
		if bps > 10000 {
			bps = 10000
		}
		return roundRate(marketRate * float64(bps) / 10000)
	}
	return s.sellRateForAmount(0, marketRate)
}

func (s *Server) sellRateForAmount(amountUSDT, marketRate float64) float64 {
	spreadBps := s.sellSpreadBps(amountUSDT, marketRate)
	return roundRate(money.SubtractBps(money.RateFromFloat(marketRate), spreadBps).Float64())
}

func (s *Server) sellSpreadBps(amountUSDT, marketRate float64) int {
	if s.cfg.SellUsdtBrlRate > 0 && marketRate > 0 {
		spread := int(math.Round((1 - s.cfg.SellUsdtBrlRate/marketRate) * 10000))
		if spread < 0 {
			return 0
		}
		return spread
	}
	if s.cfg.SellRateBps > 0 {
		spread := 10000 - s.cfg.SellRateBps
		if spread < 0 {
			return 0
		}
		return spread
	}
	minBps := s.cfg.SellSpreadMinBps
	maxBps := s.cfg.SellSpreadMaxBps
	if minBps < 0 {
		minBps = 0
	}
	if maxBps < minBps {
		maxBps = minBps
	}
	marketValue := amountUSDT * marketRate
	if s.cfg.SellSpreadHighValueBrl > 0 && marketValue >= s.cfg.SellSpreadHighValueBrl {
		return minBps
	}
	return maxBps
}

func (s *Server) sellQuote(amountUSDT, marketRate float64) (sellRate, payoutBRL, spreadBRL float64) {
	sellRateDecimal, payout, spread := s.sellQuoteUnits(money.TokenFromFloat(amountUSDT), money.RateFromFloat(marketRate))
	return roundRate(sellRateDecimal.Float64()), payout.Float64(), spread.Float64()
}

func (s *Server) sellQuoteUnits(amount money.TokenUnits, marketRate money.RateDecimal) (money.RateDecimal, money.MoneyMinor, money.MoneyMinor) {
	sellRate := money.RateFromFloat(s.sellRateForAmount(amount.Float64(), marketRate.Float64()))
	payout := money.FiatFromTokens(amount, sellRate)
	marketValue := money.FiatFromTokens(amount, marketRate)
	spread := money.MoneyMinor(0)
	if marketValue > payout {
		spread = marketValue - payout
	}
	return sellRate, payout, spread
}

func (s *Server) sellPolicy(marketRate, sellRate float64) map[string]any {
	spreadBps := 0
	if marketRate > 0 && sellRate > 0 && sellRate < marketRate {
		spreadBps = int(math.Round((1 - sellRate/marketRate) * 10000))
	}
	return map[string]any{
		"marketRate":       roundRate(marketRate),
		"rate":             sellRate,
		"sellRateBps":      s.cfg.SellRateBps,
		"spreadBps":        spreadBps,
		"fixedSellRateBRL": s.cfg.SellUsdtBrlRate > 0,
		"fiatCurrency":     "BRL",
		"description":      "Cotacao de venda USDT para PIX BRL",
		"backendEnforced":  true,
	}
}
