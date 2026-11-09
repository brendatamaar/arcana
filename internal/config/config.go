// Package config loads and validates Arcana configuration.
package config

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"

	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/pathutil"
)

// DefaultPath is the operator configuration path.
const DefaultPath = "/etc/arcana/config.yaml"

// Config contains the configuration supported in v0.1.
type Config struct {
	Server    Server     `koanf:"server"`
	DataDir   string     `koanf:"data_dir"`
	Runtime   Runtime    `koanf:"runtime"`
	Limits    Limits     `koanf:"limits"`
	Workspace Workspace  `koanf:"workspace"`
	Network   Network    `koanf:"network"`
	Templates []Template `koanf:"templates"`
	Audit     Audit      `koanf:"audit"`
	Log       Log        `koanf:"log"`
}

// Server configures listeners and shutdown.
type Server struct {
	Listen                 string `koanf:"listen"`
	MetricsListen          string `koanf:"metrics_listen"`
	ShutdownTimeoutSeconds int    `koanf:"shutdown_timeout_seconds"`
	DestroyOnShutdown      bool   `koanf:"destroy_on_shutdown"`
}

// Runtime selects the isolation driver.
type Runtime struct {
	Driver string `koanf:"driver"`
	Docker Docker `koanf:"docker"`
}

// Docker configures the Docker connection and OCI runtime.
type Docker struct {
	Host                 string `koanf:"host"`
	OCIRuntime           string `koanf:"oci_runtime"`
	AllowInsecureRuntime bool   `koanf:"allow_insecure_runtime"`
}

// Resources defines per-sandbox limits.
type Resources struct {
	CPUs     float64 `koanf:"cpus"`
	MemoryMB int64   `koanf:"memory_mb"`
	DiskMB   int64   `koanf:"disk_mb"`
	PIDs     int64   `koanf:"pids"`
}

// Defaults adds lifetime defaults to resources.
type Defaults struct {
	Resources          `koanf:",squash"`
	TTLSeconds         int `koanf:"ttl_seconds"`
	IdleTimeoutSeconds int `koanf:"idle_timeout_seconds"`
}

// Limits bounds sandbox creation, execution and file transfer.
type Limits struct {
	MaxSandboxes       int       `koanf:"max_sandboxes"`
	MaxSandboxesPerKey int       `koanf:"max_sandboxes_per_key"`
	MaxTTLSeconds      int       `koanf:"max_ttl_seconds"`
	Defaults           Defaults  `koanf:"defaults"`
	Maximums           Resources `koanf:"maximums"`
	Exec               Exec      `koanf:"exec"`
	Files              Files     `koanf:"files"`
}

// Exec bounds command execution.
type Exec struct {
	DefaultTimeoutSeconds   int   `koanf:"default_timeout_seconds"`
	MaxTimeoutSeconds       int   `koanf:"max_timeout_seconds"`
	MaxOutputBytes          int64 `koanf:"max_output_bytes"`
	MaxConcurrentPerSandbox int   `koanf:"max_concurrent_per_sandbox"`
}

// Files bounds file transfer and allowed paths.
type Files struct {
	MaxFileBytes    int64    `koanf:"max_file_bytes"`
	MaxArchiveBytes int64    `koanf:"max_archive_bytes"`
	AllowedRoots    []string `koanf:"allowed_roots"`
}

// Workspace selects disk quota enforcement.
type Workspace struct {
	Backend              string `koanf:"backend"`
	CheckIntervalSeconds int    `koanf:"check_interval_seconds"`
}

// Network contains the supported policy and future subnet allocation settings.
type Network struct {
	MaxAllowEntries int                 `koanf:"max_allow_entries"`
	ProxyImage      string              `koanf:"proxy_image"`
	ProxyDNS        []string            `koanf:"proxy_dns"`
	Presets         map[string][]string `koanf:"presets"`
	DefaultMode     string              `koanf:"default_mode"`
	SubnetPool      string              `koanf:"subnet_pool"`
	SubnetPrefix    int                 `koanf:"subnet_prefix"`
}

// Template maps a name to an image and optional sandbox defaults.
type Template struct {
	NetworkPreset string            `koanf:"network_preset"`
	Name          string            `koanf:"name"`
	Image         string            `koanf:"image"`
	WarmPool      int               `koanf:"warm_pool"`
	Resources     Resources         `koanf:"resources"`
	TTLSeconds    int               `koanf:"ttl_seconds"`
	Env           map[string]string `koanf:"env"`
	Workdir       string            `koanf:"workdir"`
}

// Audit configures retention of metadata-only events.
type Audit struct {
	RetentionDays int `koanf:"retention_days"`
}

// Log selects structured logging output.
type Log struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

// Default returns an independent safe configuration.
func Default() Config {
	return Config{
		Server:    Server{"127.0.0.1:7070", "127.0.0.1:9090", 30, false},
		DataDir:   "/var/lib/arcana",
		Runtime:   Runtime{"docker", Docker{"unix:///var/run/docker.sock", "runsc", false}},
		Limits:    Limits{20, 10, 86400, Defaults{Resources{1, 1024, 2048, 256}, 1800, 600}, Resources{4, 8192, 20480, 4096}, Exec{120, 3600, 10485760, 8}, Files{52428800, 524288000, []string{"/workspace", "/tmp"}}},
		Workspace: Workspace{"volume", 15},
		Network:   Network{DefaultMode: "none", SubnetPool: "10.200.0.0/16", SubnetPrefix: 28, MaxAllowEntries: 50, ProxyImage: "ghcr.io/brendatamaar/arcana:latest", ProxyDNS: []string{"1.1.1.1", "1.0.0.1"}, Presets: map[string][]string{"python": {"pypi.org", "files.pythonhosted.org"}, "node": {"registry.npmjs.org"}, "go": {"proxy.golang.org", "sum.golang.org", "storage.googleapis.com"}}},
		Templates: []Template{{Name: "base", Image: "arcana-base:local"}},
		Audit:     Audit{30},
		Log:       Log{"info", "json"},
	}
}

// Load applies a YAML file, then ARCANA_ environment overrides to defaults.
// An empty filename loads defaults and environment only; explicit missing files fail.
// Scalar values use their native types. Lists and maps use YAML syntax in overrides.
func Load(filename string) (Config, error) {
	k := koanf.New(".")
	if filename != "" {
		// The operator selects this path with --config; it is never sandbox input.
		b, err := os.ReadFile(filename) // #nosec G304 -- operator-controlled config path
		if err != nil {
			return Config{}, fmt.Errorf("reading config: %w", err)
		}
		if err = k.Load(rawbytes.Provider(b), yaml.Parser()); err != nil {
			return Config{}, fmt.Errorf("parsing config: %w", err)
		}
	}
	// Environment is parsed with YAML so nested scalar, list and map values retain types.
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(name, "ARCANA_") {
			continue
		}
		key := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(name, "ARCANA_"), "__", "."))
		parsed, err := yaml.Parser().Unmarshal([]byte("value: " + value))
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid YAML value", key)
		}
		if err = k.Set(key, parsed["value"]); err != nil {
			return Config{}, fmt.Errorf("setting %s: %w", key, err)
		}
	}
	c := Default()
	if err := k.Unmarshal("", &c); err != nil {
		return Config{}, fmt.Errorf("decoding config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate rejects unsupported settings and unsafe or inconsistent limits.
func (c Config) Validate() error {
	var problems []error
	bad := func(field, message string) { problems = append(problems, fmt.Errorf("%s: %s", field, message)) }
	positiveFields(reflect.ValueOf(c), "", bad)
	for field, address := range map[string]string{"server.listen": c.Server.Listen, "server.metrics_listen": c.Server.MetricsListen} {
		_, port, err := net.SplitHostPort(address)
		n, e := strconv.Atoi(port)
		if err != nil || e != nil || n < 1 || n > 65535 {
			bad(field, "must be host:port with port 1..65535")
		}
	}
	if !path.IsAbs(c.DataDir) && !filepath.IsAbs(c.DataDir) {
		bad("data_dir", "must be absolute")
	}
	if c.Runtime.Driver != "docker" {
		bad("runtime.driver", "must be docker")
	}
	if c.Runtime.Docker.Host == "" {
		bad("runtime.docker.host", "must not be empty")
	}
	switch c.Runtime.Docker.OCIRuntime {
	case "runsc":
	case "runc":
		if !c.Runtime.Docker.AllowInsecureRuntime {
			bad("runtime.docker.allow_insecure_runtime", "must be true for runc")
		}
	default:
		bad("runtime.docker.oci_runtime", "must be runsc or runc")
	}
	if c.Limits.MaxSandboxesPerKey > c.Limits.MaxSandboxes {
		bad("limits.max_sandboxes_per_key", "exceeds limits.max_sandboxes")
	}
	compareResources(c.Limits.Defaults.Resources, c.Limits.Maximums, "limits.defaults", bad)
	if c.Limits.Defaults.TTLSeconds > c.Limits.MaxTTLSeconds {
		bad("limits.defaults.ttl_seconds", "exceeds limits.max_ttl_seconds")
	}
	if c.Limits.Exec.MaxTimeoutSeconds > protocol.MaxTimeoutSeconds {
		bad("limits.exec.max_timeout_seconds", "must not exceed the helper hard ceiling of 3600")
	}
	if c.Limits.Exec.DefaultTimeoutSeconds > c.Limits.Exec.MaxTimeoutSeconds {
		bad("limits.exec.default_timeout_seconds", "exceeds limits.exec.max_timeout_seconds")
	}
	if c.Limits.Files.MaxFileBytes > c.Limits.Files.MaxArchiveBytes {
		bad("limits.files.max_file_bytes", "exceeds limits.files.max_archive_bytes")
	}
	if len(c.Limits.Files.AllowedRoots) == 0 || len(c.Limits.Files.AllowedRoots) > pathutil.MaxRoots {
		bad("limits.files.allowed_roots", "must contain 1..16 roots")
	}
	if c.Limits.Files.MaxArchiveBytes > math.MaxInt64-4096 {
		bad("limits.files.max_archive_bytes", "must allow one overflow-detection byte")
	}
	for _, root := range c.Limits.Files.AllowedRoots {
		if !path.IsAbs(root) || strings.ContainsRune(root, 0) || path.Clean(root) != root || len(root) > 4096 {
			bad("limits.files.allowed_roots", "must contain clean absolute paths")
		}
	}
	if c.Workspace.Backend != "volume" && c.Workspace.Backend != "tmpfs" {
		bad("workspace.backend", "must be volume or tmpfs")
	}
	if c.Network.DefaultMode != "none" && c.Network.DefaultMode != "allowlist" {
		bad("network.default_mode", "must be none or allowlist")
	}
	prefix, err := netip.ParsePrefix(c.Network.SubnetPool)
	if err != nil {
		bad("network.subnet_pool", "must be a CIDR")
	} else if prefix != prefix.Masked() || c.Network.SubnetPrefix < prefix.Bits() || c.Network.SubnetPrefix > 29 || !prefix.Addr().Is4() || c.Network.SubnetPrefix-prefix.Bits() > 16 {
		bad("network.subnet_prefix", "must fit the canonical subnet pool")
	}
	if c.Network.MaxAllowEntries < 1 || c.Network.MaxAllowEntries > 50 {
		bad("network.max_allow_entries", "must be 1..50")
	}
	if c.Network.ProxyImage == "" {
		bad("network.proxy_image", "must not be empty")
	}
	if _, err := netpolicy.DNSResolver(c.Network.ProxyDNS); err != nil {
		bad("network.proxy_dns", "must contain 1..4 DNS IPs")
	}
	if len(c.Network.Presets) > 50 {
		bad("network.presets", "at most 50 presets")
	}
	for _, hosts := range c.Network.Presets {
		if len(hosts) > c.Network.MaxAllowEntries {
			bad("network.presets", "too many entries")
		}
		for _, host := range hosts {
			if _, err := netpolicy.Host(host); err != nil {
				bad("network.presets", "invalid hostname")
			}
		}
	}
	if len(c.Templates) > 1000 {
		bad("templates", "must contain at most 1000 entries")
	}
	if len(c.Templates) == 0 {
		bad("templates", "must not be empty")
	}
	poolTarget := 0
	names := map[string]bool{}
	for i, t := range c.Templates {
		field := fmt.Sprintf("templates.%d", i)
		if t.Name == "" || names[t.Name] {
			bad(field+".name", "must be nonempty and unique")
		}
		names[t.Name] = true
		if t.Image == "" {
			bad(field+".image", "must not be empty")
		}
		if t.NetworkPreset != "" {
			if _, ok := c.Network.Presets[t.NetworkPreset]; !ok {
				bad(field+".network_preset", "unknown network preset")
			}
		}
		poolTarget += t.WarmPool
		if t.WarmPool < 0 || t.WarmPool > c.Limits.MaxSandboxes {
			bad(field+".warm_pool", "must be within 0..limits.max_sandboxes")
		}
		if t.TTLSeconds < 0 || t.TTLSeconds > c.Limits.MaxTTLSeconds {
			bad(field+".ttl_seconds", "must be zero or within limits.max_ttl_seconds")
		}
		compareResources(t.Resources, c.Limits.Maximums, field+".resources", bad)
		if t.Workdir != "" && !path.IsAbs(t.Workdir) {
			bad(field+".workdir", "must be absolute")
		}
	}
	if poolTarget > c.Limits.MaxSandboxes {
		bad("templates", "combined warm_pool must not exceed limits.max_sandboxes")
	}
	if c.Audit.RetentionDays < 1 || c.Audit.RetentionDays > 3650 {
		bad("audit.retention_days", "must be 1..3650")
	}
	if c.Log.Level != "debug" && c.Log.Level != "info" && c.Log.Level != "warn" && c.Log.Level != "error" {
		bad("log.level", "must be debug, info, warn or error")
	}
	if c.Log.Format != "json" && c.Log.Format != "text" {
		bad("log.format", "must be json or text")
	}
	return errors.Join(problems...)
}

func compareResources(d, m Resources, field string, bad func(string, string)) {
	dv, mv := reflect.ValueOf(d), reflect.ValueOf(m)
	for i := 0; i < dv.NumField(); i++ {
		var value, ceiling float64
		if dv.Field(i).Kind() == reflect.Float64 {
			value, ceiling = dv.Field(i).Float(), mv.Field(i).Float()
		} else {
			value, ceiling = float64(dv.Field(i).Int()), float64(mv.Field(i).Int())
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > ceiling {
			bad(field+"."+dv.Type().Field(i).Tag.Get("koanf"), "must be finite, nonnegative and within limits.maximums")
		}
	}
}

func positiveFields(v reflect.Value, prefix string, bad func(string, string)) {
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		tag := v.Type().Field(i).Tag.Get("koanf")
		key := tag
		if prefix != "" {
			key = prefix + "." + tag
		}
		if strings.Contains(tag, "squash") {
			key = prefix
		}
		switch f.Kind() {
		case reflect.Struct:
			positiveFields(f, key, bad)
		case reflect.Int, reflect.Int64:
			if f.Int() <= 0 {
				bad(key, "must be positive")
			}
		case reflect.Float64:
			if f.Float() <= 0 || math.IsNaN(f.Float()) || math.IsInf(f.Float(), 0) {
				bad(key, "must be finite and positive")
			}
		}
	}
}
