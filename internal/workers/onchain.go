package workers

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/models"
	rpcpool "payment-gateway/internal/rpc"
)

// erc20TransferTopic = keccak256("Transfer(address,address,uint256)")
var erc20TransferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

const (
	requiredConfirmations = uint64(6)  // 6 blocks ≈ 18 s finality on BSC
	scanBlockRange        = uint64(50) // blocks per poll tick
	usdtDecimals          = 18         // BSC USDT decimals
)

// OnchainWorker monitors BSC for USDT deposits to pending sell orders.
type OnchainWorker struct {
	bus  *EventBus
	db   *database.DB
	cfg  *config.Config
	pool *rpcpool.Pool
	dlq  *DeadLetterQueue
}

func NewOnchainWorker(bus *EventBus, db *database.DB, cfg *config.Config) *OnchainWorker {
	w := &OnchainWorker{bus: bus, db: db, cfg: cfg, dlq: NewDLQ(1000, nil)}
	if cfg.BscRpcUrls != "" {
		pool, err := rpcpool.NewPool(cfg.BscRpcUrls)
		if err != nil {
			slog.Warn("OnchainWorker: RPC pool init failed", "err", err)
		} else {
			w.pool = pool
		}
	}
	return w
}

func (ow *OnchainWorker) Start(ctx context.Context) {
	slog.Info("OnchainWorker BSC iniciado")
	if ow.pool != nil {
		ow.pool.StartHealthChecks(ctx, 30*time.Second)
	}

	mobilePayout := ow.bus.Subscribe("mobile.payout.requested")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("OnchainWorker: encerrando")
			return
		case ev, ok := <-mobilePayout:
			if !ok {
				return
			}
			go ow.forwardMobilePayout(ev)
		case <-ticker.C:
			ow.poll(ctx)
		}
	}
}

func (ow *OnchainWorker) poll(ctx context.Context) {
	if ow.pool == nil || ow.cfg.BscUsdtContract == "" {
		slog.Debug("OnchainWorker: BSC não configurado, pulando poll")
		return
	}

	pollCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	latestBlock, err := ow.pool.BlockNumber(pollCtx)
	if err != nil {
		slog.Warn("OnchainWorker: erro ao buscar bloco", "err", err)
		return
	}

	if latestBlock <= requiredConfirmations {
		slog.Debug("OnchainWorker: aguardando blocos suficientes", "latest", latestBlock)
		return
	}
	scanHead := latestBlock - requiredConfirmations

	fromBlock := ow.getCursor(pollCtx, scanHead)
	if fromBlock >= scanHead {
		return
	}
	toBlock := min64(fromBlock+scanBlockRange, scanHead)
	if toBlock <= fromBlock {
		slog.Warn("OnchainWorker: faixa de blocos invalida, pulando poll",
			"from", fromBlock, "to", toBlock, "latest", latestBlock, "scan_head", scanHead)
		return
	}

	// Fetch pending orders. On error: do NOT advance cursor — we'd miss deposits.
	pendingOrders, err := ow.db.GetPendingOrders(pollCtx)
	if err != nil {
		slog.Warn("OnchainWorker: erro ao buscar ordens pendentes — cursor não avançado", "err", err)
		return
	}
	if len(pendingOrders) == 0 {
		ow.saveCursor(pollCtx, toBlock)
		return
	}

	// Build address (lowercase) → []orderID  (one-to-many: multiple orders can share an address)
	addrSet := make(map[string][]string, len(pendingOrders))
	for _, o := range pendingOrders {
		if o.Address != "" {
			key := strings.ToLower(o.Address)
			addrSet[key] = append(addrSet[key], o.ID)
		}
	}
	if len(addrSet) == 0 {
		ow.saveCursor(pollCtx, toBlock)
		return
	}

	query := ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(fromBlock + 1),
		ToBlock:   new(big.Int).SetUint64(toBlock),
		Addresses: []common.Address{common.HexToAddress(ow.cfg.BscUsdtContract)},
		Topics:    [][]common.Hash{{erc20TransferTopic}},
	}
	logs, err := ow.pool.FilterLogs(pollCtx, query)
	if err != nil {
		slog.Warn("OnchainWorker: erro ao filtrar logs — cursor não avançado",
			"from", fromBlock, "to", toBlock, "err", err)
		return // do NOT advance cursor on RPC error
	}

	slog.Debug("OnchainWorker: scan",
		"blocks", fmt.Sprintf("%d→%d", fromBlock+1, toBlock),
		"logs", len(logs), "orders", len(pendingOrders))

	// safeToBlock tracks the earliest unconfirmed deposit we've seen.
	// We must NOT advance cursor past (unconfirmed_block - 1) so we revisit
	// those blocks once they accumulate enough confirmations.
	safeToBlock := toBlock
	for _, log := range logs {
		if len(log.Topics) < 3 {
			continue
		}
		toAddr := strings.ToLower(common.HexToAddress(log.Topics[2].Hex()).Hex())
		orderIDs, ok := addrSet[toAddr]
		if !ok {
			continue
		}
		if latestBlock < log.BlockNumber+requiredConfirmations {
			// This deposit is not yet confirmed — hold cursor at (block - 1)
			if log.BlockNumber > 0 && log.BlockNumber-1 < safeToBlock {
				safeToBlock = log.BlockNumber - 1
			}
			slog.Debug("OnchainWorker: depósito aguardando confirmações",
				"tx", log.TxHash.Hex(), "block", log.BlockNumber,
				"conf_remaining", (log.BlockNumber+requiredConfirmations)-latestBlock)
			continue
		}
		rawAmount := new(big.Int).SetBytes(log.Data)
		for _, orderID := range orderIDs {
			ow.confirmDeposit(ctx, orderID, log.TxHash.Hex(), rawAmount, log.BlockNumber)
		}
	}

	// Only advance cursor to the safe boundary
	if safeToBlock > fromBlock {
		ow.saveCursor(pollCtx, safeToBlock)
	}
}

func (ow *OnchainWorker) confirmDeposit(ctx context.Context, orderID, txHash string, rawAmount *big.Int, blockNum uint64) {
	cCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	order, err := ow.db.GetOrder(cCtx, orderID)
	if err != nil || order == nil {
		slog.Warn("OnchainWorker: ordem não encontrada", "order_id", orderID)
		return
	}
	// Idempotency check — skip orders already past deposit-waiting states
	if order.Status != models.StatusAguardandoDeposito &&
		order.Status != models.StatusAguardandoValidacao {
		return
	}

	// Detect replay: if deposit_tx already matches this txHash, skip
	if order.DepositTx != nil && *order.DepositTx == txHash {
		slog.Debug("OnchainWorker: depósito já registrado, ignorando", "tx", txHash)
		return
	}

	divisor := new(big.Float).SetInt(
		new(big.Int).Exp(big.NewInt(10), big.NewInt(usdtDecimals), nil))
	amountFloat, _ := new(big.Float).Quo(
		new(big.Float).SetInt(rawAmount), divisor).Float64()

	tol := ow.cfg.BscDepositTolerancePct
	if tol <= 0 {
		tol = 0.02
	}
	if order.AmountUSDT > 0 {
		diff := (amountFloat - order.AmountUSDT) / order.AmountUSDT
		if diff < -tol {
			slog.Warn("OnchainWorker: depósito abaixo da tolerância",
				"order_id", orderID, "expected", order.AmountUSDT,
				"received", amountFloat, "diff_pct", fmt.Sprintf("%.2f%%", diff*100))
			_ = ow.db.UpdateOrderStatus(cCtx, orderID, "aguardando_validacao", map[string]any{
				"depositTx": txHash, "depositAmount": amountFloat, "block": blockNum,
			})
			return
		}
	}

	slog.Info("OnchainWorker: depósito confirmado",
		"order_id", orderID, "tx", txHash, "amount_usdt", amountFloat, "block", blockNum)

	if err := ow.db.UpdateOrderStatus(cCtx, orderID, "pago", map[string]any{
		"depositTx": txHash, "depositAmount": amountFloat, "block": blockNum,
	}); err != nil {
		slog.Error("OnchainWorker: erro ao atualizar status", "order_id", orderID, "err", err)
		return
	}

	ow.bus.Publish(Event{
		Type:    "payout.requested",
		OrderID: orderID,
		Payload: map[string]any{"txHash": txHash, "depositAmount": amountFloat, "blockNumber": blockNum},
	})
}

func (ow *OnchainWorker) getCursor(ctx context.Context, latestBlock uint64) uint64 {
	block, found, err := ow.db.GetCursor(ctx, "BSC")

	if err != nil || !found || block == 0 {
		return subtractFloor(latestBlock, 1000)
	}

	// evita pedir histórico gigante no RPC
	const maxLookback uint64 = 50000

	if latestBlock > uint64(block) &&
		latestBlock-uint64(block) > maxLookback {

		slog.Warn("BSC cursor muito antigo, resetando",
			"old_cursor", block,
			"latest", latestBlock,
		)

		return subtractFloor(latestBlock, 1000)
	}

	return uint64(block)
}

func (ow *OnchainWorker) saveCursor(ctx context.Context, block uint64) {
	if err := ow.db.SaveCursor(ctx, "BSC", int64(block)); err != nil {
		slog.Warn("OnchainWorker: erro ao salvar cursor", "block", block, "err", err)
	}
}

func (ow *OnchainWorker) forwardMobilePayout(ev Event) {
	slog.Info("OnchainWorker: encaminhando mobile payout", "order_id", ev.OrderID)
	ow.bus.Publish(Event{
		Type:    "payout.requested",
		OrderID: ev.OrderID,
		Payload: ev.Payload,
	})
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func subtractFloor(n, delta uint64) uint64 {
	if n <= delta {
		return 0
	}
	return n - delta
}
