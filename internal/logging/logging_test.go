package logging

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestLogger_RedactsSecretsAndHonorsLevel(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			logger, err := New(&out, format, "info")
			if err != nil {
				t.Fatal(err)
			}
			logger.Debug("hidden debug message")
			logger.Info("key arc_live_0123456789abcdefghijklmnopqrstuv", "request_id", "req_visible", "api_key", "secret-value", "env", map[string]string{"NAME": "env-value"}, "credential", Secret("other-secret"), "error", fmt.Errorf("env-value"))
			logger.Info("group", slog.Group("env", slog.String("NAME", "group-secret")))
			text := out.String()
			for _, secret := range []string{"hidden debug message", "arc_live_", "secret-value", "env-value", "other-secret", "group-secret"} {
				if strings.Contains(text, secret) {
					t.Fatalf("leaked %q: %s", secret, text)
				}
			}
			if !strings.Contains(text, "req_visible") || !strings.Contains(text, "REDACTED") {
				t.Fatalf("missing context: %s", text)
			}
		})
	}
}

func TestLogger_RejectsInvalidOptions(t *testing.T) {
	for _, options := range [][2]string{{"html", "info"}, {"json", "invalid"}} {
		if _, err := New(&bytes.Buffer{}, options[0], options[1]); err == nil {
			t.Fatalf("accepted %v", options)
		}
	}
}

func TestEnvKeys_DiscardsValues(t *testing.T) {
	keys := EnvKeys(map[string]string{"Z": "secret", "A": "secret"})
	if strings.Join(keys, ",") != "A,Z" {
		t.Fatalf("keys: %v", keys)
	}
}

// The level is not a secret: operators filter on it. It is a slog.Level, which is an "any" value, so
// the blanket redaction of arbitrary objects must not swallow it.
func TestLogger_KeepsTheBuiltInLevelVisible(t *testing.T) {
	for format, want := range map[string]string{"json": `"level":"WARN"`, "text": "level=WARN"} {
		var out bytes.Buffer
		logger, err := New(&out, format, "info")
		if err != nil {
			t.Fatal(err)
		}
		logger.Warn("careful", "request_id", "req_x")
		logger.Error("failed", "object", struct{ Secret string }{"s3cret"})
		text := out.String()
		if !strings.Contains(text, want) || !strings.Contains(text, "ERROR") {
			t.Fatalf("%s: the level was redacted: %s", format, text)
		}
		if strings.Contains(text, "s3cret") {
			t.Fatalf("%s: an arbitrary object leaked: %s", format, text)
		}
	}
}
