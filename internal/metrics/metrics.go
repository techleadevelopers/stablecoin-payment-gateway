// Package metrics provides lightweight Prometheus-compatible instrumentation
// for the ChainFX gateway. It uses only the Go standard library — no external
// dependencies — and exposes metrics in the Prometheus text exposition format
// at GET /metrics (protected by admin bearer auth in production).
//
// Metric catalogue:
//
//	chainfx_m2m_overpayment_total         counter  On-chain M2M deposits exceeding required USDT
//	chainfx_m2m_overpayment_usdt_total    counter  Cumulative excess USDT amount received
//	chainfx_mcp_tools_call_total          counter  Total /mcp/tools/call requests (labels: status)
//	chainfx_mcp_rate_limited_total        counter  Requests rejected by the per-key rate limiter
//	chainfx_webhook_delivery_failure_total counter  Webhook delivery attempts that failed
//	chainfx_onchain_confirmations_floor   gauge    Minimum confirmation floor per network (informational)
package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─── Global registry ──────────────────────────────────────────────────────────

var global = newRegistry()

// ─── Public API ───────────────────────────────────────────────────────────────

// IncOverpayment records one overpayment event and accumulates the excess USDT.
// intentID is stored in a short audit log (last 1 000 events, capped by size).
func IncOverpayment(intentID string, excessUSDT float64) {
	global.overpaymentTotal.Add(1)
	// accumulate as micro-USDT to avoid float arithmetic on an atomic
	global.overpaymentUSDTMicro.Add(int64(excessUSDT * 1_000_000))
	global.appendOverpaymentEvent(intentID, excessUSDT)
}

// IncMCPToolCall records one completed /mcp/tools/call execution.
// status is "ok" or "error".
func IncMCPToolCall(status string) {
	global.mcpToolCallTotal.Add(1)
	if status == "error" {
		global.mcpToolCallErrors.Add(1)
	}
}

// IncMCPRateLimited records one request rejected by the per-key rate limiter.
func IncMCPRateLimited() {
	global.mcpRateLimitedTotal.Add(1)
}

// IncWebhookFailure records one webhook delivery failure.
func IncWebhookFailure() {
	global.webhookFailureTotal.Add(1)
}

// SetOnchainConfirmationFloor records the effective floor per network (informational).
func SetOnchainConfirmationFloor(network string, floor uint64) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.onchainFloors[strings.ToUpper(network)] = floor
}

// Handler returns an HTTP handler that serves Prometheus text format metrics.
// Wire it at GET /metrics behind your admin auth middleware.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(global.render()))
	}
}

// ─── Registry ─────────────────────────────────────────────────────────────────

type registry struct {
	// Counters (thread-safe via sync/atomic)
	overpaymentTotal     atomic.Int64
	overpaymentUSDTMicro atomic.Int64 // micro-USDT (×1_000_000)
	mcpToolCallTotal     atomic.Int64
	mcpToolCallErrors    atomic.Int64
	mcpRateLimitedTotal  atomic.Int64
	webhookFailureTotal  atomic.Int64

	// Structured state (protected by mu)
	mu            sync.RWMutex
	onchainFloors map[string]uint64        // network → min confirmations
	opLog         []overpaymentLogEntry    // ring buffer, max 1 000 entries
	startedAt     time.Time
}

type overpaymentLogEntry struct {
	IntentID  string
	ExcessUSDT float64
	At        time.Time
}

func newRegistry() *registry {
	return &registry{
		onchainFloors: make(map[string]uint64),
		startedAt:     time.Now(),
	}
}

func (reg *registry) appendOverpaymentEvent(intentID string, excess float64) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	const maxLog = 1000
	if len(reg.opLog) >= maxLog {
		// drop oldest
		reg.opLog = reg.opLog[1:]
	}
	reg.opLog = append(reg.opLog, overpaymentLogEntry{
		IntentID:   intentID,
		ExcessUSDT: excess,
		At:         time.Now().UTC(),
	})
}

// render produces a Prometheus text-format snapshot.
func (reg *registry) render() string {
	reg.mu.RLock()
	floors := make(map[string]uint64, len(reg.onchainFloors))
	for k, v := range reg.onchainFloors {
		floors[k] = v
	}
	reg.mu.RUnlock()

	uptimeSec := time.Since(reg.startedAt).Seconds()

	var b strings.Builder

	// ── chainfx_m2m_overpayment_total ──────────────────────────────────────
	b.WriteString("# HELP chainfx_m2m_overpayment_total Total on-chain deposits exceeding the required M2M intent amount.\n")
	b.WriteString("# TYPE chainfx_m2m_overpayment_total counter\n")
	fmt.Fprintf(&b, "chainfx_m2m_overpayment_total %d\n", reg.overpaymentTotal.Load())

	b.WriteString("# HELP chainfx_m2m_overpayment_usdt_total Cumulative excess USDT received across all overpayments.\n")
	b.WriteString("# TYPE chainfx_m2m_overpayment_usdt_total counter\n")
	microUsdt := reg.overpaymentUSDTMicro.Load()
	fmt.Fprintf(&b, "chainfx_m2m_overpayment_usdt_total %.6f\n", float64(microUsdt)/1_000_000)

	// ── chainfx_mcp_tools_call_total ───────────────────────────────────────
	b.WriteString("# HELP chainfx_mcp_tools_call_total Total /mcp/tools/call requests processed.\n")
	b.WriteString("# TYPE chainfx_mcp_tools_call_total counter\n")
	total := reg.mcpToolCallTotal.Load()
	errors := reg.mcpToolCallErrors.Load()
	fmt.Fprintf(&b, "chainfx_mcp_tools_call_total{status=\"ok\"} %d\n", total-errors)
	fmt.Fprintf(&b, "chainfx_mcp_tools_call_total{status=\"error\"} %d\n", errors)

	// ── chainfx_mcp_rate_limited_total ─────────────────────────────────────
	b.WriteString("# HELP chainfx_mcp_rate_limited_total Requests rejected by the MCP per-API-key rate limiter.\n")
	b.WriteString("# TYPE chainfx_mcp_rate_limited_total counter\n")
	fmt.Fprintf(&b, "chainfx_mcp_rate_limited_total %d\n", reg.mcpRateLimitedTotal.Load())

	// ── chainfx_webhook_delivery_failure_total ─────────────────────────────
	b.WriteString("# HELP chainfx_webhook_delivery_failure_total Webhook HTTP delivery attempts that returned a non-2xx status or timed out.\n")
	b.WriteString("# TYPE chainfx_webhook_delivery_failure_total counter\n")
	fmt.Fprintf(&b, "chainfx_webhook_delivery_failure_total %d\n", reg.webhookFailureTotal.Load())

	// ── chainfx_onchain_confirmation_floor ─────────────────────────────────
	b.WriteString("# HELP chainfx_onchain_confirmation_floor Effective minimum block confirmation floor per network (safety override).\n")
	b.WriteString("# TYPE chainfx_onchain_confirmation_floor gauge\n")
	for network, floor := range floors {
		fmt.Fprintf(&b, "chainfx_onchain_confirmation_floor{network=%q} %d\n", network, floor)
	}

	// ── chainfx_process_uptime_seconds ─────────────────────────────────────
	b.WriteString("# HELP chainfx_process_uptime_seconds Seconds since the API process started.\n")
	b.WriteString("# TYPE chainfx_process_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "chainfx_process_uptime_seconds %.2f\n", uptimeSec)

	return b.String()
}

// ─── Recent overpayment log (for /app/risk or alerting integrations) ──────────

// OverpaymentEvent is a structured record of a single overpayment.
type OverpaymentEvent struct {
	IntentID   string    `json:"intent_id"`
	ExcessUSDT float64   `json:"excess_usdt"`
	At         time.Time `json:"at"`
}

// RecentOverpayments returns up to n recent overpayment events (newest last).
// Safe for concurrent access.
func RecentOverpayments(n int) []OverpaymentEvent {
	global.mu.RLock()
	defer global.mu.RUnlock()
	src := global.opLog
	if n <= 0 || n > len(src) {
		n = len(src)
	}
	start := len(src) - n
	out := make([]OverpaymentEvent, n)
	for i, e := range src[start:] {
		out[i] = OverpaymentEvent{IntentID: e.IntentID, ExcessUSDT: e.ExcessUSDT, At: e.At}
	}
	return out
}

// HasPendingOverpayments returns true when any overpayment has been detected
// since the process started. Used as a health/alert gate.
func HasPendingOverpayments() bool {
	return global.overpaymentTotal.Load() > 0
}

// Snapshot returns a JSON-serializable summary of current counters.
// Intended for the /app/risk dashboard endpoint.
func Snapshot() map[string]any {
	microUsdt := global.overpaymentUSDTMicro.Load()
	return map[string]any{
		"overpayment_count":    global.overpaymentTotal.Load(),
		"overpayment_usdt":     float64(microUsdt) / 1_000_000,
		"mcp_calls_total":      global.mcpToolCallTotal.Load(),
		"mcp_calls_errors":     global.mcpToolCallErrors.Load(),
		"mcp_rate_limited":     global.mcpRateLimitedTotal.Load(),
		"webhook_failures":     global.webhookFailureTotal.Load(),
		"uptime_seconds":       time.Since(global.startedAt).Seconds(),
	}
}
