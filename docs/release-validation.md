# v0.1.0 release validation

The release candidate was validated on 2026-11-03 using the owner-authorized
Linux VPS, with new private demo state inside the project workspace. Running
`python3 scripts/check_quickstart.py` executed the marked README blocks verbatim:

- `go install` produced the v0.1.0-stamped binary with embedded helpers.
- Doctor passed the Linux/cgroups/Docker/runsc/data/database/image prerequisites.
  Missing KVM was an informational warning.
- The daemon became healthy on localhost and created its private admin key.
- A sandbox printed `Hello from Arcana` through streaming exec.
- File put/get preserved the input, verified by `cmp`.
- Sandbox removal and graceful daemon shutdown completed successfully.

The GitHub security job also executes this checker on `ubuntu-24.04` before
integration/escape tests. This uses a new Arcana install and new data directory
on a [fresh GitHub-hosted VM](https://docs.github.com/en/actions/reference/runners/github-hosted-runners),
with the documented Go/Docker prerequisites already supplied by the runner image
and gVisor installed from its official apt repository. Before the release tag is
published, CI checks out the candidate commit; the same files are tagged only
after verification succeeds.

The checker executes the README commands instead of a separate implementation
of the quickstart. It bounds execution time and terminates its process group on
timeout. The demo explicitly opts into destruction on shutdown; production
defaults and all sandbox hardening remain unchanged. Bootstrap logs and keys stay
in the private demo directory and are not printed to CI output.

The [candidate CI run](https://github.com/brendatamaar/arcana/actions/runs/41587693910)
passed for commit f8c400f, including the README quickstart on the new Ubuntu VM,
make check, integration/escape under runsc, and security-tagged lint. Windows
make check and the Linux static build also passed. The final documentation-only
amendment is checked again before publishing the annotated release tag.
