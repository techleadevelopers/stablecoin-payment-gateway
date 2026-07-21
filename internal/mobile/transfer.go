package mobile

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/privacy"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"golang.org/x/crypto/bcrypt"
)

const (
	bscUSDCContractMobile     = "0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d"
	polygonUSDCContractMobile = "0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174"
	defaultPolygonUSDTMobile  = "0xc2132D05D31c914a87C6611C10748AEb04B58e8F"
)

func (s *Server) handleWalletTransfer(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuario nao encontrado"})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao preparar wallet custodial"})
		return
	}
	if !mobileUserKYCApproved(user) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":     "kyc obrigatorio para enviar cripto",
			"kycStatus": user.KYCStatus,
		})
		return
	}
	if user.WalletAddress == nil || strings.TrimSpace(*user.WalletAddress) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet do usuario nao registrada"})
		return
	}

	var req struct {
		To      string `json:"to"`
		Amount  string `json:"amount"`
		Asset   string `json:"asset"`
		Network string `json:"network"`
		PIN     string `json:"pin"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}

	to := strings.TrimSpace(req.To)
	if !common.IsHexAddress(to) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "to deve ser um endereco EVM valido"})
		return
	}
	asset := strings.ToUpper(strings.TrimSpace(req.Asset))
	if asset == "" {
		asset = "USDT"
	}
	network := normalizeMobileTransferNetwork(req.Network)
	if network == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network deve ser BSC ou POLYGON"})
		return
	}

	token, decimals, chainID, err := s.mobileTransferToken(asset, network)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	rawAmount, err := parseTokenAmount(req.Amount, decimals)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	if user.PinHash != nil && strings.TrimSpace(*user.PinHash) != "" {
		if strings.TrimSpace(req.PIN) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "pin obrigatorio para transferencia"})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(*user.PinHash), []byte(req.PIN)); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "pin invalido"})
			return
		}
	}

	from := common.HexToAddress(*user.WalletAddress).Hex()
	keyRecord, err := mobileDB(s.db).GetCustodialWalletKey(r.Context(), user.ID, from)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar chave custodial"})
		return
	}
	if keyRecord == nil || strings.TrimSpace(keyRecord.EncryptedPrivateKey) == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":          "wallet sem chave custodial no backend",
			"wallet_address": from,
			"next_step":      "importe a private key criptografada ou use uma wallet custodial criada pelo app",
		})
		return
	}

	recipient := common.HexToAddress(to)
	tokenAddress := common.HexToAddress(token)
	txHash, err := s.sendCustodialMobileERC20Transfer(
		r.Context(),
		keyRecord.EncryptedPrivateKey,
		from,
		recipient,
		tokenAddress,
		rawAmount,
		network,
		int64(chainID),
	)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	_ = mobileDB(s.db).RecordMobileWalletTransfer(
		r.Context(),
		user.ID,
		from,
		recipient.Hex(),
		tokenAddress.Hex(),
		asset,
		network,
		req.Amount,
		rawAmount.String(),
		txHash,
		idempotencyKeyFromCtx(r.Context()),
	)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"mode":           "backend_custodial_erc20_transfer",
		"from":           from,
		"tx_hash":        txHash,
		"chainId":        chainID,
		"network":        network,
		"asset":          asset,
		"token_contract": tokenAddress.Hex(),
		"recipient":      recipient.Hex(),
		"amount":         req.Amount,
		"amount_raw":     rawAmount.String(),
		"decimals":       decimals,
		"status":         "submitted",
	})
}

func (s *Server) handleWalletTransferQuote(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuario nao encontrado"})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil || user.WalletAddress == nil || strings.TrimSpace(*user.WalletAddress) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet do usuario nao registrada"})
		return
	}
	var req struct {
		To      string `json:"to"`
		Amount  string `json:"amount"`
		Asset   string `json:"asset"`
		Network string `json:"network"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	to := strings.TrimSpace(req.To)
	if !common.IsHexAddress(to) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "to deve ser um endereco EVM valido"})
		return
	}
	asset := strings.ToUpper(strings.TrimSpace(req.Asset))
	if asset == "" {
		asset = "USDT"
	}
	network := normalizeMobileTransferNetwork(req.Network)
	if network == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network deve ser BSC ou POLYGON"})
		return
	}
	token, decimals, chainID, err := s.mobileTransferToken(asset, network)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	rawAmount, err := parseTokenAmount(req.Amount, decimals)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	rpcURL := s.mobileTransferRPCURL(network)
	if rpcURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": network + " RPC nao configurado para cotacao de transferencia"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "falha ao conectar RPC " + network})
		return
	}
	defer client.Close()
	from := common.HexToAddress(*user.WalletAddress)
	recipient := common.HexToAddress(to)
	tokenAddress := common.HexToAddress(token)
	data := common.FromHex(erc20TransferCalldata(recipient, rawAmount))
	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "falha ao estimar gas price"})
		return
	}
	gasLimit, err := client.EstimateGas(ctx, ethereum.CallMsg{From: from, To: &tokenAddress, Value: big.NewInt(0), Data: data})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "falha ao estimar gas"})
		return
	}
	if gasLimit < 65_000 {
		gasLimit = 65_000
	}
	gasLimitWithBuffer := gasLimit + gasLimit/5
	feeWei := new(big.Int).Mul(new(big.Int).SetUint64(gasLimitWithBuffer), gasPrice)
	feeNative := bigIntToFloat(feeWei, 18)
	nativeSymbol := mobileTransferNativeSymbol(network)
	feeBRL := 0.0
	if nativeSymbol != "" {
		if nativePrice := mobileAssetPriceBRL(s.PriceCache(), nativeSymbol); nativePrice > 0 {
			feeBRL = feeNative * nativePrice
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"asset":                     asset,
		"network":                   network,
		"chainId":                   chainID,
		"from":                      from.Hex(),
		"recipient":                 recipient.Hex(),
		"token_contract":            tokenAddress.Hex(),
		"amount":                    req.Amount,
		"amount_raw":                rawAmount.String(),
		"decimals":                  decimals,
		"gas_limit":                 gasLimitWithBuffer,
		"gas_price_wei":             gasPrice.String(),
		"estimated_network_fee_wei": feeWei.String(),
		"estimated_network_fee":     feeNative,
		"network_fee_symbol":        nativeSymbol,
		"estimated_fee_brl":         feeBRL,
		"requires_pin":              user.PinHash != nil && strings.TrimSpace(*user.PinHash) != "",
	})
}

func (s *Server) sendCustodialMobileERC20Transfer(ctx context.Context, encryptedPrivateKey, expectedFrom string, recipient, token common.Address, amount *big.Int, network string, expectedChainID int64) (string, error) {
	codec, err := privacy.New(s.mobileWalletEncryptionSecret())
	if err != nil {
		return "", err
	}
	privateKeyHex, err := codec.Decrypt(encryptedPrivateKey)
	if err != nil {
		return "", fmt.Errorf("falha ao abrir chave custodial")
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKeyHex), "0x"))
	if err != nil {
		return "", fmt.Errorf("chave custodial invalida")
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	if !strings.EqualFold(from.Hex(), expectedFrom) {
		return "", fmt.Errorf("chave custodial nao corresponde a wallet do usuario")
	}

	rpcURL := s.mobileTransferRPCURL(network)
	if rpcURL == "" {
		return "", fmt.Errorf("%s RPC nao configurado para transferencia mobile", network)
	}
	txCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	client, err := ethclient.DialContext(txCtx, rpcURL)
	if err != nil {
		return "", fmt.Errorf("falha ao conectar RPC %s: %w", network, err)
	}
	defer client.Close()

	chainID, err := client.ChainID(txCtx)
	if err != nil {
		return "", fmt.Errorf("falha ao ler chainId: %w", err)
	}
	if expectedChainID > 0 && chainID.Int64() != expectedChainID {
		return "", fmt.Errorf("chainId invalido: esperado %d recebido %d", expectedChainID, chainID.Int64())
	}
	nonce, err := client.PendingNonceAt(txCtx, from)
	if err != nil {
		return "", fmt.Errorf("falha ao ler nonce: %w", err)
	}
	data := common.FromHex(erc20TransferCalldata(recipient, amount))
	gasPrice, err := client.SuggestGasPrice(txCtx)
	if err != nil {
		return "", fmt.Errorf("falha ao estimar gas price: %w", err)
	}
	gasLimit, err := client.EstimateGas(txCtx, ethereum.CallMsg{From: from, To: &token, Value: big.NewInt(0), Data: data})
	if err != nil {
		return "", fmt.Errorf("falha ao estimar gas: %w", err)
	}
	if gasLimit < 65_000 {
		gasLimit = 65_000
	}
	tx := types.NewTransaction(nonce, token, big.NewInt(0), gasLimit+gasLimit/5, gasPrice, data)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	if err != nil {
		return "", fmt.Errorf("falha ao assinar transferencia: %w", err)
	}
	if err := client.SendTransaction(txCtx, signed); err != nil {
		return "", fmt.Errorf("falha ao enviar transferencia: %w", err)
	}
	return signed.Hash().Hex(), nil
}

func normalizeMobileTransferNetwork(network string) string {
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "", "BSC", "BINANCE", "BEP20":
		return "BSC"
	case "POL", "POLYGON", "MATIC":
		return "POLYGON"
	default:
		return ""
	}
}

func (s *Server) mobileTransferToken(asset, network string) (string, int, int, error) {
	switch network {
	case "BSC":
		switch asset {
		case "USDT":
			if s.cfg == nil || !common.IsHexAddress(s.cfg.BscUsdtContract) {
				return "", 0, 0, fmt.Errorf("BSC USDT nao configurado")
			}
			return s.cfg.BscUsdtContract, 18, s.mobileTransferChainID("BSC"), nil
		case "USDC":
			return bscUSDCContractMobile, 18, s.mobileTransferChainID("BSC"), nil
		}
	case "POLYGON":
		switch asset {
		case "USDT":
			token := defaultPolygonUSDTMobile
			if s.cfg != nil && common.IsHexAddress(s.cfg.PolygonUsdtContract) {
				token = s.cfg.PolygonUsdtContract
			}
			return token, 6, s.mobileTransferChainID("POLYGON"), nil
		case "USDC":
			return polygonUSDCContractMobile, 6, s.mobileTransferChainID("POLYGON"), nil
		}
	}
	return "", 0, 0, fmt.Errorf("asset/network nao suportado para transferencia mobile")
}

func (s *Server) mobileTransferChainID(network string) int {
	if s == nil || s.cfg == nil {
		if network == "POLYGON" {
			return 137
		}
		return 56
	}
	switch network {
	case "POLYGON":
		if s.cfg.PolygonChainID > 0 {
			return int(s.cfg.PolygonChainID)
		}
		return 137
	default:
		if s.cfg.BscChainID > 0 {
			return int(s.cfg.BscChainID)
		}
		return 56
	}
}

func (s *Server) mobileTransferRPCURL(network string) string {
	if s == nil || s.cfg == nil {
		return ""
	}
	switch network {
	case "POLYGON":
		return firstCSVValue(s.cfg.PolygonRpcUrls)
	default:
		return firstCSVValue(s.cfg.BscRpcUrls)
	}
}

func mobileTransferNativeSymbol(network string) string {
	switch network {
	case "POLYGON":
		return "MATIC"
	case "BSC":
		return "BNB"
	default:
		return ""
	}
}

func firstCSVValue(raw string) string {
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func parseTokenAmount(amount string, decimals int) (*big.Int, error) {
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return nil, fmt.Errorf("amount obrigatorio")
	}
	rat, ok := new(big.Rat).SetString(amount)
	if !ok || rat.Sign() <= 0 {
		return nil, fmt.Errorf("amount deve ser positivo")
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	rat.Mul(rat, new(big.Rat).SetInt(scale))
	if !rat.IsInt() {
		return nil, fmt.Errorf("amount tem mais casas decimais que o token suporta")
	}
	return rat.Num(), nil
}

func erc20TransferCalldata(to common.Address, amount *big.Int) string {
	selector := []byte{0xa9, 0x05, 0x9c, 0xbb}
	data := make([]byte, 0, 68)
	data = append(data, selector...)
	data = append(data, common.LeftPadBytes(to.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(amount.Bytes(), 32)...)
	return "0x" + hex.EncodeToString(data)
}
