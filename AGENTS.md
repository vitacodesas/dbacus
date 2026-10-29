# AGENTS.md — Reglas para agentes ejecutores

Dbacus: cliente SQL de escritorio, inspirado en Tabularis. **Fuente de verdad: `docs/specs/`.** Si el código y un spec difieren, el spec manda; si el spec está mal, corrígelo en el mismo PR y di por qué.

## Stack (no cambiar sin ADR)
- Backend: Go (≥1.26) + **Wails 3 (v3.0.0 estable), versión exacta pineada en `go.mod`** (ver `docs/specs/adr/ADR-001`). Concurrencia con goroutines + `context`.
- Frontend: Vue 3 (`<script setup lang="ts">`) + Vite + TypeScript estricto + **Pinia (última)** + **Tailwind**. Editor: Monaco.
- Persistencia local: SQLite (`modernc.org/sqlite`, sin CGO). Secretos: llavero del SO (`zalando/go-keyring`).
- Driver inicial: MySQL (`go-sql-driver/mysql`). La capa de drivers es extensible (spec 03).

## Reglas de oro
1. **Go hace el trabajo pesado; Vue solo pinta.** Nada de parseo SQL, formateo masivo, exportación, filtrado/ordenado de resultados ni lógica de red en el frontend.
2. Vue **nunca** habla con la BD ni lee secretos. Solo llama a servicios Go (bindings generados) y escucha eventos.
3. Contratos Go↔Vue: solo los definidos en `docs/specs/02-contrato-go-vue.md`. Cambiar un DTO/evento = actualizar el spec primero.
4. Toda operación larga recibe `context.Context` y es cancelable. Ninguna goroutine sin dueño ni sin forma de terminar (usar `errgroup`/`sync.WaitGroup`).
5. Nunca registrar contraseñas, passphrases ni SQL completo con literales en logs (spec 10).
6. Errores al frontend siempre como `AppError` tipado (spec 02), nunca strings sueltos.
7. Pinia guarda estado de UI y caché de lectura; la verdad de datos vive en Go.

## Estructura de carpetas
```
main.go                      # bootstrap Wails
internal/app/                # wiring, ciclo de vida, registro de servicios
internal/services/           # ConnectionService, SchemaService, QueryService, HistoryService, ExportService (expuestos a Vue)
internal/driver/             # interfaz Driver + registry; internal/driver/mysql/
internal/session/            # SessionManager: pools y conexiones dedicadas por pestaña
internal/tunnel/             # túnel SSH (dialer)
internal/store/              # SQLite: perfiles, historial, ajustes
internal/secrets/            # keyring
internal/results/            # buffer de resultados por queryId
internal/apperr/             # AppError y códigos
frontend/src/{api,stores,components,views,composables,types,styles}
docs/specs/                  # specs y ADRs
```

## Cómo trabajar una tarea
1. Lee la tarea en `docs/specs/12-backlog.md` y los specs que referencia.
2. Implementa solo lo de la tarea; respeta su "Fuera de alcance".
3. Cumple su Definition of Done (tests incluidos) y los criterios de aceptación del spec.
4. Convenciones: Go con `gofmt`/`go vet`/`golangci-lint`; TS con `vue-tsc --noEmit` y ESLint; commits pequeños y descriptivos.
5. Comentarios de código: solo el "por qué" no obvio. Imita el estilo del código circundante.

## Seguridad y toolchain
- **Toolchain**: `go.mod` fija `toolchain go1.26.10` (corrige vulnerabilidades de la stdlib que `govulncheck` encontró en 1.26.4); no lo bajes. Si falla un build local por la versión, deja que Go descargue el toolchain.
- **Solo lectura**: un perfil `ReadOnly` rechaza en Go (`sqlsplit.IsReadOnlySafe`) todo lo que no sea lectura y abre las conexiones con `SET SESSION TRANSACTION READ ONLY`. Toda ruta nueva que ejecute SQL del usuario debe pasar por `QueryService.Execute` o adquirir la conexión con `Session.AcquireConn` (que ya la aplica).

## Comandos
- Requisitos: Go ≥1.26, Node ≥24, `wails3` v3.0.0 (`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0`), Docker para integración.
- Dev (hot reload): `wails3 dev` · Build: `wails3 build` (binario en `bin/`).
- Bindings TS (tras cambiar servicios Go o eventos): `wails3 generate bindings -clean=true -ts -i`.
- Go: `go vet ./... && go test -race ./...` · integración (Docker): `colima start`, luego `DOCKER_HOST=unix://$HOME/.colima/default/docker.sock TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock go test -tags=integration ./...`. Imágenes por defecto: mysql:8.0, mysql:8.4, mariadb:10.11 (`DBACUS_IT_IMAGES` las cambia).
- Front (`cd frontend`): `npm run typecheck && npm run lint && npm test`.
- Todo junto: `scripts/smoke.sh`. En macOS, `go vet`/`go test` sueltos imprimen avisos `ld: warning … built for newer 'macOS'`: son inocuos; `export MACOSX_DEPLOYMENT_TARGET=12.0 CGO_CFLAGS=-mmacosx-version-min=12.0 CGO_LDFLAGS=-mmacosx-version-min=12.0` los quita (smoke.sh ya lo hace; `wails3 build` también).
- E2E de UI (backend simulado): `cd frontend && PW_CHANNEL=chrome npm run test:e2e` (usa el Chrome instalado; sin `PW_CHANNEL`, `npx playwright install chromium` primero). Rendimiento: `scripts/bench.sh [--integration]`; smoke manual en app real: `docs/smoke-manual.md`.
- Pruebas de UI sin ventana nativa: `wails3 task build:server DEV=true && ./bin/dbacus-server` y abrir `http://localhost:8080` (no soporta diálogos nativos).

## Notas de dependencias
- **Monaco** está fijado en `0.56.0` y `dompurify` se fuerza a `3.4.17` con `overrides` en `frontend/package.json`: ambas versiones de Monaco (0.56 y 0.57) arrastran un `dompurify` con vulnerabilidades críticas y `npm audit` debe seguir limpio (spec 10). Al actualizar Monaco, comprobar `npm audit` antes de quitar el override.
- El worker de Monaco se crea con `new Worker(new URL(...), {type:"module"})`, no con `?worker`: este último rompe `wails3 dev` (pantalla en blanco) porque el optimizador de Vite pierde el export por defecto. Probar siempre también con `wails3 dev`, no solo con el build.
- Las rutas `monaco-editor/esm/...` se resuelven con un alias de Vite y `paths` de TypeScript porque el campo `exports` del paquete no las expone.
- Los tests de integración usan `tmpfs` para los datos de MySQL (sin volúmenes anónimos en el disco de Docker). Al lanzar contenedores a mano usa `docker run --rm --tmpfs /var/lib/mysql …` y `docker rm -fv`.
