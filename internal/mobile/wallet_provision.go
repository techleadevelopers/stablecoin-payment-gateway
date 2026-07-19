package mobile

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"payment-gateway/internal/models"
	"payment-gateway/internal/privacy"

	"github.com/ethereum/go-ethereum/crypto"
)

func (s *Server) ensureUserWallet(ctx context.Context, user *models.User) (*models.User, error) {
	if user == nil {
		return nil, nil
	}
	if user.WalletAddress != nil && strings.TrimSpace(*user.WalletAddress) != "" {
		return user, nil
	}
	if s == nil || s.db == nil {
		return user, nil
	}

	secret := s.mobileWalletEncryptionSecret()
	codec, err := privacy.New(secret)
	if err != nil {
		return nil, err
	}

	for attempts := 0; attempts < 3; attempts++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			return nil, err
		}
		privateKeyHex := "0x" + hex.EncodeToString(crypto.FromECDSA(key))
		encryptedKey, err := codec.Encrypt(privateKeyHex)
		if err != nil {
			return nil, err
		}
		address := crypto.PubkeyToAddress(key.PublicKey).Hex()

		updated, err := mobileDB(s.db).AttachSystemWallet(ctx, user.ID, address, encryptedKey)
		if err == nil {
			return updated, nil
		}
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate") &&
			!strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, err
		}
	}
	return nil, fmt.Errorf("nao foi possivel gerar carteira unica para o usuario")
}

func (s *Server) mobileWalletEncryptionSecret() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	if secret := strings.TrimSpace(envOr("MOBILE_WALLET_ENCRYPTION_SECRET", "")); secret != "" {
		return secret
	}
	if secret := strings.TrimSpace(s.cfg.LGPDSecret); secret != "" {
		return secret
	}
	if secret := strings.TrimSpace(s.cfg.WebhookSecret); secret != "" {
		return secret
	}
	return strings.TrimSpace(s.mcfg.JWTSecret)
}
