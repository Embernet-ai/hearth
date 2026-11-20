#!/usr/bin/env bash
# Build and deploy to a Kubernetes cluster, tagged with the git sha.
#
# The tag is the git sha and never a moving name like :dev or :latest. This is
# not tidiness. A mutable tag with imagePullPolicy: IfNotPresent means the
# Deployment spec does not change between deploys, so Kubernetes has no reason
# to do anything, and the pod keeps serving old code while every probe reports
# healthy. That failure is completely silent and it has already happened once.
#
#   scripts/deploy.sh [namespace]
set -euo pipefail

NS="${1:-hearth}"
RELEASE="${HEARTH_RELEASE:-hearth}"
REPO="${HEARTH_IMAGE_REPO:-ghcr.io/embernet-ai/hearth}"

cd "$(dirname "$0")/.."

if [ -n "$(git status --porcelain)" ]; then
  echo "warning: working tree is dirty, the deployed binary will report dirty=true" >&2
fi

SHA=$(git rev-parse --short HEAD)
IMG="$REPO:sha-$SHA"

echo "==> building $IMG"
podman build -t "$IMG" .
echo "    size: $(podman image inspect "$IMG" --format '{{.Size}}' | awk '{printf "%.1f MB", $1/1024/1024}')"

if [ "${HEARTH_PUSH:-1}" = "1" ]; then
  echo "==> pushing"
  podman push "$IMG"
fi

# k3s uses containerd, not a container daemon, so a locally built image has to be
# imported for a single-node cluster to see it without a registry round trip.
if command -v k3s >/dev/null 2>&1; then
  echo "==> importing into k3s containerd"
  podman save "$IMG" | k3s ctr images import - >/dev/null
fi

echo "==> rolling out to $NS"
# The chart is the only description of the deployment, so a rollout is a helm
# upgrade with the tag overridden, not a kubectl set image that would drift from
# what the chart says and be silently reverted by the next helm run.
#
# Deliberately no --wait. It hides the rollout behind a spinner and then reports
# a timeout with nothing useful in it, and a failed --wait leaves the release in
# a pending state that blocks the next upgrade.
helm upgrade --install "$RELEASE" "$(dirname "$0")/../deploy/charts/hearth" \
  -n "$NS" --set image.tag="sha-$SHA"
kubectl -n "$NS" rollout status deployment/"$RELEASE" --timeout=180s

# Verify the thing that is running is the thing that was built. /healthz reports
# the VCS revision stamped into the binary at compile time, so this compares the
# artifact against the commit rather than trusting the rollout.
echo "==> verifying the deployed revision"
want=$(git rev-parse HEAD)
got=$(kubectl -n "$NS" exec deploy/"$RELEASE" -- /hearth --version 2>/dev/null || true)
if [ -z "$got" ]; then
  # The binary has no --version flag; ask the running server instead.
  got=$(kubectl -n "$NS" run verify-$$ --rm -i --restart=Never --quiet \
        --image=curlimages/curl:8.11.1 -- \
        -s "http://$RELEASE.$NS.svc.cluster.local/healthz" 2>/dev/null \
        | grep -o '"revision":"[^"]*"' | cut -d'"' -f4 || true)
fi
if [ "$got" = "$want" ]; then
  echo "    ok: running $got"
else
  echo "    MISMATCH: running '$got', expected '$want'" >&2
  exit 1
fi
