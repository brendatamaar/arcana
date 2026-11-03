//go:build linux && (integration || escape)

package escape

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func docker(ctx context.Context, t *testing.T, args ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- trusted test harness argv, never interpreted by a shell.
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	output, err := command.Output()
	if err != nil {
		t.Fatalf("docker %v: %v: %s", args, err, diagnostic.String())
	}
	return strings.TrimSpace(string(output))
}

func requireRunsc(ctx context.Context, t *testing.T) {
	t.Helper()
	runtimes := docker(ctx, t, "info", "--format", "{{json .Runtimes}}")
	if !strings.Contains(runtimes, `"runsc"`) {
		if os.Getenv("ARCANA_REQUIRE_RUNSC") == "1" {
			t.Fatal("runsc is required in security CI")
		}
		t.Skip("runsc unavailable: escape tests cannot prove isolation with runc")
	}
}
