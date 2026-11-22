# PLAN

## Arsitektur ringkas
- **Binary tunggal (`cmd/arcana`)**: HTTP/WebSocket/SSE API (`internal/api`, `internal/stream`, OpenAPI `api/openapi.yaml` → `oapi-codegen`), CLI (`pkg/client`), MCP stdio (`internal/mcp`), dan dashboard React ter-embed (`web/` → `internal/webui`).
- **Control plane (`internal/orchestrator`)**: state machine lifecycle sandbox, quota & rate limit, sync/streaming exec, operasi file & snapshot (selalu via exec di dalam sandbox), warm pool, TTL/idle reaper, volume watchdog, startup reconciliation, event bus (`internal/events`), audit async (`internal/audit`), dan Prometheus metrics (`internal/metrics`).
- **Storage & Auth**: pure-Go SQLite (`internal/store` — `modernc.org/sqlite`, goose, sqlc) dan auth API key / cookie session + CSRF allowlist (`internal/auth`).
- **Runtime & Isolation (`internal/runtime`)**: driver pluggable — Docker (`runsc` default / `runc` opt-in), `containerd` v2 (`runsc`, volume host-dir, allowlist CNI), Firecracker microVM (`cmd/arcana-init`, `cmd/envd` gRPC over vsock/Unix), dan `fake` untuk unit test.
- **Helper & Network**: helper statis `sbx-exec` (`internal/sbxexec`, subreaper + pidfd) dan egress allowlist via jaringan internal + dedicated `runsc` proxy (`internal/netpolicy`).

## Fase
| ID | Nama | Status | Depends on | Parallel-safe with |
|----|------|--------|------------|--------------------|
| P0 | Foundations & spikes | done | - | - |
| P1 | Core MVP (v0.1) | done | P0 | - |
| P2 | Self-hosting ready (v0.2) | done | P1 | - |
| P3 | Agent ecosystem & showcase (v0.3) | done | P2 | - |
| P4 | Stretch runtimes & platform | done | P3 | - |

---

## P0 - Foundations & spikes
Status: done
Depends on: -
Parallel-safe with: -

### Goal
Membangun scaffold repo, CI dasar, dan memvalidasi 4 risiko utama (Docker+`runsc` exec/TTY, terminasi process tree, workspace/file via exec, jaringan allowlist) sebelum implementasi MVP.

### Handoff notes
- Selesai penuh (T0.1–T0.6); bukti di `docs/spikes/001`–`004` dan `docs/decisions/0001`–`0004`.
- Kode throwaway `spikes/` sudah dihapus; `sbx-exec` prototipe dan fixture test dipertahankan untuk P1.
- Temuan penting: Docker embedded DNS dan hot-attach interface pada proxy `runsc` tidak andal, sehingga proxy memerlukan DNS eksplisit dan semua interface terpasang sebelum start.

---

## P1 - Core MVP (v0.1)
Status: done
Depends on: P0
Parallel-safe with: -

### Goal
Merilis MVP self-hosted v0.1 yang aman secara default: lifecycle sandbox, sync & streaming exec, Files API, reaper/reconciliation, Go client, CLI, `doctor`, dan escape suite v1 di CI `runsc`.

### Handoff notes
- Selesai penuh (T1.1–T1.18), dirilis pada tag `v0.1.0` (`1fd1e0d`), semua CI & escape suite v1 (`ESC-01`–`14`, `ESC-20`, `ESC-21`) hijau di `runsc`.
- Komponen kunci: `internal/config`, `internal/store`, `internal/auth`, `internal/api`, `internal/runtime/docker`, `internal/helper` (embed static amd64/arm64 `sbx-exec`), `internal/orchestrator`, `internal/stream`, `pkg/client`, `internal/doctor`, `test/escape`.
- Jebakan/catatan: helper memisahkan transport exit (`0`/`124`/`125`) dari exit code command asli; tmpfs `/workspace` perlu opsi `exec` eksplisit; `make check` wajib memakai `CGO_ENABLED=0` build-safe dan `-race` untuk test.

---

## P2 - Self-hosting ready (v0.2)
Status: done
Depends on: P1
Parallel-safe with: -

### Goal
Menyiapkan Arcana untuk produksi self-hosted: template resmi, warm pool, egress allowlist + proxy terisolasi, audit log, metrics Prometheus/Grafana, rate limiting, escape suite v2, installer, dan dokumentasi operator.

### Handoff notes
- Selesai penuh (T2.1–T2.11), dirilis pada tag `v0.2.0` (`15a49e8`) dengan artefak GoReleaser (deb/rpm/tarball/GHCR) dan `scripts/install.sh`.
- Komponen kunci: `templates/{base,python,node,go}`, `internal/netpolicy` (subnet allocator + dedicated `runsc` proxy per sandbox), `internal/audit`, `internal/metrics`, `deploy/{compose,grafana,systemd}`, `ESC-15`–`19`.
- Jebakan/catatan: Docker 28 mengabaikan `MacAddress` pada `NetworkConnect`, sehingga IP/MAC proxy wajib ditetapkan saat container create; `make test-escape` perlu dijalankan serial (`GOFLAGS=-p=1`) pada host kecil agar `ESC-03` tidak terpengaruh beban paralel.

---

## P3 - Agent ecosystem & showcase (v0.3)
Status: done
Depends on: P2
Parallel-safe with: -

### Goal
Menambahkan ekosistem agent dan showcase v0.3: snapshot & parallel fork, MCP server, SDK Python & TypeScript, reference fix-agent, benchmark harness, dashboard React read-only, situs docs VitePress, dan devcontainer.

### Handoff notes
- Selesai penuh (T3.1–T3.10), dirilis pada tag `v0.3.0` (`d65b1ee`); 8 job CI hijau (check, shellcheck, markdown, sdk, docs, web, goreleaser, security `runsc`).
- Komponen kunci: `internal/mcp`, `sdk/{python,typescript}`, `examples/fix-agent`, `internal/bench`, `internal/events`, `web/` + `internal/webui`, `docs/` (VitePress + VHS tapes), `.devcontainer/`.
- Catatan `OPEN` non-blocking tersisa:
  - `T3.4`: `make eval-agent` belum pernah dijalankan dengan model LLM lokal nyata (baru scripted server); `docs/agent-results.md` belum berisi hasil model nyata.
  - `T3.8`: deployment GitHub Pages butuh aktivasi manual di Settings → Pages → Source: GitHub Actions.
  - `T3.9`: devcontainer belum diuji langsung di GitHub Codespaces (hanya cek lokal).
  - `T3.5`: benchmark density dibatasi 12 sandbox pada VM 2 vCPU/1.9 GB; butuh rerun di reference host tenang.

---

## P4 - Stretch runtimes & platform
Status: done
Depends on: P3
Parallel-safe with: -

### Goal
Menambahkan kapabilitas lanjutan: daemon `envd` gRPC, driver Firecracker, perbandingan runtime, dashboard web full write+CSRF, driver `containerd` (termasuk volume & allowlist CNI), dan Helm chart single-node.

### Handoff notes
- Selesai hingga commit `cf72d8a` (T4.1–T4.6 + follow-up wiring `runtime.driver`, `containerd` volume & allowlist CNI, Helm kind e2e, dan perbaikan logging/flaky test `8d6f196`/`d4ac35a`/`bb4dd78`). Belum ada tag rilis `v0.4.0`.
- Komponen kunci: `cmd/envd` & `internal/envd`, `internal/sbxexec`, `cmd/arcana-init`, `internal/runtime/firecracker`, `internal/runtime/containerd`, `internal/runtime/factory`, `deploy/helm/arcana`, `ESC-25`–`27` (CSRF/cookie allowlist).
- Catatan `OPEN` tersisa (kandidat fase P5 bila dibutuhkan):
  - **Firecracker (T4.2/T4.3)**: belum pernah boot di host `/dev/kvm` nyata (`TestRealFirecracker`, jailer, guest init, vsock, kolom benchmark Firecracker); belum ada allowlist networking atau memory snapshots.
  - **containerd (T4.5)**: terverifikasi di containerd nested (`scripts/containerd-*.sh` pada `runsc` & `runc`), namun belum diuji di bare-host, host dengan default-deny `FORWARD` firewall, atau diukur latensi create `runsc` bare-host.
  - **Helm (T4.6)**: terverifikasi end-to-end di `kind` (`make helm-e2e`), namun multi-node, CNI lain, TLS ingress, dan CI `helm-e2e` belum diuji.
  - **Rilis**: bila ingin merilis fitur P4 sebagai `v0.4.0`, buat fase baru untuk verifikasi CI penuh, update README/CHANGELOG, dan tagging.
