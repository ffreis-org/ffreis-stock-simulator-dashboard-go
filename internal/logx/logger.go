package logx

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

func New(service string) *slog.Logger {
	return newWithEnv(service, os.Getenv, os.Stderr)
}

func newWithEnv(service string, getenv func(string) string, w io.Writer) *slog.Logger {
	format, formatWarn := parseFormat(getenv("LOG_FORMAT"))
	level, levelWarn := parseLevel(getenv("LOG_LEVEL"))
	addSource, sourceWarn := parseSource(getenv("LOG_SOURCE"))

	handlerOpts := &slog.HandlerOptions{
		Level:     level,
		AddSource: addSource,
	}

	var handler slog.Handler
	switch format {
	case "json":
		handler = slog.NewJSONHandler(w, handlerOpts)
	default:
		handler = slog.NewTextHandler(w, handlerOpts)
	}

	logger := slog.New(handler).With("service", service)

	if levelWarn != nil {
		logger.Warn(
			"invalid log env; using default",
			"key", "LOG_LEVEL",
			"value", strings.TrimSpace(getenv("LOG_LEVEL")),
			"default", "info",
		)
	}

	if formatWarn != nil {
		logger.Warn(
			"invalid log env; using default",
			"key", "LOG_FORMAT",
			"value", strings.TrimSpace(getenv("LOG_FORMAT")),
			"default", "text",
		)
	}

	if sourceWarn != nil {
		logger.Warn(
			"invalid log env; using default",
			"key", "LOG_SOURCE",
			"value", strings.TrimSpace(getenv("LOG_SOURCE")),
			"default", "false",
		)
	}

	return logger
}

func parseFormat(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return "text", nil
	}
	if v == "text" || v == "json" {
		return v, nil
	}
	return "text", errInvalidValue
}

func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, errInvalidValue
	}
}

func parseSource(raw string) (bool, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return false, errInvalidValue
	}
	return parsed, nil
}

var errInvalidValue = &invalidValueError{}

type invalidValueError struct{}

func (e *invalidValueError) Error() string {
	return "invalid value"
}
