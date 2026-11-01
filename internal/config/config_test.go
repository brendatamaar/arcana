package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		c, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if c.Runtime.Docker.OCIRuntime != "runsc" || c.Network.DefaultMode != "none" || c.Runtime.Docker.AllowInsecureRuntime {
			t.Fatal("unsafe defaults")
		}
	})
	t.Run("file and environment precedence", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(file, []byte("server:\n  listen: 127.0.0.1:8080\nlimits:\n  defaults:\n    cpus: 2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ARCANA_SERVER__LISTEN", "127.0.0.1:8081")
		t.Setenv("ARCANA_LIMITS__FILES__ALLOWED_ROOTS", "[/workspace]")
		c, err := Load(file)
		if err != nil {
			t.Fatal(err)
		}
		if c.Server.Listen != "127.0.0.1:8081" || c.Limits.Defaults.CPUs != 2 || len(c.Limits.Files.AllowedRoots) != 1 {
			t.Fatalf("overrides not applied: %+v", c)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, err := Load(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("accepted missing file")
		}
	})
	t.Run("bad yaml", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "bad")
		if err := os.WriteFile(file, []byte("[:"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(file); err == nil {
			t.Fatal("accepted bad yaml")
		}
	})
	t.Run("bad nested env", func(t *testing.T) {
		t.Setenv("ARCANA_LIMITS__EXEC__MAX_OUTPUT_BYTES", "0")
		if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "limits.exec.max_output_bytes") {
			t.Fatalf("error: %v", err)
		}
	})
	t.Run("example", func(t *testing.T) {
		if _, err := Load("../../deploy/config.example.yaml"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name, field string
		change      func(*Config)
	}{
		{"port", "server.listen", func(c *Config) { c.Server.Listen = "localhost:99999" }},
		{"metrics", "server.metrics_listen", func(c *Config) { c.Server.MetricsListen = "bad" }},
		{"shutdown", "server.shutdown_timeout_seconds", func(c *Config) { c.Server.ShutdownTimeoutSeconds = 0 }},
		{"data", "data_dir", func(c *Config) { c.DataDir = "relative" }},
		{"driver", "runtime.driver", func(c *Config) { c.Runtime.Driver = "unknown" }},
		{"host", "runtime.docker.host", func(c *Config) { c.Runtime.Docker.Host = "" }},
		{"runtime", "runtime.docker.oci_runtime", func(c *Config) { c.Runtime.Docker.OCIRuntime = "unknown" }},
		{"runc guard", "runtime.docker.allow_insecure_runtime", func(c *Config) { c.Runtime.Docker.OCIRuntime = "runc" }},
		{"quota", "limits.max_sandboxes_per_key", func(c *Config) { c.Limits.MaxSandboxesPerKey = 21 }},
		{"ttl", "limits.defaults.ttl_seconds", func(c *Config) { c.Limits.Defaults.TTLSeconds = 86401 }},
		{"cpu", "limits.defaults.cpus", func(c *Config) { c.Limits.Defaults.CPUs = 5 }},
		{"memory", "limits.defaults.memory_mb", func(c *Config) { c.Limits.Defaults.MemoryMB = 8193 }},
		{"disk", "limits.defaults.disk_mb", func(c *Config) { c.Limits.Defaults.DiskMB = 20481 }},
		{"pids", "limits.defaults.pids", func(c *Config) { c.Limits.Defaults.PIDs = 4097 }},
		{"timeout", "limits.exec.default_timeout_seconds", func(c *Config) { c.Limits.Exec.DefaultTimeoutSeconds = 3601 }},
		{"size", "limits.files.max_file_bytes", func(c *Config) { c.Limits.Files.MaxFileBytes = 524288001 }},
		{"roots empty", "limits.files.allowed_roots", func(c *Config) { c.Limits.Files.AllowedRoots = nil }},
		{"roots relative", "limits.files.allowed_roots", func(c *Config) { c.Limits.Files.AllowedRoots = []string{"relative"} }},
		{"roots unclean", "limits.files.allowed_roots", func(c *Config) { c.Limits.Files.AllowedRoots = []string{"/workspace/.."} }},
		{"backend", "workspace.backend", func(c *Config) { c.Workspace.Backend = "unknown" }},
		{"network", "network.default_mode", func(c *Config) { c.Network.DefaultMode = "open" }},
		{"cidr", "network.subnet_pool", func(c *Config) { c.Network.SubnetPool = "invalid" }},
		{"prefix", "network.subnet_prefix", func(c *Config) { c.Network.SubnetPrefix = 33 }},
		{"templates", "templates", func(c *Config) { c.Templates = nil }},
		{"image", "templates.0.image", func(c *Config) { c.Templates[0].Image = "" }},
		{"name", "templates.0.name", func(c *Config) { c.Templates[0].Name = "" }},
		{"pool", "templates.0.warm_pool", func(c *Config) { c.Templates[0].WarmPool = 1 }},
		{"template resources", "templates.0.resources.cpus", func(c *Config) { c.Templates[0].Resources.CPUs = -1 }},
		{"template ttl", "templates.0.ttl_seconds", func(c *Config) { c.Templates[0].TTLSeconds = -1 }},
		{"template workdir", "templates.0.workdir", func(c *Config) { c.Templates[0].Workdir = "relative" }},
		{"log level", "log.level", func(c *Config) { c.Log.Level = "bad" }},
		{"log format", "log.format", func(c *Config) { c.Log.Format = "bad" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Default()
			tt.change(&c)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("want %s error, got %v", tt.field, err)
			}
		})
	}
	t.Run("explicit runc opt in", func(t *testing.T) {
		c := Default()
		c.Runtime.Docker.OCIRuntime = "runc"
		c.Runtime.Docker.AllowInsecureRuntime = true
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}
