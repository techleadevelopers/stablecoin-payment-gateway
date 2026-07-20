package mobile

import (
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
)

func (s *Server) handleCreateSupportTicket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Category string `json:"category"`
		Subject  string `json:"subject"`
		Message  string `json:"message"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	req.Category = strings.TrimSpace(req.Category)
	req.Subject = strings.TrimSpace(req.Subject)
	req.Message = strings.TrimSpace(req.Message)
	if req.Category == "" || req.Subject == "" || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "category, subject e message obrigatorios"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ticket_id":  "mob_sup_" + strings.ReplaceAll(database.NewID(), "-", ""),
		"user_id":    userIDFromCtx(r),
		"category":   req.Category,
		"subject":    req.Subject,
		"status":     "received",
		"created_at": time.Now().UTC(),
	})
}

func (s *Server) handleImportWalletToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Network  string `json:"network"`
		Contract string `json:"contract"`
		Symbol   string `json:"symbol"`
		Name     string `json:"name"`
		Decimals int    `json:"decimals"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	req.Network = strings.ToUpper(strings.TrimSpace(req.Network))
	req.Contract = strings.TrimSpace(req.Contract)
	req.Symbol = strings.ToUpper(strings.TrimSpace(req.Symbol))
	req.Name = strings.TrimSpace(req.Name)
	if req.Network == "" || req.Contract == "" || req.Symbol == "" || req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network, contract, symbol e name obrigatorios"})
		return
	}
	if req.Decimals <= 0 {
		req.Decimals = 18
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"token_import_id": "mob_tok_" + strings.ReplaceAll(database.NewID(), "-", ""),
		"user_id":         userIDFromCtx(r),
		"network":         req.Network,
		"contract":        req.Contract,
		"symbol":          req.Symbol,
		"name":            req.Name,
		"decimals":        req.Decimals,
		"status":          "received",
		"created_at":      time.Now().UTC(),
	})
}
