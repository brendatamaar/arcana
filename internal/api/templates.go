package api

import (
	"net/http"

	"github.com/brendatamaar/arcana/internal/api/gen"
)

// ListTemplates lists installation-wide configuration to authenticated keys.
func (s *Server) ListTemplates(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	templates, err := s.lifecycle.Templates()
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	items := make([]gen.Template, 0, len(templates))
	for _, t := range templates {
		cpu, memory, disk, pids := float32(t.Resources.CPUs), t.Resources.MemoryMB, t.Resources.DiskMB, t.Resources.PIDs
		items = append(items, gen.Template{Name: t.Name, Image: t.Image, Resources: gen.Resources{Cpus: &cpu, MemoryMb: &memory, DiskMb: &disk, Pids: &pids}, TtlSeconds: t.TTLSeconds, IdleTimeoutSeconds: t.IdleTimeoutSeconds, Workdir: t.Workdir, NetworkMode: t.NetworkMode, WarmPool: t.WarmPool, EnvKeys: t.EnvKeys})
	}
	s.respond(w, r, http.StatusOK, gen.TemplateList{Items: items})
}
