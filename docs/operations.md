# Operating Arcana

## What lives where

| Path | Contents |
|---|---|
| `/etc/arcana/config.yaml` | Configuration ([reference](configuration.md)) |
| `data_dir` (default `/var/lib/arcana`) | `arcana.db` (SQLite, WAL mode: keys, sandbox records, audit events), `admin.key` (bootstrap key file), the embedded exec helper |
| Docker | Sandbox containers, workspace volumes, per-sandbox networks and proxies, all labelled as installation-owned |

Keep `data_dir` mode `0700` and owned by the service user.

## Service control

```sh
sudo systemctl status arcana
sudo systemctl restart arcana        # healthy sandboxes survive by default
journalctl -u arcana -f
arcana doctor --config /etc/arcana/config.yaml
```

Restarting keeps healthy sandboxes unless `server.destroy_on_shutdown` is true.
Commands interrupted by a restart fail closed. Startup reconciliation removes
orphaned containers, volumes, networks and proxies that this installation owns.
Shutdown waits up to `server.shutdown_timeout_seconds`.

## API keys

```sh
arcana keys --help        # create, list and revoke keys against the local database
```

The first start writes a bootstrap admin key to `data_dir/admin.key` (mode
`0600`) and prints it once. Rotate it by creating a new admin key and revoking
the bootstrap key. Keys are stored hashed. Never put keys in command arguments
or log them.

## Logs

Structured logs go to stderr (`log.format` `json` or `text`, `log.level`). They
never contain API keys, exec output or file contents. Under systemd they are in
the journal. If you run the container with a log driver, remember the bootstrap
key is printed once at first start; the Compose stack disables the driver for
that reason.

## Metrics

Prometheus metrics are served on `server.metrics_listen` (default
`127.0.0.1:9090`) at `GET /metrics`, separate from the API listener and
unauthenticated, so keep it on loopback or a private network. Series are
prefixed `arcana_`: API request counts and latency, sandbox create latency and
destroy counts, exec counts and duration, egress decisions and reconcile
orphans. A Prometheus and Grafana stack with a provisioned dashboard is in
[`deploy/compose`](https://github.com/brendatamaar/arcana/blob/main/deploy/compose/README.md).

## Audit log

Metadata-only events (never bodies, output or credentials) are stored in SQLite
for `audit.retention_days` and queried with `GET /v1/audit` using an admin key.
See [audit](audit.md). Creation rate limiting is described in
[rate limits](rate-limits.md).

## Backups

Stop the service (or use SQLite's online backup) and copy `data_dir`:

```sh
sudo systemctl stop arcana
sudo tar -czf arcana-backup-$(date +%F).tar.gz -C /var/lib arcana
sudo systemctl start arcana
```

This preserves API keys, sandbox metadata, audit history and snapshot archives
(`data_dir/snapshots`). Sandbox contents live in Docker volumes and are
ephemeral by design; only snapshots outlive a sandbox. Restoring requires the
same `data_dir` path and a matching or newer Arcana version. A backup taken
while a snapshot is being written may contain a `.part` file; Arcana removes
those at startup.

## Snapshots

A snapshot is a gzip tar of a sandbox's `/workspace`, streamed out through exec
and stored under `data_dir/snapshots/<id>.tar.gz` (directory `0700`, files
`0600`) with its size and SHA-256 in the database. It captures the workspace
only: not running processes, not files outside `/workspace`, and not environment
values (only the key names are recorded). The host never opens sandbox files and
never extracts an archive; a restore verifies the stored hash, then extracts
inside the new sandbox.

Limits (see [configuration](configuration.md)): `snapshots.max_per_key`,
`snapshots.max_total_bytes` (compressed bytes on disk) and `snapshots.max_fork`.
Each archive is also capped by `limits.files.max_archive_bytes`. Size
`data_dir`'s filesystem for `max_total_bytes`.

Quiesce writers before snapshotting: if the workspace changes while `tar` reads
it, the snapshot fails rather than recording an inconsistent archive. Creating a
snapshot and restoring one are ordinary supervised execs, so they are subject to
the exec timeout (`limits.exec.default_timeout_seconds`); very large workspaces
may need it raised. A restore that fails verification or extraction destroys the
new sandbox with reason `restore_failed`. After a crash, startup reconciliation
removes interrupted `.part` files and archives that have no database record.
Deleting a snapshot removes the archive first, then the record.

## Upgrades

Back up, replace the binary or package, then `systemctl restart arcana`. Read
the CHANGELOG first. Run `arcana doctor` afterwards. See [install](install.md).

## Capacity

`limits.max_sandboxes` and the per-sandbox maximums bound host usage; size them
to the host's RAM, CPU and disk. Each allowlist sandbox adds a proxy container
(256 MiB, 0.5 CPU). With `workspace.backend: tmpfs`, workspace data consumes RAM.
