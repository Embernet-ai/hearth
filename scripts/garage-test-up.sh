#!/usr/bin/env bash
# Starts a throwaway single-node Garage for the object store tests and prints the
# variables the tests read. It is what CI runs, and it works the same on a laptop.
#
#   eval "$(scripts/garage-test-up.sh)"      # start it and export TEST_GARAGE_*
#   podman rm -f hearth-test-garage          # and when done
#
# Podman, never Docker. Garage needs a layout before it will serve, and a key that
# may create buckets (the tests make a bucket of their own each).
set -euo pipefail

name="${GARAGE_TEST_NAME:-hearth-test-garage}"
port="${GARAGE_TEST_PORT:-3900}"
image="${GARAGE_TEST_IMAGE:-docker.io/dxflrs/garage:v2.4.1}"
dir="$(mktemp -d)"
trap 'rm -f "$dir/garage.toml"' EXIT

cat > "$dir/garage.toml" <<TOML
metadata_dir = "/tmp/meta"
data_dir = "/tmp/data"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "[::]:3901"
rpc_secret = "$(openssl rand -hex 32)"

[s3_api]
s3_region = "garage"
api_bind_addr = "[::]:3900"
TOML

podman rm -f "$name" >/dev/null 2>&1 || true
podman run -d --name "$name" -p "127.0.0.1:${port}:3900" \
  -v "$dir/garage.toml:/etc/garage.toml:ro,Z" "$image" >/dev/null

g() { podman exec "$name" /garage "$@"; }
for _ in $(seq 1 60); do g status >/dev/null 2>&1 && break; sleep 1; done
g status >/dev/null 2>&1 || { podman logs "$name" >&2; echo "garage never came up" >&2; exit 1; }

id="$(g node id -q 2>/dev/null | cut -d@ -f1)"
g layout assign -z dc1 -c 1G "$id" >/dev/null
g layout apply --version 1 >/dev/null

key_id="GK$(openssl rand -hex 12)"
key_secret="$(openssl rand -hex 32)"
g key import --yes -n hearth-test "$key_id" "$key_secret" >/dev/null
g key allow --create-bucket hearth-test >/dev/null

# Garage answers S3 once the layout is live.
for _ in $(seq 1 60); do
  [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/")" != "000" ] && break
  sleep 1
done

echo "export TEST_GARAGE_ENDPOINT=127.0.0.1:${port}"
echo "export TEST_GARAGE_ACCESS_KEY=${key_id}"
echo "export TEST_GARAGE_SECRET_KEY=${key_secret}"
echo "export TEST_GARAGE_REGION=garage"
