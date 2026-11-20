#!/usr/bin/env bash
# Renders the chart in the ways a person hosting Hearth themselves will use it, and
# checks that what they set arrives in the pod. Needs helm, nothing else.
set -euo pipefail
cd "$(dirname "$0")/.."
chart=deploy/charts/hearth
fail=0

render() { helm template hearth "$chart" -n hearth "$@" 2>&1; }
expect() { # name, text, pattern
  if grep -q -E -- "$3" <<<"$2"; then echo "ok: $1"; else echo "FAIL: $1 (wanted /$3/)"; fail=1; fi
}
reject() {
  if grep -q -E -- "$3" <<<"$2"; then echo "FAIL: $1 (did not want /$3/)"; fail=1; else echo "ok: $1"; fi
}

out=$(render)
expect "bundled postgres is the default" "$out" 'name: hearth-postgres|kind: StatefulSet'
expect "the default database URL points at the bundled server" "$out" 'sslmode=disable'

out=$(render --set postgresql.enabled=false --set externalDatabase.host=db.example.com --set externalDatabase.sslmode=verify-full)
expect "external host reaches the URL" "$out" '@db.example.com:5432/hearth\?sslmode=verify-full'
expect "the pod waits for the external host" "$out" 'nc -z db.example.com 5432'
reject "no bundled postgres is rendered" "$out" 'component: postgres'

out=$(render --set postgresql.enabled=false --set externalDatabase.dsnKey=DATABASE_DSN)
expect "a DSN is read from the secret" "$out" 'key: DATABASE_DSN'
reject "a DSN means no wait on a made-up host" "$out" 'nc -z'

if render --set postgresql.enabled=false >/dev/null 2>&1; then
  echo "FAIL: turning off postgres without saying where it is should not render"; fail=1
else
  echo "ok: no database and no address refuses to render"
fi

out=$(render --set s3.endpoint=objects.example.com:443 --set s3.region=eu-central-1 --set s3.useSSL=true --set s3.pathStyle=true)
expect "s3 endpoint" "$out" 'value: "objects.example.com:443"'
expect "s3 region" "$out" 'HEARTH_S3_REGION'
expect "s3 tls" "$out" 'HEARTH_S3_USE_SSL'
expect "s3 path style" "$out" 'HEARTH_S3_PATH_STYLE'

out=$(render --set mail.smtp.host=smtp.example.com:587 --set mail.smtp.from=hearth@example.com --set mail.smtp.username=hearth)
expect "smtp host" "$out" 'HEARTH_SMTP_HOST'
expect "smtp password from the secret" "$out" 'key: SMTP_PASSWORD'

out=$(render --set oidc.enabled=true --set oidc.issuer=https://accounts.google.com --set oidc.clientId=abc --set oidc.allowedDomains=example.com)
expect "any oidc issuer" "$out" 'value: "https://accounts.google.com"'

out=$(render)
n=$(grep -c "kind: NetworkPolicy" <<<"$out" || true)
if [ "$n" = "0" ]; then echo "ok: a default render has no NetworkPolicy at all"; else echo "FAIL: a default render has $n NetworkPolicy objects, wanted 0"; fail=1; fi
out=$(render | awk 'BEGIN{RS="\n---\n"} /kind: NetworkPolicy/ && /name: hearth-garage\n/ {print "ingress-policy"}')
reject "the object store has no ingress NetworkPolicy by default" "$out" 'ingress-policy'
out=$(render --set garage.networkPolicy.enabled=true | awk 'BEGIN{RS="\n---\n"} /kind: NetworkPolicy/ && /name: hearth-garage\n/ {print "ingress-policy"}')
expect "and it can be turned on" "$out" 'ingress-policy'

out=$(render --set opensearch.enabled=false)
reject "opensearch is off by default" "$out" 'component: opensearch'

exit $fail
