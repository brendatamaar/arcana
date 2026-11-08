package orchestrator

import "testing"

func TestTemplates_ResolvedDefaultsAndIndependentMetadata(t *testing.T) {
	s, _, _, _, _ := fixture(t)
	s.cfg.Templates[0].Env = map[string]string{"SECRET": "do-not-return"}
	s.cfg.Templates[0].TTLSeconds = 120
	s.cfg.Templates[0].Resources.MemoryMB = 512
	items, err := s.Templates()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Resources.MemoryMB != 512 || items[0].Resources.CPUs != 2 || items[0].TTLSeconds != 120 || items[0].IdleTimeoutSeconds != 120 || items[0].EnvKeys[0] != "SECRET" {
		t.Fatalf("%+v", items)
	}
	items[0].EnvKeys[0] = "CHANGED"
	again, err := s.Templates()
	if err != nil || again[0].EnvKeys[0] != "SECRET" {
		t.Fatal("metadata aliases service configuration")
	}
	s.cfg.Templates[0].Workdir = "/etc"
	if _, err := s.Templates(); err == nil {
		t.Fatal("invalid template accepted")
	}
}

func TestNetworkPolicy_PresetsLimitsAndNoneGuard(t *testing.T) {
	s, _, _, _, _ := fixture(t)
	plan, err := resolve(s.cfg, CreateRequest{Template: "base", NetworkMode: "allowlist", NetworkPresets: []string{"python"}, NetworkAllow: []string{"EXAMPLE.COM", "pypi.org"}})
	if err != nil || len(plan.spec.NetworkAllow) != 3 {
		t.Fatalf("%+v %v", plan, err)
	}
	for _, request := range []CreateRequest{
		{Template: "base", NetworkMode: "none", NetworkAllow: []string{"example.com"}},
		{Template: "base", NetworkMode: "allowlist", NetworkPresets: []string{"unknown"}},
		{Template: "base", NetworkMode: "allowlist", NetworkAllow: []string{"127.0.0.1"}},
	} {
		if _, err := resolve(s.cfg, request); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}
