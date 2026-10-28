#!/usr/bin/env bash
# Benchmarks de T6.2: micro-benchmarks Go y, con Docker, la carga de 1 M de filas contra MySQL/MariaDB reales.
# Uso: scripts/bench.sh [--integration]   (resultados en docs/benchmarks.md se actualizan a mano con la salida)
set -euo pipefail
cd "$(dirname "$0")/.."

echo "› micro-benchmarks"
go test -run '^$' -bench . -benchtime 1s ./internal/results ./internal/export ./internal/sqlsplit

if [[ "${1:-}" == "--integration" ]]; then
  echo "› 1 M de filas (requiere Docker; con colima: colima start)"
  export DOCKER_HOST="${DOCKER_HOST:-unix://$HOME/.colima/default/docker.sock}"
  export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE="${TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE:-/var/run/docker.sock}"
  go test -tags integration -count=1 -v -run 'TestQueryPerfIntegration|TestExportServiceIntegration/.*/1M' ./internal/services/ | grep -E "perf_integration|export_integration|^(---|    ---|ok|FAIL)"
fi
