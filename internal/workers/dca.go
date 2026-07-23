package workers

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

// DCAWorker executes due Dollar-Cost-Averaging buy strategies.
// Uses SELECT FOR UPDATE SKIP LOCKED so multiple pod instances never
// process the same strategy simultaneously.
type DCAWorker struct {
	bus *EventBus
	db  *database.DB
	cfg *config.Config
	dlq *DeadLetterQueue
	prices interface {
		GetPrice(string) float64
	}
}

func NewDCAWorker(bus *EventBus, db *database.DB, cfg *config.Config, prices interface {
	GetPrice(string) float64
}) *DCAWorker {
	return &DCAWorker{bus: bus, db: db, cfg: cfg, dlq: NewPersistentDLQ(db, 500), prices: prices}
}

func (dw *DCAWorker) Start(ctx context.Context) {
	slog.Info("DCAWorker iniciado — verificando estratégias a cada minuto")
	dw.dlq.StartPeriodicLog(ctx, 5*time.Minute)

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("DCAWorker: encerrando")
			return
		case <-ticker.C:
			dw.runDue(ctx)
		}
	}
}

type dcaStrategy struct {
	ID          string
	UserID      string
	TokenSymbol string
	Network     string
	AmountBRL   float64
	Frequency   string
}

func (dw *DCAWorker) runDue(ctx context.Context) {
	// Use a transaction with SKIP LOCKED so concurrent instances/pods don't double-execute
	tx, err := dw.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("DCAWorker: erro ao iniciar transação", "err", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.QueryContext(ctx, `
		SELECT id, user_id, token_symbol, network, amount_brl, frequency
		FROM   dca_strategies
		WHERE  active = true
		  AND  next_execution <= NOW()
		ORDER  BY next_execution ASC
		LIMIT  50
		FOR UPDATE SKIP LOCKED
	`)
	if err != nil {
		slog.Warn("DCAWorker: erro ao buscar estratégias", "err", err)
		return
	}

	var strategies []dcaStrategy
	for rows.Next() {
		var s dcaStrategy
		if err := rows.Scan(&s.ID, &s.UserID, &s.TokenSymbol, &s.Network, &s.AmountBRL, &s.Frequency); err != nil {
			slog.Warn("DCAWorker: scan error", "err", err)
			continue
		}
		strategies = append(strategies, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("DCAWorker: erro ao iterar rows", "err", err)
		return
	}
	if len(strategies) == 0 {
		return
	}

	// Pre-schedule next executions inside the transaction to prevent re-runs
	for _, s := range strategies {
		next := nextExecution(s.Frequency)
		if _, err := tx.ExecContext(ctx,
			"UPDATE dca_strategies SET next_execution=$1 WHERE id=$2", next, s.ID); err != nil {
			slog.Warn("DCAWorker: erro ao agendar próxima execução", "strategy_id", s.ID, "err", err)
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("DCAWorker: erro ao commitar transação", "err", err)
		return
	}

	slog.Info("DCAWorker: executando estratégias", "count", len(strategies))
	for _, s := range strategies {
		go dw.execute(ctx, s)
	}
}

func (dw *DCAWorker) execute(ctx context.Context, s dcaStrategy) {
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	slog.Info("DCAWorker: executando DCA",
		"strategy_id", s.ID, "user_id", s.UserID,
		"token", s.TokenSymbol, "network", s.Network, "amount_brl", s.AmountBRL)

	if dw.cfg.AllowSimulations && !dw.cfg.IsProduction() {
		// Dev simulation: record the investment directly
		if _, err := dw.db.SQL.ExecContext(execCtx, `
			UPDATE dca_strategies
			SET    total_invested = total_invested + $1
			WHERE  id = $2`, s.AmountBRL, s.ID); err != nil {
			slog.Warn("DCAWorker: erro ao atualizar simulação", "strategy_id", s.ID, "err", err)
		} else {
			slog.Info("DCAWorker: DCA simulado concluído", "strategy_id", s.ID)
		}
		return
	}

	destAddress, err := dw.userWalletAddress(execCtx, s.UserID)
	if err != nil {
		slog.Warn("DCAWorker: erro ao buscar carteira do usuario", "strategy_id", s.ID, "user_id", s.UserID, "err", err)
		dw.dlq.Push(Event{
			Type:    "dca.buy.requested",
			OrderID: s.ID,
			Payload: map[string]any{
				"user_id": s.UserID, "asset": s.TokenSymbol, "token_symbol": s.TokenSymbol,
				"network": s.Network, "amount_brl": s.AmountBRL, "source": "dca", "strategy_id": s.ID,
				"error": err.Error(),
			},
		}, 1, err.Error())
		return
	}
	if destAddress == "" {
		slog.Warn("DCAWorker: usuario sem carteira para DCA", "strategy_id", s.ID, "user_id", s.UserID)
		return
	}

	buy, err := dw.createPaidBuyOrder(execCtx, s, destAddress)
	if err != nil {
		slog.Warn("DCAWorker: erro ao criar buy order para DCA", "strategy_id", s.ID, "err", err)
		dw.dlq.Push(Event{
			Type:    "dca.buy.requested",
			OrderID: s.ID,
			Payload: map[string]any{
				"user_id": s.UserID, "asset": s.TokenSymbol, "token_symbol": s.TokenSymbol,
				"network": s.Network, "amount_brl": s.AmountBRL, "dest_address": destAddress,
				"source": "dca", "strategy_id": s.ID, "error": err.Error(),
			},
		}, 1, err.Error())
		return
	}
	dw.bus.Publish(Event{
		Type:    "buy.paid",
		OrderID: buy.ID,
		Payload: map[string]any{
			"user_id": s.UserID, "asset": s.TokenSymbol, "token_symbol": s.TokenSymbol,
			"network": s.Network, "amount_brl": s.AmountBRL, "dest_address": destAddress,
			"source": "dca", "strategy_id": s.ID,
		},
	})
}

func (dw *DCAWorker) createPaidBuyOrder(ctx context.Context, s dcaStrategy, destAddress string) (*database.BuyOrder, error) {
	rate := dw.dcaBuyRate(s.TokenSymbol)
	if rate <= 0 {
		return nil, fmt.Errorf("cotacao indisponivel para %s", s.TokenSymbol)
	}
	cryptoAmount := s.AmountBRL / rate
	if cryptoAmount <= 0 {
		return nil, fmt.Errorf("amount DCA invalido")
	}
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	buy, err := dw.db.CreateBuyOrder(ctx, database.BuyOrderInput{
		Status:            "pago_fiat",
		AmountBRL:         s.AmountBRL,
		AmountFiat:        s.AmountBRL,
		FiatCurrency:      "BRL",
		PaymentMethod:     "dca_internal",
		ProviderPaymentID: "dca-" + s.ID + "-" + time.Now().UTC().Format("20060102150405"),
		RequestID:         "dca-" + s.ID,
		FeeBRL:            0,
		PayoutBRL:         s.AmountBRL,
		CryptoAmount:      cryptoAmount,
		Asset:             strings.ToUpper(strings.TrimSpace(s.TokenSymbol)),
		Network:           strings.ToUpper(strings.TrimSpace(s.Network)),
		DestAddress:       strings.TrimSpace(destAddress),
		RateLocked:        rate,
		RateLockExpiresAt: expiresAt,
		PixPayload: map[string]any{
			"provider":    "dca_internal",
			"source":      "dca",
			"strategy_id": s.ID,
			"user_id":     s.UserID,
		},
	})
	if err != nil {
		return nil, err
	}
	_, _ = dw.db.SQL.ExecContext(ctx, "UPDATE buy_orders SET user_id=$1::uuid WHERE id=$2::uuid", s.UserID, buy.ID)
	_, _ = dw.db.SQL.ExecContext(ctx, `
		UPDATE dca_strategies
		SET total_invested = total_invested + $1,
		    total_tokens = total_tokens + $2
		WHERE id = $3`, s.AmountBRL, cryptoAmount, s.ID)
	_ = dw.db.AddBuyEvent(ctx, buy.ID, "dca.buy.created", map[string]any{
		"strategy_id": s.ID,
		"user_id":     s.UserID,
		"asset":       s.TokenSymbol,
		"network":     s.Network,
	})
	return buy, nil
}

func (dw *DCAWorker) dcaBuyRate(asset string) float64 {
	if dw == nil || dw.cfg == nil || dw.prices == nil {
		return 0
	}
	asset = strings.ToUpper(strings.TrimSpace(asset))
	usdtBRL := dw.prices.GetPrice("BRL")
	if asset == "USDT" {
		return dcaAddBps(usdtBRL, dw.cfg.BuyRateSpreadBps)
	}
	source := asset + "USDT_SOURCE"
	usd := dw.prices.GetPrice(source)
	if usd <= 0 {
		usd = dw.prices.GetPrice(asset + "USDT")
	}
	if usd <= 0 || usdtBRL <= 0 {
		return 0
	}
	return dcaAddBps(usd*usdtBRL, dw.cfg.BuyRateSpreadBps)
}

func dcaAddBps(value float64, bps int) float64 {
	if value <= 0 {
		return 0
	}
	if bps < 0 {
		bps = 0
	}
	return value * (1 + float64(bps)/10000)
}

func (dw *DCAWorker) userWalletAddress(ctx context.Context, userID string) (string, error) {
	var address sql.NullString
	err := dw.db.SQL.QueryRowContext(ctx, `
		SELECT wallet_address
		FROM users
		WHERE id=$1::uuid AND deleted_at IS NULL`, userID).Scan(&address)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(address.String), nil
}

func nextExecution(frequency string) time.Time {
	switch frequency {
	case "weekly":
		return time.Now().Add(7 * 24 * time.Hour)
	case "monthly":
		return time.Now().AddDate(0, 1, 0)
	default: // daily
		return time.Now().Add(24 * time.Hour)
	}
}
