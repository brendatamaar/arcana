---
layout: home
hero:
  name: Arcana
  text: Run untrusted code safely, on your own hardware
  tagline: A self-hosted sandbox orchestrator for AI agents. gVisor isolation, no network by default, streamed output, files, snapshots and forks, an MCP server and a read-only dashboard.
  actions:
    - theme: brand
      text: Quickstart
      link: /quickstart
    - theme: alt
      text: Architecture
      link: /architecture
    - theme: alt
      text: Security model
      link: /security
features:
  - title: Safe by default
    details: Sandboxes run under gVisor as a non-root user with every capability dropped, a read-only root filesystem and no network. An escape test suite runs in CI against real sandboxes.
  - title: Made for agent loops
    details: Create a sandbox, run commands with live output, move files in and out, snapshot a workspace and fork it to try several fixes in parallel. A warm pool makes acquisition feel instant.
  - title: Self-hosted and observable
    details: One static binary and one config file. The doctor command diagnoses the host; Prometheus metrics, an audit log and a live dashboard show what is happening.
  - title: Works with your tools
    details: REST and WebSocket API, a Go client, Python and TypeScript SDKs, and an MCP server for Claude and other MCP clients.
---
