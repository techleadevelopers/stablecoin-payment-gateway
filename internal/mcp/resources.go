package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"payment-gateway/internal/webhooks"
)

// Resource describes an MCP resource: read-only context an agent can fetch.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

func (s *Server) resources() []Resource {
	return []Resource{
		{URI: "chainfx://rates/latest", Name: "Cotações atuais", Description: "USDT/BRL, USDT/USD, BTC/USDT e demais pares suportados.", MimeType: "application/json"},
		{URI: "chainfx://webhooks/events", Name: "Eventos de automação", Description: "Lista de eventos disponíveis para n8n/Zapier/Make.", MimeType: "application/json"},
		{URI: "chainfx://webhooks/subscriptions", Name: "Assinaturas de webhook", Description: "Assinaturas de automação configuradas atualmente.", MimeType: "application/json"},
		{URI: "chainfx://orders/{id}", Name: "Ordem por id", Description: "Detalhes de uma ordem de compra ou venda específica. Substitua {id} pelo id real.", MimeType: "application/json"},
	}
}

func (s *Server) handleResourcesList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"resources": s.resources()})
}

type resourceReadRequest struct {
	URI string `json:"uri"`
}

func (s *Server) handleResourcesRead(w http.ResponseWriter, r *http.Request) {
	var req resourceReadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeMCPError(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	content, err := s.readResource(r.Context(), req.URI)
	if err != nil {
		writeMCPError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"contents": []map[string]any{{"uri": req.URI, "mimeType": "application/json", "json": content}},
	})
}

func (s *Server) readResource(ctx context.Context, uri string) (any, error) {
	switch {
	case uri == "chainfx://rates/latest":
		return s.toolGetRates(), nil
	case uri == "chainfx://webhooks/events":
		return webhooks.AllEvents(), nil
	case uri == "chainfx://webhooks/subscriptions":
		return s.db.ListWebhookSubscriptions(ctx)
	case strings.HasPrefix(uri, "chainfx://orders/"):
		id := strings.TrimPrefix(uri, "chainfx://orders/")
		return s.toolGetOrderStatus(ctx, map[string]any{"orderId": id})
	default:
		return nil, fmt.Errorf("recurso desconhecido: %s", uri)
	}
}
