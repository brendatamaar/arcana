# Agent rules

## Project & Stack
- **Arcana**: self-hosted sandbox orchestrator untuk AI agent dalam 1 binary Go statis (`CGO_ENABLED=0`, Go di `go.mod`).
- **Backend**: `net/http` + OpenAPI (`api/openapi.yaml` → `oapi-codegen`), SQLite (`modernc.org/sqlite`, goose, sqlc), `koanf/v2`, `coder/websocket`, `prometheus/client_golang`, MCP Go SDK; runtime Docker (`moby/moby/client`, `runsc` default), `containerd/v2`, Firecracker (`envd` gRPC).
- **Web & SDK**: React 19 + TS strict + Vite + Tailwind + TanStack Query (`web/`, pnpm); SDK Python & TypeScript (`sdk/`); VitePress (`docs/`).

## Commands
- `make check` — `verify-generated` + `lint` + `test` (`-race`); **wajib lulus sebelum commit**.
- `make build` / `make build-all` — build `sbx-exec` + `arcana` (`build-all` menyertakan dashboard `web/`).
- `make test-integration` / `make test-escape` — butuh Linux + Docker + `runsc` (wajib bila menyentuh runtime, exec, files, network, reconcile, atau security).
- `make web-check` / `make web-e2e` — lint, typecheck, test, generated types, Playwright smoke (wajib bila menyentuh `web/`, `internal/webui/`, atau `api/openapi.yaml`).
- `make sdk-verify-generated` / `make sdk-test` / `make helm-check` — verifikasi SDK & Helm chart bila relevan.
- `make generate` / `make web-generate` / `make sdk-generate` — regenerasi kode setelah mengubah OpenAPI, SQL, atau proto.

## Konvensi & Aturan Keamanan (non-negotiable)
- **Default aman**: runtime default `runsc`, network `none`, hardening FR-RL-2; jangan bypass `allow_insecure_runtime`; dilarang mode `open` atau TLS interception.
- **Boundary host/sandbox**: tanpa concatenasi shell string; operasi file/arsip/snapshot hanya di dalam sandbox via exec + shared path validator; host tidak pernah menyentuh/ekstrak workspace.
- **Auth & batas**: cek ownership tiap resource (non-admin dapat `404`), semua buffer/read/pagination berbatas, secret hanya disimpan sebagai hash / file `0600`, tes escape tidak boleh diperlemah.
- **Dashboard**: cookie hanya untuk allowlist `classifyRoute` + `Origin` + CSRF token (ADR 0008), tidak pernah ke exec/file; dilarang `dangerouslySetInnerHTML`/`innerHTML` atau melonggarkan CSP.
- **Generated code**: jangan edit manual `internal/api/gen/`, `internal/store/db/`, `internal/envd/envdpb/*.pb.go`, `web/src/api/schema.d.ts`, `sdk/typescript/src/schema.d.ts`, `sdk/python/src/arcana/_generated/`; jangan commit isi `internal/webui/dist/`.
- **Detail lengkap** (repo layout, konvensi Go/web, 13 aturan keamanan, daftar dependency disetujui, prosedur API change, Conventional Commits): lihat `docs/AGENTS.md`.

<!-- repo-playbook:start (dikelola oleh repo-playbook/install.sh; jangan edit manual) -->
## Workflow per fase

- Sumber kebenaran: `docs/PLAN.md` (status fase), detail fase di PLAN.md atau `docs/phases/`, keputusan di `docs/DECISIONS.md`. Format: `.agents/playbook/formats.md`.
- Skill tersedia: phase-setup-new, phase-setup-existing, phase-start, phase-resume, phase-status, phase-routing. Jika skill tidak termuat otomatis, baca `.agents/skills/<nama>/SKILL.md` lalu ikuti isinya.
- Sebelum kerja: baca PLAN.md + detail fase yang ditugaskan. Kerjakan HANYA scope fase; hal di luar scope dicatat di Handoff notes.
- Selesai = DoD fase lulus, dijalankan sendiri. DoD gagal 3x berturut-turut: berhenti dan laporkan.
- Commit kecil. Update Handoff notes di tengah & akhir fase, dan status di PLAN.md.
- Keputusan teknis baru: tambah 1 entri di DECISIONS.md.
- Delegasi ke provider lain dan fallback saat limit: ikuti skill phase-routing (`.agents/playbook/routing.md`).
- Laporan ringkas; jangan dump file atau log panjang.
<!-- repo-playbook:end -->
