// Package doctor diagnoses host prerequisites before API admission.
package doctor

import (
	"context"
	"fmt"
	"time"

	"github.com/brendatamaar/arcana/internal/config"
)

// Probe supplies one independently testable host observation.
type (
	Probe interface {
		Check(context.Context, string) (string, error)
	}
	// Result is one diagnostic with its remediation hint and startup severity.
	Result struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
		Hint   string `json:"hint,omitempty"`
	}
	// Report contains every check, including checks performed after another fails.
	Report struct {
		Checks []Result `json:"checks"`
	}
)

// Failed reports whether startup must be refused.
func (r Report) Failed() bool {
	for _, check := range r.Checks {
		if check.Status == "fail" {
			return true
		}
	}
	return false
}

// Run evaluates host probes without skipping independent checks after failures.
func Run(ctx context.Context, cfg config.Config, probe Probe) Report {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	checks := []struct {
		name, hint string
		blocking   bool
	}{
		{"os", "Run Arcana on a dedicated Linux host or Linux VM.", true},
		{"kernel", "Use a Linux kernel supporting child subreapers and pidfd signalling (Linux >= 5.3 for runc), or upgrade runsc.", cfg.Runtime.Docker.OCIRuntime == "runc"},
		{"cgroups", "Mount and enable cgroups v2; configure Docker to use it.", true},
		{"docker", "Install Docker Engine >= 24 and grant access to the configured Docker socket.", true},
		{"data_dir", "Create data_dir with private permissions and grant the Arcana user write access.", true},
		{"database", "Check data_dir permissions and back up/repair the SQLite database; migrations must complete.", true},
		{"kvm", "Optional: enable virtualization and expose /dev/kvm for future Firecracker support.", false},
		{"runsc", "Install gVisor from its apt repository, run sudo runsc install, then restart Docker. Build at least one configured template image first.", cfg.Runtime.Docker.OCIRuntime == "runsc"},
	}
	report := Report{Checks: make([]Result, 0, len(checks)+min(len(cfg.Templates), 1000)+2)}
	for _, check := range checks {
		detail, err := probe.Check(ctx, check.name)
		status := "pass"
		hint := ""
		if err != nil {
			status = "warn"
			if check.blocking {
				status = "fail"
			}
			detail = err.Error()
			hint = check.hint
		}
		report.Checks = append(report.Checks, Result{check.name, status, detail, hint})
	}
	if cfg.Runtime.Docker.OCIRuntime == "runc" {
		status := "fail"
		detail := "runc requires explicit allow_insecure_runtime opt-in"
		if cfg.Runtime.Docker.AllowInsecureRuntime {
			status = "warn"
			detail = "INSECURE RUNTIME: runc shares the host kernel; explicitly opted in"
		}
		report.Checks = append(report.Checks, Result{"runtime_policy", status, detail, "Prefer runsc for untrusted code."})
		detail, err := probe.Check(ctx, "runc")
		status = "pass"
		hint := ""
		if err != nil {
			status = "fail"
			detail = err.Error()
			hint = "Check the explicitly opted-in runc runtime and configured template images."
		}
		report.Checks = append(report.Checks, Result{"runc", status, detail, hint})
	}
	if len(cfg.Templates) > 1000 {
		report.Checks = append(report.Checks, Result{"templates", "fail", "template check limit exceeded", "Configure at most 1000 templates."})
		return report
	}
	for _, template := range cfg.Templates {
		name := "image:" + template.Image
		detail, err := probe.Check(ctx, name)
		status := "pass"
		hint := ""
		if err != nil {
			status = "warn"
			detail = err.Error()
			hint = fmt.Sprintf("Build or pull configured image %s; for base: docker build -t arcana-base:local templates/base", template.Image)
		}
		report.Checks = append(report.Checks, Result{name, status, detail, hint})
	}
	return report
}
