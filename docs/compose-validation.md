# T2.6 Compose validation

Validated 2026-11-09 with Docker Compose v5.5.1 and the digest-pinned images in
deploy/compose/docker-compose.yml.

Commands:
- make templates proxy-image
- go test -race -cover ./internal/metrics/... (87.8% coverage)
- python3 scripts/check_compose.py

The smoke check created a unique temporary stack and private host state,
started Arcana under the host user's UID/GID with Docker group access, and
verified:
- runsc-backed sandbox creation and three successful /bin/true execs;
- Prometheus scraped arcana_execs_total and returned the recorded executions;
- Grafana provisioned dashboard uid arcana with its panels;
- a query through Grafana's provisioned Prometheus datasource returned the live
  execution data;
- sandbox deletion and removal of the test project's containers and volumes.

The initial attempt found a host UID/image UID mismatch. Compose now exposes
ARCANA_UID/ARCANA_GID while sandbox execution remains UID 1000. Published ports
were allocated on loopback for this check. No credentials were printed or
retained in Docker logs; the bootstrap key was read from private state.
No browser or screenshot was required to verify the dashboard/datasource data.
