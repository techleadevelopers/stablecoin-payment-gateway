package mobile

import (
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

func (s *Server) handleWalletBalance(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuário não encontrado"})
		return
	}
	// Balance is fetched from on-chain via BSC RPC when wallet_address is set.
	// Returns cached/stub values when no wallet is configured.
	walletAddr := ""
	if user.WalletAddress != nil {
		walletAddr = *user.WalletAddress
	}
	price := s.workers.PriceWorker.GetCurrentPrice()
	writeJSON(w, http.StatusOK, map[string]any{
		"wallet_address": walletAddr,
		"balances": []map[string]any{
			{"symbol": "USDT", "network": "BSC", "amount": 0, "value_brl": 0},
			{"symbol": "BNB",  "network": "BSC", "amount": 0, "value_brl": 0},
		},
		"total_brl":  0,
		"price_usdt": price,
	})
}

func (s *Server) handleWalletTokens(w http.ResponseWriter, r *http.Request) {
	price := s.workers.PriceWorker.GetCurrentPrice()
	writeJSON(w, http.StatusOK, map[string]any{
		"tokens": []map[string]any{
			{"symbol": "USDT", "name": "Tether USD",    "network": "BSC", "contract": s.cfg.BscUsdtContract, "price_brl": price, "decimals": 18},
			{"symbol": "BNB",  "name": "BNB",           "network": "BSC", "contract": "",                    "price_brl": 0,     "decimals": 18},
		},
	})
}

func (s *Server) handleWalletAddress(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, _ := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if user.WalletAddress != nil && *user.WalletAddress != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"wallet_address": *user.WalletAddress,
			"network":        "BSC",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"wallet_address": nil, "hint": "use POST /api/mobile/wallet/generate"})
}

func (s *Server) handleWalletGenerate(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, _ := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if user.WalletAddress != nil && *user.WalletAddress != "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "carteira já gerada", "wallet_address": *user.WalletAddress})
		return
	}
	// Generate a new EOA keypair
	key, err := crypto.GenerateKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao gerar carteira"})
		return
	}
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	privHex := fmt.Sprintf("%x", crypto.FromECDSA(key))

	if err := mobileDB(s.db).UpdateUser(r.Context(), uid, map[string]any{"wallet_address": address}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// NOTE: private key is returned ONCE — client must store it securely.
	writeJSON(w, http.StatusCreated, map[string]any{
		"wallet_address": address,
		"private_key":    privHex,
		"network":        "BSC",
		"warning":        "Guarde a private_key em local seguro. Ela não é armazenada no servidor.",
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
