package orchestrator

import (
	"slices"

	"github.com/brendatamaar/arcana/internal/config"
)

// Template describes configured defaults without environment values.
type Template struct {
	Name                           string
	Image                          string
	Resources                      config.Resources
	TTLSeconds, IdleTimeoutSeconds int64
	Workdir, NetworkMode           string
	WarmPool                       int
	EnvKeys                        []string
}

// Templates returns independent metadata using the same resolution as Create.
func (s *Service) Templates() ([]Template, error) {
	items := make([]Template, 0, len(s.cfg.Templates))
	for _, template := range s.cfg.Templates {
		plan, err := resolve(s.cfg, CreateRequest{Template: template.Name})
		if err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(plan.spec.Env))
		for key := range plan.spec.Env {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		r := plan.spec.Resources
		items = append(items, Template{Name: template.Name, Image: template.Image, Resources: config.Resources{CPUs: r.CPUs, MemoryMB: r.MemoryMB, DiskMB: r.DiskMB, PIDs: r.PIDs}, TTLSeconds: plan.ttl, IdleTimeoutSeconds: plan.idle, Workdir: plan.spec.Workdir, NetworkMode: plan.spec.NetworkMode, WarmPool: template.WarmPool, EnvKeys: keys})
	}
	return items, nil
}
