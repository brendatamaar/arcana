# Installing Arcana

Arcana needs a Linux host (amd64 or arm64) with Docker Engine and, for the
default isolation, [gVisor](https://gvisor.dev/docs/user_guide/install/) (`runsc`)
registered as a Docker runtime. Use a dedicated host or VM: access to the
Docker socket is root-equivalent. Run `arcana doctor` after any install to
check the prerequisites; it prints a fix hint for every failure.

## Installer script (recommended)

```sh
curl -fsSLO https://github.com/brendatamaar/arcana/releases/latest/download/install.sh
sudo sh install.sh --systemd
```

The script detects the architecture, downloads the release archive and
`checksums.txt`, **refuses to continue unless the SHA-256 matches**, installs
`/usr/local/bin/arcana`, keeps any existing `/etc/arcana/config.yaml`, and runs
`arcana doctor`. With `--systemd` it also creates an `arcana` system user in the
`docker` group, a `0700` `/var/lib/arcana`, and `/etc/systemd/system/arcana.service`.
The service is installed but not started:

```sh
sudo systemctl enable --now arcana
sudo cat /var/lib/arcana/admin.key   # one-time bootstrap admin key
```

Options: `--version vX.Y.Z` pins a release, `--no-doctor` skips the final check.
Read the script before running it; it is short and `shellcheck`-clean.

## Packages

Each release attaches `.deb` and `.rpm` packages for amd64 and arm64:

```sh
sudo dpkg -i arcana_<version>_linux_amd64.deb    # Debian, Ubuntu
sudo rpm -i arcana_<version>_linux_amd64.rpm     # Fedora, RHEL
```

Packages install `/usr/bin/arcana`, the unit `/lib/systemd/system/arcana.service`
and `/etc/arcana/config.yaml` (marked as a config file, so upgrades keep your
edits). The post-install step creates the `arcana` user and data directory but
never starts the service. Verify downloads against `checksums.txt` from the
release page.

## Container image

```sh
docker pull ghcr.io/brendatamaar/arcana:<version>
```

The multi-arch image is the same static binary, runs as UID 1000 and is also the
default allowlist-proxy image (`network.proxy_image`). To run the daemon in a
container use the ready-made Compose stack in
[`deploy/compose`](../deploy/compose/README.md), which mounts the Docker socket
and the data directory at the same absolute path.

## From source

```sh
go install github.com/brendatamaar/arcana/cmd/arcana@latest
```

or `make build` in a checkout (`bin/arcana`). See the README quickstart for a
complete walkthrough on Ubuntu 24.04.

## Upgrading

Replace the binary (rerun the installer, install the newer package, or pull the
new image) and restart. Database migrations run at startup. Back up the data
directory first (see [operations](operations.md)). Downgrades are not supported.
