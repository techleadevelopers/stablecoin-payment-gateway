package workers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

type BuySendWorker struct {
	bus    *EventBus
	db     *database.DB
	cfg    *config.Config
	client *http.Client
	mu     sync.Mutex
	active map[string]struct{}
}

func NewBuySendWorker(bus *EventBus, db *database.DB, cfg *config.Config) *BuySendWorker {
	return &BuySendWorker{
		bus:    bus,
		db:     db,
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
		active: make(map[string]struct{}),
	}
}

func (bw *BuySendWorker) Start(ctx context.Context) {
	buyChan := bw.bus.Subscribe("buy.paid")
	slog.Info("BuySendWorker escutando eventos 'buy.paid'")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Desligando BuySendWorker")
			return
		case <-ticker.C:
			bw.recoverPendingBuys(ctx)
		case event, ok := <-buyChan:
			if !ok {
				return
			}
			bw.dispatch(event)
		}
	}
}

func (bw *BuySendWorker) dispatch(event Event) {
	if !bw.markActive(event.OrderID) {
		return
	}
	go func() {
		defer bw.clearActive(event.OrderID)
		bw.processBuyOnchainSend(event)
	}()
}

func (bw *BuySendWorker) recoverPendingBuys(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	buys, err := bw.db.ListPendingBuys(scanCtx)
	if err != nil {
		slog.Error("Erro ao varrer BUYs pendentes para recovery", "error", err)
		return
	}
	for _, buy := range buys {
		bw.dispatch(Event{Type: "buy.recovery", OrderID: buy.ID})
	}
	if len(buys) > 0 {
		slog.Info("Recovery BUY varreu ordens pagas pendentes", "count", len(buys))
	}
}

func (bw *BuySendWorker) markActive(orderID string) bool {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	if _, exists := bw.active[orderID]; exists {
		return false
	}
	bw.active[orderID] = struct{}{}
	return true
}

func (bw *BuySendWorker) clearActive(orderID string) {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	delete(bw.active, orderID)
}

func (bw *BuySendWorker) processBuyOnchainSend(event Event) {
	start := time.Now()
	orderID := event.OrderID
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	buy, err := bw.db.GetBuyOrder(ctx, orderID)
	if err != nil {
		slog.Error("Erro ao buscar buy order", "buy_order_id", orderID, "error", err)
		return
	}
	if buy == nil || (buy.Status != "pago_fiat" && buy.Status != "pago_pix") {
		return
	}

	if bw.cfg.SignerUrl == "" || bw.cfg.SignerHmacSecret == "" {
		if bw.cfg.AllowSimulations && !bw.cfg.IsProduction() {
			txHash := "buy-sim-" + orderID
			if err := bw.db.UpdateBuyOrderStatus(ctx, orderID, "enviado", map[string]any{"txHashOut": txHash}); err != nil {
				slog.Error("Erro ao persistir envio BUY simulado", "buy_order_id", orderID, "error", err)
				return
			}
			slog.Warn("Signer nao configurado; envio BUY simulado", "buy_order_id", orderID, "tx_hash", txHash)
			return
		}
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": "SIGNER_URL ou SIGNER_HMAC_SECRET nao configurado"})
		slog.Error("Envio BUY bloqueado: signer ausente", "buy_order_id", orderID)
		return
	}

	payload := map[string]any{
		"to":             buy.DestAddress,
		"amount":         fmt.Sprintf("%.8f", buy.CryptoAmount),
		"tokenContract":  bw.cfg.TronUsdtContract,
		"network":        "TRON",
		"idempotencyKey": "buy-" + buy.ID,
	}
	body, _ := json.Marshal(payload)
	ts := fmt.Sprintf("%d", time.Now().Unix())
	nonceRaw := make([]byte, 8)
	_, _ = rand.Read(nonceRaw)
	nonce := hex.EncodeToString(nonceRaw)

	mac := hmac.New(sha256.New, []byte(bw.cfg.SignerHmacSecret))
	mac.Write([]byte(ts + "." + nonce + "." + string(body)))
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bw.cfg.SignerUrl+"/hd/transfer", bytes.NewReader(body))
	if err != nil {
		slog.Error("Erro ao montar request para signer BUY", "buy_order_id", orderID, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-ts", ts)
	req.Header.Set("x-nonce", nonce)
	req.Header.Set("x-signer-hmac", signature)

	resp, err := bw.client.Do(req)
	if err != nil {
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": err.Error()})
		slog.Error("Erro no signer BUY", "buy_order_id", orderID, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := fmt.Sprintf("signer status %d", resp.StatusCode)
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": errMsg})
		slog.Error("Signer rejeitou BUY", "buy_order_id", orderID, "status", resp.StatusCode)
		return
	}

	var signed struct {
		TxHash string `json:"txHash"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&signed)
	if signed.TxHash == "" {
		signed.TxHash = "signer-accepted-" + orderID
	}
	if err := bw.db.UpdateBuyOrderStatus(ctx, orderID, "enviado", map[string]any{"txHashOut": signed.TxHash}); err != nil {
		slog.Error("Erro ao atualizar BUY enviado", "buy_order_id", orderID, "error", err)
		return
	}
	bw.bus.Publish(Event{Type: "buy.sent", OrderID: orderID, Payload: map[string]any{"txHash": signed.TxHash}})
	slog.Info("Envio cripto BUY processado", "buy_order_id", orderID, "duration_ms", time.Since(start).Milliseconds())
}
