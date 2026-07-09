package security

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// RequestContext contém metadados da requisição validada
type RequestContext struct {
	RequestID string
	Timestamp int64
	Nonce     string
	APIKey    string
	ClientIP  string
	UserAgent string
	Path      string
	Method    string
	BodyHash  string
	Validated bool
	StartTime time.Time
}

// RequestValidator valida requisições completas
type RequestValidator struct {
	hmac         *HMACValidator
	nonceManager *NonceManager
	rateLimiter  RateLimiter
	maxBodySize  int64
}

// RequestValidatorConfig configuração do validador
type RequestValidatorConfig struct {
	HMACSecret      string
	HMACMaxSkew     int64
	NonceStore      NonceStore
	NonceTTL        time.Duration
	RateLimit       int
	RateWindow      time.Duration
	RateLimiterType RateLimiterType
	MaxBodySize     int64
	OldSecret       string
}

// NewRequestValidator cria um novo validador de requisições
func NewRequestValidator(config RequestValidatorConfig) *RequestValidator {
	hmac := NewHMACValidator(config.HMACSecret, config.HMACMaxSkew)
	if config.OldSecret != "" {
		hmac.SetOldSecret(config.OldSecret)
	}

	nonceMgr := NewNonceManager(config.NonceStore, config.NonceTTL)
	rateLimiter := NewRateLimiter(config.RateLimiterType, config.RateLimit, config.RateWindow)

	return &RequestValidator{
		hmac:         hmac,
		nonceManager: nonceMgr,
		rateLimiter:  rateLimiter,
		maxBodySize:  config.MaxBodySize,
	}
}

// ValidateRequest valida uma requisição HTTP completa
func (rv *RequestValidator) ValidateRequest(r *http.Request) (*RequestContext, error) {
	ctx := &RequestContext{
		RequestID: generateRequestID(),
		StartTime: time.Now(),
		Path:      r.URL.Path,
		Method:    r.Method,
		ClientIP:  getClientIP(r),
		UserAgent: r.UserAgent(),
	}

	// 1. Limite de tamanho do body
	if rv.maxBodySize > 0 && r.ContentLength > rv.maxBodySize {
		return ctx, fmt.Errorf("body size exceeds limit: %d > %d", r.ContentLength, rv.maxBodySize)
	}

	// 2. Lê body com limite
	body, err := io.ReadAll(io.LimitReader(r.Body, rv.maxBodySize))
	if err != nil {
		return ctx, fmt.Errorf("failed to read body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	// 3. Calcula hash do body
	bodyHash := sha256.Sum256(body)
	ctx.BodyHash = hex.EncodeToString(bodyHash[:])

	// 4. Extrai headers de segurança
	tsStr := r.Header.Get("X-Request-Timestamp")
	nonce := r.Header.Get("X-Request-Nonce")
	hmacHeader := r.Header.Get("X-Request-Signature")
	apiKey := r.Header.Get("X-API-Key")

	if apiKey != "" {
		ctx.APIKey = apiKey
	}

	// 5. Valida HMAC (se presente)
	if hmacHeader != "" {
		if err := rv.hmac.ValidateHMAC(tsStr, nonce, hmacHeader, body); err != nil {
			return ctx, fmt.Errorf("hmac validation failed: %w", err)
		}
		ctx.Validated = true
	}

	// 6. Valida nonce (anti-replay)
	if nonce != "" {
		if err := rv.nonceManager.ValidateNonce(r.Context(), nonce); err != nil {
			return ctx, fmt.Errorf("nonce validation failed: %w", err)
		}
	}

	// 7. Rate limit (por IP + API Key)
	rateKey := ctx.ClientIP
	if ctx.APIKey != "" {
		rateKey = ctx.APIKey
	}
	if !rv.rateLimiter.Allow(rateKey) {
		return ctx, fmt.Errorf("rate limit exceeded for key: %s", rateKey)
	}

	// 8. Extrai timestamp
	if tsStr != "" {
		ts, _ := strconv.ParseInt(tsStr, 10, 64)
		ctx.Timestamp = ts
	}
	ctx.Nonce = nonce

	return ctx, nil
}

// --- Middleware HTTP ---

// SecurityMiddleware middleware HTTP que aplica todas as validações
func (rv *RequestValidator) SecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := rv.ValidateRequest(r)
		if err != nil {
			http.Error(w, fmt.Sprintf("Security validation failed: %v", err), http.StatusUnauthorized)
			return
		}

		// Adiciona contexto na requisição
		ctxValue := context.WithValue(r.Context(), RequestContextKey, ctx)
		next.ServeHTTP(w, r.WithContext(ctxValue))
	})
}

// --- Context Keys ---

type contextKey string

const RequestContextKey contextKey = "request_context"

// GetRequestContext recupera o contexto da requisição
func GetRequestContext(ctx context.Context) *RequestContext {
	if val := ctx.Value(RequestContextKey); val != nil {
		if rc, ok := val.(*RequestContext); ok {
			return rc
		}
	}
	return nil
}

// --- Helpers ---

func generateRequestID() string {
	return fmt.Sprintf("req-%d-%s", time.Now().UnixNano(), hex.EncodeToString([]byte(GenerateNonce()[:8])))
}

func getClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}
