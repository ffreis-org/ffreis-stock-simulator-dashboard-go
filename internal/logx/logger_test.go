package logx

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewWithEnv_DefaultTextInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := newWithEnv("stock-dashboard", emptyEnv, &buf)

	logger.Info("hello", "component", "test")
	logger.Debug("debug-hidden")

	out := buf.String()
	if !strings.Contains(out, "level=INFO") {
		t.Fatalf("expected text INFO level log, got: %s", out)
	}
	if !strings.Contains(out, "service=stock-dashboard") {
		t.Fatalf("expected service field in output, got: %s", out)
	}
	if strings.Contains(out, "debug-hidden") {
		t.Fatalf("unexpected debug log at default info level: %s", out)
	}
}

func TestNewWithEnv_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	env := mapEnv(map[string]string{
		"LOG_FORMAT": "json",
		"LOG_LEVEL":  "debug",
	})
	logger := newWithEnv("stock-dashboard", env, &buf)
	logger.Debug("json-debug")

	out := buf.String()
	if !strings.Contains(out, "\"level\":\"DEBUG\"") {
		t.Fatalf("expected JSON debug level log, got: %s", out)
	}
	if !strings.Contains(out, "\"service\":\"stock-dashboard\"") {
		t.Fatalf("expected JSON service field, got: %s", out)
	}
}

func TestNewWithEnv_InvalidValuesWarnAndFallback(t *testing.T) {
	var buf bytes.Buffer
	env := mapEnv(map[string]string{
		"LOG_LEVEL":  "verbose",
		"LOG_FORMAT": "yaml",
		"LOG_SOURCE": "sometimes",
	})
	logger := newWithEnv("stock-dashboard", env, &buf)
	logger.Info("after-invalid")

	out := buf.String()
	for _, key := range []string{"LOG_LEVEL", "LOG_FORMAT", "LOG_SOURCE"} {
		if !strings.Contains(out, key) {
			t.Fatalf("expected warning for %s, got: %s", key, out)
		}
	}
	if !strings.Contains(out, "after-invalid") {
		t.Fatalf("expected info log after fallback, got: %s", out)
	}
}

func emptyEnv(string) string {
	return ""
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
