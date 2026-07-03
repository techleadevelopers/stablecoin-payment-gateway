package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"meu-gateway-go/internal/config"
	"meu-gateway-go/internal/database"
)

type OnchainWorker struct {
	bus    *EventBus
	db     *database.DB
	cfg    *config.Config
	client *http.Client
}

// TronEventResponse mapeia exatamente o JSON retornado pela API de eventos da rede TRON
type TronEventResponse struct {
	Data []struct {
		TransactionID string `json:"transaction_id"`
		BlockNumber   uint64 `json:"block_number"`
		Result        struct {
			To    string `json:"to"`
			Value string `json:"value"`
		} `json:"result"`
	} `json:"data"`
	Meta struct {
		Fingerprint string `json:"fingerprint"`
	} `json:"meta"`
}

func NewOnchainWorker(bus *EventBus, db *database.DB, cfg *config.Config) *OnchainWorker {
	return &OnchainWorker{
		bus: bus,
		db:  db,
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second, // Evita conexões presas na rede TRON
		},
	}
}

func (ow *OnchainWorker) Start(ctx context.Context) {
	slog.Info("OnchainWorker TRON inicializado em background.")

	// Polling a cada 10 segundos idêntico ao Node
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Desligando OnchainWorker de forma segura...")
			return
		case <-ticker.C:
			ow.pollTronEvents(ctx)
		}
	}
}

func (ow *OnchainWorker) pollTronEvents(ctx context.Context) {
	if ow.cfg.TronUsdtContract == "" {
		slog.Warn("TRON_USDT_CONTRACT não configurado; pulando listener on-chain.")
		return
	}

	start := time.Now()
	slog.Debug("Iniciando varredura de blocos na TRON...")

	// 1. Em produção, buscaríamos as ordens pendentes do Postgres.
	// Simulando a obtenção das carteiras temporárias ativas no gateway.
	mockPendingAddresses := map[string]string{
		"TXYZ1234567890AddressDerivadoDoJoao": "uuid-da-ordem-do-joao",
	}

	// 2. Monta a URL para buscar eventos do contrato inteligente de USDT
	url := fmt.Sprintf("%s/v1/contracts/%s/events?event_name=Transfer&only_confirmed=true&limit=50",
		strings.TrimSuffix(ow.cfg.TronFullNodeUrl, "/"),
		ow.cfg.TronUsdtContract,
	)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		slog.Error("Erro ao montar requisição TRON", "error", err)
		return
	}

	resp, err := ow.client.Do(req)
	if err != nil {
		slog.Error("Erro ao consultar API de eventos TRON", "error", err)
		return
	}
	defer resp.Body.Close()

	var result TronEventResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("Erro ao parsear eventos da rede TRON", "error", err)
		return
	}

	// 3. Processa cada transferência encontrada no bloco
	for _, ev := range result.Data {
		// A API da TRON pode retornar endereços em formato Hexadecimal ou Base58 (Tratamos o mapeamento)
		toAddress := ev.Result.To 
		
		orderID, exists := mockPendingAddresses[toAddress]
		if !exists {
			continue
		}

		// Converte o valor retornado (Sun) para unidade USDT (6 decimais na rede TRON)
		rawAmount, _ := strconv.ParseFloat(ev.Result.Value, 64)
		amountUSDT := rawAmount / 1_000_000.0

		slog.Info("Depósito detectado na blockchain TRON", 
			"order_id", orderID, 
			"address", toAddress, 
			"amount_usdt", amountUSDT,
			"tx_hash", ev.TransactionID,
		)

		// 4. Dispara o evento de sucesso de pagamento
		ow.bus.Publish(Event{
			Type:    "onchain.detected",
			OrderID: orderID,
			Payload: map[string]interface{}{
				"tx_hash":     ev.TransactionID,
				"amount_usdt": amountUSDT,
			},
		})

		// 5. Encaminha automaticamente para a esteira de Payout PIX
		ow.bus.Publish(Event{
			Type:    "payout.requested",
			OrderID: orderID,
		})
	}

	slog.Info("Ciclo de polling TRON finalizado", "duration_ms", time.Since(start).Milliseconds())
}