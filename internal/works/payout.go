package workers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

type PayoutWorker struct {
	bus *EventBus
	db  *database.DB
	cfg *config.Config
}

func NewPayoutWorker(bus *EventBus, db *database.DB, cfg *config.Config) *PayoutWorker {
	return &PayoutWorker{
		bus: bus,
		db:  db,
		cfg: cfg,
	}
}

// Start assina a fila de eventos e processa cada requisição de PIX concorrentemente
func (pw *PayoutWorker) Start(ctx context.Context) {
	// Se inscreve na fila interna (Substitui o subscribe do Node)
	payoutChan := pw.bus.Subscribe("payout.requested")
	slog.Info("PayoutWorker escutando eventos 'payout.requested'...")

	for {
		select {
		case <-ctx.Done():
			slog.Info("Desligando PayoutWorker de forma limpa...")
			return
		case event, ok := <-payoutChan:
			if !ok {
				return
			}

			// Dispara uma Goroutine por pagamento para que um PIX lento não trave os outros
			go pw.processPayout(event)
		}
	}
}

func (pw *PayoutWorker) processPayout(event Event) {
	start := time.Now()
	orderID := event.OrderID

	slog.Info("Processando solicitação de Payout PIX", "order_id", orderID)

	// Simulação idêntica à lógica do seu Node.js quando falta o Token do PagBank
	if pw.cfg.PagSeguroApiToken == "" {
		slog.Warn("PagBank token não configurado, executando simulação de PIX", "order_id", orderID)

		// NOTA: Nas próximas etapas vamos criar as queries reais no DB.
		// Aqui simulamos a atualização do status da ordem para 'concluida'.

		// Publica o encerramento do fluxo no barramento interno
		pw.bus.Publish(Event{
			Type:    "payout.settled",
			OrderID: orderID,
			Payload: map[string]interface{}{
				"status":      "concluida",
				"tx_hash_pix": fmt.Sprintf("pix-sim-%s", orderID),
			},
		})

		slog.Info("Payout simulado concluído com sucesso",
			"order_id", orderID,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return
	}

	// TODO: Na Parte 4 adicionaremos a chamada HTTP real à API do PagBank usando a URL:
	// pw.cfg.PagSeguroApiBaseUrl + "/instant-payments"
}
