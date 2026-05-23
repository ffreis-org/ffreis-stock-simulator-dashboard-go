package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ffreis-stock-simulator-dashboard-go/internal/logx"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//go:embed templates/* static/* docs/*
var assetsFS embed.FS

type app struct {
	httpClient     *http.Client
	simBaseURL     string
	tmpl           *template.Template
	swaggerEnabled bool
	swaggerToken   string
	openAPISpec    []byte
	logger         *slog.Logger
	maxBodyBytes   int64
	upstream       upstreamConfig
	metrics        *metricsCollector
	simCapTTL      time.Duration
	simCapMu       sync.RWMutex
	simCapStatus   simCapabilitiesStatus
	simCapExpiry   time.Time
}

type routeBinding struct {
	Pattern string
	Handler http.HandlerFunc
}

type serverConfig struct {
	ListenAddr        string
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
}

type upstreamConfig struct {
	Timeout      time.Duration
	MaxAttempts  int
	BaseDelay    time.Duration
	MaxDelay     time.Duration
	MaxBodyBytes int64
}

type ctxKey string

const (
	ctxKeyRequestID ctxKey = "request_id"
)

type pageData struct {
	SimulatorBaseURL string
	PollMs           int
}

type readyzResponse struct {
	Status        string `json:"status"`
	EngineEnabled bool   `json:"engine_enabled"`
	EngineReady   bool   `json:"engine_ready"`
}

type marketWindowHandle struct {
	Start        int     `json:"start"`
	End          int     `json:"end"`
	T            int     `json:"t"`
	CurrentPrice float64 `json:"current_price"`
}

type observation struct {
	MarketWindowHandle marketWindowHandle `json:"market_window_handle"`
	PortfolioVector    []float64          `json:"portfolio_vector"`
	OrderSummaryVector []float64          `json:"order_summary_vector"`
	Done               bool               `json:"done"`
}

type observeResponse struct {
	Observation observation `json:"observation"`
}

type resetRequest struct {
	Seed *int64 `json:"seed"`
}

type encodedAction struct {
	SideCode      int      `json:"side_code"`
	Units         float64  `json:"units"`
	OrderTypeCode int      `json:"order_type_code"`
	HasLimitPrice bool     `json:"has_limit_price"`
	LimitPrice    *float64 `json:"limit_price,omitempty"`
}

type stepManyRequest struct {
	Actions []encodedAction `json:"actions"`
}

type stepFormRequest struct {
	Side       string   `json:"side"`
	Units      float64  `json:"units"`
	OrderType  string   `json:"order_type"`
	LimitPrice *float64 `json:"limit_price"`
}

type stateEnvelope struct {
	SimulatorBaseURL string                `json:"simulator_base_url"`
	FetchedAt        string                `json:"fetched_at"`
	Readyz           readyzResponse        `json:"readyz"`
	Observation      *observation          `json:"observation,omitempty"`
	SimCapabilities  simCapabilitiesStatus `json:"sim_capabilities"`
	LastError        string                `json:"last_error,omitempty"`
}

type simCapabilitiesStatus struct {
	Available bool   `json:"available"`
	CheckedAt string `json:"checked_at"`
	Reason    string `json:"reason,omitempty"`
}

type apiErrorEnvelope struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

type metricsCollector struct {
	registry         *prometheus.Registry
	httpRequests     *prometheus.CounterVec
	httpDurations    *prometheus.HistogramVec
	upstreamRequests *prometheus.CounterVec
	upstreamDuration *prometheus.HistogramVec
}

const swaggerUIHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width,initial-scale=1" />
    <title>Stock Dashboard API Docs</title>
    <style>
      :root {
        color-scheme: light dark;
      }
      html, body {
        margin: 0;
        padding: 0;
        font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial;
      }
      .wrap {
        max-width: 1100px;
        margin: 0 auto;
        padding: 1rem;
      }
      h1 {
        margin: 0 0 0.5rem 0;
      }
      p {
        margin: 0.25rem 0 1rem 0;
      }
      pre {
        margin: 0;
        padding: 1rem;
        overflow: auto;
        border: 1px solid #8884;
        border-radius: 8px;
        background: #00000008;
      }
    </style>
  </head>
  <body>
    <div class="wrap">
      <h1>Stock Dashboard OpenAPI</h1>
      <p>Raw spec is available at <code>/openapi.yaml</code>.</p>
      <pre id="spec">Loading OpenAPI spec...</pre>
    </div>
    <script>
      fetch("/openapi.yaml", { credentials: "same-origin" })
        .then((r) => {
          if (!r.ok) {
            throw new Error("HTTP " + r.status);
          }
          return r.text();
        })
        .then((text) => {
          document.getElementById("spec").textContent = text;
        })
        .catch((err) => {
          document.getElementById("spec").textContent = "Failed to load /openapi.yaml: " + err;
        });
    </script>
  </body>
</html>`

func main() {
	logger := logx.New("stock-dashboard")
	if err := run(logger); err != nil {
		logger.Error("dashboard terminated with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadRunConfig()
	if err != nil {
		return err
	}

	mc := newMetricsCollector()
	tmpl, staticSubFS, openAPISpec, err := loadEmbeddedAssets()
	if err != nil {
		return err
	}

	a := newApp(logger, mc, tmpl, openAPISpec, cfg)
	handler := newHTTPHandler(logger, mc, a, staticSubFS, cfg)

	server := &http.Server{
		Addr:              cfg.http.ListenAddr,
		Handler:           handler,
		ReadTimeout:       cfg.http.ReadTimeout,
		WriteTimeout:      cfg.http.WriteTimeout,
		IdleTimeout:       cfg.http.IdleTimeout,
		ReadHeaderTimeout: cfg.http.ReadHeaderTimeout,
		MaxHeaderBytes:    cfg.http.MaxHeaderBytes,
	}

	logRunConfig(logger, a, cfg)
	return serveWithShutdown(logger, server, cfg.http.ShutdownTimeout)
}

type runConfig struct {
	simBaseURL     string
	pollMs         int
	swaggerEnabled bool
	swaggerToken   string
	metricsEnabled bool
	pprofEnabled   bool
	http           serverConfig
	upstream       upstreamConfig
	simCapTTL      time.Duration
}

func loadRunConfig() (runConfig, error) {
	simBaseURL := strings.TrimRight(getEnv("SIMULATOR_BASE_URL", "http://localhost:8000"), "/")
	if _, err := url.ParseRequestURI(simBaseURL); err != nil {
		return runConfig{}, fmt.Errorf("invalid SIMULATOR_BASE_URL %q: %w", simBaseURL, err)
	}

	pollMs := getEnvInt("DASHBOARD_POLL_MS", 2000)
	swaggerEnabled := getEnvBool("SWAGGER_ENABLED", false)
	swaggerToken := strings.TrimSpace(os.Getenv("SWAGGER_TOKEN"))
	listenAddr := ":" + strconv.Itoa(getEnvInt("DASHBOARD_PORT", 8080))
	metricsEnabled := getEnvBool("METRICS_ENABLED", false)
	pprofEnabled := getEnvBool("DEBUG_PPROF_ENABLED", false)

	httpCfg := serverConfig{
		ListenAddr:        listenAddr,
		ReadTimeout:       getEnvDuration("HTTP_READ_TIMEOUT", 10*time.Second),
		WriteTimeout:      getEnvDuration("HTTP_WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:       getEnvDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ReadHeaderTimeout: getEnvDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ShutdownTimeout:   getEnvDuration("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),
		MaxHeaderBytes:    getEnvInt("HTTP_MAX_HEADER_BYTES", 1_048_576),
	}

	upstreamCfg := upstreamConfig{
		Timeout:      getEnvDuration("UPSTREAM_TIMEOUT", 8*time.Second),
		MaxAttempts:  max(getEnvInt("UPSTREAM_RETRY_MAX_ATTEMPTS", 3), 1),
		BaseDelay:    getEnvDuration("UPSTREAM_RETRY_BASE_DELAY", 100*time.Millisecond),
		MaxDelay:     getEnvDuration("UPSTREAM_RETRY_MAX_DELAY", 1*time.Second),
		MaxBodyBytes: int64(getEnvInt("REQUEST_BODY_MAX_BYTES", 1_048_576)),
	}

	simCapTTL := normalizedSimCapTTL(getEnvDuration("SIM_CAPABILITIES_TTL", 5*time.Second))

	return runConfig{
		simBaseURL:     simBaseURL,
		pollMs:         pollMs,
		swaggerEnabled: swaggerEnabled,
		swaggerToken:   swaggerToken,
		metricsEnabled: metricsEnabled,
		pprofEnabled:   pprofEnabled,
		http:           httpCfg,
		upstream:       upstreamCfg,
		simCapTTL:      simCapTTL,
	}, nil
}

func normalizedSimCapTTL(simCapTTL time.Duration) time.Duration {
	if simCapTTL <= 0 {
		return 5 * time.Second
	}
	return simCapTTL
}

func loadEmbeddedAssets() (*template.Template, fs.FS, []byte, error) {
	tmpl, err := template.ParseFS(assetsFS, "templates/index.gohtml")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse template: %w", err)
	}

	staticSubFS, err := fs.Sub(assetsFS, "static")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load static assets: %w", err)
	}

	openAPISpec, err := assetsFS.ReadFile("docs/openapi.yaml")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load openapi spec: %w", err)
	}

	return tmpl, staticSubFS, openAPISpec, nil
}

func newApp(logger *slog.Logger, mc *metricsCollector, tmpl *template.Template, openAPISpec []byte, cfg runConfig) *app {
	a := &app{
		httpClient:     &http.Client{Timeout: cfg.upstream.Timeout},
		simBaseURL:     cfg.simBaseURL,
		tmpl:           tmpl,
		swaggerEnabled: cfg.swaggerEnabled,
		swaggerToken:   cfg.swaggerToken,
		openAPISpec:    openAPISpec,
		logger:         logger,
		maxBodyBytes:   cfg.upstream.MaxBodyBytes,
		upstream:       cfg.upstream,
		metrics:        mc,
		simCapTTL:      cfg.simCapTTL,
	}
	return a
}

func newHTTPHandler(logger *slog.Logger, mc *metricsCollector, a *app, staticSubFS fs.FS, cfg runConfig) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSubFS))))
	for _, route := range a.baseRouteBindings(cfg.pollMs) {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	if a.swaggerEnabled {
		for _, route := range a.swaggerRouteBindings() {
			mux.HandleFunc(route.Pattern, route.Handler)
		}
	}
	if cfg.metricsEnabled {
		mux.Handle("GET /metrics", promhttp.HandlerFor(mc.registry, promhttp.HandlerOpts{}))
	}
	if cfg.pprofEnabled {
		registerPprofRoutes(mux)
	}

	var handler http.Handler = mux
	handler = loggingMiddleware(logger, mc, handler)
	handler = securityHeadersMiddleware(handler)
	handler = recoveryMiddleware(logger, handler)
	handler = requestIDMiddleware(handler)
	return handler
}

func logRunConfig(logger *slog.Logger, a *app, cfg runConfig) {
	logger.Info(
		"starting dashboard server",
		"listen_addr", cfg.http.ListenAddr,
		"simulator_base_url", cfg.simBaseURL,
		"swagger_enabled", a.swaggerEnabled,
		"metrics_enabled", cfg.metricsEnabled,
		"pprof_enabled", cfg.pprofEnabled,
		"http_read_timeout", cfg.http.ReadTimeout.String(),
		"http_write_timeout", cfg.http.WriteTimeout.String(),
		"http_idle_timeout", cfg.http.IdleTimeout.String(),
		"http_shutdown_timeout", cfg.http.ShutdownTimeout.String(),
		"upstream_timeout", cfg.upstream.Timeout.String(),
		"upstream_retry_max_attempts", cfg.upstream.MaxAttempts,
		"sim_capabilities_ttl", cfg.simCapTTL.String(),
	)
}

func serveWithShutdown(logger *slog.Logger, server *http.Server, shutdownTimeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		if errors.Is(shutdownCtx.Err(), context.DeadlineExceeded) {
			logger.Warn("shutdown timed out", "timeout", shutdownTimeout.String())
		}
		return fmt.Errorf("shutdown failed: %w", err)
	}

	err := <-errCh
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server error after shutdown: %w", err)
	}

	logger.Info("server shutdown complete")
	return nil
}

func getEnv(key string, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func getEnvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	case "0", "false", "f", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = generateRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func recoveryMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				requestID := requestIDFromContext(r.Context())
				logger.Error(
					"panic recovered",
					"panic", rec,
					"path", r.URL.Path,
					"method", r.Method,
					"request_id", requestID,
					"stack", string(debug.Stack()),
				)
				if strings.HasPrefix(r.URL.Path, "/api/") {
					writeJSON(w, http.StatusInternalServerError, apiErrorEnvelope{
						Error:     "internal server error",
						RequestID: requestID,
					})
					return
				}
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")

		csp := "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
		}
		if r.URL.Path == "/" || r.URL.Path == "/swagger" {
			csp = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
		}
		w.Header().Set("Content-Security-Policy", csp)

		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}

		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(logger *slog.Logger, metrics *metricsCollector, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		var panicValue any
		defer func() {
			// Always log and observe — even on panic. Previously we re-panicked
			// here, which short-circuited the logger/metrics calls below and
			// dropped the request from observability entirely. Now we record
			// the panic context and continue. net/http will still log the
			// underlying panic stack at the server layer.
			if rec := recover(); rec != nil {
				panicValue = rec
				recorder.status = http.StatusInternalServerError
				// Attempt to write a 500 if the handler hadn't already
				// written a response. WriteHeader is a no-op if headers
				// were already sent.
				recorder.WriteHeader(http.StatusInternalServerError)
			}
			duration := time.Since(start)
			requestID := requestIDFromContext(r.Context())
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"duration_ms", duration.Milliseconds(),
				"remote_addr", r.RemoteAddr,
				"user_agent", r.UserAgent(),
				"request_id", requestID,
			}
			if panicValue != nil {
				attrs = append(attrs, "panic", panicValue)
				logger.Error("http request panicked", attrs...)
			} else {
				logger.Info("http request completed", attrs...)
			}
			if metrics != nil {
				metrics.observeHTTP(r.Method, r.URL.Path, recorder.status, duration)
			}
		}()
		next.ServeHTTP(recorder, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.status = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, ok := ctx.Value(ctxKeyRequestID).(string)
	if !ok {
		return ""
	}
	return value
}

func generateRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) baseRouteBindings(pollMs int) []routeBinding {
	return []routeBinding{
		{Pattern: "GET /", Handler: a.handleIndex(pollMs)},
		{Pattern: "GET /healthz", Handler: handleHealthz},
		{Pattern: "GET /api/state", Handler: a.handleState},
		{Pattern: "POST /api/reset", Handler: a.handleReset},
		{Pattern: "POST /api/step", Handler: a.handleStep},
		{Pattern: "GET /api/sim/capabilities", Handler: a.handleSimCapabilities},
		{Pattern: "GET /api/sim/flows", Handler: a.handleSimFlows},
		{Pattern: "POST /api/sim/branches", Handler: a.handleSimCreateBranch},
		{Pattern: "POST /api/sim/flows/{id}/step_many", Handler: a.handleSimStepMany},
		{Pattern: "GET /api/sim/flows/{id}/observe", Handler: a.handleSimObserveFlow},
		{Pattern: "GET /api/sim/flows/{id}/trace", Handler: a.handleSimTraceFlow},
		{Pattern: "DELETE /api/sim/flows/{id}", Handler: a.handleSimDeleteFlow},
	}
}

func (a *app) swaggerRouteBindings() []routeBinding {
	return []routeBinding{
		{Pattern: "GET /openapi.yaml", Handler: a.handleOpenAPI},
		{Pattern: "GET /swagger", Handler: a.handleSwaggerUI},
	}
}

func routePatterns(bindings []routeBinding) []string {
	patterns := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		patterns = append(patterns, binding.Pattern)
	}
	return patterns
}

func (a *app) handleIndex(pollMs int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		data := pageData{
			SimulatorBaseURL: a.simBaseURL,
			PollMs:           pollMs,
		}
		if err := a.tmpl.ExecuteTemplate(w, "index.gohtml", data); err != nil {
			a.logger.Error("rendering dashboard template failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (a *app) handleState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env := stateEnvelope{
		SimulatorBaseURL: a.simBaseURL,
		FetchedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	env.SimCapabilities = a.getSimCapabilities(ctx, false)

	ready, err := a.fetchReadyz(ctx)
	if err != nil {
		env.LastError = err.Error()
		writeJSON(w, http.StatusBadGateway, env)
		return
	}
	env.Readyz = ready

	if ready.Status == "ready" && ready.EngineEnabled && ready.EngineReady {
		obs, err := a.fetchObserve(ctx)
		if err != nil {
			env.LastError = err.Error()
			writeJSON(w, http.StatusBadGateway, env)
			return
		}
		env.Observation = &obs.Observation
	}

	writeJSON(w, http.StatusOK, env)
}

func (a *app) handleReset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.maxBodyBytes)
	var payload resetRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid reset payload", http.StatusBadRequest)
		return
	}

	statusCode, body, err := a.forwardJSON(r.Context(), http.MethodPost, "/v1/reset", payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeRawJSON(w, statusCode, body)
}

func (a *app) handleStep(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.maxBodyBytes)
	var payload stepFormRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid step payload", http.StatusBadRequest)
		return
	}

	action, err := toEncodedAction(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stepPayload := stepManyRequest{Actions: []encodedAction{action}}
	statusCode, body, err := a.forwardJSON(r.Context(), http.MethodPost, "/v1/step_many", stepPayload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeRawJSON(w, statusCode, body)
}

func (a *app) handleSimCapabilities(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("refresh"))
	refresh := raw == "1" || strings.EqualFold(raw, "true")
	caps := a.getSimCapabilities(r.Context(), refresh)
	writeJSON(w, http.StatusOK, caps)
}

func (a *app) handleSimFlows(w http.ResponseWriter, r *http.Request) {
	if !a.ensureSimAvailable(w, r) {
		return
	}
	statusCode, body, err := a.forwardNoBody(r.Context(), http.MethodGet, withQuery("/v1/flows", r.URL.RawQuery))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeRawJSON(w, statusCode, body)
}

func (a *app) handleSimCreateBranch(w http.ResponseWriter, r *http.Request) {
	if !a.ensureSimAvailable(w, r) {
		return
	}
	raw, ok := readJSONPayload(w, r, a.maxBodyBytes, "invalid simulation branch payload")
	if !ok {
		return
	}
	statusCode, body, err := a.forwardRawJSON(r.Context(), http.MethodPost, "/v1/branches", raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeRawJSON(w, statusCode, body)
}

func (a *app) handleSimStepMany(w http.ResponseWriter, r *http.Request) {
	if !a.ensureSimAvailable(w, r) {
		return
	}
	flowID := strings.TrimSpace(r.PathValue("id"))
	if flowID == "" {
		http.Error(w, "flow id is required", http.StatusBadRequest)
		return
	}
	raw, ok := readJSONPayload(w, r, a.maxBodyBytes, "invalid simulation step payload")
	if !ok {
		return
	}
	path := fmt.Sprintf("/v1/flows/%s/step_many", url.PathEscape(flowID))
	statusCode, body, err := a.forwardRawJSON(r.Context(), http.MethodPost, path, raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeRawJSON(w, statusCode, body)
}

func (a *app) handleSimObserveFlow(w http.ResponseWriter, r *http.Request) {
	if !a.ensureSimAvailable(w, r) {
		return
	}
	flowID := strings.TrimSpace(r.PathValue("id"))
	if flowID == "" {
		http.Error(w, "flow id is required", http.StatusBadRequest)
		return
	}
	path := withQuery(fmt.Sprintf("/v1/flows/%s/observe", url.PathEscape(flowID)), r.URL.RawQuery)
	statusCode, body, err := a.forwardNoBody(r.Context(), http.MethodGet, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeRawJSON(w, statusCode, body)
}

func (a *app) handleSimTraceFlow(w http.ResponseWriter, r *http.Request) {
	if !a.ensureSimAvailable(w, r) {
		return
	}
	flowID := strings.TrimSpace(r.PathValue("id"))
	if flowID == "" {
		http.Error(w, "flow id is required", http.StatusBadRequest)
		return
	}
	path := withQuery(fmt.Sprintf("/v1/flows/%s/trace", url.PathEscape(flowID)), r.URL.RawQuery)
	statusCode, body, err := a.forwardNoBody(r.Context(), http.MethodGet, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeRawJSON(w, statusCode, body)
}

func (a *app) handleSimDeleteFlow(w http.ResponseWriter, r *http.Request) {
	if !a.ensureSimAvailable(w, r) {
		return
	}
	flowID := strings.TrimSpace(r.PathValue("id"))
	if flowID == "" {
		http.Error(w, "flow id is required", http.StatusBadRequest)
		return
	}
	path := withQuery(fmt.Sprintf("/v1/flows/%s", url.PathEscape(flowID)), r.URL.RawQuery)
	statusCode, body, err := a.forwardNoBody(r.Context(), http.MethodDelete, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeRawJSON(w, statusCode, body)
}

func (a *app) ensureSimAvailable(w http.ResponseWriter, r *http.Request) bool {
	caps := a.getSimCapabilities(r.Context(), false)
	if caps.Available {
		return true
	}
	reason := "simulations are not available from upstream simulator"
	if caps.Reason != "" {
		reason = caps.Reason
	}
	writeJSON(w, http.StatusServiceUnavailable, apiErrorEnvelope{
		Error:     reason,
		RequestID: requestIDFromContext(r.Context()),
	})
	return false
}

func (a *app) getSimCapabilities(ctx context.Context, force bool) simCapabilitiesStatus {
	now := time.Now()

	a.simCapMu.RLock()
	cached := a.simCapStatus
	expiry := a.simCapExpiry
	ttl := a.simCapTTL
	a.simCapMu.RUnlock()
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	if !force && !expiry.IsZero() && now.Before(expiry) {
		return cached
	}

	probed := a.probeSimCapabilities(ctx)
	a.simCapMu.Lock()
	a.simCapStatus = probed
	a.simCapExpiry = now.Add(ttl)
	a.simCapMu.Unlock()
	return probed
}

func (a *app) probeSimCapabilities(ctx context.Context) simCapabilitiesStatus {
	out := simCapabilitiesStatus{
		Available: false,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
	statusCode, _, err := a.forwardNoBody(ctx, http.MethodGet, "/v1/flows")
	if err != nil {
		out.Reason = fmt.Sprintf("capability probe failed: %v", err)
		return out
	}
	if statusCode == http.StatusNotFound {
		out.Reason = "upstream does not expose /v1/flows"
		return out
	}
	if statusCode >= http.StatusInternalServerError {
		out.Reason = fmt.Sprintf("upstream /v1/flows returned %d", statusCode)
		return out
	}
	out.Available = true
	return out
}

func (a *app) isSwaggerAuthorized(r *http.Request) bool {
	if a.swaggerToken == "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("X-Swagger-Token")) == a.swaggerToken {
		return true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		if strings.TrimSpace(auth[len("bearer "):]) == a.swaggerToken {
			return true
		}
	}
	return false
}

func (a *app) authorizeSwaggerOr401(w http.ResponseWriter, r *http.Request) bool {
	if a.isSwaggerAuthorized(r) {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="swagger"`)
	http.Error(w, "swagger auth required", http.StatusUnauthorized)
	return false
}

func (a *app) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeSwaggerOr401(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.openAPISpec)
}

func (a *app) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeSwaggerOr401(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, swaggerUIHTML)
}

func toEncodedAction(in stepFormRequest) (encodedAction, error) {
	side := strings.ToLower(strings.TrimSpace(in.Side))
	orderType := strings.ToLower(strings.TrimSpace(in.OrderType))

	if side == "" {
		side = "hold"
	}
	if orderType == "" {
		orderType = "market"
	}

	var sideCode int
	switch side {
	case "hold":
		sideCode = 0
	case "buy":
		sideCode = 1
	case "sell":
		sideCode = -1
	default:
		return encodedAction{}, errors.New("side must be one of: hold, buy, sell")
	}

	var orderTypeCode int
	switch orderType {
	case "market":
		orderTypeCode = 0
	case "limit":
		orderTypeCode = 1
	default:
		return encodedAction{}, errors.New("order_type must be one of: market, limit")
	}

	if in.Units < 0 {
		return encodedAction{}, errors.New("units must be >= 0")
	}
	if side == "hold" {
		in.Units = 0
		orderTypeCode = 0
		in.LimitPrice = nil
	}
	if side != "hold" && in.Units == 0 {
		return encodedAction{}, errors.New("units must be > 0 for buy/sell")
	}
	if orderTypeCode == 1 && side != "hold" && in.LimitPrice == nil {
		return encodedAction{}, errors.New("limit_price is required for limit orders")
	}

	return encodedAction{
		SideCode:      sideCode,
		Units:         in.Units,
		OrderTypeCode: orderTypeCode,
		HasLimitPrice: in.LimitPrice != nil,
		LimitPrice:    in.LimitPrice,
	}, nil
}

func (a *app) fetchReadyz(ctx context.Context) (readyzResponse, error) {
	var out readyzResponse
	statusCode, body, err := a.forwardNoBody(ctx, http.MethodGet, "/readyz")
	if err != nil {
		return out, err
	}
	if statusCode >= 500 {
		return out, errors.New("simulator /readyz returned server error")
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (a *app) fetchObserve(ctx context.Context) (observeResponse, error) {
	var out observeResponse
	statusCode, body, err := a.forwardNoBody(ctx, http.MethodGet, "/v1/observe")
	if err != nil {
		return out, err
	}
	if statusCode >= 400 {
		return out, errors.New("simulator /v1/observe returned error")
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (a *app) forwardNoBody(ctx context.Context, method string, path string) (int, []byte, error) {
	return a.doUpstreamRequest(ctx, method, path, nil, "")
}

func (a *app) forwardJSON(ctx context.Context, method string, path string, payload any) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	return a.doUpstreamRequest(ctx, method, path, raw, "application/json")
}

func (a *app) forwardRawJSON(ctx context.Context, method string, path string, raw []byte) (int, []byte, error) {
	return a.doUpstreamRequest(ctx, method, path, raw, "application/json")
}

func (a *app) doUpstreamRequest(ctx context.Context, method string, path string, payload []byte, contentType string) (int, []byte, error) {
	var last upstreamAttempt
	for attempt := 1; attempt <= a.upstream.MaxAttempts; attempt++ {
		last = a.tryUpstreamOnce(ctx, method, path, payload, contentType)
		if last.err != nil {
			if !a.shouldRetryError(ctx, last.err, attempt) {
				return 0, nil, last.err
			}
			a.sleepBackoff(ctx, attempt)
			continue
		}

		if shouldRetryUpstreamStatus(last.status, attempt, a.upstream.MaxAttempts) {
			a.sleepBackoff(ctx, attempt)
			continue
		}

		return last.status, last.body, nil
	}

	if last.err != nil {
		return 0, nil, last.err
	}
	return last.status, last.body, nil
}

type upstreamAttempt struct {
	status int
	body   []byte
	err    error
}

func (a *app) tryUpstreamOnce(ctx context.Context, method string, path string, payload []byte, contentType string) upstreamAttempt {
	attemptCtx, cancel := context.WithTimeout(ctx, a.upstream.Timeout)
	defer cancel()

	start := time.Now()
	req, err := a.newUpstreamRequest(attemptCtx, method, path, payload, contentType)
	if err != nil {
		return upstreamAttempt{err: err}
	}

	status, body, err := a.doHTTP(req)
	a.observeUpstreamAttempt(path, status, err, time.Since(start))
	return upstreamAttempt{status: status, body: body, err: err}
}

func (a *app) newUpstreamRequest(ctx context.Context, method string, path string, payload []byte, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.simBaseURL+path, upstreamBody(payload))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

func upstreamBody(payload []byte) io.Reader {
	if payload == nil {
		return nil
	}
	return bytes.NewReader(payload)
}

func (a *app) doHTTP(req *http.Request) (int, []byte, error) {
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func (a *app) observeUpstreamAttempt(path string, status int, err error, duration time.Duration) {
	if a.metrics == nil {
		return
	}
	if err != nil {
		a.metrics.observeUpstream(path, "error", duration)
		return
	}
	a.metrics.observeUpstream(path, upstreamResultLabel(status), duration)
}

func upstreamResultLabel(status int) string {
	if status >= 500 {
		return "server_error"
	}
	if status >= 400 {
		return "client_error"
	}
	return "ok"
}

func shouldRetryUpstreamStatus(status, attempt, maxAttempts int) bool {
	if status < 500 {
		return false
	}
	return attempt < maxAttempts
}

func (a *app) shouldRetryError(ctx context.Context, err error, attempt int) bool {
	if attempt >= a.upstream.MaxAttempts {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return true
}

func (a *app) sleepBackoff(ctx context.Context, attempt int) {
	delay := a.upstream.BaseDelay * time.Duration(1<<(attempt-1))
	if delay > a.upstream.MaxDelay {
		delay = a.upstream.MaxDelay
	}
	if delay <= 0 {
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func newMetricsCollector() *metricsCollector {
	reg := prometheus.NewRegistry()

	httpRequests := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dashboard_http_requests_total",
			Help: "Total HTTP requests handled by the dashboard.",
		},
		[]string{"method", "path", "status"},
	)

	httpDurations := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "dashboard_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	upstreamRequests := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dashboard_upstream_requests_total",
			Help: "Total upstream requests sent to simulator.",
		},
		[]string{"endpoint", "result"},
	)

	upstreamDuration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "dashboard_upstream_request_duration_seconds",
			Help:    "Upstream request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	reg.MustRegister(httpRequests, httpDurations, upstreamRequests, upstreamDuration)

	return &metricsCollector{
		registry:         reg,
		httpRequests:     httpRequests,
		httpDurations:    httpDurations,
		upstreamRequests: upstreamRequests,
		upstreamDuration: upstreamDuration,
	}
}

func (m *metricsCollector) observeHTTP(method, path string, status int, duration time.Duration) {
	if m == nil {
		return
	}
	statusLabel := strconv.Itoa(status)
	m.httpRequests.WithLabelValues(method, path, statusLabel).Inc()
	m.httpDurations.WithLabelValues(method, path).Observe(duration.Seconds())
}

func (m *metricsCollector) observeUpstream(endpoint, result string, duration time.Duration) {
	if m == nil {
		return
	}
	m.upstreamRequests.WithLabelValues(endpoint, result).Inc()
	m.upstreamDuration.WithLabelValues(endpoint).Observe(duration.Seconds())
}

func registerPprofRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "marshal error", http.StatusInternalServerError)
		return
	}
	writeRawJSON(w, status, raw)
}

func writeRawJSON(w http.ResponseWriter, status int, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func withQuery(path string, rawQuery string) string {
	if strings.TrimSpace(rawQuery) == "" {
		return path
	}
	return path + "?" + rawQuery
}

func readJSONPayload(w http.ResponseWriter, r *http.Request, maxBodyBytes int64, invalidMessage string) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, invalidMessage, http.StatusBadRequest)
		return nil, false
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		http.Error(w, invalidMessage, http.StatusBadRequest)
		return nil, false
	}
	if !json.Valid(raw) {
		http.Error(w, invalidMessage, http.StatusBadRequest)
		return nil, false
	}
	return raw, true
}
