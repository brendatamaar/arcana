#!/usr/bin/env bash
# Build a read-only ext4 rootfs for the Firecracker driver from a template image.
#
#   sudo scripts/build-rootfs.sh <docker-image> <output.ext4> [envd-binary] [arcana-init-binary]
#
# Needs root (so file ownership in the image survives), Docker, GNU tar and mke2fs. Run it
# only on images you built or trust: the image's files are extracted on this host as root,
# the same trust you already place in the Docker daemon. The result gets envd, an
# `sbx-exec` link to it, the guest init and the image's ENV (a filesystem export drops it).
set -euo pipefail

if [ "$#" -lt 2 ]; then
	echo "usage: $0 <docker-image> <output.ext4> [envd-binary] [arcana-init-binary]" >&2
	exit 2
fi
image=$1
output=$2
envd=${3:-bin/envd}
init=${4:-bin/arcana-init}
if [ "$(id -u)" -ne 0 ]; then
	echo "run as root so image file ownership is preserved" >&2
	exit 1
fi
for file in "$envd" "$init"; do
	[ -x "$file" ] || { echo "missing executable: $file (run make envd arcana-init)" >&2; exit 1; }
done

work=$(mktemp -d)
container=
cleanup() {
	[ -z "$container" ] || docker rm -f "$container" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

root=$work/root
mkdir -p "$root"
container=$(docker create "$image" /bin/true)
# --numeric-owner keeps the image's UIDs; GNU tar refuses members that climb out of $root.
docker export "$container" | tar -x --numeric-owner --no-overwrite-dir -C "$root"

mkdir -p "$root/opt/arcana" "$root/etc/arcana" "$root/sbin" "$root/proc" "$root/sys" "$root/dev" "$root/tmp" "$root/run" "$root/workspace"
install -m 0755 "$envd" "$root/opt/arcana/envd"
ln -sf envd "$root/opt/arcana/sbx-exec"
install -m 0755 "$init" "$root/sbin/arcana-init"
docker image inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$image" >"$root/etc/arcana/env"
chown 1000:1000 "$root/workspace"
chmod 1777 "$root/tmp"

used_kib=$(du -sk "$root" | cut -f1)
size_mib=$((used_kib * 13 / 10 / 1024 + 64))
rm -f "$output"
truncate -s "${size_mib}M" "$output"
mke2fs -q -t ext4 -F -m 0 -O ^has_journal -L rootfs -d "$root" "$output"
chmod 0444 "$output"
echo "wrote $output (${size_mib} MiB) from $image"
