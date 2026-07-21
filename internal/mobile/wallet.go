package mobile

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	rpcpool "payment-gateway/internal/rpc"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ─── Balance cache ────────────────────────────────────────────────────────────
// Reduz chamadas RPC de cada request para uma por walletAddr a cada 30 segundos.

const walletBalanceCacheTTL = 30 * time.Second

type walletBalanceCacheEntry struct {
	bscUSDT   float64
	bnb       float64
	polyUSDT  float64
	matic     float64
	expiresAt time.Time
}

var (
	walletBalanceCacheMu sync.RWMutex
	walletBalanceCache   = make(map[string]walletBalanceCacheEntry)
)

func getWalletBalanceCache(key string) (walletBalanceCacheEntry, bool) {
	walletBalanceCacheMu.RLock()
	defer walletBalanceCacheMu.RUnlock()
	e, ok := walletBalanceCache[key]
	if !ok || time.Now().After(e.expiresAt) {
		return walletBalanceCacheEntry{}, false
	}
	return e, true
}

func setWalletBalanceCache(key string, e walletBalanceCacheEntry) {
	walletBalanceCacheMu.Lock()
	defer walletBalanceCacheMu.Unlock()
	e.expiresAt = time.Now().Add(walletBalanceCacheTTL)
	walletBalanceCache[key] = e
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

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

	// Fetch BSC + Polygon balances concurrently with cache
	balResult := s.mobileOnchainWalletBalancesAll(r.Context(), walletAddr)

	usdtPrice := mobileAssetPriceBRL(s.PriceCache(), "USDT")
	bnbPrice := mobileAssetPriceBRL(s.PriceCache(), "BNB")
	maticPrice := mobileAssetPriceBRL(s.PriceCache(), "MATIC")

	usdtValueBRL := balResult.bscUSDT * usdtPrice
	bnbValueBRL := balResult.bnb * bnbPrice
	polyUSDTValueBRL := balResult.polyUSDT * usdtPrice
	maticValueBRL := balResult.matic * maticPrice

	balances := []map[string]any{
		{
			"symbol": "USDT", "name": "Tether USD", "network": "BSC",
			"amount": balResult.bscUSDT, "value_brl": usdtValueBRL,
			"price_brl": usdtPrice, "change_24h": mobileAssetChange24h(s.PriceCache(), "USDT"),
		},
		{
			"symbol": "BNB", "name": "BNB", "network": "BSC",
			"amount": balResult.bnb, "value_brl": bnbValueBRL,
			"price_brl": bnbPrice, "change_24h": mobileAssetChange24h(s.PriceCache(), "BNB"),
		},
		{
			"symbol": "USDT", "name": "Tether USD (Polygon)", "network": "POLYGON",
			"amount": balResult.polyUSDT, "value_brl": polyUSDTValueBRL,
			"price_brl": usdtPrice, "change_24h": mobileAssetChange24h(s.PriceCache(), "USDT"),
		},
		{
			"symbol": "MATIC", "name": "Polygon", "network": "POLYGON",
			"amount": balResult.matic, "value_brl": maticValueBRL,
			"price_brl": maticPrice, "change_24h": mobileAssetChange24h(s.PriceCache(), "MATIC"),
		},
	}

	seen := map[string]bool{"USDT": true, "BNB": true, "MATIC": true}
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

	totalBRL := usdtValueBRL + bnbValueBRL + polyUSDTValueBRL + maticValueBRL
	writeJSON(w, http.StatusOK, map[string]any{
		"wallet_address": walletAddr,
		"balances":       balances,
		"total_brl":      totalBRL,
		"price_usdt":     usdtPrice,
	})
}

// mobileOnchainWalletBalancesAll busca saldos BSC + Polygon em paralelo.
// Resultado cacheado por walletBalanceCacheTTL para evitar RPC excessivo.
func (s *Server) mobileOnchainWalletBalancesAll(ctx context.Context, walletAddr string) walletBalanceCacheEntry {
	if s == nil || s.cfg == nil || strings.TrimSpace(walletAddr) == "" || !common.IsHexAddress(walletAddr) {
		return walletBalanceCacheEntry{}
	}

	cacheKey := strings.ToLower(walletAddr)
	if cached, ok := getWalletBalanceCache(cacheKey); ok {
		return cached
	}

	var (
		result walletBalanceCacheEntry
		wg     sync.WaitGroup
		mu     sync.Mutex
	)

	wallet := common.HexToAddress(walletAddr)
	balCtx, cancel := context.WithTimeout(ctx, 8*time.Second) // increased for parallel calls
	defer cancel()

	// ── BSC ──────────────────────────────────────────────────────────────────
	if rpcURLs := strings.TrimSpace(s.cfg.BscRpcUrls); rpcURLs != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool, err := rpcpool.NewPool(rpcURLs)
			if err != nil {
				return
			}
			var bscUSDT, bnb float64
			if native, err := pool.BalanceAt(balCtx, wallet); err == nil && native != nil {
				bnb = bigIntToFloat(native, 18)
			}
			usdtContract := strings.TrimSpace(s.cfg.BscUsdtContract)
			if usdtContract != "" && common.IsHexAddress(usdtContract) {
				if raw, err := mobileERC20BalanceOf(balCtx, pool, wallet, common.HexToAddress(usdtContract)); err == nil && raw != nil {
					bscUSDT = bigIntToFloat(raw, 18)
				}
			}
			mu.Lock()
			result.bscUSDT = bscUSDT
			result.bnb = bnb
			mu.Unlock()
		}()
	}

	// ── Polygon ───────────────────────────────────────────────────────────────
	if rpcURLs := strings.TrimSpace(s.cfg.PolygonRpcUrls); rpcURLs != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool, err := rpcpool.NewPool(rpcURLs)
			if err != nil {
				return
			}
			var polyUSDT, matic float64
			if native, err := pool.BalanceAt(balCtx, wallet); err == nil && native != nil {
				matic = bigIntToFloat(native, 18)
			}
			usdtContract := strings.TrimSpace(s.cfg.PolygonUsdtContract)
			if usdtContract != "" && common.IsHexAddress(usdtContract) {
				if raw, err := mobileERC20BalanceOf(balCtx, pool, wallet, common.HexToAddress(usdtContract)); err == nil && raw != nil {
					polyUSDT = bigIntToFloat(raw, 18)
				}
			}
			mu.Lock()
			result.polyUSDT = polyUSDT
			result.matic = matic
			mu.Unlock()
		}()
	}

	wg.Wait()
	setWalletBalanceCache(cacheKey, result)
	return result
}

// mobileOnchainWalletBalances é mantida para compatibilidade com código legado.
// Retorna apenas USDT BSC e BNB. Novos callers devem usar mobileOnchainWalletBalancesAll.
func (s *Server) mobileOnchainWalletBalances(ctx context.Context, walletAddr string) (usdtAmount, bnbAmount float64) {
	r := s.mobileOnchainWalletBalancesAll(ctx, walletAddr)
	return r.bscUSDT, r.bnb
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
	maticPrice := mobileAssetPriceBRL(s.PriceCache(), "MATIC")
	tokens := []map[string]any{
		{"symbol": "USDT", "name": "Tether USD", "network": "BSC", "contract": s.cfg.BscUsdtContract, "price_brl": price, "decimals": 18},
		{"symbol": "BNB", "name": "BNB", "network": "BSC", "contract": "", "price_brl": bnbPrice, "decimals": 18},
		{"symbol": "USDT", "name": "Tether USD", "network": "POLYGON", "contract": s.cfg.PolygonUsdtContract, "price_brl": price, "decimals": 6},
		{"symbol": "MATIC", "name": "Polygon", "network": "POLYGON", "contract": "", "price_brl": maticPrice, "decimals": 18},
	}
	seen := map[string]bool{"USDT": true, "BNB": true, "MATIC": true}
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
			"networks":       []string{"BSC", "POLYGON"},
			"network":        "BSC", // compatibilidade retroativa
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
		"networks":       []string{"BSC", "POLYGON"}, // mesmo endereço EVM funciona nas duas redes
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
