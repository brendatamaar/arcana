# Allowlist networking

The default remains `none`. Untrusted sandboxes never get an outbound route.
An allowlist sandbox receives its own internal Docker network and an isolated
UID 1000 runsc proxy, with all interfaces attached before startup. The proxy
alone also joins Docker's outbound bridge. One proxy per sandbox keeps policy
and lifecycle independent; each proxy has a 256 MiB memory limit, 0.5 CPU and
64 PIDs, in addition to the sandbox's own limits.

Build the local proxy image with `make proxy-image` and configure
`network.proxy_image: arcana:local`. Releases publish the default
GHCR image. DNS upstreams are explicit operator-configured IPs; Docker
embedded DNS is not used for destination resolution. Choose a subnet pool
disjoint from other host routes and Docker networks. Doctor checks overlap,
and allocations are derived from Docker state, including after restart.

Create using `arcana sandbox create --template python --network allowlist
--network-preset python --allow github.com`, or the equivalent REST network
object. Presets and exact DNS names combine up to `network.max_allow_entries`
(maximum 50). Wildcards and IP literals are rejected. Mode `open` is unsupported.
HTTP_PROXY/HTTPS_PROXY and lowercase equivalents are injected with empty
NO_PROXY; applications that bypass proxies fail to reach the internet.

Only ports 80 and 443 are supported. HTTPS uses CONNECT without TLS
interception. Plain HTTP requires matching URL authority and Host. Every DNS
answer is checked; private, loopback, link-local, multicast, documentation,
translation and other reserved ranges are rejected. The proxy dials the
already-validated address and never resolves it again during connection.

Proxy control uses a private container-local Unix socket reached by a bounded
trusted Docker exec command. No control ports or host directories are exposed.
Only a hash of the random control token is in Docker configuration; the raw
token travels over the trusted Docker exec channel. Host access to the Docker
socket remains root-equivalent. Proxies have readonly roots, dropped
capabilities, no-new-privileges, bounded tmpfs and no Docker socket.

Connections are capped at 128, tunnels at five minutes, HTTP uploads at 50 MiB
and downloads at 64 MiB. Control connections are capped at 16. Metadata-only
decisions use a 1024-entry queue with overflow counting; the audit writer connects this
queue to SQLite storage. No headers, credentials, URLs or bodies are
recorded. A restart recreates proxies, interrupting existing connections while
preserving healthy sandboxes. Stable internal proxy IP/MAC addresses keep
sandbox neighbour caches valid. Orphaned proxies and networks are removed
before API admission.
