# Configuration reference

Arcana reads YAML from `/etc/arcana/config.yaml` (override with `--config`).
Every value is validated at startup with an error naming the key. Unknown keys
are ignored, so a misspelt key silently keeps its default; check spelling against
this page. Any key can be overridden by an environment variable: prefix
`ARCANA_`, uppercase the path and use `__` between levels, for example
`ARCANA_SERVER__LISTEN=0.0.0.0:7070`. Files are applied over built-in defaults,
then environment variables over the file. A commented starting point is
[`deploy/config.example.yaml`](../deploy/config.example.yaml).

A test fails if a key in the config struct is missing from this page.

## Server

| Key | Default | Meaning |
|---|---|---|
| `server.listen` | `127.0.0.1:7070` | API listener. Keep on loopback; use a TLS reverse proxy for remote access. |
| `server.metrics_listen` | `127.0.0.1:9090` | Prometheus listener (unauthenticated). |
| `server.shutdown_timeout_seconds` | `30` | Graceful shutdown budget. |
| `server.destroy_on_shutdown` | `false` | Destroy all sandboxes when the daemon stops instead of preserving healthy ones. |
| `data_dir` | `/var/lib/arcana` | Absolute path for the database, admin key file and helper. Keep `0700`. |

## Runtime

| Key | Default | Meaning |
|---|---|---|
| `runtime.driver` | `docker` | Only `docker` is supported. |
| `runtime.docker.host` | `unix:///var/run/docker.sock` | Docker endpoint. Access is root-equivalent. |
| `runtime.docker.oci_runtime` | `runsc` | `runsc` (gVisor) or `runc`. |
| `runtime.docker.allow_insecure_runtime` | `false` | Must be `true` to select `runc`, which shares the host kernel. |

## Limits

| Key | Default | Meaning |
|---|---|---|
| `limits.max_sandboxes` | `20` | Concurrent sandboxes on the host. |
| `limits.max_sandboxes_per_key` | `10` | Concurrent sandboxes per API key; must not exceed `limits.max_sandboxes`. |
| `limits.create_per_minute_per_key` | `30` | Creation token bucket per key, 1..10000. See [rate limits](rate-limits.md). |
| `limits.max_ttl_seconds` | `86400` | Longest allowed sandbox TTL. |
| `limits.defaults.cpus` | `1` | CPUs when a request omits them. |
| `limits.defaults.memory_mb` | `1024` | Memory (MiB) default. |
| `limits.defaults.disk_mb` | `2048` | Workspace quota (MiB) default. |
| `limits.defaults.pids` | `256` | Process limit default. |
| `limits.defaults.ttl_seconds` | `1800` | TTL default; must not exceed `limits.max_ttl_seconds`. |
| `limits.defaults.idle_timeout_seconds` | `600` | Idle timeout default. |
| `limits.maximums.cpus` | `4` | Largest CPU request accepted. |
| `limits.maximums.memory_mb` | `8192` | Largest memory request. |
| `limits.maximums.disk_mb` | `20480` | Largest disk request. |
| `limits.maximums.pids` | `4096` | Largest PID limit request. |
| `limits.exec.default_timeout_seconds` | `120` | Exec timeout default; must not exceed the maximum. |
| `limits.exec.max_timeout_seconds` | `3600` | Longest exec timeout; 3600 is the helper's hard ceiling. |
| `limits.exec.max_output_bytes` | `10485760` | Captured output cap per exec. |
| `limits.exec.max_concurrent_per_sandbox` | `8` | Concurrent execs in one sandbox. |
| `limits.files.max_file_bytes` | `52428800` | Largest single file transfer; must not exceed the archive limit. |
| `limits.files.max_archive_bytes` | `524288000` | Largest archive upload. |
| `limits.files.allowed_roots` | `[/workspace, /tmp]` | 1..16 clean absolute sandbox paths the file API may touch. |

## Workspace

| Key | Default | Meaning |
|---|---|---|
| `workspace.backend` | `volume` | `volume` (named volume, quota polled; bursts can exceed it) or `tmpfs` (hard size limit, uses RAM). |
| `workspace.check_interval_seconds` | `15` | Quota polling interval for `volume`. |

## Network

See [networking](networking.md) for behaviour.

| Key | Default | Meaning |
|---|---|---|
| `network.default_mode` | `none` | `none` or `allowlist`. `open` does not exist. |
| `network.max_allow_entries` | `50` | Hostnames per sandbox (1..50), presets plus explicit entries. |
| `network.proxy_image` | `ghcr.io/brendatamaar/arcana:latest` | Image for per-sandbox egress proxies. For local builds use `arcana:local` from `make proxy-image`. |
| `network.proxy_dns` | `[1.1.1.1, 1.0.0.1]` | 1..4 upstream DNS server IPs used by proxies. |
| `network.presets` | python, node and go presets | Map of preset name to exact hostnames (at most 50 presets, no wildcards or IPs). |
| `network.subnet_pool` | `10.200.0.0/16` | CIDR from which per-sandbox networks are carved. Must not overlap other routes or Docker networks. |
| `network.subnet_prefix` | `28` | Prefix length of each carved subnet; must fit the pool. |

## Templates

`templates` is a list (1..1000 entries). The default is a single `base` template
using `arcana-base:local`. The combined `warm_pool` of all templates must not
exceed `limits.max_sandboxes`.

| Key | Default | Meaning |
|---|---|---|
| `templates[].name` | `base` | Template name used at creation. |
| `templates[].image` | `arcana-base:local` | Docker image. Build the standard ones with `make templates`. |
| `templates[].warm_pool` | `0` | Pre-created idle sandboxes kept ready for this template. |
| `templates[].network_preset` | empty | Preset from `network.presets` applied to allowlist sandboxes created from this template. |
| `templates[].resources.cpus` | inherit | Default CPUs for this template. |
| `templates[].resources.memory_mb` | inherit | Default memory. |
| `templates[].resources.disk_mb` | inherit | Default disk. |
| `templates[].resources.pids` | inherit | Default PID limit. |
| `templates[].ttl_seconds` | inherit | Default TTL. |
| `templates[].env` | none | Environment variables set in every sandbox from this template. Values are never logged or audited. |
| `templates[].workdir` | image default | Working directory for execs. |

Template defaults never exceed `limits.maximums`.

## Snapshots

| Key | Default | Meaning |
|---|---|---|
| `snapshots.max_per_key` | `20` | Stored snapshots per API key. Creation beyond the limit fails with `quota_exceeded`. |
| `snapshots.max_total_bytes` | `10737418240` | Total compressed bytes under `data_dir/snapshots`. Must be at least `limits.files.max_archive_bytes`, which also caps each snapshot. |
| `snapshots.max_fork` | `10` | Largest `count` for one fork request. Must not exceed `limits.max_sandboxes`. |

## Audit and logging

| Key | Default | Meaning |
|---|---|---|
| `audit.retention_days` | `30` | Days of audit events kept, 1..3650. See [audit](audit.md). |
| `log.level` | `info` | `debug`, `info`, `warn` or `error`. |
| `log.format` | `json` | `json` or `text`. |
