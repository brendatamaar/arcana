# 0004: Internal networks and an outbound CONNECT proxy

Status: accepted with measured setup requirements. Date: 2026-11-01.
Tasks: T0.5, T0.6.

Keep one internal Docker network per allowlist sandbox, allocated from the
configured pool with /28 subnets. A proxy connects to that internal network and
a separate outbound network. Only the proxy resolves upstream destinations and
opens allowed connections. Default sandbox network mode remains none; open
mode and TLS interception remain unsupported.

[Evidence and topology](../spikes/004-network.md) show an allowed CONNECT
establishing upstream TCP, denied hostname/metadata proxy requests, no direct
internet/metadata route, and isolation from a live listener in a second network.
Both sandboxes and Docker remained healthy.

runsc requires its full interface set before start in the tested setup. Create,
connect, then start the proxy. Recreate it when that set changes and reconcile
networks consistently; document connection interruption during that operation.

Do not rely on Docker embedded DNS at 127.0.0.11 for runsc user-defined bridges.
Configure upstream resolvers on the outbound proxy only (`network.proxy_dns`).
The spike used 1.1.1.1. The proxy must validate the hostname, resolve, reject
forbidden IPv4/IPv6 ranges and dial the checked address without re-resolution.
These choices add no direct DNS or internet route to untrusted sandboxes.

The test proxy is a small CONNECT-only fixture. T2.4 must implement plain HTTP,
complete reserved-range rejection, per-sandbox policies, bounded concurrency,
byte counts, control channel and audit; T2.8 must implement all network escape
cases. Do not treat the Phase 0 smoke tests as that production escape suite.
