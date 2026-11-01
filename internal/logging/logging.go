// Package logging constructs structured loggers and safe logging values.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

// Secret is a value that is always redacted by slog.
type Secret string

// LogValue prevents secrets from reaching handlers.
func (Secret) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// EnvKeys returns sorted environment names; values must never be passed to logging.
func EnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// New constructs a logger with key redaction and API-key scrubbing.
// Callers must use EnvKeys for environments and Secret for other credentials.
func New(out io.Writer, format, level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("log.level: %w", err)
	}
	keys := regexp.MustCompile(`arc_live_[A-Za-z0-9]{32}`)
	opts := &slog.HandlerOptions{Level: l, ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		for _, group := range groups {
			if group == "env" || group == "environment" {
				return slog.String(a.Key, "[REDACTED]")
			}
		}
		switch strings.ToLower(a.Key) {
		case "api_key", "authorization", "token", "password", "secret", "env", "environment", "stdout", "stderr", "output", "file_contents":
			return slog.String(a.Key, "[REDACTED]")
		}
		if a.Value.Kind() == slog.KindString {
			a.Value = slog.StringValue(keys.ReplaceAllString(a.Value.String(), "[REDACTED]"))
		}
		// Arbitrary objects can contain secrets. Log only explicit scalar attributes.
		if a.Value.Kind() == slog.KindAny {
			a.Value = slog.StringValue("[REDACTED]")
		}
		return a
	}}
	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(out, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(out, opts)), nil
	default:
		return nil, fmt.Errorf("log.format: must be json or text")
	}
}
