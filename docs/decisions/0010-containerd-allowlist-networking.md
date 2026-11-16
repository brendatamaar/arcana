# 0010: allowlist networking for the containerd driver

Status: accepted; **run against a real containerd with gVisor and runc, nested in a container** (see
Verification). Date: 2026-11-16. Task: T4.5 follow-up. The owner chose this design over a lighter one that
would have changed PRD FR-NW-2.

The containerd driver (ADR 0009) supported network mode `none` only. This adds `allowlist` with the Docker
driver's guarantees (FR-NW-2, FR-NW-3, ESC-15 to ESC-19), built on CNI instead of Docker networks. It is off
unless `runtime.containerd.allowlist` is true.

## Design

```
sandbox netns            private bridge arcXXXXXXXXXXXX             proxy netns               shared bridge
 eth0 169.254.77.2/29 ── (no address, no route on the host) ── eth1 169.254.77.3/29  eth0 172.29.x.y ── arcana-egress0 ── NAT ── internet
```

- Each sandbox gets a network namespace created by Arcana and joined by its container. Its only interface sits
  on a private L2 bridge with no IP of its own, so the host has no route to it, and the sandbox has one route,
  the `/29` link, and no default route.
- A dedicated proxy container (`prx-<sandbox id>`, the `arcana proxy` command from `network.proxy_image`, UID 1000,
  no capabilities, read-only root, 64 PIDs, 256 MB) is the only other thing on that bridge. Its second interface
  is on one shared bridge `arcana-egress0` with a gateway, a default route and masquerade, like Docker's default
  bridge. The control channel is the Docker driver's, now shared code (`netpolicy.Control`): the `proxy-control`
  helper run inside the proxy container; the token is held in memory and only its hash is in the container's
  environment.
- Sandboxes get `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY=` pointing at `169.254.77.3:3128`, replacing anything
  they were given. Direct connections fail with `ENETUNREACH` because there is no route.
- **The link addresses are fixed and reused by every sandbox.** Each sandbox's bridge is a separate segment in
  separate namespaces, so the addresses never meet. This replaces the Docker driver's per-sandbox subnet
  allocation: there is nothing to allocate, store, or exhaust. The proxy's MAC is derived from its address, so a
  recreated proxy looks identical to the sandbox's neighbour cache. The shared egress subnet
  (`runtime.containerd.egress_subnet`, default `172.29.0.0/20`) is the only address space, and bounds concurrent
  allowlist sandboxes to its size.
- **No CNI libraries.** `cni.go` runs the plugin binaries as the specification defines (environment plus
  configuration on stdin). The plugins `bridge`, `host-local` and `static` are a host prerequisite, like runsc.
  Namespaces are created with `unshare` and a bind mount, and a bridge is removed with one `RTM_DELLINK` netlink
  request, in the already approved `golang.org/x/sys`. No new Go dependency.
- **Needs root.** Creating namespaces and running the bridge plugin need `CAP_SYS_ADMIN` and `CAP_NET_ADMIN`,
  so Arcana must run as root for allowlist sandboxes; Create refuses otherwise, and `arcana doctor` reports it
  with the other prerequisites (plugins present, proxy image in the namespace, egress subnet not overlapping the
  host's addresses or routes).
- **Lifecycle.** Create builds sandbox namespace, proxy namespace and all attachments, starts the proxy,
  pushes the policy and waits until it answers, and only then starts the sandbox. Any failure unwinds in
  reverse. Destroy flushes a last batch of decisions, then removes the proxy, every CNI attachment, the bridge
  and both namespaces, continuing past failures. At startup `PruneOrphans` removes networks nothing keeps, and
  rebuilds the proxy of every kept allowlist sandbox with the allowlist read back from a label on the sandbox
  container (`arcana.allow`).

## Defects found by running it for real

Unit tests with a fake host passed before any of these:

1. **The gVisor shim resolves containers by ID prefix.** A proxy named `sbx-<id>-proxy` made `sbx-<id>`
   ambiguous and the sandbox unloadable. The proxy is `prx-<id>`; a test asserts no name prefixes another.
2. **gVisor takes interfaces over.** A runsc proxy strips its interfaces' addresses from the namespace when it
   starts, so a second proxy started in the same namespace (after an Arcana restart) had a link with no address.
   Restoring a proxy now rebuilds its whole network (CNI DEL, new namespace, ADD at the same address and MAC)
   and leaves the sandbox alone; a test asserts that order. runc did not show this.
3. **CNI DEL can fail with "no such device".** gVisor tears down a stopped sandbox's interfaces itself, so the
   bridge plugin finds the link and then loses it. DEL must be idempotent, so exactly that message on DEL is
   treated as success; ADD and every other failure surface.
4. **Doctor rejected its own bridge.** After the first allowlist sandbox, the egress bridge's connected route
   overlaps the egress subnet, so the overlap check failed at the next start. Routes of our own bridge are
   ignored.

## Verification

Run on a real containerd 2.2 (nested in a privileged container, as ADR 0009) with the pinned CNI plugins
v1.6.2, under **runsc and runc**, against the real internet:

- `TestContainerd_AllowlistNetworking` (as root inside the containerd container): allowlisted host over HTTP and
  CONNECT, a host off the list gets 403, direct connections to the internet, the egress gateway and a private
  address fail with `ENETUNREACH`, only one route and not a default one, two sandboxes with separate proxies,
  decisions reported, destroy removes the bridge and namespaces and leaves the other sandbox working, and a
  restarted driver replaces the proxy and keeps the allowlist.
- `scripts/containerd-escape.sh`: ESC-15 to ESC-19 through the public API (`TestEscapeV2_ContainerdNetworkPublicAPI`),
  including DNS rebinding against a trusted resolver and the audit trail of every refusal. A mutation check
  (giving the sandbox the egress bridge) made ESC-15 fail.
- `scripts/containerd-allowlist-e2e.sh`: a real `arcana serve` as root: doctor, an allowlisted and a refused
  request, no direct route, `egress.decision` events in the audit log, a restart that replaces the proxy, and a
  destroy that leaves no namespace or bridge.

## Limits

- **Nested harness only.** Not run on a bare host, on another CNI plugin version, with `iptables-legacy`, or
  under ufw/firewalld. The harness's own iptables has an ACCEPT forward policy.
- **Host firewalls.** The bridge plugin adds NAT rules, not forwarding rules. A host whose `FORWARD` policy is
  DROP (Docker, ufw and firewalld set one) needs to allow forwarding for `arcana-egress0`, or the proxy cannot
  reach the internet. The CNI `firewall` plugin would handle this but needs plugin chaining, which is not built.
  `arcana doctor` does not check it.
- IPv4 only. The proxy's resolvers must be reachable from the egress bridge.
- The proxy can reach the host through the egress bridge's gateway address, like Docker's bridge; it only
  connects to allowlisted names that resolve to public addresses, and ESC-17 includes the gateway.
- Nothing measured for latency or density. The test run times include the unexplained 5 s runsc container
  create of ADR 0009 for every container, two per allowlist sandbox.
- Firecracker still supports network mode `none` only.
