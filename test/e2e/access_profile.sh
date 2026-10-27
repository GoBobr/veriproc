#!/usr/bin/env bash
# e2e: access-profile flow against a local daemon.
#
# Exercises, end to end and without restarting the daemon:
#   1. bootstrap an operator entry in the access profile (out-of-band, file owner)
#   2. `veriproc users add` grants a reader subject (operator-gated, token printed once)
#   3. the reader token authenticates for reads against the running daemon
#   4. a reader is refused on a mutating endpoint (403)
#   5. `veriproc users revoke` removes the reader (operator-gated)
#   6. the revoked token is rejected immediately - no daemon restart
#
# Output is the artifact: run with `bash test/e2e/access_profile.sh 2>&1 | tee out.log`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

WORK="$(mktemp -d /tmp/veriproc-e2e.XXXXXX)"
trap 'kill "${DAEMON_PID:-}" 2>/dev/null || true; rm -rf "$WORK"' EXIT

PROFILE="$WORK/access.profile"
ADDR="127.0.0.1:18471"
API="http://$ADDR"

step() { printf '\n=== %s\n' "$*"; }
ok()   { printf 'OK: %s\n' "$*"; }
die()  { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

step "build binaries"
go build -o "$WORK/veriprocd" ./cmd/veriprocd
go build -o "$WORK/veriproc"  ./cmd/veriproc
ok "veriprocd + veriproc built"

step "bootstrap: seed the first operator entry (file owner, out-of-band)"
OPERATOR_TOKEN="$(openssl rand -hex 32 2>/dev/null || head -c32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
printf 'root:operator:%s  # bootstrap operator\n' "$OPERATOR_TOKEN" > "$PROFILE"
chmod 600 "$PROFILE"
ok "profile created at $PROFILE (mode 600)"

step "start daemon with the access profile (anonymous readers enabled)"
mkdir -p "$WORK/stations" "$WORK/roots"
export VERIPROC_ACCESS_PROFILE="$PROFILE"
export VERIPROC_ALLOW_ANONYMOUS_READERS=1
export VERIPROC_API_URL="$API"
export VERIPROC_TOKEN="$OPERATOR_TOKEN"
"$WORK/veriprocd" \
  --http-addr "$ADDR" \
  --instance-id e2e-access \
  --log-level warn &
DAEMON_PID=$!
for _ in $(seq 1 100); do
  curl -sf "$API/health" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -sf "$API/health" >/dev/null || die "daemon did not become healthy"
ok "daemon healthy at $API"

step "users list (operator credential, no tokens printed)"
LIST_OUT="$("$WORK/veriproc" users list --profile "$PROFILE")"
printf '%s\n' "$LIST_OUT"
echo "$LIST_OUT" | grep -q "root" || die "list missing bootstrap operator"
echo "$LIST_OUT" | grep -qE "[0-9a-f]{32,}" && die "list leaked a token" || true
ok "list shows subjects only"

step "users add: grant a reader (operator-gated, token printed once)"
ADD_OUT="$("$WORK/veriproc" users add --profile "$PROFILE" --label 'E2E reader' e2e-reader reader)"
printf '%s\n' "$ADD_OUT"
READER_TOKEN="$(printf '%s\n' "$ADD_OUT" | sed -n 's/.*store it now): //p')"
[ -n "$READER_TOKEN" ] || die "add did not print a token"
grep -q "sha256:" "$PROFILE" || die "token not stored in digest form"
grep -q "$READER_TOKEN" "$PROFILE" && die "plaintext token written to profile" || true
ok "reader granted; token stored as digest only"

step "users add gate: a reader credential cannot grant users"
if VERIPROC_TOKEN="$READER_TOKEN" "$WORK/veriproc" users add --profile "$PROFILE" mallory reader 2>"$WORK/gate.err"; then
  die "reader was allowed to add a user"
fi
grep -qi "operator access is required" "$WORK/gate.err" || die "gate message missing"
ok "reader refused (exit $?) with operator-gate message"

step "authenticate with the reader token (read succeeds)"
# The daemon re-reads the profile at most once per reload interval (5s), so
# retry until the grant propagates.
TASKS_CODE=000
for _ in $(seq 1 70); do
  TASKS_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $READER_TOKEN" "$API/api/v1/tasks")"
  [ "$TASKS_CODE" = "200" ] && break
  sleep 0.1
done
[ "$TASKS_CODE" = "200" ] || die "reader read got $TASKS_CODE, want 200"
ok "reader read -> 200 (grant propagated without restart)"

step "reader is refused on a mutating endpoint"
MUT_CODE="$(curl -s -o "$WORK/mut.json" -w '%{http_code}' \
  -X POST -H "Authorization: Bearer $READER_TOKEN" -H 'Content-Type: application/json' \
  -d '{"station_id":"X"}' "$API/api/v1/stations/X/pause")"
[ "$MUT_CODE" = "403" ] || die "reader mutation got $MUT_CODE, want 403"
grep -qi "operator" "$WORK/mut.json" || die "403 body should name operator requirement"
ok "reader mutation -> 403 (operator required)"

step "anonymous read is admitted (allow_unauthenticated_readers)"
ANON_CODE="$(curl -s -o /dev/null -w '%{http_code}' "$API/api/v1/tasks")"
[ "$ANON_CODE" = "200" ] || die "anonymous read got $ANON_CODE, want 200"
ANON_MUT="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"station_id":"X"}' "$API/api/v1/stations/X/pause")"
[ "$ANON_MUT" = "401" ] || die "anonymous mutation got $ANON_MUT, want 401"
ok "anonymous read -> 200, anonymous mutation -> 401"

step "users revoke: remove the reader (operator-gated)"
REV_OUT="$("$WORK/veriproc" users revoke --profile "$PROFILE" e2e-reader)"
printf '%s\n' "$REV_OUT"
grep -q "e2e-reader" "$PROFILE" && die "reader still in profile" || true
ok "reader revoked"

step "revoked token rejected immediately (no daemon restart)"
REVOKE_CODE=200
for _ in $(seq 1 70); do
  REVOKE_CODE="$(curl -s -o "$WORK/rev.json" -w '%{http_code}' -H "Authorization: Bearer $READER_TOKEN" "$API/api/v1/tasks")"
  [ "$REVOKE_CODE" = "401" ] && break
  sleep 0.1
done
[ "$REVOKE_CODE" = "401" ] || die "revoked token got $REVOKE_CODE, want 401"
grep -qi "no access-profile entry" "$WORK/rev.json" || die "401 body should explain no profile entry"
ok "revoked token -> 401 without restart"

step "operator token still works after the revocation"
OP_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $OPERATOR_TOKEN" "$API/api/v1/tasks")"
[ "$OP_CODE" = "200" ] || die "operator read got $OP_CODE, want 200"
ok "operator unaffected"

printf '\nALL E2E STEPS PASSED\n'