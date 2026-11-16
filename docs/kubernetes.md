# Running on Kubernetes (Helm)

`deploy/helm/arcana` installs Arcana on **one** node of a Kubernetes cluster. Read the trade-offs first:
this is not a cloud-native deployment, and it gives the pod control of the node.

## What it is, and what it is not

Arcana makes sandboxes by calling a container runtime, and this chart points it at **the node's Docker Engine**
through the Docker socket. The sandboxes are Docker containers (gVisor by default) on that node. They are not
Kubernetes pods, so the scheduler, resource requests, quotas, `NetworkPolicy` and the pod network do not see
them. The chart runs only the Arcana control plane as a pod.

- **Root-equivalent on the node.** Docker socket access is root on the host (NFR-SEC-5). Anyone who can edit
  the Deployment, exec into the pod or read its mounts is root on that node. Use a dedicated node (taint it)
  and restrict RBAC on the namespace.
- **The node must run Docker Engine 24 or newer, with gVisor registered.** Most managed clusters use
  containerd or CRI-O and have no Docker Engine; this chart does not work there. Run `arcana doctor` on the
  node. A containerd driver exists ([ADR 0009](decisions/0009-containerd-driver.md)) and is selectable with
  `runtime.driver: containerd`, but this chart does not mount a containerd socket, CNI plugins or the
  privileges it needs, so it cannot be used from this chart yet.
- **Pinned to one node, one replica.** Sandboxes live in that node's Docker Engine and the data directory is a
  path on that node. Two Arcanas on one database and one engine would fight over the same containers, so the
  Deployment is fixed at one replica with the `Recreate` strategy. There is no high availability: if the node
  is lost, so are its sandboxes.
- **Sandboxes use node resources Kubernetes does not know about.** Reserve capacity for them (for example with
  the kubelet's `system-reserved`), or the scheduler will place other pods on resources the sandboxes need.
- **Pod Security.** A `hostPath` volume is not allowed by the `baseline` or `restricted` Pod Security
  Standards. The namespace needs `pod-security.kubernetes.io/enforce: privileged` (or an exemption).
- **Plain HTTP.** Arcana serves HTTP and warns when it listens beyond loopback, which it must to be reachable
  by a Service. Put a TLS-terminating ingress or proxy in front before exposing it, and consider the optional
  `NetworkPolicy` (it needs a CNI that enforces policies). The Prometheus listener has no authentication; it is
  off the Service by default.

## Why the data directory is a `hostPath` at the same path

Arcana writes its helper binary (`sbx-exec`) under its data directory and asks Docker to bind-mount it
read-only into every sandbox. Docker resolves that source on **the node**, not inside the pod. So the data
directory must be a node path, mounted into the pod at the identical path, and the chart refuses a `dataDir`
of `/`. The directory must already exist and belong to the UID and GID Arcana runs as (a non-root user), since
the chart does not add a root init container:

```sh
sudo mkdir -p /var/lib/arcana && sudo chown 1000:1000 /var/lib/arcana && sudo chmod 700 /var/lib/arcana
stat -c %g /var/run/docker.sock   # the Docker group's GID, needed below
```

The database, admin key and snapshots live there and survive pod restarts and upgrades. Sandboxes also keep
running across an Arcana restart and are reconciled on start (`destroy_on_shutdown` is pinned to `false`).

## Install

```sh
kubectl create namespace arcana
kubectl label namespace arcana pod-security.kubernetes.io/enforce=privileged
helm install arcana deploy/helm/arcana --namespace arcana \
  --set nodeName=<the node's kubernetes.io/hostname> \
  --set docker.socketGroup=<the socket's GID>
```

`nodeName` and `docker.socketGroup` are required, and `docker.socketGroup` cannot be 0: the pod gets the
socket's group as a supplemental group instead of running as root. The pod runs as a non-root user with a
read-only root filesystem, all capabilities dropped, no privilege escalation and no Kubernetes API token.
On first start Arcana prints an admin API key once and saves it as `admin.key` in the data directory; read it
with `kubectl logs` or from the node. Reach the API with `kubectl port-forward`, and check the install with
`helm test arcana --namespace arcana`.

## Configuration

Values are in `deploy/helm/arcana/values.yaml`. `config` is Arcana's own configuration (see the
[configuration reference](configuration.md)); the chart pins `data_dir`, the listen addresses,
`runtime.driver`, the Docker socket and `destroy_on_shutdown`, and a value cannot override those. Using runc
needs `config.runtime.docker.allow_insecure_runtime: true`; the chart refuses runc without it. By default the
egress proxy uses the same image as the server.

## Troubleshooting

Arcana checks its prerequisites before it listens, so a bad value shows up as a crash loop with a clear
message in `kubectl logs`:

- `permission denied while trying to connect to the docker API`: `docker.socketGroup` is not the GID that owns
  the socket on the node.
- `creating database: open ...: permission denied`: the data directory on the node is not owned by
  `user.uid`/`user.gid` (it must exist and be writable by that user; the chart does not chown it).
- A pod stuck `Pending` or `CreateContainerError` on a `hostPath`: the node lacks the data directory or the
  socket, or the namespace does not allow `hostPath` volumes (see Pod Security above).

## What was and was not verified

`scripts/check_helm.py` (`make helm-check`, and a CI job) lints the chart, validates the rendered objects with
`kubeconform` and asserts the safety properties on the rendered manifests; it also checks that bad or unsafe
values are refused. The rendered configuration loads in Arcana.

`scripts/kind-helm-e2e.sh` (`make helm-e2e`) installs the chart on a throwaway [kind](https://kind.sigs.k8s.io/)
cluster and checks the real thing: the pod starts under its restricted security context and passes Arcana's
startup checks (including a gVisor test container), a sandbox can be created and run commands through the
Service, `helm test` passes, the pod is neither privileged nor writable, an upgrade replaces the pod while the
sandbox, its workspace and the admin key survive, uninstalling leaves the sandbox running with its data in
place, and a wrong socket group produces the error above. It was run from scratch on a 2 vCPU host (Kubernetes
in a Docker container, runsc registered in the host Docker).

That is a stand-in for a real node, not one: the kind node mounts the **host's** Docker socket, so the pod
controls the host Docker Engine rather than one inside a real node, and the node is a container on the same
host. Not tested: a multi-node cluster, other CNIs and a `NetworkPolicy`, an ingress with TLS, nodes where Docker
Engine is installed by a real distribution (for example k3s with cri-dockerd), arm64, and the new CI job on
GitHub.
