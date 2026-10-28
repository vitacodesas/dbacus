#!/usr/bin/env bash
# Smoke de la cadena Wails 3 + Vue (T0.1): bindings, typecheck/lint/tests del front, vet/tests de Go y build.
# Se ejecuta tras cualquier actualización de Wails (ADR-001).
set -euo pipefail
cd "$(dirname "$0")/.."

# En macOS, sin esto cada binario de test enlaza con decenas de avisos «built for newer macOS» (el build de Wails ya lo fija).
if [[ "$(uname)" == "Darwin" ]]; then
  export MACOSX_DEPLOYMENT_TARGET=12.0 CGO_CFLAGS=-mmacosx-version-min=12.0 CGO_LDFLAGS=-mmacosx-version-min=12.0
fi

echo "› bindings"
wails3 generate bindings -clean=true -ts -i

echo "› frontend"
(cd frontend && npm ci --silent && npm run typecheck && npm run lint && npm test)

echo "› go"
go vet ./...
go test -race ./...

echo "› build"
wails3 build

echo "OK"
