// Package webhooks implements outbound automation notifications for n8n,
// Zapier, Make.com (Integromat) and any other generic HTTP receiver.
//
// It listens on the internal workers.EventBus and, for every canonical
// automation trigger (order.created, order.completed, order.failed,
// payment.received, payout.sent, price.change, dca.executed), fans the
// event out to every active subscription that opted into it.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/workers"
)

// Canonical automation trigger events, as consumed by n8n / Zapier / Make.
const (
	EventOrderCreated    = "order.created"
	EventOrderCompleted  = "order.completed"
	EventOrderFailed     = "order.failed"
	EventPaymentReceived = "payment.received"
	EventPayoutSent      = "payout.sent"
	EventPriceChange     = "price.change"
	EventDCAExecuted     = "dca.executed"
)

// AllEvents lists every trigger automation platforms can subscribe to.
func AllEvents() []string {
	return []string{
		EventOrderCreated,
		EventOrderCompleted,
		EventOrderFailed,
		EventPaymentReceived,
		EventPayoutSent,
		EventPriceChange,
		EventDCAExecuted,
	}
}

// Known third-party automation providers. "generic" is used for any plain
// HTTP receiver that isn't one of the well-known platforms.
const (
	ProviderN8N     = "n8n"
	ProviderZapier  = "zapier"
	ProviderMake    = "make"
	ProviderGeneric = "generic"
)

// Dispatcher subscribes to the internal event bus and delivers webhooks to
// every registered automation subscription.
type Dispatcher struct {
	db         *database.DB
	cfg        *config.Config
	client     *http.Client
	maxRetries int
}

// New creates a Dispatcher. Call Start to begin consuming bus events.
func New(db *database.DB, cfg *config.Config) *Dispatcher {
	maxRetries := cfg.WebhooksMaxRetries
	if maxRetries <= 0 {
		maxRetries = 1
	}
	return &Dispatcher{
		db:         db,
		cfg:        cfg,
		client:     &http.Client{Timeout: 10 * time.Second},
		maxRetries: maxRetries,
	}
}

// busEventMapping translates an internal worker bus event type into zero or
// more canonical automation triggers.
func busEventMapping(eventType string) []string {
	switch eventType {
	case "order.created":
		return []string{EventOrderCreated}
	case "buy.created":
		return []string{EventOrderCreated}
	case "buy.paid":
		return []string{EventPaymentReceived}
	case "payout.settled":
		return []string{EventOrderCompleted, EventPayoutSent}
	case "buy.sent", "sweep.sent":
		return []string{EventOrderCompleted}
	case "price.updated":
		return []string{EventPriceChange}
	case "dca.buy.requested":
		return []string{EventDCAExecuted}
	case "order.failed", "buy.failed":
		return []string{EventOrderFailed}
	default:
		return nil
	}
}

// Start subscribes to every relevant bus event type and dispatches webhooks
// in the background until ctx is cancelled.
func (d *Dispatcher) Start(ctx context.Context, bus *workers.EventBus) {
	if !d.cfg.WebhooksEnabled {
		slog.Info("Webhooks: automação desabilitada (WEBHOOKS_ENABLED=false)")
		return
	}
	busTypes := []string{
		"order.created", "buy.created", "buy.paid", "payout.settled",
		"buy.sent", "sweep.sent", "price.updated", "dca.buy.requested",
		"order.failed", "buy.failed",
	}
	for _, t := range busTypes {
		ch := bus.Subscribe(t)
		go d.consume(ctx, bus, t, ch)
	}
	slog.Info("Webhooks: dispatcher de automação (n8n/Zapier/Make) iniciado")
}

func (d *Dispatcher) consume(ctx context.Context, bus *workers.EventBus, eventType string, ch chan workers.Event) {
	defer bus.Unsubscribe(eventType, ch)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			d.handle(ctx, ev)
		}
	}
}

func (d *Dispatcher) handle(ctx context.Context, ev workers.Event) {
	triggers := busEventMapping(ev.Type)
	if len(triggers) == 0 {
		return
	}
	payload := map[string]any{
		"orderId":     ev.OrderID,
		"sourceEvent": ev.Type,
		"data":        ev.Payload,
		"timestamp":   time.Now().UTC(),
	}
	for _, trigger := range triggers {
		d.Emit(ctx, trigger, payload)
	}
}

// Emit delivers an automation event to every active subscription listening
// for it. It is exported so other parts of the app (or the MCP server) can
// trigger events synthetically, e.g. for testing a Zap/scenario.
func (d *Dispatcher) Emit(ctx context.Context, event string, payload map[string]any) []map[string]any {
	subs, err := d.db.ListActiveWebhookSubscriptionsForEvent(ctx, event)
	if err != nil {
		slog.Warn("Webhooks: erro ao listar assinaturas", "event", event, "err", err)
		return nil
	}
	payload["event"] = event
	results := make([]map[string]any, 0, len(subs))
	for _, sub := range subs {
		result := d.deliverWithRetry(ctx, sub, event, payload)
		results = append(results, result)
	}
	return results
}

func (d *Dispatcher) deliverWithRetry(ctx context.Context, sub *database.WebhookSubscription, event string, payload map[string]any) map[string]any {
	var last map[string]any
	for attempt := 1; attempt <= d.maxRetries; attempt++ {
		result, statusCode, ok, deliveryErr := d.deliverOnce(ctx, sub, event, payload)
		_ = d.db.RecordWebhookDelivery(ctx, sub.ID, event, payload, statusCode, ok, deliveryErr, attempt)
		last = result
		if ok {
			return result
		}
		if attempt < d.maxRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	return last
}

func (d *Dispatcher) deliverOnce(ctx context.Context, sub *database.WebhookSubscription, event string, payload map[string]any) (map[string]any, int, bool, string) {
	// Re-validate at send time: DNS can change between subscription creation
	// and delivery (rebinding), so never trust a stored URL blindly.
	if err := ValidateTargetURL(sub.TargetURL); err != nil {
		return map[string]any{"subscriptionId": sub.ID, "attempted": false, "error": err.Error()}, 0, false, err.Error()
	}
	raw, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.TargetURL, bytes.NewReader(raw))
	if err != nil {
		return map[string]any{"subscriptionId": sub.ID, "attempted": false, "error": err.Error()}, 0, false, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ChainFX-Automation/1.0")
	req.Header.Set("X-ChainFX-Event", event)
	req.Header.Set("X-ChainFX-Provider", providerHeaderName(sub.Provider))
	if sig := sign(sub.Secret, raw); sig != "" {
		req.Header.Set("X-ChainFX-Signature", sig)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return map[string]any{"subscriptionId": sub.ID, "attempted": true, "ok": false, "error": err.Error()}, 0, false, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	result := map[string]any{
		"subscriptionId": sub.ID,
		"attempted":      true,
		"ok":             ok,
		"statusCode":     resp.StatusCode,
		"body":           string(body),
	}
	errMsg := ""
	if !ok {
		errMsg = "unexpected status code"
	}
	return result, resp.StatusCode, ok, errMsg
}

func providerHeaderName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderN8N:
		return "n8n"
	case ProviderZapier:
		return "zapier"
	case ProviderMake:
		return "make"
	default:
		return "generic"
	}
}

func sign(secret string, raw []byte) string {
	if strings.TrimSpace(secret) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ValidateTargetURL rejects webhook targets that could be used for SSRF:
// non-http(s) schemes, and hosts resolving to loopback, link-local, or
// private-range addresses. Call this both when a subscription is created
// and immediately before every delivery attempt (DNS can change between
// creation and send time).
func ValidateTargetURL(raw string) error {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("targetUrl inválida: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("targetUrl deve ser http ou https")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("targetUrl sem host")
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("targetUrl não pode apontar para localhost")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("não foi possível resolver o host da targetUrl: %w", err)
	}
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return fmt.Errorf("targetUrl aponta para um endereço interno/privado não permitido")
		}
	}
	return nil
}

func isDisallowedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	privateBlocks := []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "127.0.0.0/8", "fc00::/7", "::1/128",
		"100.64.0.0/10", // carrier-grade NAT
	}
	for _, block := range privateBlocks {
		_, cidr, err := net.ParseCIDR(block)
		if err == nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// IsKnownEvent reports whether event is one of the canonical triggers.
func IsKnownEvent(event string) bool {
	for _, e := range AllEvents() {
		if e == event {
			return true
		}
	}
	return false
}
