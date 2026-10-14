#!/bin/sh
set -eu
# The key remains in a secret file, not in Compose configuration or command arguments.
TS_AUTHKEY=$(cat /run/secrets/tailscale_auth_key)
[ -n "$TS_AUTHKEY" ] || { echo "Missing Tailscale auth key" >&2; exit 1; }
export TS_AUTHKEY
exec /usr/local/bin/containerboot
