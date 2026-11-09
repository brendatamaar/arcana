#!/usr/bin/env python3
"""Verify the provisioned dashboard against real Arcana execs, then remove test state."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parent.parent
COMPOSE = ROOT / "deploy/compose/docker-compose.yml"


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def request(url, method="GET", body=None, key=None):
    data = None if body is None else json.dumps(body).encode()
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Authorization"] = "Bearer " + key
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(
        urllib.request.Request(url, data=data, headers=headers, method=method),
        timeout=10,
    ) as response:
        data = response.read(1_048_577)
        if len(data) > 1_048_576:
            raise RuntimeError("response exceeds verification cap")
        return json.loads(data) if data else None


def wait(check, timeout=180):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, ValueError):
            pass
        time.sleep(0.25)
    raise RuntimeError("Compose verification deadline exceeded")


def main():
    with tempfile.TemporaryDirectory(prefix="arcana-compose-check-") as directory:
        state = Path(directory) / "state"
        state.mkdir(mode=0o700)
        if os.getuid() == 0:
            os.chown(state, 1000, 1000)
        ports = set()
        while len(ports) < 3:
            ports.add(free_port())
        api_port, prom_port, grafana_port = sorted(ports)
        env = dict(os.environ, ARCANA_UID=str(os.getuid() or 1000),ARCANA_GID=str(os.getgid() or 1000),ARCANA_DATA_DIR=str(state),
                   DOCKER_GID=str(Path("/var/run/docker.sock").stat().st_gid),
                   ARCANA_API_PORT=str(api_port), PROMETHEUS_PORT=str(prom_port),
                   GRAFANA_PORT=str(grafana_port))
        command = ["docker", "compose", "-p", "arcana-check-" + uuid.uuid4().hex[:8],
                   "-f", str(COMPOSE)]
        api = f"http://127.0.0.1:{api_port}"
        prometheus = f"http://127.0.0.1:{prom_port}"
        grafana = f"http://127.0.0.1:{grafana_port}"
        try:
            subprocess.run(command + ["up", "-d", "--wait"], env=env, check=True)
            wait(lambda: request(api + "/v1/health").get("status") == "ok")
            key = (state / "admin.key").read_text().strip()
            sandbox = request(api + "/v1/sandboxes", "POST",
                              {"template": "base", "ttl_seconds": 600}, key)
            for _ in range(3):
                result = request(api + "/v1/sandboxes/" + sandbox["id"] + "/exec",
                                 "POST", {"cmd": ["/bin/true"]}, key)
                if result["exit_code"] != 0:
                    raise RuntimeError("real sandbox exec failed")
            def populated():
                result = request(prometheus + "/api/v1/query?query=sum(arcana_execs_total)")
                values = result.get("data", {}).get("result", [])
                return values and float(values[0]["value"][1]) >= 3
            wait(populated)
            dashboard = wait(lambda: request(grafana + "/api/dashboards/uid/arcana"))
            if not dashboard.get("dashboard", {}).get("panels"):
                raise RuntimeError("Grafana dashboard was not provisioned")
            def datasource_populated():
                result = request(grafana + "/api/datasources/proxy/uid/arcana-prometheus/api/v1/query?query=sum(arcana_execs_total)")
                values = result.get("data", {}).get("result", [])
                return values and float(values[0]["value"][1]) >= 3
            wait(datasource_populated)
            request(api + "/v1/sandboxes/" + sandbox["id"], "DELETE", key=key)
            print("PASS: real execs, Prometheus samples, provisioned Grafana dashboard and datasource query")
        finally:
            subprocess.run(command + ["down", "--volumes", "--remove-orphans"],
                           env=env, check=True)


if __name__ == "__main__":
    main()
