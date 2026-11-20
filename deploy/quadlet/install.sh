#!/usr/bin/env bash
# Installs Hearth on one Linux box with rootless Podman and Quadlet: Hearth, Postgres 17
# and Garage (S3 storage), each a systemd user service, all updating themselves from
# their registry. No root, no Docker, no Compose.
#
#   ./install.sh https://hearth.example.com
#
# Needs Podman 5 or newer, systemd, and curl. Run it as the user that will own Hearth.
# Run it again and it keeps every secret and volume it already made.
set -euo pipefail

base_url="${1:?usage: install.sh https://hearth.example.com}"
here="$(cd "$(dirname "$0")" && pwd)"
cfg="$HOME/.config/hearth"
units="$HOME/.config/containers/systemd"

command -v podman >/dev/null || { echo "podman is not installed" >&2; exit 1; }
command -v openssl >/dev/null || { echo "openssl is not installed" >&2; exit 1; }
mkdir -p "$cfg" "$units"
chmod 700 "$cfg"

# Services have to keep running after you log out.
loginctl enable-linger "$USER" >/dev/null 2>&1 || true

secret() { # name, value-command: make the secret once and never overwrite it
  if podman secret inspect "$1" >/dev/null 2>&1; then return; fi
  printf '%s' "$2" | podman secret create "$1" - >/dev/null
}

pg_password="$(openssl rand -hex 24)"
secret hearth-postgres-password "$pg_password"
if ! podman secret inspect hearth-database-url >/dev/null 2>&1; then
  secret hearth-database-url "postgres://hearth:${pg_password}@hearth-postgres:5432/hearth?sslmode=disable"
fi
secret hearth-encryption-key "$(openssl rand -base64 32)"

if [ ! -f "$cfg/garage.toml" ]; then
  cat > "$cfg/garage.toml" <<TOML
metadata_dir = "/var/lib/garage/meta"
data_dir = "/var/lib/garage/data"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "[::]:3901"
rpc_secret = "$(openssl rand -hex 32)"

[s3_api]
s3_region = "garage"
api_bind_addr = "[::]:3900"
TOML
  chmod 600 "$cfg/garage.toml"
fi

if [ ! -f "$cfg/hearth.env" ]; then
  sed "s#^HEARTH_PUBLIC_BASE_URL=.*#HEARTH_PUBLIC_BASE_URL=${base_url}#" "$here/hearth.env.example" > "$cfg/hearth.env"
fi

cp "$here"/*.container "$here"/*.network "$here"/*.volume "$units/"
systemctl --user daemon-reload

systemctl --user start hearth-garage.service
echo "waiting for Garage"
for _ in $(seq 1 60); do
  podman exec hearth-garage /garage status >/dev/null 2>&1 && break
  sleep 2
done

# One-time storage setup: a single node, a bucket, and a key for Hearth.
if ! podman secret inspect hearth-s3-access-key >/dev/null 2>&1; then
  node_id="$(podman exec hearth-garage /garage node id -q 2>/dev/null | cut -d@ -f1)"
  podman exec hearth-garage /garage layout assign -z home -c 20G "$node_id" >/dev/null 2>&1
  podman exec hearth-garage /garage layout apply --version 1 >/dev/null 2>&1
  podman exec hearth-garage /garage bucket create hearth >/dev/null 2>&1
  key_out="$(podman exec hearth-garage /garage key create hearth-app 2>/dev/null)"
  key_id="$(sed -n 's/^Key ID: *//p' <<<"$key_out" | head -n1)"
  key_secret="$(sed -n 's/^Secret key: *//p' <<<"$key_out" | head -n1)"
  podman exec hearth-garage /garage bucket allow --read --write --owner hearth --key hearth-app >/dev/null 2>&1
  secret hearth-s3-access-key "$key_id"
  secret hearth-s3-secret-key "$key_secret"
fi

systemctl --user start hearth-postgres.service hearth.service

# Pull new images when the registry has them, once a day, and restart the service that uses them.
systemctl --user enable --now podman-auto-update.timer >/dev/null
echo
echo "Hearth is starting at ${base_url} (listening on 127.0.0.1:8080)."
echo "Put a TLS reverse proxy in front of it, then open the address and create the owner account."
echo "Settings live in $cfg/hearth.env. After editing it: systemctl --user restart hearth"
