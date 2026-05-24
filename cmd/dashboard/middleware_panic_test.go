package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLoggingMiddleware_PanicIsObservedNotSilenced verifies the post-fix
// contract: when a downstream handler panics, the logging middleware must
//  1. Not re-panic (the old behaviour short-circuited the deferred log/metric
//     observation and dropped the request from observability).
//  2. Log the request at level Error with status=500 and a `panic` attr.
//  3. Record a metric for the request with status 500.
//
// The standard net/http server still recovers panics at the connection layer
// and prevents the test process from dying; we simulate that by invoking the
// middleware via ServeHTTP and asserting on the captured log output.
func TestLoggingMiddlewarePanicIsObservedNotSilenced(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	metrics := newMetricsCollector()

	h := loggingMiddleware(logger, metrics, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("synthetic handler panic for middleware test")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/explode", nil)
	rr := httptest.NewRecorder()

	// Recover any re-panicked panic at the test boundary so a regression to
	// the old behaviour produces a clean test failure rather than crashing
	// the test binary.
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("loggingMiddleware re-panicked (%v); should swallow + log", rec)
		}
	}()

	h.ServeHTTP(rr, req)

	out := buf.String()
	for _, want := range []string{
		`"msg":"http request panicked"`,
		`"path":"/api/explode"`,
		`"status":500`,
		`"panic":"synthetic handler panic for middleware test"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in log output:\n%s", want, out)
		}
	}
	// And the success-path log message must NOT be emitted, since this was a
	// panic.
	if strings.Contains(out, `"http request completed"`) {
		t.Errorf("unexpected 'http request completed' log on panicking handler:\n%s", out)
	}
}

// TestLoggingMiddleware_HappyPathStillLogsCompleted is a regression guard so
// the panic-handling branch doesn't accidentally suppress the normal "completed"
// log for non-panicking requests.
func TestLoggingMiddlewareHappyPathStillLogsCompleted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := loggingMiddleware(logger, newMetricsCollector(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/ok", nil))

	out := buf.String()
	if !strings.Contains(out, `"http request completed"`) {
		t.Errorf("non-panicking request missing 'completed' log:\n%s", out)
	}
	if strings.Contains(out, `"http request panicked"`) {
		t.Errorf("non-panicking request wrongly logged 'panicked':\n%s", out)
	}
}
