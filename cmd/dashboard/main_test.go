package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoggingMiddleware_LogsRequestFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	metrics := newMetricsCollector()
	h := loggingMiddleware(logger, metrics, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/state", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("User-Agent", "middleware-test")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	out := buf.String()
	for _, expected := range []string{
		`"msg":"http request completed"`,
		`"method":"POST"`,
		`"path":"/api/state"`,
		`"status":201`,
		`"remote_addr":"127.0.0.1:12345"`,
		`"user_agent":"middleware-test"`,
		`"request_id":""`,
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("missing %s in log output: %s", expected, out)
		}
	}
}

func TestLoggingMiddleware_LogsDefaultStatus200(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	h := loggingMiddleware(logger, newMetricsCollector(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	out := buf.String()
	if !strings.Contains(out, `"status":200`) {
		t.Fatalf("expected default status=200 in log output, got: %s", out)
	}
}

func TestRequestIDMiddleware_GeneratesAndPropagates(t *testing.T) {
	var got string
	h := requestIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = requestIDFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	headerValue := rr.Header().Get("X-Request-ID")
	if headerValue == "" {
		t.Fatal("expected X-Request-ID header to be set")
	}
	if got == "" || got != headerValue {
		t.Fatalf("expected request id in context and header to match, got ctx=%q header=%q", got, headerValue)
	}
}

func TestRequestIDMiddleware_PreservesIncoming(t *testing.T) {
	const incoming = "incoming-id-123"
	h := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = w
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", incoming)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Header().Get("X-Request-ID") != incoming {
		t.Fatalf("expected incoming request id to be preserved")
	}
}

func TestRecoveryMiddleware_APIPathReturnsJSON500(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	h := recoveryMiddleware(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil).WithContext(context.WithValue(context.Background(), ctxKeyRequestID, "req-1"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	var env apiErrorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("expected JSON error envelope, got err: %v body: %s", err, rr.Body.String())
	}
	if env.Error == "" || env.RequestID != "req-1" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

func TestSecurityHeadersMiddleware_SetsHeaders(t *testing.T) {
	h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	for _, header := range []string{"X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Content-Security-Policy"} {
		if rr.Header().Get(header) == "" {
			t.Fatalf("expected %s header to be set", header)
		}
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected Cache-Control no-store for API routes")
	}
}

func TestHandleReset_RejectsOversizedBody(t *testing.T) {
	a := &app{maxBodyBytes: 4}
	req := httptest.NewRequest(http.MethodPost, "/api/reset", io.NopCloser(strings.NewReader(`{"seed":123456}`)))
	rr := httptest.NewRecorder()
	a.handleReset(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rr.Code)
	}
}

func TestDoUpstreamRequest_RetriesOnServerErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("retry"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	a := &app{
		httpClient: &http.Client{},
		simBaseURL: srv.URL,
		upstream: upstreamConfig{
			Timeout:     2 * time.Second,
			MaxAttempts: 3,
			BaseDelay:   1 * time.Millisecond,
			MaxDelay:    5 * time.Millisecond,
		},
		metrics: newMetricsCollector(),
	}

	code, body, err := a.forwardNoBody(context.Background(), http.MethodGet, "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != http.StatusOK || string(body) != "ok" {
		t.Fatalf("unexpected response: status=%d body=%q", code, string(body))
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

func TestDoUpstreamRequest_ContextDeadlineStopsRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("retry"))
	}))
	defer srv.Close()

	a := &app{
		httpClient: &http.Client{},
		simBaseURL: srv.URL,
		upstream: upstreamConfig{
			Timeout:     2 * time.Second,
			MaxAttempts: 10,
			BaseDelay:   50 * time.Millisecond,
			MaxDelay:    50 * time.Millisecond,
		},
		metrics: newMetricsCollector(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err := a.forwardNoBody(ctx, http.MethodGet, "/")
	if err == nil && ctx.Err() == nil {
		t.Fatal("expected context deadline or retry termination error")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", ctx.Err())
	}
}

func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusOK, map[string]interface{}{"key": "value"})

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if data["key"] != "value" {
		t.Errorf("expected key=value, got %v", data)
	}
}

func TestReadJSONPayload_ValidJSON(t *testing.T) {
	body := bytes.NewReader([]byte(`{"name":"test"}`))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rr := httptest.NewRecorder()

	payload, ok := readJSONPayload(rr, req, 1024, "invalid")
	if !ok {
		t.Fatalf("readJSONPayload failed, status: %d", rr.Code)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if data["name"] != "test" {
		t.Errorf("expected name=test, got %v", data)
	}
}

func TestReadJSONPayload_InvalidJSON(t *testing.T) {
	body := bytes.NewReader([]byte(`{invalid}`))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rr := httptest.NewRecorder()

	_, ok := readJSONPayload(rr, req, 1024, "parse error")
	if ok {
		t.Fatal("expected readJSONPayload to fail on invalid JSON")
	}
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestWriteRawJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	data := []byte(`{"key":"value"}`)
	writeRawJSON(rr, http.StatusOK, data)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}
	if rr.Body.String() != string(data) {
		t.Errorf("expected body %s, got %s", data, rr.Body.String())
	}
}

func TestRegisterPprofRoutes(t *testing.T) {
	mux := http.NewServeMux()
	registerPprofRoutes(mux)

	testPaths := []string{
		"/debug/pprof/",
		"/debug/pprof/heap",
		"/debug/pprof/goroutine",
	}

	for _, path := range testPaths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code == http.StatusNotFound {
			t.Errorf("pprof route %s not registered", path)
		}
	}
}
