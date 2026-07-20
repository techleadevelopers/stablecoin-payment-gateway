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
//	chainfx_penalty_box_active_bans             gauge    Active temporary bans in the local penalty box
//	chainfx_penalty_box_bans_total              counter  Temporary bans created by the penalty box
//	chainfx_penalty_box_violations_total        counter  Rate-limit violations counted by the penalty box
//	chainfx_penalty_box_escalations_total       counter  Bans after the first offense
//	chainfx_penalty_box_blocked_requests_total  counter  Requests blocked before handlers by the penalty box
//	chainfx_webhook_delivery_failure_total      counter  Webhook delivery attempts that failed
//	chainfx_onchain_confirmations_floor         gauge    Minimum confirmation floor per network (informational)
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

// SetPenaltyBoxActiveBans records the local number of active temporary bans.
func SetPenaltyBoxActiveBans(count int) {
	if count < 0 {
		count = 0
	}
	global.penaltyBoxActiveBans.Store(int64(count))
}

// IncPenaltyBoxViolation records one rate-limit violation counted by the penalty box.
func IncPenaltyBoxViolation() {
	global.penaltyBoxViolationsTotal.Add(1)
}

// IncPenaltyBoxBan records one temporary ban created by the penalty box.
func IncPenaltyBoxBan() {
	global.penaltyBoxBansTotal.Add(1)
}

// IncPenaltyBoxEscalation records a ban after the first offense.
func IncPenaltyBoxEscalation() {
	global.penaltyBoxEscalationsTotal.Add(1)
}

// IncPenaltyBoxBlockedRequest records one request blocked before application handlers.
func IncPenaltyBoxBlockedRequest() {
	global.penaltyBoxBlockedRequestsTotal.Add(1)
}

// IncWebhookFailure records one webhook delivery failure.
func IncWebhookFailure() {
	global.webhookFailureTotal.Add(1)
}

// ── Gas Station ───────────────────────────────────────────────────────────────

// IncPaymasterRelay records one successfully submitted relay request.
func IncPaymasterRelay() {
	global.paymasterRelayTotal.Add(1)
}

// IncPaymasterRelayError records one relay that entered the DLQ.
func IncPaymasterRelayError() {
	global.paymasterRelayErrors.Add(1)
}

// IncPaymasterFeeUsdt accumulates the USDT fee earned on a relay.
func IncPaymasterFeeUsdt(feeUsdt float64) {
	global.paymasterFeeUsdtMicro.Add(int64(feeUsdt * 1_000_000))
}

// ── Auto-Sweeper ──────────────────────────────────────────────────────────────

// IncAutoSweeperRun records one Auto-Sweeper tick.
func IncAutoSweeperRun() {
	global.autosweeperRunsTotal.Add(1)
}

// IncAutoSweeperError records one failed Auto-Sweeper execution.
func IncAutoSweeperError() {
	global.autosweeperErrors.Add(1)
}

// IncAutoSweeperSwept accumulates the USDT amount moved in a successful sweep.
func IncAutoSweeperSwept(usdtAmount float64) {
	global.autosweeperSweptMicro.Add(int64(usdtAmount * 1_000_000))
}

// SetOnchainConfirmationFloor records the effective floor per network (informational).
func SetOnchainConfirmationFloor(network string, floor uint64) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.onchainFloors[strings.ToUpper(network)] = floor
}

type NFCSettlementSnapshot struct {
	Counts                     map[string]int64
	QueueAgeSeconds            float64
	SubmitLatencySeconds       float64
	ConfirmationLatencySeconds float64
	EndToEndSeconds            float64
	EfiBalanceBRL              float64
	EfiPendingBRL              float64
	EfiSubmittedBRL            float64
	EfiMinBufferBRL            float64
	EfiAvailableRealBRL        float64
}

func SetNFCSettlementSnapshot(snapshot NFCSettlementSnapshot) {
	global.mu.Lock()
	defer global.mu.Unlock()
	if snapshot.Counts == nil {
		snapshot.Counts = map[string]int64{}
	}
	global.nfcSettlement = snapshot
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

	penaltyBoxActiveBans           atomic.Int64
	penaltyBoxBansTotal            atomic.Int64
	penaltyBoxViolationsTotal      atomic.Int64
	penaltyBoxEscalationsTotal     atomic.Int64
	penaltyBoxBlockedRequestsTotal atomic.Int64

	// Gas Station (Paymaster) counters
	paymasterRelayTotal   atomic.Int64
	paymasterRelayErrors  atomic.Int64
	paymasterFeeUsdtMicro atomic.Int64 // cumulative fee in micro-USDT

	// Auto-Sweeper counters
	autosweeperRunsTotal  atomic.Int64
	autosweeperErrors     atomic.Int64
	autosweeperSweptMicro atomic.Int64 // cumulative swept USDT in micro-USDT

	// Structured state (protected by mu)
	mu            sync.RWMutex
	nfcSettlement NFCSettlementSnapshot
	onchainFloors map[string]uint64     // network → min confirmations
	opLog         []overpaymentLogEntry // ring buffer, max 1 000 entries
	startedAt     time.Time
}

type overpaymentLogEntry struct {
	IntentID   string
	ExcessUSDT float64
	At         time.Time
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
	nfcSettlement := reg.nfcSettlement
	nfcCounts := make(map[string]int64, len(nfcSettlement.Counts))
	for k, v := range nfcSettlement.Counts {
		nfcCounts[k] = v
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

	b.WriteString("# HELP chainfx_penalty_box_active_bans Active temporary bans in this API instance.\n")
	b.WriteString("# TYPE chainfx_penalty_box_active_bans gauge\n")
	fmt.Fprintf(&b, "chainfx_penalty_box_active_bans %d\n", reg.penaltyBoxActiveBans.Load())

	b.WriteString("# HELP chainfx_penalty_box_bans_total Temporary bans created by the penalty box.\n")
	b.WriteString("# TYPE chainfx_penalty_box_bans_total counter\n")
	fmt.Fprintf(&b, "chainfx_penalty_box_bans_total %d\n", reg.penaltyBoxBansTotal.Load())

	b.WriteString("# HELP chainfx_penalty_box_violations_total Rate-limit violations counted by the penalty box.\n")
	b.WriteString("# TYPE chainfx_penalty_box_violations_total counter\n")
	fmt.Fprintf(&b, "chainfx_penalty_box_violations_total %d\n", reg.penaltyBoxViolationsTotal.Load())

	b.WriteString("# HELP chainfx_penalty_box_escalations_total Bans after the first offense.\n")
	b.WriteString("# TYPE chainfx_penalty_box_escalations_total counter\n")
	fmt.Fprintf(&b, "chainfx_penalty_box_escalations_total %d\n", reg.penaltyBoxEscalationsTotal.Load())

	b.WriteString("# HELP chainfx_penalty_box_blocked_requests_total Requests blocked before application handlers by the penalty box.\n")
	b.WriteString("# TYPE chainfx_penalty_box_blocked_requests_total counter\n")
	fmt.Fprintf(&b, "chainfx_penalty_box_blocked_requests_total %d\n", reg.penaltyBoxBlockedRequestsTotal.Load())

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

	// ── chainfx_paymaster_relay_total ──────────────────────────────────────
	b.WriteString("# HELP nfc_settlement_status_total Current NFC merchant settlements by status.\n")
	b.WriteString("# TYPE nfc_settlement_status_total gauge\n")
	for _, status := range []string{"PENDING", "SUBMITTED", "SUBMISSION_UNKNOWN", "CONFIRMED", "REJECTED", "MANUAL_REVIEW"} {
		fmt.Fprintf(&b, "nfc_settlement_%s_total %d\n", strings.ToLower(status), nfcCounts[status])
	}
	b.WriteString("# HELP nfc_settlement_queue_age_seconds Age in seconds of the oldest active NFC settlement queue item.\n")
	b.WriteString("# TYPE nfc_settlement_queue_age_seconds gauge\n")
	fmt.Fprintf(&b, "nfc_settlement_queue_age_seconds %.2f\n", nfcSettlement.QueueAgeSeconds)
	b.WriteString("# HELP nfc_settlement_submit_latency_seconds Average seconds from settlement creation to provider submission.\n")
	b.WriteString("# TYPE nfc_settlement_submit_latency_seconds gauge\n")
	fmt.Fprintf(&b, "nfc_settlement_submit_latency_seconds %.2f\n", nfcSettlement.SubmitLatencySeconds)
	b.WriteString("# HELP nfc_settlement_confirmation_latency_seconds Average seconds from provider submission to confirmation.\n")
	b.WriteString("# TYPE nfc_settlement_confirmation_latency_seconds gauge\n")
	fmt.Fprintf(&b, "nfc_settlement_confirmation_latency_seconds %.2f\n", nfcSettlement.ConfirmationLatencySeconds)
	b.WriteString("# HELP nfc_settlement_end_to_end_seconds Average seconds from settlement creation to confirmation.\n")
	b.WriteString("# TYPE nfc_settlement_end_to_end_seconds gauge\n")
	fmt.Fprintf(&b, "nfc_settlement_end_to_end_seconds %.2f\n", nfcSettlement.EndToEndSeconds)
	b.WriteString("# HELP nfc_efi_treasury_brl Operational Efi treasury BRL snapshot.\n")
	b.WriteString("# TYPE nfc_efi_treasury_brl gauge\n")
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"balance\"} %.2f\n", nfcSettlement.EfiBalanceBRL)
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"pending\"} %.2f\n", nfcSettlement.EfiPendingBRL)
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"submitted\"} %.2f\n", nfcSettlement.EfiSubmittedBRL)
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"min_buffer\"} %.2f\n", nfcSettlement.EfiMinBufferBRL)
	fmt.Fprintf(&b, "nfc_efi_treasury_brl{kind=\"available_real\"} %.2f\n", nfcSettlement.EfiAvailableRealBRL)

	b.WriteString("# HELP chainfx_paymaster_relay_total Gas Station relay requests submitted successfully.\n")
	b.WriteString("# TYPE chainfx_paymaster_relay_total counter\n")
	fmt.Fprintf(&b, "chainfx_paymaster_relay_total %d\n", reg.paymasterRelayTotal.Load())

	b.WriteString("# HELP chainfx_paymaster_relay_errors_total Relay requests that exhausted retries and entered DLQ.\n")
	b.WriteString("# TYPE chainfx_paymaster_relay_errors_total counter\n")
	fmt.Fprintf(&b, "chainfx_paymaster_relay_errors_total %d\n", reg.paymasterRelayErrors.Load())

	b.WriteString("# HELP chainfx_paymaster_fee_usdt_total Cumulative USDT fee earned by the Gas Station.\n")
	b.WriteString("# TYPE chainfx_paymaster_fee_usdt_total counter\n")
	fmt.Fprintf(&b, "chainfx_paymaster_fee_usdt_total %.6f\n", float64(reg.paymasterFeeUsdtMicro.Load())/1_000_000)

	// ── chainfx_autosweeper_* ───────────────────────────────────────────────
	b.WriteString("# HELP chainfx_autosweeper_runs_total Auto-Sweeper ticker executions.\n")
	b.WriteString("# TYPE chainfx_autosweeper_runs_total counter\n")
	fmt.Fprintf(&b, "chainfx_autosweeper_runs_total %d\n", reg.autosweeperRunsTotal.Load())

	b.WriteString("# HELP chainfx_autosweeper_errors_total Auto-Sweeper executions that failed.\n")
	b.WriteString("# TYPE chainfx_autosweeper_errors_total counter\n")
	fmt.Fprintf(&b, "chainfx_autosweeper_errors_total %d\n", reg.autosweeperErrors.Load())

	b.WriteString("# HELP chainfx_autosweeper_swept_usdt_total Cumulative USDT moved from hot to cold wallet.\n")
	b.WriteString("# TYPE chainfx_autosweeper_swept_usdt_total counter\n")
	fmt.Fprintf(&b, "chainfx_autosweeper_swept_usdt_total %.6f\n", float64(reg.autosweeperSweptMicro.Load())/1_000_000)

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
	global.mu.RLock()
	nfcSettlement := global.nfcSettlement
	global.mu.RUnlock()
	return map[string]any{
		"overpayment_count":       global.overpaymentTotal.Load(),
		"overpayment_usdt":        float64(microUsdt) / 1_000_000,
		"mcp_calls_total":         global.mcpToolCallTotal.Load(),
		"mcp_calls_errors":        global.mcpToolCallErrors.Load(),
		"mcp_rate_limited":        global.mcpRateLimitedTotal.Load(),
		"penalty_box_active_bans": global.penaltyBoxActiveBans.Load(),
		"penalty_box_bans":        global.penaltyBoxBansTotal.Load(),
		"penalty_box_violations":  global.penaltyBoxViolationsTotal.Load(),
		"penalty_box_escalations": global.penaltyBoxEscalationsTotal.Load(),
		"penalty_box_blocked":     global.penaltyBoxBlockedRequestsTotal.Load(),
		"webhook_failures":        global.webhookFailureTotal.Load(),
		"paymaster_relays":        global.paymasterRelayTotal.Load(),
		"paymaster_relay_errors":  global.paymasterRelayErrors.Load(),
		"paymaster_fee_usdt":      float64(global.paymasterFeeUsdtMicro.Load()) / 1_000_000,
		"autosweeper_runs":        global.autosweeperRunsTotal.Load(),
		"autosweeper_errors":      global.autosweeperErrors.Load(),
		"autosweeper_swept_usdt":  float64(global.autosweeperSweptMicro.Load()) / 1_000_000,
		"nfc_settlement":          nfcSettlement,
		"uptime_seconds":          time.Since(global.startedAt).Seconds(),
	}
}
