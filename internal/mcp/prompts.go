package mcp

import (
	"net/http"

	"payment-gateway/internal/agents"
)

// handlePromptsList exposes the prompt template catalog defined in
// internal/agents/prompts.go so MCP clients can discover reusable prompts
// for market analysis, recommendations, anomaly detection, price
// prediction and transaction summaries.
func (s *Server) handlePromptsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"prompts": agents.ListPromptTemplates()})
}
