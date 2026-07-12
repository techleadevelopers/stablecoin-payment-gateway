package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"payment-gateway/internal/agents"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/email"
	"payment-gateway/internal/webhooks"
	"payment-gateway/internal/workers"
)

type Server struct {
	cfg              *config.Config
	db               *database.DB
	workers          *workers.WorkerManager
	email            *email.Service
	limiter          rateLimitStore
	globalLimiter    rateLimitStore
	webhookRegistry  *webhooks.Registry
	webhooks         *webhooks.Dispatcher
	webhookLogs      *webhooks.Logs
	webhookDashboard *webhooks.Dashboard
	agents           *agents.Client
}

type requestIDContextKey struct{}

type buyFeeBreakdown struct {
	Tier          string  `json:"tier"`
	ServiceBps    int     `json:"serviceBps"`
	ServiceFee    float64 `json:"serviceFee"`
	NetworkFee    float64 `json:"networkFee"`
	MinFee        float64 `json:"minFee"`
	TotalFee      float64 `json:"totalFee"`
	RateSpreadBps int     `json:"rateSpreadBps"`
}

func New(cfg *config.Config, db *database.DB, workerMgr *workers.WorkerManager, mailer *email.Service) *Server {
	webhookDispatcher := webhooks.New(db, cfg)
	return &Server{
		cfg:              cfg,
		db:               db,
		workers:          workerMgr,
		email:            mailer,
		limiter:          newConfiguredRateLimiter(cfg, cfg.OrderRateLimitWindowMs, cfg.OrderRateLimitMax),
		globalLimiter:    newConfiguredRateLimiter(cfg, cfg.RateLimitWindowMs, cfg.RateLimitMax),
		webhookRegistry:  webhooks.NewRegistry(db),
		webhooks:         webhookDispatcher,
		webhookLogs:      webhooks.NewLogs(db),
		webhookDashboard: webhooks.NewDashboard(db),
		agents:           agents.NewClient(cfg),
	}
}

func csvContains(csv, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, item := range strings.Split(csv, ",") {
		if strings.TrimSpace(item) == value {
			return true
		}
	}
	return false
}

func cloneJSONRequest(r *http.Request, payload any) *http.Request {
	raw, _ := json.Marshal(payload)
	clone := r.Clone(r.Context())
	clone.Body = io.NopCloser(bytes.NewReader(raw))
	clone.ContentLength = int64(len(raw))
	clone.Header = r.Header.Clone()
	clone.Header.Set("Content-Type", "application/json")
	return clone
}

func chainFXFakeWallet() string {
	return "0x000000000000000000000000000000000000dEaD"
}

func maskCSVKeys(csv string) []string {
	var out []string
	for _, item := range strings.Split(csv, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, maskAPIKey(item))
	}
	return out
}

func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 12 {
		return key
	}
	return key[:8] + "..." + key[len(key)-4:]
}
