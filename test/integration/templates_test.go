//go:build linux && integration

package integration

import (
	"context"
	"testing"
	"time"
)

func TestTemplates_OfflineWorkspace(t *testing.T) {
	for _, name := range []string{"base", "python", "node", "go"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			id := docker(t, ctx, "run", "-d", "--runtime=runsc", "--network=none", "--read-only", "--user=1000:1000", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--tmpfs=/tmp:size=64m", "--tmpfs=/run/sbx:size=4m,uid=1000,gid=1000", "--pids-limit=128", "--cpus=1", "--memory=512m", "--memory-swap=512m", "--mount=type=volume,dst=/workspace", "arcana-"+name+":local")
			t.Cleanup(func() {
				cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
				defer done()
				docker(t, cleanup, "rm", "-fv", id)
			})
			docker(t, ctx, "exec", id, "test", "-w", "/workspace")
			if got := docker(t, ctx, "exec", id, "id", "-u"); got != "1000" {
				t.Fatalf("UID = %s", got)
			}
			for _, tool := range []string{"tar", "du", "find", "git"} {
				docker(t, ctx, "exec", id, tool, "--version")
			}
			switch name {
			case "python":
				docker(t, ctx, "exec", id, "python", "-m", "venv", "/workspace/.venv")
				docker(t, ctx, "exec", id, "python", "-m", "pip", "install", "--no-index", "pip")
				docker(t, ctx, "exec", id, "python", "-c", "import sys; assert sys.prefix == '/workspace/.venv'")
			case "node":
				docker(t, ctx, "exec", id, "node", "-e", "const fs=require('fs'); fs.mkdirSync('/workspace/pkg'); fs.writeFileSync('/workspace/pkg/package.json', JSON.stringify({name:'offline-probe',version:'1.0.0'}))")
				docker(t, ctx, "exec", id, "npm", "install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund", "./pkg")
				docker(t, ctx, "exec", id, "test", "-f", "/workspace/node_modules/offline-probe/package.json")
			case "go":
				docker(t, ctx, "exec", id, "go", "mod", "init", "example.com/offline")
				docker(t, ctx, "exec", id, "go", "mod", "download")
				docker(t, ctx, "exec", id, "go", "env", "GOPATH", "GOCACHE")
			}
		})
	}
}
