package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// HMACValidator gerencia a validação de assinaturas HMAC
type HMACValidator struct {
	activeSecret string
	oldSecret    string
	maxSkew      int64
}

// NewHMACValidator cria um novo validador HMAC
func NewHMACValidator(activeSecret string, maxSkew int64) *HMACValidator {
	return &HMACValidator{
		activeSecret: activeSecret,
		maxSkew:      maxSkew,
	}
}

// SetOldSecret define o segredo antigo para rotação
func (h *HMACValidator) SetOldSecret(oldSecret string) {
	h.oldSecret = oldSecret
}

// RotateSecret rotaciona o segredo ativo
func (h *HMACValidator) RotateSecret(newSecret string) {
	h.oldSecret = h.activeSecret
	h.activeSecret = newSecret
}

// ValidateHMAC valida a assinatura HMAC-SHA256
func (h *HMACValidator) ValidateHMAC(tsStr, nonce, hmacHeader string, body []byte) error {
	if h.activeSecret == "" || hmacHeader == "" || tsStr == "" || nonce == "" {
		return fmt.Errorf("credenciais de assinatura ausentes")
	}

	// 1. Valida timestamp (anti-replay por tempo)
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fmt.Errorf("timestamp inválido: %w", err)
	}

	now := time.Now().Unix()
	diff := now - ts
	if diff < 0 {
		diff = -diff
	}
	if diff > h.maxSkew {
		return fmt.Errorf("requisição expirada (timestamp skew: %d segundos)", diff)
	}

	// 2. Monta o payload canônico
	// Formato: ts.nonce.bodyHash
	bodyHash := sha256.Sum256(body)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	// 3. Tenta validar com segredo ativo
	if h.validateWithSecret(h.activeSecret, tsStr, nonce, bodyHashHex, hmacHeader) {
		return nil
	}

	// 4. Se falhar e tiver segredo antigo, tenta com ele (rotação)
	if h.oldSecret != "" && h.validateWithSecret(h.oldSecret, tsStr, nonce, bodyHashHex, hmacHeader) {
		return nil
	}

	return fmt.Errorf("assinatura HMAC inválida")
}

// validateWithSecret valida HMAC com um segredo específico
func (h *HMACValidator) validateWithSecret(secret, tsStr, nonce, bodyHashHex, hmacHeader string) bool {
	signatureRaw := fmt.Sprintf("%s.%s.%s", tsStr, nonce, bodyHashHex)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signatureRaw))
	expectedMac := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(hmacHeader), []byte(expectedMac))
}

// GenerateHMAC gera uma assinatura HMAC para uma requisição
func GenerateHMAC(secret, tsStr, nonce string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	bodyHashHex := hex.EncodeToString(bodyHash[:])
	signatureRaw := fmt.Sprintf("%s.%s.%s", tsStr, nonce, bodyHashHex)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signatureRaw))
	return hex.EncodeToString(mac.Sum(nil))
}

// CanonicalRequest gera um request canônico para logging/auditoria
func CanonicalRequest(method, path string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	return fmt.Sprintf("%s %s %s", method, path, hex.EncodeToString(bodyHash[:]))
}

// GenerateRawBodyHMAC signs the legacy signer payload format: ts.nonce.rawBody.
func GenerateRawBodyHMAC(secret, tsStr, nonce string, body []byte) string {
	signatureRaw := fmt.Sprintf("%s.%s.%s", tsStr, nonce, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signatureRaw))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignRawBodyHeaders applies the signer anti-replay headers used by transfer workers.
func SignRawBodyHeaders(req *http.Request, secret string, body []byte) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := GenerateNonce()
	req.Header.Set("x-ts", ts)
	req.Header.Set("x-nonce", nonce)
	req.Header.Set("x-signer-hmac", GenerateRawBodyHMAC(secret, ts, nonce, body))
}
