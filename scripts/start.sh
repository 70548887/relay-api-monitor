#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
# Foreground mode: systemd owns shutdown and reaps both children.
if [ -z "${RELAYLAB_CONFIG:-}" ] && [ -f "$root/config.yaml" ]; then
  export RELAYLAB_CONFIG="$root/config.yaml"
fi
exec "$root/bin/relaylab-launcher" --relaylab "$root/bin/relaylab" "$@"
