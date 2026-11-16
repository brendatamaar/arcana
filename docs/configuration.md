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
| `runtime.driver` | `docker` | `docker`, `containerd` or `firecracker`. Settings of the drivers you do not select are not checked. Firecracker supports network mode `none` only, and containerd needs `runtime.containerd.allowlist` for anything else; see [drivers](#drivers). |
| `runtime.docker.host` | `unix:///var/run/docker.sock` | Docker endpoint. Access is root-equivalent. |
| `runtime.docker.oci_runtime` | `runsc` | `runsc` (gVisor) or `runc`. |
| `runtime.docker.allow_insecure_runtime` | `false` | Must be `true` to select `runc`, which shares the host kernel. |
| `runtime.containerd.address` | `/run/containerd/containerd.sock` | containerd's socket. Access is root-equivalent. |
| `runtime.containerd.namespace` | `arcana` | containerd namespace for Arcana's containers and images. Template images must be imported into it. |
| `runtime.containerd.oci_runtime` | `runsc` | `runsc` (shim `io.containerd.runsc.v1`) or `runc` (`io.containerd.runc.v2`). |
| `runtime.containerd.allow_insecure_runtime` | `false` | Must be `true` to select `runc`. |
| `runtime.containerd.fifo_dir` | `/run/containerd/fifo` | Directory for exec named pipes. Arcana and containerd's shim must both reach it; the default is root-only, so a non-root Arcana needs another path (created `0700`). |
| `runtime.containerd.allowlist` | `false` | Turn on allowlist networking for containerd ([ADR 0010](decisions/0010-containerd-allowlist-networking.md)). Needs Arcana to run as root, the CNI plugins `bridge`, `host-local` and `static`, and `network.proxy_image` imported into the namespace. Without it only network mode `none` works. |
| `runtime.containerd.cni_bin_dir` | `/opt/cni/bin` | Where the CNI plugins are (`/usr/lib/cni` on Debian and Ubuntu packages). Used only with `allowlist`. |
| `runtime.containerd.egress_subnet` | `172.29.0.0/20` | The shared outbound bridge's subnet; each sandbox's proxy takes one address, so the size bounds concurrent allowlist sandboxes. Must not overlap the host's networks (`arcana doctor` checks). Used only with `allowlist`. |
| `runtime.containerd.state_dir` | `/run/arcana` | Network namespaces for allowlist sandboxes. Used only with `allowlist`. |
| `runtime.firecracker.firecracker_binary` | `/usr/bin/firecracker` | The Firecracker binary. |
| `runtime.firecracker.jailer_binary` | `/usr/bin/jailer` | The jailer that confines each VMM. Required unless `allow_unjailed`. The jailer needs Arcana to run as root. |
| `runtime.firecracker.allow_unjailed` | `false` | Run Firecracker without the jailer. Development only. |
| `runtime.firecracker.kernel_image` | `/var/lib/arcana/firecracker/vmlinux` | The guest kernel (virtio-blk, vsock and ext4 built in). Must be on the same filesystem as `data_dir`. |
| `runtime.firecracker.kernel_args` | empty | Extra kernel arguments, appended after the fixed ones; `arcana.*` settings cannot be overridden. At most 512 bytes. |
| `runtime.firecracker.rootfs_dir` | `/var/lib/arcana/firecracker/rootfs` | Rootfs images built by `scripts/build-rootfs.sh`, one per template image. Same filesystem as `data_dir`. |
| `runtime.firecracker.jailer_uid` | `10001` | Unprivileged UID the VMM runs as. |
| `runtime.firecracker.jailer_gid` | `10001` | Unprivileged GID the VMM runs as. |
| `runtime.firecracker.vsock_port` | `1024` | Guest vsock port of `envd`. |
| `runtime.firecracker.boot_timeout_seconds` | `30` | How long a VM may take to boot and answer `envd` (1..300). |

### Drivers

- **docker** is the full-featured driver: disk-backed or tmpfs workspaces and allowlist networking.
- **containerd** ([ADR 0009](decisions/0009-containerd-driver.md)) talks to containerd directly. It
  supports network mode `none`, and `allowlist` when `runtime.containerd.allowlist` is on (root, CNI plugins and the
  proxy image needed; see [ADR 0010](decisions/0010-containerd-allowlist-networking.md)). Template images must already be in its namespace
  (`ctr -n arcana images import`). Arcana only needs access to the socket, not to Docker. Workspaces are tmpfs,
  or (the default `volume` backend) a host directory per sandbox under `data_dir/containerd/workspaces`, bind
  mounted at `/workspace`. The directory must belong to the sandbox user, so **volume workspaces need Arcana to
  run as root (it chowns the directory) or as UID 1000**; `arcana doctor` checks this and reports any other user
  with the fix (`workspace.backend: tmpfs`). Size is a soft limit enforced by the volume watchdog, as with Docker.
- **firecracker** ([ADR 0007](decisions/0007-firecracker-driver.md)) runs each sandbox in a microVM. It needs
  `/dev/kvm`, Firecracker, the jailer and a guest kernel, supports network mode `none` only, and each template
  image needs a rootfs built with `scripts/build-rootfs.sh`. State and jails live under `data_dir/firecracker`.

A request for `allowlist` networking with a driver that cannot provide it is refused with `invalid_request`, and
a configuration that makes `allowlist` the default or gives a template a `network_preset` is rejected at start.

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

## Dashboard sessions and live streams

| Key | Default | Meaning |
|---|---|---|
| `ui.enabled` | `true` | Serve the dashboard backend: sessions, `/v1/events` and exec output tails. |
| `ui.session_ttl_seconds` | `43200` | Lifetime of a dashboard session cookie, up to 30 days. |
| `ui.allowed_origins` | none | Extra `scheme://host[:port]` origins allowed to make cookie-authenticated requests, needed when Arcana is served under another hostname (for example behind a reverse proxy that rewrites `Host`). |
| `ui.event_buffer` | `1000` | Recent events kept so a reconnecting client can resume with `Last-Event-ID`. |
| `limits.exec.tail_buffer_bytes` | `262144` | In-memory output kept per exec for the live tail (never written to disk). |
| `limits.exec.tail_retention_seconds` | `300` | How long an exec's output stays readable after it ends. |
| `limits.exec.max_tail_subscribers` | `10` | Concurrent output-tail readers per exec. |

See [events and sessions](events.md).

## Audit and logging

| Key | Default | Meaning |
|---|---|---|
| `audit.retention_days` | `30` | Days of audit events kept, 1..3650. See [audit](audit.md). |
| `log.level` | `info` | `debug`, `info`, `warn` or `error`. |
| `log.format` | `json` | `json` or `text`. |
