#!/usr/bin/env bash
set -euo pipefail

bin="${BIN:-./offshore-buoy-smoke}"
tmpdir="${TMPDIR:-/tmp}/offshore-buoy-smoke-$$"
db="$tmpdir/smoke.db"
addr="127.0.0.1:18080"
pid=""

cleanup() {
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -f "$bin" "$db" "$db-shm" "$db-wal"
  rmdir "$tmpdir" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "$tmpdir"
go build -o "$bin" ./cmd/server
"$bin" server -addr "$addr" -sqlite "$db" -inspection-period 1h &
pid="$!"

for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:18080/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

health="$(curl -fsS "http://127.0.0.1:18080/healthz")"
ready="$(curl -fsS "http://127.0.0.1:18080/readyz")"

[[ "$health" == *'"status":"ok"'* ]]
[[ "$ready" == *'"status":"ready"'* ]]
