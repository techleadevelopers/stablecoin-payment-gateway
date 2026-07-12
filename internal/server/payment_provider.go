package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/certutil"
)

func (s *Server) createPaymentIntent(ctx context.Context, buyID string, amountFiat float64, fiatCurrency, paymentMethod string, customer paymentCustomerInput) (map[string]any, error) {
	if paymentMethod == "stripe" {
		return map[string]any{
			"provider":     "stripe",
			"status":       "requires_provider_checkout",
			"buyId":        buyID,
			"amount":       amountFiat,
			"currency":     fiatCurrency,
			"instructions": "create Stripe Checkout/PaymentIntent client-side or upstream and include metadata.buyId",
		}, nil
	}
	if paymentMethod != "pix" {
		return nil, fmt.Errorf("rail de pagamento nao suportado")
	}
	if !s.efiPixConfigured() {
		if s.cfg.AllowSimulations && !s.cfg.IsProduction() {
			return map[string]any{"provider": "efi", "mode": "simulation", "pixKey": "chavepix@nexswap.com", "qrCodeUrl": "/images/qrcode.png", "buyId": buyID}, nil
		}
		return nil, fmt.Errorf("credenciais EfÃ­ Pix nao configuradas")
	}
	return s.createEfiPixCharge(ctx, buyID, amountFiat, customer)
}

func (s *Server) efiPixConfigured() bool {
	return strings.TrimSpace(s.cfg.EfiClientID) != "" &&
		strings.TrimSpace(s.cfg.EfiClientSecret) != "" &&
		strings.TrimSpace(s.cfg.EfiPixKey) != "" &&
		(strings.TrimSpace(s.cfg.EfiCertificatePath) != "" || strings.Trim(strings.TrimSpace(s.cfg.EfiCertificateP12), `"'`) != "")
}

func (s *Server) efiCertificateSource() string {
	if strings.Trim(strings.TrimSpace(s.cfg.EfiCertificateP12), `"'`) != "" {
		return "base64"
	}
	if strings.TrimSpace(s.cfg.EfiCertificatePath) != "" {
		return "file"
	}
	return "missing"
}

func (s *Server) efiCertificateReady() (bool, string) {
	if !s.efiPixConfigured() {
		return false, "credenciais EfÃ­ incompletas"
	}
	if _, err := s.loadEfiCertificate(strings.TrimSpace(s.cfg.EfiCertificatePath), strings.TrimSpace(s.cfg.EfiCertificateKey)); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (s *Server) createEfiPixCharge(ctx context.Context, buyID string, amountFiat float64, customer paymentCustomerInput) (map[string]any, error) {
	client, err := s.efiHTTPClient()
	if err != nil {
		return nil, err
	}
	token, err := s.efiAccessToken(ctx, client)
	if err != nil {
		return nil, err
	}
	txid := efiTxIDFromBuyID(buyID)
	payload := map[string]any{
		"calendario": map[string]any{"expiracao": s.cfg.RateLockSec},
		"valor":      map[string]any{"original": fmt.Sprintf("%.2f", amountFiat)},
		"chave":      s.cfg.EfiPixKey,
		"solicitacaoPagador": fmt.Sprintf(
			"Pedido %s - USDT BSC",
			buyID,
		),
	}
	if debtor := buildEfiDebtor(customer); len(debtor) > 0 {
		payload["devedor"] = debtor
	}

	raw, _ := json.Marshal(payload)
	endpoint := strings.TrimRight(s.cfg.EfiApiBaseURL, "/") + "/v2/cob/" + txid
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("EfÃ­ rejeitou cobranca PIX: status %d body %s", resp.StatusCode, compactProviderBody(body))
	}
	var provider map[string]any
	if err := json.Unmarshal(body, &provider); err != nil {
		return nil, fmt.Errorf("EfÃ­ respondeu JSON invalido")
	}
	provider["provider"] = "efi"
	provider["buyId"] = buyID
	provider["txid"] = defaultString(fmt.Sprint(provider["txid"]), txid)
	provider["providerPaymentId"] = txid
	provider["providerStatus"] = resp.StatusCode
	provider["pixKey"] = s.cfg.EfiPixKey
	if qr, err := s.efiQRCode(ctx, client, token, provider); err == nil {
		for key, value := range qr {
			provider[key] = value
		}
	} else {
		return nil, err
	}
	if mapString(provider, "qrCodeUrl") == "" || mapString(provider, "pixCopiaECola") == "" {
		return nil, fmt.Errorf("EfÃ­ nao retornou QR Code Pix completo")
	}
	if feeBps := s.cfg.EfiPixFeeBps; feeBps > 0 {
		provider["providerFeeBps"] = feeBps
		provider["providerFeeFiatEstimated"] = math.Round((amountFiat*float64(feeBps)/10000)*100) / 100
	}
	if customer.Name != "" || customer.Email != "" || customer.CPF != "" || customer.Phone != "" || customer.BirthDate != "" || len(customer.Address) > 0 {
		provider["customerSubmitted"] = buildCustomerAudit(s.cfg.LGPDSecret, customer.CPF, customer.Phone, customer.Email, customer.Name, customer.BirthDate, customer.Address)
	}
	return provider, nil
}

func (s *Server) efiQRCode(ctx context.Context, client *http.Client, token string, charge map[string]any) (map[string]any, error) {
	locID := nestedFloat(charge, "loc", "id")
	if locID <= 0 {
		return nil, fmt.Errorf("cobranca EfÃ­ sem loc.id")
	}
	endpoint := fmt.Sprintf("%s/v2/loc/%d/qrcode", strings.TrimRight(s.cfg.EfiApiBaseURL, "/"), int64(locID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("EfÃ­ rejeitou QR Code Pix: status %d body %s", resp.StatusCode, compactProviderBody(body))
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return map[string]any{
		"pixCopiaECola":     mapString(data, "qrcode"),
		"qrCodeUrl":         mapString(data, "imagemQrcode"),
		"linkVisualizacao":  mapString(data, "linkVisualizacao"),
		"qrCodeProviderRaw": data,
	}, nil
}

func (s *Server) efiHTTPClient() (*http.Client, error) {
	certPath := strings.TrimSpace(s.cfg.EfiCertificatePath)
	keyPath := strings.TrimSpace(s.cfg.EfiCertificateKey)
	cert, err := s.loadEfiCertificate(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}},
	}, nil
}

// loadEfiCertificate delegates to the shared certutil loader (used by both
// this direct-charge HTTP client and the PSP EfiAdapter wired in cmd/api/main.go,
// so PKCS#12/PEM decoding logic lives in exactly one place).
func (s *Server) loadEfiCertificate(certPath, keyPath string) (tls.Certificate, error) {
	return certutil.LoadCertificate(certPath, keyPath, s.cfg.EfiCertificateP12, s.cfg.EfiCertificatePass)
}

func (s *Server) efiAccessToken(ctx context.Context, client *http.Client) (string, error) {
	raw := []byte(`{"grant_type":"client_credentials"}`)
	endpoint := strings.TrimRight(s.cfg.EfiApiBaseURL, "/") + "/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.cfg.EfiClientID, s.cfg.EfiClientSecret)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("EfÃ­ OAuth rejeitou credenciais: status %d", resp.StatusCode)
	}
	var data struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &data); err != nil || strings.TrimSpace(data.AccessToken) == "" {
		return "", fmt.Errorf("EfÃ­ OAuth respondeu token invÃ¡lido")
	}
	return data.AccessToken, nil
}

func buildEfiDebtor(customer paymentCustomerInput) map[string]any {
	cpf := onlyDigits(customer.CPF)
	name := strings.TrimSpace(customer.Name)
	if cpf == "" || name == "" {
		return nil
	}
	return map[string]any{
		"cpf":  cpf,
		"nome": name,
	}
}

func efiTxIDFromBuyID(buyID string) string {
	clean := onlyAlphaNum(buyID)
	if len(clean) >= 26 && len(clean) <= 35 {
		return clean
	}
	if len(clean) > 35 {
		return clean[:35]
	}
	return strings.ToUpper(clean + strings.Repeat("0", 26-len(clean)))
}

func buyIDFromEfiTxID(txid string) string {
	clean := onlyAlphaNum(txid)
	if len(clean) == 32 {
		return fmt.Sprintf("%s-%s-%s-%s-%s", clean[0:8], clean[8:12], clean[12:16], clean[16:20], clean[20:32])
	}
	return txid
}

func onlyAlphaNum(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func copyAddressField(out, in map[string]any, target string, keys ...string) {
	if value := firstMapString(in, keys...); value != "" {
		out[target] = value
	}
}

func firstMapString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := values[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func mapString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	if raw, ok := values[key]; ok {
		if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}
