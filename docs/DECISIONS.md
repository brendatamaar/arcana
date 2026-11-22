# DECISIONS

Log keputusan teknis ringkas untuk workflow per fase. Untuk latar belakang dan bukti lengkap, lihat ADR di `docs/decisions/` (indeks situs docs: `docs/adrs.md`).

| Tanggal | Keputusan | Alasan | Fase |
|---|---|---|---|
| 2026-10-13 | Docker Engine API (`moby/moby/client`) + gVisor (`runsc`) sebagai runtime default; `runc` wajib opt-in (`allow_insecure_runtime`) | Isolasi kuat tanpa perlu KVM dan umum untuk self-hoster (ADR 0001) | P0 |
| 2026-11-01 | Helper statis `sbx-exec` (stdlib-only, subreaper + pidfd + status atomik) untuk eksekusi dan terminasi process tree | `docker exec` tidak bisa menghentikan proses anak/detached secara andal (ADR 0002) | P0, P1 |
| 2026-11-01 | Semua operasi file, arsip, dan snapshot dijalankan di dalam sandbox via exec + shared path validator; host tidak pernah menyentuh isi workspace | Menghindari serangan symlink host-side dan masalah visibilitas overlay gVisor (ADR 0003) | P0, P1 |
| 2026-11-08 | Egress `allowlist` memakai jaringan internal per sandbox + dedicated `runsc` CONNECT/HTTP proxy dengan IP/MAC tetap; mode `open` & TLS interception dilarang | Menjaga isolasi jaringan ketat, anti-SSRF setelah DNS resolve, tanpa mengorbankan kesederhanaan model keamanan (ADR 0004) | P0, P2 |
| 2026-11-01 | Pure-Go SQLite (`modernc.org/sqlite`, WAL, single writer) + goose + sqlc + OpenAPI-first (`api/openapi.yaml` → `oapi-codegen`) | Menjamin binary statis `CGO_ENABLED=0` dan kontrak API tunggal yang terverifikasi di CI | P1 |
| 2026-11-12 | SDK Python (`openapi-python-client`) dan TypeScript (`openapi-typescript` + `openapi-fetch`) di-generate dari OpenAPI dengan wrapper manual untuk streaming/raw/error | Menjaga konsistensi skema lintas bahasa tanpa ketergantungan framework berat (ADR 0005) | P3 |
| 2026-11-12 | Dashboard React + Vite di-embed via `internal/webui` dengan cookie session `HttpOnly`, SSE `/v1/events`, dan strict CSP | Tidak perlu token di browser storage; satu binary tetap self-contained | P3 |
| 2026-11-15 | `envd` dibangun sebagai gRPC daemon di atas `internal/sbxexec` (Unix socket / host-only `AF_VSOCK`, tanpa TLS) dan Firecracker memakai HTTP client stdlib tanpa `firecracker-go-sdk` | Satu binary statis untuk guest microVM dengan reuse subreaper/pidfd; menghindari bloat dependency (ADR 0006, ADR 0007) | P4 |
| 2026-11-15 | Aksi tulis dashboard dibatasi pada allowlist `classifyRoute`, wajib `Origin` + synchronizer CSRF token (`X-CSRF-Token`), dan re-entry admin key untuk manajemen key; exec/file tidak pernah menerima cookie | Menutup akses cookie ke stream/file dan mengamankan mutasi browser (ADR 0008) | P4 |
| 2026-11-16 | Driver `containerd` v2 dengan shim `runsc`, bind-mount workspace host dir untuk `volume`, dan allowlist CNI + dedicated proxy container; Helm chart single-node pinned Deployment (`Recreate`) | Menyamai jaminan hardening/allowlist Docker driver secara langsung di containerd; Deployment pinned menghindari konflik DB/Docker per node (ADR 0009, ADR 0010) | P4 |
