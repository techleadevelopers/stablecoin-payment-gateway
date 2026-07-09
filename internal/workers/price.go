package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"payment-gateway/internal/httpclient"
)

type PriceWorker struct {
	bus    *EventBus
	client *http.Client
	mu     sync.RWMutex
	prices map[string]float64
}

type CoinGeckoResponse struct {
	Tether struct {
		Brl float64 `json:"brl"`
		Usd float64 `json:"usd"`
		Eur float64 `json:"eur"`
	} `json:"tether"`
	Bitcoin struct {
		Usd float64 `json:"usd"`
	} `json:"bitcoin"`
}

type BinanceTickerResponse struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

func NewPriceWorker(bus *EventBus) *PriceWorker {
	return &PriceWorker{
		bus:    bus,
		client: httpclient.Default(),
		prices: make(map[string]float64),
	}
}

func (pw *PriceWorker) GetCurrentPrice() float64 {
	return pw.GetPrice("BRL")
}

func (pw *PriceWorker) GetPrice(currency string) float64 {
	pw.mu.RLock()
	defer pw.mu.RUnlock()
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "BRL"
	}
	return pw.prices[currency]
}

func (pw *PriceWorker) Start(ctx context.Context) {
	slog.Info("PriceWorker inicializado")
	pw.fetchPrice()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("PriceWorker encerrado")
			return
		case <-ticker.C:
			pw.fetchPrice()
		}
	}
}

func (pw *PriceWorker) fetchPrice() {
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	prices, source, err := pw.fetchCoinGeckoPrices(ctx)
	cancel()
	if err != nil {
		slog.Warn("CoinGecko indisponivel, tentando fallback Binance", "error", err)
		ctx, cancel = context.WithTimeout(context.Background(), 8*time.Second)
		prices, source, err = pw.fetchBinancePrices(ctx)
		cancel()
	}
	if err != nil {
		slog.Error("Erro ao atualizar cotacoes", "error", err)
		return
	}

	pw.mu.Lock()
	// Só publica evento se houve mudança significativa (evita spam)
	changed := false
	for key, value := range prices {
		if value > 0 {
			if old, exists := pw.prices[key]; !exists || abs(value-old) > 0.0001 {
				pw.prices[key] = value
				changed = true
			}
		}
	}

	if changed {
		payload := make(map[string]any, len(pw.prices))
		for key, value := range pw.prices {
			payload[key] = value
		}
		pw.mu.Unlock()

		slog.Info("Cotacoes atualizadas", "source", source, "duration_ms", time.Since(start).Milliseconds())
		pw.bus.Publish(Event{Type: "price.updated", Payload: payload})
	} else {
		pw.mu.Unlock()
		slog.Debug("Cotacoes sem alteracoes significativas", "source", source)
	}
}

func (pw *PriceWorker) fetchCoinGeckoPrices(ctx context.Context) (map[string]float64, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.coingecko.com/api/v3/simple/price?ids=tether,bitcoin&vs_currencies=brl,usd,eur", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "PaymentGateway/1.0") // Bom pra evitar bloqueios

	resp, err := pw.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("coingecko status %d", resp.StatusCode)
	}

	var data CoinGeckoResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, "", err
	}

	prices := map[string]float64{
		"BRL":            data.Tether.Brl,
		"USD":            data.Tether.Usd,
		"EUR":            data.Tether.Eur,
		"BTCUSDT":        data.Bitcoin.Usd / data.Tether.Usd,
		"EURUSD":         data.Tether.Usd / data.Tether.Eur,
		"USDTBRL":        data.Tether.Brl,
		"USDTUSD":        data.Tether.Usd,
		"USDTEUR":        data.Tether.Eur,
		"BTCUSDT_SOURCE": data.Bitcoin.Usd,
	}

	if prices["BRL"] <= 0 || prices["USD"] <= 0 {
		return nil, "", fmt.Errorf("coingecko payload sem USDT BRL/USD")
	}
	if prices["EUR"] <= 0 {
		delete(prices, "EUR")
		delete(prices, "EURUSD")
		delete(prices, "USDTEUR")
	}
	if data.Bitcoin.Usd <= 0 {
		delete(prices, "BTCUSDT")
		delete(prices, "BTCUSDT_SOURCE")
	}
	return prices, "coingecko", nil
}

func (pw *PriceWorker) fetchBinancePrices(ctx context.Context) (map[string]float64, string, error) {
	symbols := map[string]string{
		"BRL":     "USDTBRL",
		"USDTBRL": "USDTBRL",
		"BTCUSDT": "BTCUSDT",
		"EURUSD":  "EURUSDT",
	}
	prices := map[string]float64{"USD": 1, "USDTUSD": 1}

	for key, symbol := range symbols {
		price, err := pw.fetchBinanceTicker(ctx, symbol)
		if err != nil {
			slog.Warn("Ticker Binance indisponivel", "symbol", symbol, "error", err)
			continue
		}
		prices[key] = price
		if symbol == "EURUSDT" && price > 0 {
			prices["EUR"] = 1 / price
			prices["USDTEUR"] = 1 / price
		}
	}
	if prices["BRL"] <= 0 {
		return nil, "", fmt.Errorf("fallback Binance sem USDTBRL")
	}
	return prices, "binance", nil
}

func (pw *PriceWorker) fetchBinanceTicker(ctx context.Context, symbol string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.binance.com/api/v3/ticker/price?symbol="+symbol, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "PaymentGateway/1.0")

	resp, err := pw.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("binance status %d", resp.StatusCode)
	}

	var data BinanceTickerResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, err
	}

	price, err := strconv.ParseFloat(data.Price, 64)
	if err != nil {
		return 0, err
	}
	return price, nil
}

// Helper
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
