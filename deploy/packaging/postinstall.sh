#!/bin/sh
# Package post-install: create the service account and data directory only.
# The service is never enabled or started automatically.
set -eu
getent group docker >/dev/null || groupadd --system docker
id arcana >/dev/null 2>&1 || useradd --system --home-dir /var/lib/arcana --shell /usr/sbin/nologin --gid docker arcana
install -d -m 0700 -o arcana /var/lib/arcana
chgrp docker /etc/arcana/config.yaml 2>/dev/null || true
if command -v systemctl >/dev/null 2>&1; then systemctl daemon-reload >/dev/null 2>&1 || true; fi
exit 0
