package bitcoin

import (
	"context"
	"log/slog"
	"time"
)

// BTCWorker executa dois loops em background:
//  1. depositScanner — varre todos os endereços ativos em busca de novos UTXOs.
//  2. confirmationTracker — atualiza confirmações das transações pendentes.
//
// Ambos são independentes da rail EVM e não tocam nenhum worker existente.
type BTCWorker struct {
	svc *Service
}

// NewBTCWorker cria o worker. Retorna nil se svc for nil (BTC desabilitado).
func NewBTCWorker(svc *Service) *BTCWorker {
	if svc == nil {
		return nil
	}
	return &BTCWorker{svc: svc}
}

// Start inicia os dois loops e bloqueia até ctx ser cancelado.
func (w *BTCWorker) Start(ctx context.Context) {
	cfg := w.svc.Config()
	slog.Info("btc: worker iniciado",
		"network", cfg.Network,
		"deposit_interval", cfg.DepositScanInterval,
		"confirm_interval", cfg.TxScanInterval,
	)

	depositTicker := time.NewTicker(cfg.DepositScanInterval)
	confirmTicker := time.NewTicker(cfg.TxScanInterval)
	defer depositTicker.Stop()
	defer confirmTicker.Stop()

	// Executar imediatamente na inicialização
	w.scanDeposits(ctx)
	w.trackConfirmations(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("btc: worker encerrado")
			return
		case <-depositTicker.C:
			w.scanDeposits(ctx)
		case <-confirmTicker.C:
			w.trackConfirmations(ctx)
		}
	}
}

// scanDeposits varre todos os endereços ativos e sincroniza UTXOs via provider.
func (w *BTCWorker) scanDeposits(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	addresses, err := w.svc.GetAllActiveAddresses(scanCtx)
	if err != nil {
		slog.Error("btc: erro ao buscar endereços ativos", "err", err)
		return
	}

	for _, addr := range addresses {
		select {
		case <-scanCtx.Done():
			return
		default:
		}
		if err := w.svc.SyncAddressUTXOs(scanCtx, addr); err != nil {
			slog.Warn("btc: erro ao sincronizar UTXOs",
				"address", addr.Address, "err", err)
		}
	}
}

// trackConfirmations atualiza o status de transações em broadcast/pending.
func (w *BTCWorker) trackConfirmations(ctx context.Context) {
	trackCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	pending, err := w.svc.GetPendingTransactions(trackCtx)
	if err != nil {
		slog.Error("btc: erro ao buscar transações pendentes", "err", err)
		return
	}

	for _, tx := range pending {
		select {
		case <-trackCtx.Done():
			return
		default:
		}
		if err := w.svc.UpdateTransactionConfirmation(trackCtx, tx); err != nil {
			slog.Warn("btc: erro ao atualizar confirmações",
				"txid", tx.Txid, "err", err)
		}
	}
}
