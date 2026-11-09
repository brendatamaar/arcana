#!/bin/sh
# Install Arcana from a GitHub release, verifying its SHA-256 checksum.
#
#   curl -fsSLO https://github.com/brendatamaar/arcana/releases/latest/download/install.sh
#   sh install.sh [--version vX.Y.Z] [--systemd]
#
# Options:
#   --version TAG   release tag to install (default: latest release)
#   --systemd       create the arcana user and install the systemd unit
#   --no-doctor     skip running "arcana doctor" after installing
# Environment:
#   ARCANA_BASE_URL  override the release download base (used for testing)
#   PREFIX           install prefix for the binary (default /usr/local)
set -eu

REPO="brendatamaar/arcana"
PREFIX="${PREFIX:-/usr/local}"
VERSION=""
SYSTEMD=0
DOCTOR=1

die() { echo "install.sh: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
	--version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
	--systemd) SYSTEMD=1; shift ;;
	--no-doctor) DOCTOR=0; shift ;;
	-h | --help) sed -n '2,14p' "$0"; exit 0 ;;
	*) die "unknown option: $1" ;;
	esac
done

[ "$(uname -s)" = "Linux" ] || die "Arcana runs on Linux only"
[ "$(id -u)" -eq 0 ] || die "run as root (for example: sudo sh install.sh)"

case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "unsupported architecture: $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"

if [ -z "$VERSION" ]; then
	VERSION="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" | sed 's#.*/##')"
	[ -n "$VERSION" ] || die "could not determine the latest release"
fi
case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac
NUM="${VERSION#v}"
BASE="${ARCANA_BASE_URL:-https://github.com/$REPO/releases/download/$VERSION}"
ARCHIVE="arcana_${NUM}_linux_${ARCH}.tar.gz"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

echo "Downloading Arcana $VERSION ($ARCH)"
curl -fsSL -o "$TMP/$ARCHIVE" "$BASE/$ARCHIVE" || die "download failed: $BASE/$ARCHIVE"
curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt" || die "download failed: $BASE/checksums.txt"

expected="$(awk -v f="$ARCHIVE" '$2 == f { print $1 }' "$TMP/checksums.txt")"
[ -n "$expected" ] || die "no checksum listed for $ARCHIVE"
actual="$(sha256sum "$TMP/$ARCHIVE" | awk '{ print $1 }')"
[ "$expected" = "$actual" ] || die "checksum mismatch for $ARCHIVE (expected $expected, got $actual)"
echo "Checksum verified"

mkdir "$TMP/x"
tar -xzf "$TMP/$ARCHIVE" -C "$TMP/x"
install -d "$PREFIX/bin"
install -m 0755 "$TMP/x/arcana" "$PREFIX/bin/arcana"

install -d -m 0755 /etc/arcana
if [ -e /etc/arcana/config.yaml ]; then
	echo "Keeping existing /etc/arcana/config.yaml"
else
	install -m 0640 "$TMP/x/config.example.yaml" /etc/arcana/config.yaml
fi

if [ "$SYSTEMD" -eq 1 ]; then
	command -v systemctl >/dev/null 2>&1 || die "--systemd needs systemd"
	getent group docker >/dev/null || die "the docker group does not exist; install Docker first"
	id arcana >/dev/null 2>&1 ||
		useradd --system --home-dir /var/lib/arcana --shell /usr/sbin/nologin --gid docker arcana
	install -d -m 0700 -o arcana /var/lib/arcana
	chgrp docker /etc/arcana/config.yaml
	sed "s#^ExecStart=/usr/bin/arcana#ExecStart=$PREFIX/bin/arcana#" "$TMP/x/arcana.service" >/etc/systemd/system/arcana.service
	systemctl daemon-reload
	echo "Installed arcana.service (not started). Start it with: systemctl enable --now arcana"
fi

echo "Installed $("$PREFIX/bin/arcana" version)"

if [ "$DOCTOR" -eq 1 ]; then
	echo "Running arcana doctor"
	if [ "$SYSTEMD" -eq 1 ] && command -v runuser >/dev/null 2>&1; then
		runuser -u arcana -- "$PREFIX/bin/arcana" doctor --config /etc/arcana/config.yaml || die "doctor reported problems; fix them before starting Arcana"
	else
		"$PREFIX/bin/arcana" doctor --config /etc/arcana/config.yaml || die "doctor reported problems; fix them before starting Arcana"
	fi
fi
