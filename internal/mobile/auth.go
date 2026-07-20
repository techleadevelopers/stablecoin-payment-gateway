package mobile

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ─── JWT (HS256, stdlib only) ─────────────────────────────────────────────────

type jwtClaims struct {
	Sub  string `json:"sub"`
	Exp  int64  `json:"exp"`
	Iat  int64  `json:"iat"`
	Type string `json:"type"` // "access" | "refresh"
}

func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func issueToken(secret string, claims jwtClaims) (string, error) {
	header := b64url([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := header + "." + b64url(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return body + "." + b64url(mac.Sum(nil)), nil
}

func verifyToken(secret, token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}
	body := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	expected := b64url(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(expected)) {
		return nil, fmt.Errorf("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims jwtClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	if time.Now().Unix() > claims.Exp {
		return nil, fmt.Errorf("token expired")
	}
	return &claims, nil
}

func (s *Server) newAccessToken(userID string) (string, error) {
	exp := time.Now().Add(time.Duration(s.mcfg.JWTExpiresMin) * time.Minute).Unix()
	return issueToken(s.mcfg.JWTSecret, jwtClaims{Sub: userID, Exp: exp, Iat: time.Now().Unix(), Type: "access"})
}

func (s *Server) newRefreshToken(userID string) (string, error) {
	exp := time.Now().Add(time.Duration(s.mcfg.RefreshExpiresDays) * 24 * time.Hour).Unix()
	return issueToken(s.mcfg.RefreshSecret, jwtClaims{Sub: userID, Exp: exp, Iat: time.Now().Unix(), Type: "refresh"})
}

// requireAuth middleware — injects user ID into context.
type contextKey string

const ctxUserID contextKey = "uid"

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token não informado"})
			return
		}
		claims, err := verifyToken(s.mcfg.JWTSecret, auth[7:])
		if err != nil || claims.Type != "access" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token inválido ou expirado"})
			return
		}
		if s.db != nil {
			active, err := mobileDB(s.db).IsUserActive(r.Context(), claims.Sub)
			if err != nil || !active {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "usuario nao encontrado ou conta excluida"})
				return
			}
		}
		ctx := context.WithValue(r.Context(), ctxUserID, claims.Sub)
		next(w, r.WithContext(ctx))
	}
}

func userIDFromCtx(r *http.Request) string {
	v, _ := r.Context().Value(ctxUserID).(string)
	return v
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email               string `json:"email"`
		Password            string `json:"password"`
		FullName            string `json:"full_name"`
		Phone               string `json:"phone"`
		CPF                 string `json:"cpf"`
		BirthDate           string `json:"birth_date"`
		AddressPostalCode   string `json:"address_postal_code"`
		AddressStreet       string `json:"address_street"`
		AddressNumber       string `json:"address_number"`
		AddressNeighborhood string `json:"address_neighborhood"`
		AddressCity         string `json:"address_city"`
		AddressState        string `json:"address_state"`
		AddressCountry      string `json:"address_country"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "email e password obrigatórios"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "password deve ter no mínimo 8 caracteres"})
		return
	}
	cost := bcrypt.DefaultCost
	if isMobileLoadTestUser(req.Email) {
		cost = bcrypt.MinCost
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), cost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	user, err := mobileDB(s.db).CreateUser(r.Context(), createMobileUserInput{
		Email:               req.Email,
		PasswordHash:        string(hash),
		FullName:            strings.TrimSpace(req.FullName),
		Phone:               strings.TrimSpace(req.Phone),
		CPF:                 onlyDigitsMobile(req.CPF),
		BirthDate:           strings.TrimSpace(req.BirthDate),
		AddressPostalCode:   onlyDigitsMobile(req.AddressPostalCode),
		AddressStreet:       strings.TrimSpace(req.AddressStreet),
		AddressNumber:       strings.TrimSpace(req.AddressNumber),
		AddressNeighborhood: strings.TrimSpace(req.AddressNeighborhood),
		AddressCity:         strings.TrimSpace(req.AddressCity),
		AddressState:        strings.ToUpper(strings.TrimSpace(req.AddressState)),
		AddressCountry:      firstNonEmptyStr(strings.ToUpper(strings.TrimSpace(req.AddressCountry)), "BR"),
	})
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "email já cadastrado"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar carteira do usuario"})
		return
	}
	access, _ := s.newAccessToken(user.ID)
	refresh, _ := s.newRefreshToken(user.ID)
	if err := mobileDB(s.db).SaveRefreshToken(r.Context(), user.ID, refresh); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar sessao"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":         s.sanitizeUser(user),
		"accessToken":  access,
		"refreshToken": refresh,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "email e password obrigatórios"})
		return
	}
	user, err := mobileDB(s.db).GetUserByEmail(r.Context(), req.Email)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "credenciais inválidas"})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "credenciais inválidas"})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar carteira do usuario"})
		return
	}
	access, _ := s.newAccessToken(user.ID)
	refresh, _ := s.newRefreshToken(user.ID)
	if err := mobileDB(s.db).SaveRefreshToken(r.Context(), user.ID, refresh); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar sessao"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":         s.sanitizeUser(user),
		"accessToken":  access,
		"refreshToken": refresh,
	})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(r, &req); err != nil || req.RefreshToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "refreshToken obrigatório"})
		return
	}
	claims, err := verifyToken(s.mcfg.RefreshSecret, req.RefreshToken)
	if err != nil || claims.Type != "refresh" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "refresh token inválido ou expirado"})
		return
	}
	user, err := mobileDB(s.db).GetUserByID(r.Context(), claims.Sub)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "usuário não encontrado"})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar carteira do usuario"})
		return
	}
	// C-05: validate refresh token against the stored server-side digest.
	// Without this check a revoked token (after logout or password change)
	// remains valid for its full 7-day TTL — anyone with the token can still
	// obtain new access tokens even after the user has logged out.
	if user.RefreshTokenHash == nil || *user.RefreshTokenHash == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "sessão encerrada — faça login novamente"})
		return
	}
	if *user.RefreshTokenHash != refreshTokenDigest(req.RefreshToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "sessão inválida — faça login novamente"})
		return
	}
	access, _ := s.newAccessToken(user.ID)
	newRefresh, _ := s.newRefreshToken(user.ID)
	if err := mobileDB(s.db).SaveRefreshToken(r.Context(), user.ID, newRefresh); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao renovar sessao"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accessToken":  access,
		"refreshToken": newRefresh,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	_ = mobileDB(s.db).ClearRefreshToken(r.Context(), uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func isMobileLoadTestUser(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	return strings.HasPrefix(email, "loadtest+") && strings.HasSuffix(email, "@chainfx.local")
}
