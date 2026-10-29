#!/usr/bin/env bash
# Entrada: una ruta por línea (stdin). Salida: go=<bool>, frontend=<bool>, any=<bool>.
# Es una lista de EXCLUSIÓN a propósito: lo desconocido ejecuta todo, así que un filtro olvidado
# cuesta minutos de CI, nunca un fallo sin revisar.
go=false
fe=false
while IFS= read -r f || [ -n "$f" ]; do
  [ -z "$f" ] && continue
  case "$f" in
    # No afectan al binario ni a lo que se prueba.
    docs/* | *.md | LICENSE | NOTICE | THIRD_PARTY_NOTICES.md | .gitignore | .claude/* \
      | .github/dependabot.yml | .github/ISSUE_TEMPLATE/* | .github/PULL_REQUEST_TEMPLATE* \
      | scripts/gen-third-party-notices.py) ;;
    frontend/*) fe=true ;;
    *.go | go.mod | go.sum | internal/* | build/* | Taskfile.yml) go=true ;;
    # Workflows, acciones locales y cualquier otro archivo: ante la duda, todo.
    *) go=true; fe=true ;;
  esac
done
any=false
if [ "$go" = true ] || [ "$fe" = true ]; then any=true; fi
echo "go=$go"
echo "frontend=$fe"
echo "any=$any"
