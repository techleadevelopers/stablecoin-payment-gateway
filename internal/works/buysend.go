package workers

import (
	"context"
	"log/slog"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

type BuySendWorker struct {
	bus *EventBus
	db  *database.DB
	cfg *config.Config
}

func NewBuySendWorker(bus *EventBus, db *database.DB, cfg *config.Config) *BuySendWorker {
	return &BuySendWorker{
		bus: bus,
		db:  db,
		cfg: cfg,
	}
}

func (bw *BuySendWorker) Start(ctx context.Context) {
	buyChan := bw.bus.Subscribe("buy.paid")
	slog.Info("BuySendWorker escutando eventos 'buy.paid'...")

	for {
		select {
		case <-ctx.Done():
			slog.Info("Desligando BuySendWorker de forma limpa...")
			return
		case event, ok := <-buyChan:
			if !ok {
				return
			}

			go bw.processBuyOnchainSend(event)
		}
	}
}

func (bw *BuySendWorker) processBuyOnchainSend(event Event) {
	start := time.Now()
	orderID := event.OrderID

	slog.Info("Iniciando transferência on-chain para Ordem de Compra", "buy_order_id", orderID)

	// TODO: Na Parte 5 implementaremos a assinatura HMAC exigida pelo Signer do projeto:
	// 'x-signer-hmac', 'x-ts', 'x-nonce' e o POST para o microsserviço isolado.

	slog.Info("Envio cripto (Buy) simulado/processado",
		"buy_order_id", orderID,
		"duration_ms", time.Since(start).Milliseconds(),
	)
}
