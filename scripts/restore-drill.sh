#!/usr/bin/env bash
# The restore drill, end to end.
#
# Back up the live deployment, restore it into a spare database and a spare
# bucket, bring a server up on the spare, and run the whole acceptance suite
# against it. That last step is the one that turns "the restore loaded" into
# "the restore works", and it is the reason this is a script rather than a
# paragraph in a runbook: a documented drill is a drill nobody runs.
#
# Nothing here writes to the live deployment. It is read, and everything else
# happens somewhere else.
#
#   scripts/restore-drill.sh
#
# Environment, all with sensible local defaults:
#   HEARTH_DATABASE_URL       the live database (read only here)
#   DRILL_DATABASE_URL        a spare database this script will empty and fill
#   DRILL_BUCKET              a spare bucket, likewise
#   DRILL_PORT                where the restored copy listens
#   DRILL_ADMIN_PSQL          how to reach postgres to drop and create the spare
set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${DRILL_WORK:-${TMPDIR:-/tmp}}"
PORT="${DRILL_PORT:-8098}"
BUCKET="${DRILL_BUCKET:-hearth-drill}"
INDEX="${DRILL_INDEX:-hearth-drill}"
ARCHIVE="$WORK/hearth-drill.tar"

: "${HEARTH_DATABASE_URL:=postgres://hearth:test@[::1]:55433/hearth_local?sslmode=disable}"
: "${DRILL_DATABASE_URL:=postgres://hearth:test@[::1]:55433/hearth_drill?sslmode=disable}"
: "${HEARTH_S3_ENDPOINT:=[::1]:59000}"
: "${HEARTH_S3_PUBLIC_ENDPOINT:=[::1]:59000}"
: "${HEARTH_S3_ACCESS_KEY:=hearth}"
: "${HEARTH_S3_SECRET_KEY:=hearthsecret}"
: "${HEARTH_S3_BUCKET:=hearth-local}"
: "${HEARTH_OPENSEARCH_URL:=http://[::1]:59200}"
: "${DRILL_ADMIN_PSQL:=podman exec hearth-pg psql -U hearth -d postgres -q}"
export HEARTH_ENV=development HEARTH_DATABASE_URL HEARTH_S3_ENDPOINT \
       HEARTH_S3_PUBLIC_ENDPOINT HEARTH_S3_ACCESS_KEY HEARTH_S3_SECRET_KEY \
       HEARTH_S3_BUCKET
export HEARTH_PUBLIC_BASE_URL="http://127.0.0.1:$PORT"

cd "$REPO"
echo "== building =="
go build -o "$WORK/hearth.exe" ./cmd/hearth || exit 1
go build -o "$WORK/hearthctl.exe" ./cmd/hearthctl || exit 1
go build -o "$WORK/acceptance.exe" ./cmd/acceptance || exit 1
# The suite reads what a person is shown, which the interface draws from the page's data; so it draws it too (needs node).
( cd ui && npm ci --no-audit --no-fund >/dev/null && npm run build:ssr >/dev/null ) || exit 1

# The spare starts empty every time. A drill that reuses last week's spare is a
# drill that proves the restore can append to itself.
echo
echo "== emptying the spare =="
spare_name="$(printf '%s' "$DRILL_DATABASE_URL" | sed -e 's#.*/##' -e 's#?.*##')"
$DRILL_ADMIN_PSQL -c "DROP DATABASE IF EXISTS \"$spare_name\" WITH (FORCE)" >/dev/null 2>&1
$DRILL_ADMIN_PSQL -c "CREATE DATABASE \"$spare_name\"" >/dev/null 2>&1 || {
  echo "could not create $spare_name"; exit 1; }

echo
echo "== back up, verify, restore, compare =="
"$WORK/hearthctl.exe" drill -to "$DRILL_DATABASE_URL" -bucket "$BUCKET" \
  -keep "$ARCHIVE" || exit 1

echo
echo "== the restored copy, under the acceptance suite =="
# A separate search index, so nothing the restored copy finds could have come
# from the live one still being indexed beside it.
env HEARTH_ADDR=":$PORT" \
    HEARTH_DATABASE_URL="$DRILL_DATABASE_URL" \
    HEARTH_S3_BUCKET="$BUCKET" \
    HEARTH_OPENSEARCH_URL="$HEARTH_OPENSEARCH_URL" \
    HEARTH_OPENSEARCH_INDEX="$INDEX" \
    HEARTH_INBOUND_KEY="drill-inbound-key" \
    HEARTH_LOG_LEVEL="${HEARTH_LOG_LEVEL:-warn}" \
    "$WORK/hearth.exe" > "$WORK/hearth-drill.log" 2>&1 &
pid=$!

for _ in $(seq 1 60); do
  curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 1
done
if ! curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
  echo "the restored copy never became healthy:"
  tail -30 "$WORK/hearth-drill.log"
  kill $pid 2>/dev/null
  exit 1
fi

started=$(date +%s)
"$WORK/acceptance.exe" -render "$REPO/ui/dist-ssr/render.mjs" -base "http://127.0.0.1:$PORT" \
  -email "${DRILL_EMAIL:-local@hearth.invalid}" \
  -password "${DRILL_PASSWORD:-a long enough password}" \
  -inbound-key "drill-inbound-key" "$@"
rc=$?
finished=$(date +%s)

kill $pid 2>/dev/null
wait $pid 2>/dev/null

echo
if [ $rc -eq 0 ]; then
  echo "THE DRILL PASSED. A restore from backup runs the whole acceptance suite."
else
  echo "THE DRILL FAILED. The archive restored and the restored copy does not work."
fi
echo "acceptance against the restored copy took $((finished - started))s"
exit $rc
