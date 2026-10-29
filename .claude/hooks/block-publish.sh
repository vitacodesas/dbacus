#!/usr/bin/env bash
# PreToolUse(Bash): los agentes no suben ni publican nada; los push los hace el usuario a mano.
# Recibe el JSON del hook por stdin; exit 2 bloquea la llamada y devuelve stderr al agente.
set -euo pipefail

cmd=$(jq -r '.tool_input.command // ""')

# Inicio de comando: principio de línea o tras ; & | ( ` o $(
start='(^|[;&|(`]|\$\()[[:space:]]*'
# git admite opciones globales antes del subcomando (git -C dir push, git -c k=v push).
git_opts='(-C[[:space:]]+[^[:space:]]+|-c[[:space:]]+[^[:space:]]+|--[^[:space:]]+)'

patterns=(
  "${start}git([[:space:]]+${git_opts})*[[:space:]]+push([^[:alnum:]_-]|$)"
  "${start}git([[:space:]]+${git_opts})*[[:space:]]+(remote[[:space:]]+(add|set-url)|send-email)([^[:alnum:]_-]|$)"
  "${start}gh[[:space:]]+(repo[[:space:]]+(create|delete|edit|rename|fork|sync)|release[[:space:]]+(create|delete|edit|upload)|pr[[:space:]]+(create|merge|close|reopen|edit|review|comment|ready)|issue[[:space:]]+(create|close|reopen|edit|comment|delete)|workflow[[:space:]]+(run|enable|disable)|run[[:space:]]+(rerun|cancel|delete)|secret|variable|label[[:space:]]+(create|delete|edit)|auth[[:space:]]+(login|logout|refresh|setup-git))([^[:alnum:]_-]|$)"
  "${start}gh[[:space:]]+api[[:space:]].*(-X|--method)[[:space:]]*(POST|PUT|PATCH|DELETE)"
  "${start}gh[[:space:]]+api[[:space:]].*(-f|-F|--field|--raw-field|--input)([[:space:]=]|$)"
  "${start}(npm|pnpm|yarn|bun)[[:space:]]+publish([^[:alnum:]_-]|$)"
  "${start}(cargo|twine)[[:space:]]+(publish|upload)([^[:alnum:]_-]|$)"
  "${start}docker[[:space:]]+(image[[:space:]]+)?push([^[:alnum:]_-]|$)"
)

for p in "${patterns[@]}"; do
  if [[ $cmd =~ $p ]]; then
    echo "Bloqueado: los agentes no pueden subir ni publicar (git push, gh de escritura, npm/docker publish). El usuario hace el push manualmente; deja los cambios commiteados y avisa." >&2
    exit 2
  fi
done
exit 0
