package main

import (
	"payment-gateway/internal/security"
)

// ValidateHMACWrapper mantém compatibilidade com a função existente
func ValidateHMACWrapper(secret string, maxSkew int, tsStr, nonce, hmacHeader string, body []byte) error {
	// Usa a implementação do pacote security
	validator := security.NewHMACValidator(secret, int64(maxSkew))
	return validator.ValidateHMAC(tsStr, nonce, hmacHeader, body)
}
