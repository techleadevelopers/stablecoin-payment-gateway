// Package mcp implements a minimal Model Context Protocol (MCP) server so
// LLM agents (Claude, ChatGPT, custom LangGraph/OpenAI agents, etc.) can
// discover and call ChainFX platform actions safely over HTTP.
//
// It intentionally speaks a simplified HTTP+JSON dialect of MCP rather than
// the stdio/JSON-RPC transport used by desktop MCP clients, so it can be
// exposed directly as part of the existing API server:
//
//	POST /mcp/initialize
//	POST /mcp/tools/list
//	POST /mcp/tools/call
//	POST /mcp/resources/list
//	POST /mcp/resources/read
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"payment-gateway/internal/agents"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/metrics"
	"payment-gateway/internal/webhooks"
	"payment-gateway/internal/workers"
)

const protocolVersion = "2024-11-05"

// PriceSource is the minimal price lookup the MCP server needs, satisfied
// by workers.PriceWorker.
type PriceSource interface {
	GetPrice(currency string) float64
	GetCurrentPrice() float64
}

// Server exposes ChainFX capabilities (quotes, orders, capability marketplace,
// AI analysis and automation webhooks) as MCP tools, resources and prompts.
type Server struct {
	db       *database.DB
	cfg      *config.Config
	prices   PriceSource
	agents   *agents.Client
	dispatch *webhooks.Dispatcher
	rl       *mcpRateLimiter // per-API-key sliding-window limiter for /mcp/tools/call
}

// ─── Per-API-key rate limiter (sliding window, stdlib only) ──────────────────

// mcpRateLimiter enforces a per-API-key call budget on POST /mcp/tools/call.
// Limits (calls per minute):
//
//	"live" keys  (sk_live_cfx_*) : 120/min
//	"test" keys  (sk_test_cfx_*) :  40/min
//	anonymous / no key           :   5/min
type mcpRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*mcpBucket
}

type mcpBucket struct {
	timestamps []time.Time
	limit      int
}

func newMCPRateLimiter() *mcpRateLimiter {
	return &mcpRateLimiter{buckets: make(map[string]*mcpBucket)}
}

// allow returns (allowed, remaining, resetAt).
func (rl *mcpRateLimiter) allow(key string, limit int) (bool, int, time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	window := time.Minute
	cutoff := now.Add(-window)
	resetAt := now.Add(window)

	b, ok := rl.buckets[key]
	if !ok {
		b = &mcpBucket{limit: limit}
		rl.buckets[key] = b
	}

	// Evict timestamps outside the sliding window
	idx := 0
	for idx < len(b.timestamps) && b.timestamps[idx].Before(cutoff) {
		idx++
	}
	b.timestamps = b.timestamps[idx:]

	remaining := b.limit - len(b.timestamps)
	if remaining <= 0 {
		if len(b.timestamps) > 0 {
			resetAt = b.timestamps[0].Add(window)
		}
		return false, 0, resetAt
	}
	b.timestamps = append(b.timestamps, now)
	return true, remaining - 1, resetAt
}

// tierLimit returns calls-per-minute for a given MCP API key.
func tierLimit(apiKey string) int {
	switch {
	case strings.HasPrefix(apiKey, "sk_live_"):
		return 120
	case strings.HasPrefix(apiKey, "sk_test_"):
		return 40
	default:
		return 5
	}
}

// New builds an MCP server bound to the platform's shared services.
func New(db *database.DB, cfg *config.Config, prices *workers.PriceWorker, agentsClient *agents.Client, dispatcher *webhooks.Dispatcher) *Server {
	return &Server{
		db:       db,
		cfg:      cfg,
		prices:   prices,
		agents:   agentsClient,
		dispatch: dispatcher,
		rl:       newMCPRateLimiter(),
	}
}

// Authorize is called before every MCP request. It must write an error
// response (401/403) and return false when the caller is not allowed in;
// returning true lets the request proceed. This keeps internal/mcp free of
// any dependency on the main server's session/API-key machinery.
type Authorize func(w http.ResponseWriter, r *http.Request) bool

// RegisterRoutes wires the MCP HTTP endpoints onto mux, prefixing paths with
// /mcp as described in the package doc. Every route is gated by authorize —
// MCP tools can read order data and create/trigger webhook automations, so
// this must never be exposed unauthenticated.
func (s *Server) RegisterRoutes(mux *http.ServeMux, authorize Authorize) {
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !authorize(w, r) {
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /mcp/initialize", guard(s.handleInitialize))
	mux.HandleFunc("POST /mcp/tools/list", guard(s.handleToolsList))
	mux.HandleFunc("POST /mcp/tools/call", guard(s.handleToolsCall))
	mux.HandleFunc("POST /mcp/resources/list", guard(s.handleResourcesList))
	mux.HandleFunc("POST /mcp/resources/read", guard(s.handleResourcesRead))
	mux.HandleFunc("POST /mcp/prompts/list", guard(s.handlePromptsList))
}

func (s *Server) handleInitialize(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"protocolVersion": protocolVersion,
		"serverInfo": map[string]any{
			"name":        "chainfx-mcp",
			"title":       "ChainFX Capability Network MCP",
			"version":     "1.2.0",
			"description": "Economic infrastructure for AI agents: discover, execute, meter, bill and settle capabilities with stablecoin payments.",
		},
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
			"prompts":   map[string]any{},
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeMCPError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
}

func decodeJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dest)
}
