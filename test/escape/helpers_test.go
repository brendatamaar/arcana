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

// escapeRuntime is the OCI runtime under test: runsc, unless ARCANA_ESCAPE_RUNTIME=runc
// selects the runtime-comparison mode of docs/runtime-comparison.md. A runc run exists to
// show which attacks hardening alone contains; it proves nothing about isolation, and
// security CI (ARCANA_REQUIRE_RUNSC=1) refuses it.
func escapeRuntime() string {
	if os.Getenv("ARCANA_ESCAPE_RUNTIME") == "runc" {
		return "runc"
	}
	return "runsc"
}

func requireRunsc(ctx context.Context, t *testing.T) {
	t.Helper()
	runtimes := docker(ctx, t, "info", "--format", "{{json .Runtimes}}")
	if escapeRuntime() == "runc" {
		if os.Getenv("ARCANA_REQUIRE_RUNSC") == "1" {
			t.Fatal("security CI must run the escape suite on runsc, not the runc comparison mode")
		}
		if !strings.Contains(runtimes, `"runc"`) {
			t.Skip("runc is not registered with Docker")
		}
		t.Log("COMPARISON RUN on runc: this result does not demonstrate isolation")
		return
	}
	if !strings.Contains(runtimes, `"runsc"`) {
		if os.Getenv("ARCANA_REQUIRE_RUNSC") == "1" {
			t.Fatal("runsc is required in security CI")
		}
		t.Skip("runsc unavailable: escape tests cannot prove isolation with runc")
	}
}
