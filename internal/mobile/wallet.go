package mobile

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strings"
	"time"

	rpcpool "payment-gateway/internal/rpc"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func (s *Server) handleWalletBalance(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuario nao encontrado"})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar carteira do usuario"})
		return
	}

	walletAddr := ""
	if user.WalletAddress != nil {
		walletAddr = *user.WalletAddress
	}
	price := mobileAssetPriceBRL(s.PriceCache(), "USDT")
	bnbPrice := mobileAssetPriceBRL(s.PriceCache(), "BNB")
	usdtAmount, bnbAmount := s.mobileOnchainWalletBalances(r.Context(), walletAddr)
	usdtValueBRL := usdtAmount * price
	bnbValueBRL := bnbAmount * bnbPrice
	balances := []map[string]any{
		{"symbol": "USDT", "name": "Tether USD", "network": "BSC", "amount": usdtAmount, "value_brl": usdtValueBRL, "price_brl": price, "change_24h": mobileAssetChange24h(s.PriceCache(), "USDT")},
		{"symbol": "BNB", "name": "BNB", "network": "BSC", "amount": bnbAmount, "value_brl": bnbValueBRL, "price_brl": bnbPrice, "change_24h": mobileAssetChange24h(s.PriceCache(), "BNB")},
	}
	seen := map[string]bool{"USDT": true, "BNB": true}
	imported, err := mobileDB(s.db).ListWalletTokens(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	for _, token := range imported {
		symbol := strings.ToUpper(strings.TrimSpace(token.Symbol))
		if symbol == "" || seen[symbol] {
			continue
		}
		tokenPrice := mobileAssetPriceBRL(s.PriceCache(), symbol)
		balances = append(balances, map[string]any{
			"symbol":     symbol,
			"name":       token.Name,
			"network":    token.Network,
			"contract":   token.Contract,
			"amount":     0,
			"value_brl":  0,
			"price_brl":  tokenPrice,
			"change_24h": mobileAssetChange24h(s.PriceCache(), symbol),
			"decimals":   token.Decimals,
			"imported":   true,
		})
		seen[symbol] = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"wallet_address": walletAddr,
		"balances":       balances,
		"total_brl":      usdtValueBRL + bnbValueBRL,
		"price_usdt":     price,
	})
}

func (s *Server) mobileOnchainWalletBalances(ctx context.Context, walletAddr string) (usdtAmount, bnbAmount float64) {
	if s == nil || s.cfg == nil || strings.TrimSpace(walletAddr) == "" || !common.IsHexAddress(walletAddr) {
		return 0, 0
	}
	rpcURLs := strings.TrimSpace(s.cfg.BscRpcUrls)
	usdtContract := strings.TrimSpace(s.cfg.BscUsdtContract)
	if rpcURLs == "" {
		return 0, 0
	}
	balCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	pool, err := rpcpool.NewPool(rpcURLs)
	if err != nil {
		return 0, 0
	}
	wallet := common.HexToAddress(walletAddr)
	if native, err := pool.BalanceAt(balCtx, wallet); err == nil && native != nil {
		bnbAmount = bigIntToFloat(native, 18)
	}
	if usdtContract != "" && common.IsHexAddress(usdtContract) {
		if raw, err := mobileERC20BalanceOf(balCtx, pool, wallet, common.HexToAddress(usdtContract)); err == nil && raw != nil {
			usdtAmount = bigIntToFloat(raw, 18)
		}
	}
	return usdtAmount, bnbAmount
}

func mobileERC20BalanceOf(ctx context.Context, pool *rpcpool.Pool, wallet, token common.Address) (*big.Int, error) {
	var callData [36]byte
	selector, _ := hex.DecodeString("70a08231")
	copy(callData[:4], selector)
	copy(callData[16:], wallet.Bytes())

	var result []byte
	err := pool.Do(ctx, func(c *ethclient.Client) error {
		msg := map[string]string{
			"to":   token.Hex(),
			"data": "0x" + hex.EncodeToString(callData[:]),
		}
		var raw string
		if err := c.Client().CallContext(ctx, &raw, "eth_call", msg, "latest"); err != nil {
			return err
		}
		raw = strings.TrimPrefix(raw, "0x")
		if raw == "" {
			result = big.NewInt(0).Bytes()
			return nil
		}
		decoded, err := hex.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("decode balanceOf response: %w", err)
		}
		result = decoded
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return big.NewInt(0), nil
	}
	return new(big.Int).SetBytes(result), nil
}

func bigIntToFloat(value *big.Int, decimals int) float64 {
	if value == nil {
		return 0
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(value), big.NewFloat(math.Pow10(decimals))).Float64()
	return f
}

func (s *Server) handleWalletTokens(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", mobileRateCacheControl)
	price := mobileAssetPriceBRL(s.PriceCache(), "USDT")
	bnbPrice := mobileAssetPriceBRL(s.PriceCache(), "BNB")
	tokens := []map[string]any{
		{"symbol": "USDT", "name": "Tether USD", "network": "BSC", "contract": s.cfg.BscUsdtContract, "price_brl": price, "decimals": 18},
		{"symbol": "BNB", "name": "BNB", "network": "BSC", "contract": "", "price_brl": bnbPrice, "decimals": 18},
	}
	seen := map[string]bool{"USDT": true, "BNB": true}
	imported, err := mobileDB(s.db).ListWalletTokens(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	for _, token := range imported {
		symbol := strings.ToUpper(strings.TrimSpace(token.Symbol))
		if symbol == "" || seen[symbol] {
			continue
		}
		tokens = append(tokens, map[string]any{
			"symbol":     symbol,
			"name":       token.Name,
			"network":    token.Network,
			"contract":   token.Contract,
			"price_brl":  mobileAssetPriceBRL(s.PriceCache(), symbol),
			"change_24h": mobileAssetChange24h(s.PriceCache(), symbol),
			"decimals":   token.Decimals,
			"imported":   true,
		})
		seen[symbol] = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tokens": tokens,
	})
}

func (s *Server) handleWalletAddress(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuario nao encontrado"})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar carteira do usuario"})
		return
	}
	if user != nil && user.WalletAddress != nil && *user.WalletAddress != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"wallet_address": *user.WalletAddress,
			"network":        "BSC",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"wallet_address": nil, "hint": "use POST /api/mobile/wallet/generate com wallet_address"})
}

func (s *Server) handleWalletGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WalletAddress string `json:"wallet_address"`
		Address       string `json:"address"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet_address obrigatorio"})
		return
	}

	uid := userIDFromCtx(r)
	user, _ := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if user != nil && user.WalletAddress != nil && *user.WalletAddress != "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "carteira ja registrada", "wallet_address": *user.WalletAddress})
		return
	}

	address := strings.TrimSpace(req.WalletAddress)
	if address == "" {
		address = strings.TrimSpace(req.Address)
	}
	if !common.IsHexAddress(address) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet_address deve ser um endereco EVM valido"})
		return
	}
	checksummed := common.HexToAddress(address).Hex()

	if err := mobileDB(s.db).UpdateUser(r.Context(), uid, map[string]any{"wallet_address": checksummed}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"wallet_address": checksummed,
		"network":        "BSC",
		"custody":        "client",
		"message":        "wallet registrada; a private key deve permanecer somente no app/agente",
	})
}

func (s *Server) handleWalletHistory(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	orders, err := mobileDB(s.db).ListOrdersByUser(r.Context(), uid, 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": orders, "count": len(orders)})
}
