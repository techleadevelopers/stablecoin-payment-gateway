package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/httpclient"
	"payment-gateway/internal/security"
)

type SweepWorker struct {
	bus    *EventBus
	db     *database.DB
	cfg    *config.Config
	client *http.Client
}

type SweepPayload struct {
	DerivationIndex int    `json:"derivationIndex"`
	To              string `json:"to"`
	Amount          string `json:"amount"`
	TokenContract   string `json:"tokenContract"`
	Network         string `json:"network"`
	IdempotencyKey  string `json:"idempotencyKey"`
}

func NewSweepWorker(bus *EventBus, db *database.DB, cfg *config.Config) *SweepWorker {
	return &SweepWorker{
		bus:    bus,
		db:     db,
		cfg:    cfg,
		client: httpclient.Default(),
	}
}

func (sw *SweepWorker) Start(ctx context.Context) {
	slog.Info("SweepWorker inicializado com segurança anti-replay.")

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Desligando SweepWorker...")
			return
		case <-ticker.C:
			sw.executeSweeps(ctx)
		}
	}
}

func (sw *SweepWorker) executeSweeps(ctx context.Context) {
	if sw.cfg.EnableSweepStub {
		if sw.cfg.IsProduction() || !sw.cfg.AllowSimulations {
			slog.Error("Sweep stub bloqueado por configuracao de producao")
			return
		}
		pending, err := sw.db.ListPendingSweeps(ctx)
		if err != nil {
			slog.Error("Erro ao listar sweeps pendentes", "error", err)
			return
		}
		for _, sweep := range pending {
			txHash := "sweep-sim-" + sweep.ID
			_ = sw.db.MarkSweep(ctx, sweep.ID, "sent", txHash)
			slog.Info("Sweep simulado concluído", "sweep_id", sweep.ID, "tx_hash", txHash)
		}
		return
	}
	if sw.cfg.SignerUrl == "" || sw.cfg.TreasuryHot == "" {
		slog.Warn("SweepWorker suspenso: SIGNER_URL ou TREASURY_HOT ausentes.")
		return
	}

	orders, err := sw.db.OrdersToSweep(ctx)
	if err != nil {
		slog.Error("Erro ao buscar ordens para sweep", "error", err)
		return
	}
	for _, order := range orders {
		if order.DerivationIndex == nil {
			continue
		}
		amount := order.AmountUSDT
		if order.DepositAmount != nil {
			amount = *order.DepositAmount
		}
		orderID := order.ID
		if _, err := sw.db.CreateSweep(ctx, *order.DerivationIndex, order.Address, sw.cfg.TreasuryHot, amount, &orderID); err != nil {
			slog.Error("Erro ao criar sweep", "order_id", order.ID, "error", err)
		}
	}
	pending, err := sw.db.ListPendingSweeps(ctx)
	if err != nil {
		slog.Error("Erro ao listar sweeps pendentes", "error", err)
		return
	}
	for _, sweep := range pending {
		sw.sendSweep(ctx, sweep)
	}
}

func (sw *SweepWorker) sendSweep(ctx context.Context, sweep database.Sweep) {
	payload := SweepPayload{
		DerivationIndex: sweep.ChildIndex,
		To:              sweep.ToAddr,
		Amount:          fmt.Sprintf("%.8f", sweep.Amount),
		TokenContract:   sw.cfg.BscUsdtContract,
		Network:         "BSC",
		IdempotencyKey:  sweep.ID,
	}
	bodyBytes, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", sw.cfg.SignerUrl+"/hd/transfer", bytes.NewBuffer(bodyBytes))
	if err != nil {
		slog.Error("Erro ao criar request de sweep", "error", err)
		return
	}

	// Injeta os headers de segurança militar exigidos pelo seu microsserviço Signer
	req.Header.Set("Content-Type", "application/json")
	security.SignRawBodyHeaders(req, sw.cfg.SignerHmacSecret, bodyBytes)

	slog.Info("Disparando comando de Sweep seguro para o Signer", "index", payload.DerivationIndex, "sweep_id", sweep.ID)

	resp, err := sw.client.Do(req)
	if err != nil {
		slog.Error("Falha crítica na comunicação com o Signer HD", "error", err)
		_ = sw.db.MarkSweep(ctx, sweep.ID, "failed", "")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		_ = sw.db.MarkSweep(ctx, sweep.ID, "sent", "signer-accepted-"+sweep.ID)
		slog.Info("Varredura (Sweep) executada e assinada com sucesso na blockchain.")
	} else {
		_ = sw.db.MarkSweep(ctx, sweep.ID, "failed", "")
		slog.Error("O serviço Signer rejeitou a transação de Sweep", "status", resp.StatusCode)
	}
}
