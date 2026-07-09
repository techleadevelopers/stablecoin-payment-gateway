package workers

import (
	"context"
	"log/slog"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"sync"
)

type WorkerManager struct {
	Bus           *EventBus
	PriceWorker   *PriceWorker
	PayoutWorker  *PayoutWorker
	BuySendWorker *BuySendWorker
	OnchainWorker *OnchainWorker
	SweepWorker   *SweepWorker
	db            *database.DB
	cfg           *config.Config
	wg            sync.WaitGroup
}

func NewWorkerManager(db *database.DB, cfg *config.Config) *WorkerManager {
	bus := NewEventBus()

	return &WorkerManager{
		Bus:           bus,
		PriceWorker:   NewPriceWorker(bus),
		PayoutWorker:  NewPayoutWorker(bus, db, cfg),
		BuySendWorker: NewBuySendWorker(bus, db, cfg),
		OnchainWorker: NewOnchainWorker(bus, db, cfg),
		SweepWorker:   NewSweepWorker(bus, db, cfg),
		db:            db,
		cfg:           cfg,
	}
}

// StartAll liga todas as chaves do motor ao mesmo tempo.
// Cada worker roda em sua própria goroutine.
func (wm *WorkerManager) StartAll(ctx context.Context) {
	slog.Info("Iniciando todos os workers...")
	
	wm.wg.Add(5)
	
	go func() {
		defer wm.wg.Done()
		wm.PriceWorker.Start(ctx)
	}()
	
	go func() {
		defer wm.wg.Done()
		wm.PayoutWorker.Start(ctx)
	}()
	
	go func() {
		defer wm.wg.Done()
		wm.BuySendWorker.Start(ctx)
	}()
	
	go func() {
		defer wm.wg.Done()
		wm.OnchainWorker.Start(ctx)
	}()
	
	go func() {
		defer wm.wg.Done()
		wm.SweepWorker.Start(ctx)
	}()
	
	slog.Info("Todos os workers iniciados com sucesso")
}

// Shutdown aguarda todos os workers finalizarem
func (wm *WorkerManager) Shutdown(ctx context.Context) {
	slog.Info("Iniciando shutdown dos workers...")
	
	// Fecha o EventBus primeiro para parar de receber novos eventos
	wm.Bus.Shutdown()
	
	// Aguarda todos os workers finalizarem com timeout
	done := make(chan struct{})
	go func() {
		wm.wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		slog.Info("Todos os workers finalizados com sucesso")
	case <-ctx.Done():
		slog.Warn("Timeout no shutdown dos workers", "timeout", ctx.Err())
	}
}

// StartAllAndWait inicia os workers e aguarda o contexto ser cancelado
func (wm *WorkerManager) StartAllAndWait(ctx context.Context) {
	wm.StartAll(ctx)
	<-ctx.Done()
	wm.Shutdown(context.Background())
}