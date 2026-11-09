# Local self-hosting stack

Build images with `make templates proxy-image`. Create a private absolute host
state directory and export `ARCANA_DATA_DIR` to that path. Export
`DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)`, then run:

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
```

The host state directory is mounted at the **same absolute path** inside
Arcana so Docker can bind its helper binary into sandboxes. The Arcana image
runs as UID 1000 by default; set ARCANA_UID and ARCANA_GID to the host directory owner when different. That identity must be able to write the directory. Docker socket
access is root-equivalent: use a dedicated host/VM.

API: localhost:7070. Prometheus: localhost:9091. Grafana: localhost:3000,
dashboard uid `arcana`. Ports can be changed with ARCANA_API_PORT,
PROMETHEUS_PORT and GRAFANA_PORT. All published ports bind loopback.
Grafana is a provisioned anonymous **Viewer** with login and initial admin
creation disabled; configure operator authentication before remote exposure.
Prometheus and Grafana images are pinned by digest.

Read the initial API key from the private `ARCANA_DATA_DIR/admin.key` file.
Arcana's container log driver is disabled so its one-time bootstrap key cannot
be retained in Docker logs. Audit metadata remains in SQLite; operational
signals are in Prometheus. `arcana doctor` supplies prerequisite diagnostics.

Runtime stays runsc, insecure-runtime opt-in stays false, and sandbox
networking stays none. The proxy image is available for explicit allowlists.

Metrics are served separately on metrics_listen. Labels contain fixed route/
status/outcome vocabularies and configured templates; no IDs, commands, paths,
credentials or environment values are labels. Audit overflow and storage
failures have additional counters. Session/subscriber gauges remain zero
until the v0.3 features exist. Event counters reset on daemon restart; active
sandbox gauges come from SQLite and pool gauges from the live pool.

Run `python3 scripts/check_compose.py` for a real smoke test. It creates a unique
Compose project and private state, runs three sandbox execs, verifies Prometheus
samples and Grafana's provisioned dashboard/datasource, then removes only its
own containers and volumes.
