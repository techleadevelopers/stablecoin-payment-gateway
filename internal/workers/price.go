package workers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
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
	} `json:"tether"`
}

func NewPriceWorker(bus *EventBus) *PriceWorker {
	return &PriceWorker{
		bus:    bus,
		client: &http.Client{Timeout: 5 * time.Second},
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
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.coingecko.com/api/v3/simple/price?ids=tether&vs_currencies=brl,usd", nil)
	if err != nil {
		slog.Error("Erro ao criar request de preco", "error", err)
		return
	}
	resp, err := pw.client.Do(req)
	if err != nil {
		slog.Error("Erro ao consultar CoinGecko", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("CoinGecko retornou status invalido", "status", resp.StatusCode)
		return
	}

	var data CoinGeckoResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		slog.Error("Erro ao parsear preco", "error", err)
		return
	}
	pw.mu.Lock()
	pw.prices["BRL"] = data.Tether.Brl
	pw.prices["USD"] = data.Tether.Usd
	pw.mu.Unlock()

	payload := map[string]any{"BRL": data.Tether.Brl, "USD": data.Tether.Usd}
	slog.Info("Cotacao USDT atualizada", "brl", data.Tether.Brl, "usd", data.Tether.Usd, "duration_ms", time.Since(start).Milliseconds())
	pw.bus.Publish(Event{Type: "price.updated", Payload: payload})
}
