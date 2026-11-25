#!/bin/bash
set -euo pipefail
log() { echo "$(date -u +%FT%TZ) $*"; }
# A name that will not resolve, or a registry that answers late, is the network and not a failed upgrade. Retry it, and
# when it still fails say so and leave the release alone: the next run is 15 minutes away.
cx() { curl -fsS --retry 5 --retry-all-errors --retry-delay 3 --connect-timeout 10 --max-time 40 "$@"; }
retry() { local i; for i in 1 2 3 4 5; do "$@" && return 0; sleep 4; done; return 1; }
export HELM_CACHE_HOME=/tmp/helm/cache HELM_CONFIG_HOME=/tmp/helm/config HELM_DATA_HOME=/tmp/helm/data

rel=$(helm list -n "$NS" --filter "^${RELEASE}\$" -o json)
if [ "$(jq length <<<"$rel")" != 1 ]; then
  log "no release $RELEASE in $NS. Not installing anything, that is a person's job."; exit 1
fi
status=$(jq -r '.[0].status' <<<"$rel")
cur=$(jq -r '.[0].chart' <<<"$rel"); cur=${cur#"$CHART"-}
if [ "$status" != deployed ]; then
  log "$RELEASE is $status at $cur. Hands off until a person sorts it out."; exit 1
fi

if ! retry helm repo add src "$REPO_URL" --force-update >/dev/null; then
  log "could not read the chart repo $REPO_URL. $RELEASE stays at $cur. Trying again next run."; exit 0
fi
want=$(helm search repo "src/$CHART" --version ">$cur, $RANGE" -o json | jq -r '.[0].version // empty')
if [ -z "$want" ]; then
  log "$RELEASE at $cur, nothing newer in $RANGE"; exit 0
fi

# The chart can land before its image does. Upgrading to a tag that is not there takes Hearth down, so wait for it.
# Our own cluster runs the private image (IMAGE_REPO below), so the pull credential mounted at /pull (secret `ghcr`) is what reads its tag list.
reg=${IMAGE_REPO%%/*}; path=${IMAGE_REPO#*/}
auth=()
if [ -f /pull/.dockerconfigjson ]; then
  a=$(jq -r --arg r "$reg" '.auths[$r].auth // empty' /pull/.dockerconfigjson)
  [ -n "$a" ] && auth=(-H "Authorization: Basic $a")
fi
if ! tok=$(cx "${auth[@]}" "https://$reg/token?service=$reg&scope=repository:$path:pull" | jq -r .token) || [ -z "$tok" ] || [ "$tok" = null ]; then
  log "could not get a registry token from $reg to look for the $want image. $RELEASE stays at $cur. Trying again next run."; exit 0
fi
code=$(curl -s -o /dev/null -w '%{http_code}' --retry 3 --retry-all-errors --retry-delay 3 --connect-timeout 10 --max-time 40 -I \
  -H "Authorization: Bearer $tok" \
  -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json' \
  "https://$reg/v2/$path/manifests/$want")
if [ "$code" != 200 ]; then
  log "chart $want is out but $IMAGE_REPO:$want is not ($code). Trying again next run."; exit 0
fi

# After --wait the pod is Ready, which says the process started and not that it serves, and the Service can still be handing a
# connection to the pod that is going away for a moment after the roll. Ask Hearth itself, and ask patiently: wait for the
# rollout to finish, then try /healthz and the sign-in page each for about two minutes. A first answer of nothing ('000') is a
# connection that was reset or a page that was not warm yet, not a failed release, so it is retried and only a steady failure
# is reported. /healthz, then the sign-in page, which needs the database and the interface behind it.
probe() { # probe URL max-time accepted-codes...
  local url=$1 mt=$2 code="" i; shift 2
  for i in $(seq 1 20); do
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time "$mt" "$url" || true)
    for ok in "$@"; do [ "$code" = "$ok" ] && { echo "$code"; return 0; }; done
    sleep 6
  done
  echo "${code:-nothing}"; return 1
}
health() {
  local svc="http://${SERVICE}.${NS}.svc.cluster.local" code
  kubectl -n "$NS" rollout status deployment -l app.kubernetes.io/name="$CHART",app.kubernetes.io/component=app --timeout=180s || true
  if ! code=$(probe "$svc/healthz" 10 200); then
    log "HEALTH CHECK FAILED: $svc/healthz answered '$code' for two minutes after $RELEASE was upgraded to $1. Not rolling back; fix forward."
    return 1
  fi
  if ! code=$(probe "$svc/login" 30 200 303); then
    log "HEALTH CHECK FAILED: $svc/login answered '$code' for two minutes at $1. Not rolling back; fix forward."
    return 1
  fi
  log "health check passed at $1: /healthz 200 and the sign-in page $code"
}

log "upgrading $RELEASE $cur -> $want"
helm upgrade "$RELEASE" "src/$CHART" --version "$want" -n "$NS" \
  --reset-then-reuse-values --set image.repository="$IMAGE_REPO" --set image.tag="$want" --wait --timeout 10m
kubectl -n "$NS" get pod -l app.kubernetes.io/name="$CHART",app.kubernetes.io/component=app \
  -o jsonpath='{range .items[*]}{.spec.containers[0].image} {.status.phase} restarts={.status.containerStatuses[0].restartCount}{"\n"}{end}'
log "done: $RELEASE at $want"
health "$want"
