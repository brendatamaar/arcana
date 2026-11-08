package orchestrator

import (
	"fmt"
	"maps"
	"math"
	"path"
	"strings"

	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/pathutil"
	"github.com/brendatamaar/arcana/internal/runtime"
)

// Overrides preserves the distinction between absent and invalid zero resources.
type Overrides struct {
	CPUs                   *float64
	MemoryMB, DiskMB, PIDs *int64
}

// CreateRequest is a bounded, caller-supplied sandbox specification.
type CreateRequest struct {
	Template                       string
	TTLSeconds, IdleTimeoutSeconds *int64
	Resources                      Overrides
	Env, Labels                    map[string]string
	NetworkMode                    string
}

type resolved struct {
	spec      runtime.Spec
	ttl, idle int64
	template  string
	source    string
}

func resolve(cfg config.Config, request CreateRequest) (resolved, error) {
	var template *config.Template
	for i := range cfg.Templates {
		if cfg.Templates[i].Name == request.Template {
			template = &cfg.Templates[i]
			break
		}
	}
	if template == nil {
		return resolved{}, ErrTemplate
	}
	resources := cfg.Limits.Defaults.Resources
	if template.Resources.CPUs != 0 {
		resources.CPUs = template.Resources.CPUs
	}
	if template.Resources.MemoryMB != 0 {
		resources.MemoryMB = template.Resources.MemoryMB
	}
	if template.Resources.DiskMB != 0 {
		resources.DiskMB = template.Resources.DiskMB
	}
	if template.Resources.PIDs != 0 {
		resources.PIDs = template.Resources.PIDs
	}
	if request.Resources.CPUs != nil {
		resources.CPUs = *request.Resources.CPUs
	}
	if request.Resources.MemoryMB != nil {
		resources.MemoryMB = *request.Resources.MemoryMB
	}
	if request.Resources.DiskMB != nil {
		resources.DiskMB = *request.Resources.DiskMB
	}
	if request.Resources.PIDs != nil {
		resources.PIDs = *request.Resources.PIDs
	}
	if resources.CPUs <= 0 || math.IsNaN(resources.CPUs) || math.IsInf(resources.CPUs, 0) || resources.MemoryMB < 1 || resources.DiskMB < 1 || resources.PIDs < 1 {
		return resolved{}, fmt.Errorf("%w: positive finite resources required", ErrInvalid)
	}
	resources.CPUs = min(resources.CPUs, cfg.Limits.Maximums.CPUs)
	resources.MemoryMB = min(resources.MemoryMB, cfg.Limits.Maximums.MemoryMB)
	resources.DiskMB = min(resources.DiskMB, cfg.Limits.Maximums.DiskMB)
	resources.PIDs = min(resources.PIDs, cfg.Limits.Maximums.PIDs)
	ttl := int64(cfg.Limits.Defaults.TTLSeconds)
	if template.TTLSeconds != 0 {
		ttl = int64(template.TTLSeconds)
	}
	if request.TTLSeconds != nil {
		ttl = *request.TTLSeconds
	}
	idle := int64(cfg.Limits.Defaults.IdleTimeoutSeconds)
	if request.IdleTimeoutSeconds != nil {
		idle = *request.IdleTimeoutSeconds
	}
	if ttl < 1 || idle < 0 {
		return resolved{}, fmt.Errorf("%w: invalid TTL or idle timeout", ErrInvalid)
	}
	ttl = min(ttl, int64(cfg.Limits.MaxTTLSeconds))
	idle = min(idle, ttl)
	network := request.NetworkMode
	if network == "" {
		network = cfg.Network.DefaultMode
	}
	if network != "none" {
		return resolved{}, fmt.Errorf("%w: v0.1 supports network mode none", ErrInvalid)
	}
	env := maps.Clone(template.Env)
	if env == nil {
		env = make(map[string]string)
	}
	maps.Copy(env, request.Env)
	if err := validateMap(env, true); err != nil {
		return resolved{}, err
	}
	if err := validateMap(request.Labels, false); err != nil {
		return resolved{}, err
	}
	workdir := template.Workdir
	if workdir == "" {
		workdir = "/workspace"
	}
	if !allowedPath(workdir, cfg.Limits.Files.AllowedRoots) {
		return resolved{}, fmt.Errorf("%w: template workdir outside allowed roots", ErrInvalid)
	}
	return resolved{spec: runtime.Spec{Image: template.Image, Resources: runtime.Resources{CPUs: resources.CPUs, MemoryMB: resources.MemoryMB, DiskMB: resources.DiskMB, PIDs: resources.PIDs}, Env: env, Labels: maps.Clone(request.Labels), WorkspaceBackend: cfg.Workspace.Backend, NetworkMode: network, Workdir: path.Clean(workdir)}, ttl: ttl, idle: idle, template: template.Name}, nil
}

func validateMap(values map[string]string, env bool) error {
	if len(values) > 100 {
		return fmt.Errorf("%w: too many env or label entries", ErrInvalid)
	}
	total := 0
	for key, value := range values {
		total += len(key) + len(value)
		if key == "" || len(key) > 128 || len(value) > 4096 || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) || !env && strings.HasPrefix(key, "arcana.") {
			return fmt.Errorf("%w: invalid env or label entry", ErrInvalid)
		}
		if !env && len(value) > 1024 {
			return fmt.Errorf("%w: label value exceeds runtime limit", ErrInvalid)
		}
	}
	if total > 32768 {
		return fmt.Errorf("%w: env or labels exceed limit", ErrInvalid)
	}
	return nil
}

func allowedPath(value string, roots []string) bool {
	_, err := pathutil.Clean(value, roots)
	return err == nil
}
