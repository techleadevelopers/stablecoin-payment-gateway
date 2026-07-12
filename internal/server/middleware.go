package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

func (s *Server) withPublicSurfaceGuards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.rejectQueryAPIKeyInProduction(w, r) {
			return
		}
		if s.rejectUnsafeInternalRequest(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rejectQueryAPIKeyInProduction(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || s.cfg == nil || !s.cfg.IsProduction() {
		return false
	}
	if strings.TrimSpace(r.URL.Query().Get("apiKey")) == "" {
		return false
	}
	writeAPIError(w, r, http.StatusBadRequest, "SECRET_IN_QUERY_NOT_ALLOWED", "Send API keys with Authorization: Bearer or X-Api-Key headers.")
	return true
}

func (s *Server) rejectUnsafeInternalRequest(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/internal/") {
		return false
	}
	if strings.TrimSpace(r.Header.Get("x-internal-hmac")) == "" {
		writeAPIError(w, r, http.StatusUnauthorized, "INTERNAL_SIGNATURE_REQUIRED", "Internal signature is required.")
		return true
	}
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.InternalAllowedCIDRs) == "" {
		writeAPIError(w, r, http.StatusForbidden, "INTERNAL_NETWORK_NOT_ALLOWED", "Internal endpoint is not available from this network.")
		return true
	}
	if !ipAllowedByCIDRList(remoteIP(r), s.cfg.InternalAllowedCIDRs) {
		writeAPIError(w, r, http.StatusForbidden, "INTERNAL_NETWORK_NOT_ALLOWED", "Internal endpoint is not available from this network.")
		return true
	}
	return false
}

func (s *Server) withSmartRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.shouldSkipSmartRateLimit(r) {
			next.ServeHTTP(w, r)
			return
		}
		key, tier := s.smartRateLimitKey(r)
		routeClass := smartRateLimitRouteClass(r)
		limit := smartRateLimitMax(tier, routeClass, s.cfg.RateLimitMax)
		allowed, resetAt, remaining := s.globalLimiter.AllowN(key+":"+routeClass, limit)
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
		if !allowed {
			retryAfter := int(time.Until(resetAt).Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":      "rate limit exceeded",
				"tier":       tier,
				"routeClass": routeClass,
				"retryAfter": retryAfter,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) shouldSkipSmartRateLimit(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return true
	}
	switch r.URL.Path {
	case "/healthz", "/readyz", "/api/pix/webhook", "/api/pix/webhook/buy", "/api/stripe/webhook/buy":
		return true
	default:
		return false
	}
}

func (s *Server) smartRateLimitKey(r *http.Request) (string, string) {
	apiKey := chainFXAPIKey(r)
	if apiKey != "" {
		auth := s.chainFXAuthContext(r)
		tier := "api"
		switch auth.Mode {
		case "live", "live-dev":
			tier = "live"
		case "test", "development":
			tier = "test"
		}
		if !auth.Valid {
			tier = "invalid_key"
		}
		return "key:" + shortSecretHash(apiKey), tier
	}
	return "ip:" + clientIP(r), "anonymous"
}

func shortSecretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func smartRateLimitRouteClass(r *http.Request) string {
	path := r.URL.Path
	method := r.Method
	if method == http.MethodGet && (path == "/openapi.json" || path == "/llms.txt" || path == "/robots.txt" || path == "/sitemap.xml" || strings.HasPrefix(path, "/.well-known/")) {
		return "discovery"
	}
	if strings.HasPrefix(path, "/mcp/") {
		return "mcp"
	}
	if strings.Contains(path, "/execute") || strings.Contains(path, "/usage") || strings.Contains(path, "/purchase") || strings.HasSuffix(path, "/trade/execute") {
		return "execution"
	}
	if method == http.MethodPost {
		return "write"
	}
	return "read"
}

func smartRateLimitMax(tier, routeClass string, configuredMax int) int {
	if configuredMax <= 0 {
		configuredMax = 100
	}
	limits := map[string]map[string]int{
		"anonymous":   {"discovery": 180, "read": configuredMax, "write": 20, "mcp": 10, "execution": 10},
		"invalid_key": {"discovery": 60, "read": 30, "write": 10, "mcp": 5, "execution": 5},
		"test":        {"discovery": 600, "read": 400, "write": 120, "mcp": 180, "execution": 90},
		"live":        {"discovery": 2000, "read": 1200, "write": 400, "mcp": 800, "execution": 300},
		"api":         {"discovery": 600, "read": 400, "write": 100, "mcp": 120, "execution": 80},
	}
	if byClass, ok := limits[tier]; ok {
		if limit, ok := byClass[routeClass]; ok {
			return limit
		}
	}
	return configuredMax
}

func cors(cfg *config.Config, next http.Handler) http.Handler {
	allowed := strings.Split(cfg.AllowedOrigins, ",")
	allowed = append(allowed, "http://localhost:5173", "http://127.0.0.1:5173", "https://swapped-cryptocurrensy.vercel.app", "https://www.chainfx.store", "https://chainfx.store")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		w.Header().Add("Vary", "Origin")
		for _, item := range allowed {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if item == "*" {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Api-Key, x-internal-hmac, x-idempotency-key, x-efi-signature, x-chainfx-signature, Stripe-Signature")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				break
			}
			if origin != "" && item == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Api-Key, x-internal-hmac, x-idempotency-key, x-efi-signature, x-chainfx-signature, Stripe-Signature")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				break
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type statusCaptureWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCaptureWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusCaptureWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusCaptureWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusCaptureWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) withDeveloperRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusCaptureWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		duration := time.Since(start)
		slog.Info("http_request", "request_id", requestID(r), "method", r.Method, "path", r.URL.Path, "status", status, "duration_ms", duration.Milliseconds())
		if s == nil || s.db == nil || s.shouldSkipDeveloperRequestLog(r) {
			return
		}
		apiKey := chainFXAPIKey(r)
		auth := s.chainFXAuthContext(r)
		if s.cfg != nil && s.cfg.IsProduction() {
			apiKey = chainFXAPIKeyFromHeader(r)
			auth = s.chainFXAuthForKey(apiKey)
		}
		authMode := auth.Mode
		if authMode == "" {
			authMode = "anonymous"
		}
		scope := "none"
		if apiKey != "" {
			scope = "api_key"
		}
		_ = s.db.RecordAPIRequestLog(context.Background(), database.APIRequestLogInput{
			RequestID:   requestID(r),
			Method:      r.Method,
			Path:        r.URL.Path,
			RouteClass:  smartRateLimitRouteClass(r),
			StatusCode:  status,
			DurationMS:  duration.Milliseconds(),
			APIKeyHash:  shortSecretHash(apiKey),
			APIKeyScope: scope,
			AuthMode:    authMode,
			ClientIP:    clientIP(r),
			UserAgent:   r.UserAgent(),
		})
	})
}

func (s *Server) shouldSkipDeveloperRequestLog(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return true
	}
	switch r.URL.Path {
	case "/healthz", "/readyz":
		return true
	default:
		return false
	}
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = database.NewID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id)))
	})
}
