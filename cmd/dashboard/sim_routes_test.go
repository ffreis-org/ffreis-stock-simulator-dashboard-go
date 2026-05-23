package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestRouteMatrix_BaseRoutes(t *testing.T) {
	t.Parallel()

	a := &app{}
	got := routePatterns(a.baseRouteBindings(2000))
	want := []string{
		"GET /",
		"GET /healthz",
		"GET /api/state",
		"POST /api/reset",
		"POST /api/step",
		"GET /api/sim/capabilities",
		"GET /api/sim/flows",
		"POST /api/sim/branches",
		"POST /api/sim/flows/{id}/step_many",
		"GET /api/sim/flows/{id}/observe",
		"GET /api/sim/flows/{id}/trace",
		"DELETE /api/sim/flows/{id}",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("base route matrix mismatch\nwant=%v\ngot=%v", want, got)
	}
}

func TestRouteMatrix_SwaggerRoutes(t *testing.T) {
	t.Parallel()

	a := &app{}
	got := routePatterns(a.swaggerRouteBindings())
	want := []string{
		"GET /openapi.yaml",
		"GET /swagger",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("swagger route matrix mismatch\nwant=%v\ngot=%v", want, got)
	}
}

func TestOpenAPISpecCoversDashboardRoutes(t *testing.T) {
	t.Parallel()

	raw, err := assetsFS.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi spec: %v", err)
	}

	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse openapi spec: %v", err)
	}

	a := &app{}
	patterns := append(routePatterns(a.baseRouteBindings(1000)), routePatterns(a.swaggerRouteBindings())...)
	for _, pattern := range patterns {
		method, path, ok := parseRoutePattern(pattern)
		if !ok {
			t.Fatalf("invalid route pattern %q", pattern)
		}
		if path == "/" {
			continue
		}

		ops, ok := spec.Paths[path]
		if !ok {
			t.Fatalf("openapi missing path %q", path)
		}
		if _, ok := ops[strings.ToLower(method)]; !ok {
			t.Fatalf("openapi missing operation %s %s", method, path)
		}
	}
}

func TestHandleSimCapabilities_CacheAndRefresh(t *testing.T) {
	t.Parallel()

	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/flows" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		calls++
		writeRawJSON(w, http.StatusOK, []byte(`{"flows":[]}`))
	}))
	defer upstream.Close()

	a := newTestApp(upstream)
	dashboard := newDashboardServer(a)
	defer dashboard.Close()

	// first call probes upstream
	resp1, body1 := mustRequest(t, dashboard.URL, http.MethodGet, "/api/sim/capabilities", nil)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp1.StatusCode, string(body1))
	}
	var caps1 simCapabilitiesStatus
	mustUnmarshal(t, body1, &caps1)
	if !caps1.Available {
		t.Fatalf("expected available=true, got %+v", caps1)
	}

	// second call should be cached
	resp2, _ := mustRequest(t, dashboard.URL, http.MethodGet, "/api/sim/capabilities", nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}

	// refresh call should force probe
	resp3, _ := mustRequest(t, dashboard.URL, http.MethodGet, "/api/sim/capabilities?refresh=1", nil)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp3.StatusCode)
	}

	if calls != 2 {
		t.Fatalf("expected exactly 2 upstream probes, got %d", calls)
	}
}

func TestHandleSimCapabilities_Unsupported(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/flows" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	a := newTestApp(upstream)
	dashboard := newDashboardServer(a)
	defer dashboard.Close()

	resp, body := mustRequest(t, dashboard.URL, http.MethodGet, "/api/sim/capabilities", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(body))
	}

	var caps simCapabilitiesStatus
	mustUnmarshal(t, body, &caps)
	if caps.Available {
		t.Fatalf("expected unavailable capability, got %+v", caps)
	}
	if !strings.Contains(caps.Reason, "/v1/flows") {
		t.Fatalf("expected reason to mention /v1/flows, got %q", caps.Reason)
	}
}

func TestHandleSimFlows_SuccessAndQueryPassthrough(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/flows" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.RawQuery != "status=running&limit=2" {
			t.Fatalf("unexpected query: %q", r.URL.RawQuery)
		}
		writeRawJSON(w, http.StatusOK, []byte(`{"flows":[{"flow_id":"main"}]}`))
	}))
	defer upstream.Close()

	a := newTestApp(upstream)
	seedSimCapabilities(a, true, "", time.Now().Add(time.Minute))
	dashboard := newDashboardServer(a)
	defer dashboard.Close()

	resp, body := mustRequest(t, dashboard.URL, http.MethodGet, "/api/sim/flows?status=running&limit=2", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(body))
	}
	if string(body) != `{"flows":[{"flow_id":"main"}]}` {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestHandleSimCreateBranch_InvalidPayload(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()

	a := newTestApp(upstream)
	seedSimCapabilities(a, true, "", time.Now().Add(time.Minute))
	dashboard := newDashboardServer(a)
	defer dashboard.Close()

	resp, body := mustRequest(t, dashboard.URL, http.MethodPost, "/api/sim/branches", []byte("not-json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "invalid simulation branch payload") {
		t.Fatalf("unexpected error body: %s", string(body))
	}
}

func TestHandleSimStepMany_InvalidPayload(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()

	a := newTestApp(upstream)
	seedSimCapabilities(a, true, "", time.Now().Add(time.Minute))
	dashboard := newDashboardServer(a)
	defer dashboard.Close()

	resp, body := mustRequest(t, dashboard.URL, http.MethodPost, "/api/sim/flows/alt-01/step_many", []byte("{oops"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "invalid simulation step payload") {
		t.Fatalf("unexpected error body: %s", string(body))
	}
}

func TestSimRoutes_FeatureGatedFallbackWhenUnavailable(t *testing.T) {
	t.Parallel()

	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	a := newTestApp(upstream)
	seedSimCapabilities(a, false, "sim disabled upstream", time.Now().Add(time.Minute))
	dashboard := newDashboardServer(a)
	defer dashboard.Close()

	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{name: "flows", method: http.MethodGet, path: "/api/sim/flows"},
		{name: "branches", method: http.MethodPost, path: "/api/sim/branches", body: []byte(`{"source_flow_id":"main"}`)},
		{name: "step_many", method: http.MethodPost, path: "/api/sim/flows/alt-1/step_many", body: []byte(`{"actions":[]}`)},
		{name: "observe", method: http.MethodGet, path: "/api/sim/flows/alt-1/observe"},
		{name: "trace", method: http.MethodGet, path: "/api/sim/flows/alt-1/trace"},
		{name: "delete", method: http.MethodDelete, path: "/api/sim/flows/alt-1"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp, body := mustRequest(t, dashboard.URL, tc.method, tc.path, tc.body)
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d body=%s", resp.StatusCode, string(body))
			}
			var env apiErrorEnvelope
			mustUnmarshal(t, body, &env)
			if !strings.Contains(strings.ToLower(env.Error), "sim") {
				t.Fatalf("unexpected error: %q", env.Error)
			}
		})
	}

	if calls != 0 {
		t.Fatalf("expected no upstream calls while feature-gated, got %d", calls)
	}
}

func TestSimFlows_Upstream4xxNoRetry(t *testing.T) {
	t.Parallel()

	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/flows" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		calls++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer upstream.Close()

	a := newTestApp(upstream)
	a.upstream.MaxAttempts = 3
	seedSimCapabilities(a, true, "", time.Now().Add(time.Minute))
	dashboard := newDashboardServer(a)
	defer dashboard.Close()

	resp, _ := mustRequest(t, dashboard.URL, http.MethodGet, "/api/sim/flows", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if calls != 1 {
		t.Fatalf("expected 1 upstream call for 4xx, got %d", calls)
	}
}

func TestSimFlows_Upstream5xxRetries(t *testing.T) {
	t.Parallel()

	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/flows" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		calls++
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer upstream.Close()

	a := newTestApp(upstream)
	a.upstream.MaxAttempts = 3
	a.upstream.BaseDelay = 1 * time.Millisecond
	a.upstream.MaxDelay = 2 * time.Millisecond
	seedSimCapabilities(a, true, "", time.Now().Add(time.Minute))
	dashboard := newDashboardServer(a)
	defer dashboard.Close()

	resp, _ := mustRequest(t, dashboard.URL, http.MethodGet, "/api/sim/flows", nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
	if calls != 3 {
		t.Fatalf("expected 3 upstream attempts, got %d", calls)
	}
}

func TestSimProxyMappingParity_CreateStepTraceDelete(t *testing.T) {
	t.Parallel()

	upstream := newUpstreamHitRecorder(t)
	defer upstream.Close()

	a := newTestApp(upstream)
	seedSimCapabilities(a, true, "", time.Now().Add(time.Minute))
	dashboard := newDashboardServer(a)
	defer dashboard.Close()

	cases := []struct {
		name         string
		method       string
		dashboardURL string
		body         []byte
		wantMethod   string
		wantPath     string
		wantQuery    string
	}{
		{
			name:         "create branch",
			method:       http.MethodPost,
			dashboardURL: "/api/sim/branches",
			body:         []byte(`{"source_flow_id":"main","rollback_steps":2}`),
			wantMethod:   http.MethodPost,
			wantPath:     "/v1/branches",
		},
		{
			name:         "step_many",
			method:       http.MethodPost,
			dashboardURL: "/api/sim/flows/alt-1/step_many",
			body:         []byte(`{"actions":[{"side_code":1,"units":2}]}`),
			wantMethod:   http.MethodPost,
			wantPath:     "/v1/flows/alt-1/step_many",
		},
		{
			name:         "observe",
			method:       http.MethodGet,
			dashboardURL: "/api/sim/flows/alt-1/observe?include_lineage=1",
			wantMethod:   http.MethodGet,
			wantPath:     "/v1/flows/alt-1/observe",
			wantQuery:    "include_lineage=1",
		},
		{
			name:         "trace",
			method:       http.MethodGet,
			dashboardURL: "/api/sim/flows/alt-1/trace?from=10&limit=4",
			wantMethod:   http.MethodGet,
			wantPath:     "/v1/flows/alt-1/trace",
			wantQuery:    "from=10&limit=4",
		},
		{
			name:         "delete",
			method:       http.MethodDelete,
			dashboardURL: "/api/sim/flows/alt-1",
			wantMethod:   http.MethodDelete,
			wantPath:     "/v1/flows/alt-1",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp, body := mustRequest(t, dashboard.URL, tc.method, tc.dashboardURL, tc.body)
			requireHTTPStatus(t, resp, body, http.StatusCreated)

			hit := unmarshalUpstreamHit(t, body)
			assertUpstreamHit(t, hit, tc.wantMethod, tc.wantPath, tc.wantQuery, tc.body)
		})
	}
}

type upstreamHit struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Query  string          `json:"query,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

func newUpstreamHitRecorder(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()

		hit := upstreamHit{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			hit.Body = json.RawMessage(raw)
		}
		writeJSON(w, http.StatusCreated, hit)
	}))
}

func requireHTTPStatus(t *testing.T, resp *http.Response, body []byte, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("expected %d, got %d body=%s", want, resp.StatusCode, string(body))
	}
}

func unmarshalUpstreamHit(t *testing.T, body []byte) upstreamHit {
	t.Helper()
	var hit upstreamHit
	mustUnmarshal(t, body, &hit)
	return hit
}

func assertUpstreamHit(t *testing.T, hit upstreamHit, wantMethod, wantPath, wantQuery string, wantBody []byte) {
	t.Helper()
	if hit.Method != wantMethod {
		t.Fatalf("want method %s, got %s", wantMethod, hit.Method)
	}
	if hit.Path != wantPath {
		t.Fatalf("want path %s, got %s", wantPath, hit.Path)
	}
	if hit.Query != wantQuery {
		t.Fatalf("want query %q, got %q", wantQuery, hit.Query)
	}
	if len(wantBody) == 0 {
		return
	}
	if string(bytes.TrimSpace(hit.Body)) != string(bytes.TrimSpace(wantBody)) {
		t.Fatalf("forwarded body mismatch\nwant=%s\ngot=%s", string(wantBody), string(hit.Body))
	}
}

func TestHandleState_SimCapabilityAvailabilityToggles(t *testing.T) {
	t.Parallel()

	t.Run("available", func(t *testing.T) {
		t.Parallel()

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/flows":
				writeRawJSON(w, http.StatusOK, []byte(`{"flows":[{"flow_id":"main"}]}`))
			case "/readyz":
				writeRawJSON(w, http.StatusOK, []byte(`{"status":"not_ready","engine_enabled":true,"engine_ready":false}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer upstream.Close()

		a := newTestApp(upstream)
		dashboard := newDashboardServer(a)
		defer dashboard.Close()

		resp, body := mustRequest(t, dashboard.URL, http.MethodGet, "/api/state", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(body))
		}

		var env stateEnvelope
		mustUnmarshal(t, body, &env)
		if !env.SimCapabilities.Available {
			t.Fatalf("expected available sim capabilities in state: %+v", env.SimCapabilities)
		}
	})

	t.Run("unsupported", func(t *testing.T) {
		t.Parallel()

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/flows":
				http.NotFound(w, r)
			case "/readyz":
				writeRawJSON(w, http.StatusOK, []byte(`{"status":"not_ready","engine_enabled":true,"engine_ready":false}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer upstream.Close()

		a := newTestApp(upstream)
		dashboard := newDashboardServer(a)
		defer dashboard.Close()

		resp, body := mustRequest(t, dashboard.URL, http.MethodGet, "/api/state", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(body))
		}

		var env stateEnvelope
		mustUnmarshal(t, body, &env)
		if env.SimCapabilities.Available {
			t.Fatalf("expected unavailable sim capabilities in state: %+v", env.SimCapabilities)
		}
	})
}

func parseRoutePattern(pattern string) (method string, path string, ok bool) {
	parts := strings.SplitN(pattern, " ", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func newTestApp(upstream *httptest.Server) *app {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &app{
		httpClient:   upstream.Client(),
		simBaseURL:   upstream.URL,
		logger:       logger,
		maxBodyBytes: 1_048_576,
		upstream: upstreamConfig{
			Timeout:      2 * time.Second,
			MaxAttempts:  3,
			BaseDelay:    1 * time.Millisecond,
			MaxDelay:     2 * time.Millisecond,
			MaxBodyBytes: 1_048_576,
		},
		simCapTTL: 30 * time.Second,
		metrics:   newMetricsCollector(),
	}
}

func seedSimCapabilities(a *app, available bool, reason string, expiry time.Time) {
	a.simCapMu.Lock()
	a.simCapStatus = simCapabilitiesStatus{
		Available: available,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Reason:    reason,
	}
	a.simCapExpiry = expiry
	a.simCapMu.Unlock()
}

func newDashboardServer(a *app) *httptest.Server {
	mux := http.NewServeMux()
	for _, route := range a.baseRouteBindings(1000) {
		if route.Pattern == "GET /" {
			continue
		}
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	for _, route := range a.swaggerRouteBindings() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	return httptest.NewServer(mux)
}

func mustRequest(t *testing.T, baseURL string, method string, path string, body []byte) (*http.Response, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, baseURL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp, bytes.TrimSpace(raw)
}

func mustUnmarshal(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal failed: %v body=%s", err, string(raw))
	}
}
