#!/usr/bin/env python3
"""Check the Helm chart: it lints, renders valid Kubernetes objects, and keeps its safety properties.

    HELM=helm KUBECONFORM=kubeconform python3 scripts/check_helm.py

Every assertion reads the rendered manifests, so a template edit that drops a hardening setting
fails here. Refusals (missing required values, an unsafe runtime) are checked by rendering with bad
values and expecting helm to fail.
"""
import os
import subprocess
import sys

import yaml

CHART = os.path.join(os.path.dirname(__file__), "..", "deploy", "helm", "arcana")
HELM = os.environ.get("HELM", "helm")
KUBECONFORM = os.environ.get("KUBECONFORM", "kubeconform")
BASE = ["--set", "nodeName=node-1", "--set", "docker.socketGroup=999"]
failures = []


def check(condition, message):
    if not condition:
        failures.append(message)
        print("FAIL:", message)


def run(args, **kwargs):
    return subprocess.run(args, capture_output=True, text=True, **kwargs)


def render(*extra, template=None):
    command = [HELM, "template", "rel", CHART, *BASE, *extra]
    if template:
        command += ["--show-only", template]
    result = run(command)
    if result.returncode != 0:
        return None, result.stderr
    return [d for d in yaml.safe_load_all(result.stdout) if d], ""


def refused(message, *args):
    command = [HELM, "template", "rel", CHART, *args]
    result = run(command)
    check(result.returncode != 0, f"helm must refuse: {message}")


def by_kind(docs, kind):
    return next((d for d in docs if d["kind"] == kind), None)


def main():
    lint = run([HELM, "lint", CHART, *BASE])
    check(lint.returncode == 0, "helm lint failed:\n" + lint.stdout + lint.stderr)

    docs, err = render()
    check(docs is not None, "default render failed: " + err)
    if docs is None:
        return
    # The manifests are valid Kubernetes objects.
    manifest = run([HELM, "template", "rel", CHART, *BASE]).stdout
    conform = run([KUBECONFORM, "-strict", "-summary", "-kubernetes-version", "1.30.0"], input=manifest)
    check(conform.returncode == 0, "kubeconform rejected the chart:\n" + conform.stdout + conform.stderr)

    deploy = by_kind(docs, "Deployment")
    spec = deploy["spec"]
    pod = spec["template"]["spec"]
    container = pod["containers"][0]
    check(spec["replicas"] == 1, "exactly one replica")
    check(spec["strategy"]["type"] == "Recreate", "the old pod must stop before the new one starts")
    check(pod["nodeName"] == "node-1", "pinned to the configured node")
    psc, csc = pod["securityContext"], container["securityContext"]
    check(psc["runAsNonRoot"] is True and psc["runAsUser"] == 1000, "runs as a non-root user")
    check(psc["supplementalGroups"] == [999], "gets the Docker socket group as a supplemental group only")
    check(psc["seccompProfile"]["type"] == "RuntimeDefault", "default seccomp profile")
    check(csc["readOnlyRootFilesystem"] is True, "read-only root filesystem")
    check(csc["allowPrivilegeEscalation"] is False, "no privilege escalation")
    check(csc["capabilities"]["drop"] == ["ALL"], "all capabilities dropped")
    check(not csc.get("privileged"), "the container is not privileged")
    for field in ("hostNetwork", "hostPID", "hostIPC"):
        check(not pod.get(field), f"{field} stays off")
    check(pod["automountServiceAccountToken"] is False, "no Kubernetes API token mounted")
    check(container["image"] == "ghcr.io/brendatamaar/arcana:0.3.0", "image defaults to the appVersion")

    # Exactly two hostPaths: the data directory and the Docker socket, at the same paths inside.
    host = {v["name"]: v["hostPath"] for v in pod["volumes"] if "hostPath" in v}
    check(set(host) == {"data", "docker-socket"}, f"unexpected hostPath volumes: {sorted(host)}")
    mounts = {m["name"]: m["mountPath"] for m in container["volumeMounts"]}
    check(host["data"] == {"path": "/var/lib/arcana", "type": "Directory"}, "data is an existing node directory")
    check(host["docker-socket"] == {"path": "/var/run/docker.sock", "type": "Socket"}, "the socket is a Socket hostPath")
    check(mounts["data"] == host["data"]["path"], "the data directory is mounted at the node's own path")
    check(mounts["docker-socket"] == host["docker-socket"]["path"], "the socket is mounted at the node's own path")

    config = yaml.safe_load(by_kind(docs, "ConfigMap")["data"]["config.yaml"])
    check(config["data_dir"] == "/var/lib/arcana", "data_dir matches the mounted directory")
    check(config["runtime"]["docker"]["host"] == "unix:///var/run/docker.sock", "Docker host matches the socket mount")
    check(config["runtime"]["docker"]["oci_runtime"] == "runsc" and not config["runtime"]["docker"]["allow_insecure_runtime"], "runsc by default")
    check(config["network"]["proxy_image"] == container["image"], "the egress proxy uses the same image")
    check(config["server"]["destroy_on_shutdown"] is False, "sandboxes survive an Arcana restart")
    check("checksum/config" in deploy["spec"]["template"]["metadata"]["annotations"], "a config change restarts the pod")

    service = by_kind(docs, "Service")
    check(service["spec"]["type"] == "ClusterIP", "ClusterIP by default")
    check([p["name"] for p in service["spec"]["ports"]] == ["api"], "metrics are not exposed by default")
    check(by_kind(docs, "NetworkPolicy") is None, "no NetworkPolicy unless asked")

    # Options.
    docs, _ = render("--set", "service.metrics.enabled=true", "--set", "networkPolicy.enabled=true",
                     "--set", "networkPolicy.allowFrom[0].podSelector.matchLabels.app=gateway",
                     "--set", "dataDir=/srv/arcana", "--set", "docker.socketPath=/run/docker.sock",
                     "--set", "user.uid=2000", "--set", "user.gid=2000", "--set", "image.tag=sha-abc")
    check(docs is not None, "render with options failed")
    if docs:
        pod = by_kind(docs, "Deployment")["spec"]["template"]["spec"]
        check(pod["containers"][0]["image"].endswith(":sha-abc"), "image tag override")
        check({m["mountPath"] for m in pod["containers"][0]["volumeMounts"]} >= {"/srv/arcana", "/run/docker.sock"}, "custom paths are mounted")
        check(pod["securityContext"]["runAsUser"] == 2000, "custom user")
        check(by_kind(docs, "NetworkPolicy") is not None, "NetworkPolicy when enabled")
        check(len(by_kind(docs, "Service")["spec"]["ports"]) == 2, "metrics port when enabled")
        cfg = yaml.safe_load(by_kind(docs, "ConfigMap")["data"]["config.yaml"])
        check(cfg["data_dir"] == "/srv/arcana" and cfg["runtime"]["docker"]["host"] == "unix:///run/docker.sock", "config follows the values")

    # A user's config cannot override what the chart pins.
    docs, _ = render("--set", "config.data_dir=/elsewhere", "--set", "config.server.listen=127.0.0.1:1",
                     "--set", "config.server.destroy_on_shutdown=true")
    if docs:
        cfg = yaml.safe_load(by_kind(docs, "ConfigMap")["data"]["config.yaml"])
        check(cfg["data_dir"] == "/var/lib/arcana" and cfg["server"]["listen"] == "0.0.0.0:7070", "values cannot override the pinned settings")

    # Refusals.
    refused("nodeName is required", "--set", "docker.socketGroup=999")
    refused("docker.socketGroup is required", "--set", "nodeName=node-1")
    refused("a root socket group", "--set", "nodeName=n", "--set", "docker.socketGroup=0")
    refused("dataDir /", *BASE, "--set", "dataDir=/")
    refused("a relative dataDir", *BASE, "--set", "dataDir=data")
    refused("a root user", *BASE, "--set", "user.uid=0")
    refused("runc without the explicit opt-in", *BASE, "--set", "config.runtime.docker.oci_runtime=runc")
    refused("a NetworkPolicy without peers", *BASE, "--set", "networkPolicy.enabled=true")
    refused("an unknown service type", *BASE, "--set", "service.type=ExternalName")
    ok = run([HELM, "template", "rel", CHART, *BASE, "--set", "config.runtime.docker.oci_runtime=runc",
              "--set", "config.runtime.docker.allow_insecure_runtime=true"])
    check(ok.returncode == 0, "runc with the explicit opt-in is allowed")

    if failures:
        print(f"\n{len(failures)} check(s) failed")
        sys.exit(1)
    print("helm chart checks passed")


main()
