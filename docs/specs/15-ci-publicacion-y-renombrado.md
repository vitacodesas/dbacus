# 15 — Renombrado a Dbacus, repositorio público y CI/CD

> Estado: **decidido en lo grueso, con datos pendientes** (ver «Pendiente del usuario»). Nada de esto está implementado: el CI actual (`.github/workflows/ci.yml`) **nunca se ha ejecutado** y solo está probado el empaquetado en macOS.

## Decisiones tomadas (2026-10-28)
| # | Tema | Decisión |
|---|---|---|
| D1 | Nombre | **Dbacus** (ábaco + DBA). Binario `dbacus`; identificador de paquete `pet.laika.dbacus`; empresa «Laika». El nombre «Tabularis Clon» desaparece: «Tabularis» es un proyecto real de código abierto distinto. |
| D2 | Alcance del renombrado | **Todo**, con migración de datos: nombre visible, binario, identificador, módulo Go, carpeta de datos, llavero y variable de entorno. Nada de lo que el usuario ya guardó (perfiles, historial, ajustes, secretos, host keys) se pierde ni se borra. |
| D3 | Alojamiento | **GitHub público**. Se crea primero **privado**, se deja el CI en verde en los tres sistemas y solo entonces se hace público (publicar es casi irreversible: se indexa y se clona). |
| D4 | Disparadores del CI | Pull request: solo Linux (vet, tests, E2E, integración, seguridad). `main` y etiquetas `v*`: además compilan y empaquetan en macOS y Windows. |

## A. Renombrado y migración (T9.2)
**Qué cambia** (lista de comprobación; todo lo demás es texto en docs):
- `build/config.yml` (`productName`, `productIdentifier`, `description`, `comments`), `build/darwin/Info*.plist`, `build/windows/{info.json,nsis,msix,wails.exe.manifest}`, `build/linux/{desktop,nfpm}`, `Taskfile.yml` (`APP_NAME`), icono de la app si se redibuja.
- Código: `appName` en `internal/app`, `appDir` en `internal/paths` (hoy `Tabularis-Clon`), `service` en `internal/secrets` (hoy `tabularis-clon`), variable `TABULARIS_CLON_HOME` → `DBACUS_HOME` (la vieja se sigue leyendo un tiempo y avisa), `i18n app.name`, `index.html`, `package.json`.
- **Módulo Go** `tabulariswails` → `github.com/<dueño>/dbacus` (necesita el dueño de GitHub). Toca todos los imports y la carpeta de bindings generados (`frontend/bindings/tabulariswails/...` → nueva ruta): se hace en un solo commit mecánico, con `wails3 generate bindings` y la batería completa de tests antes y después.
- Docs: `README`, `AGENTS.md`, specs y ADRs (donde digan «Tabularis Clon»; los que hablan del producto original «Tabularis» como referencia funcional se dejan).

**Migración (única, idempotente, no destructiva)** al primer arranque con el nombre nuevo:
1. Si existe la carpeta de datos vieja (`<config>/Tabularis-Clon`) y la nueva no tiene `app.db`, se **copia** (no se mueve) `app.db`, `known_hosts` y los logs a `<config>/Dbacus`, comprobando la copia (tamaño y apertura de la base) antes de seguir.
2. Para cada perfil de la base copiada y cada tipo de secreto, se lee del llavero con el servicio viejo y se escribe con el nuevo (`secrets.Migrate(oldService, newService)`). Si el llavero no está disponible, se sigue sin secretos migrados y la app los pedirá al conectar (el comportamiento normal de «pedir cada vez»).
3. Se deja una marca en `settings` (`migratedFrom: "tabularis-clon"`) para no repetir, y **no se borra nada de lo viejo**. Un ajuste «Limpiar datos de la versión anterior» (confirmación explícita) lo borrará más adelante, no en esta tarea.
4. Si algo falla a mitad, la app arranca igualmente con lo que haya y registra el motivo (sin secretos); la migración se puede reintentar.
Pruebas: unitarias con llavero y carpetas simulados (copia correcta, idempotencia, destino ya existente, llavero caído, copia corrupta) y una comprobación manual en macOS con tus datos reales **antes** de borrar nada.

## B. Preparar el repositorio para hacerlo público (T9.1 y T9.6)
- **Auditoría previa (hecha en parte, 2026-10-28)**: sin credenciales ni claves privadas en el árbol ni en el historial; sin archivos grandes (lo mayor son iconos de 1,8 MB). Hallazgos a resolver antes de publicar:
  1. **Correo personal en los 20 commits** (`whary11@gmail.com`). Decisión pendiente: dejarlo, o reescribir el historial con el correo `noreply` de GitHub (`git filter-repo --mailmap`) **antes** del primer push; después ya no se puede retirar.
  2. ~~`docs/correcciones.md` nombra un entorno interno («Laika STG»)~~: generalizado (T9.1).
  3. No hay `LICENSE`: es obligatorio en un repositorio público.
- **Licencia** (pendiente de elegir): recomiendo **Apache-2.0** (concesión de patentes, la más común en herramientas de bases de datos). Antes de fijarla, revisar las licencias de las dependencias (`go-licenses`, `license-checker`): `go-sql-driver/mysql` es MPL-2.0 (copyleft solo de archivo, compatible); el resto es MIT/BSD/Apache.
- **Archivos mínimos**: `LICENSE`, `README.md` (qué es, capturas, instalación, compilar, estado), `SECURITY.md` (cómo reportar vulnerabilidades), `.github/dependabot.yml` (Go, npm, Actions), `.github/ISSUE_TEMPLATE` y `PULL_REQUEST_TEMPLATE` cortas, `CHANGELOG.md`.
- **Ajustes del repositorio**: secret scanning y push protection activados, Dependabot alerts, rama `main` protegida (PR obligatorio, checks requeridos, sin force-push), borrar ramas al fusionar.
- **Idioma**: los specs y la UI quedan en español; el `README` lleva un resumen en inglés.

### Resultado de la revisión de licencias (T9.1, 2026-10-28)
- **Go** (20 módulos que entran en el binario): 11 BSD-3, 8 MIT y 1 MPL-2.0 (`go-sql-driver/mysql`, copyleft solo a nivel de archivo: se puede enlazar desde cualquier licencia sin cambiar la nuestra, siempre que se conserve su aviso). Ninguna GPL/AGPL/LGPL ni sin licencia.
- **npm** (dependencias de producción, 89 paquetes): 73 MIT, 5 Apache-2.0, 3 ISC, 1 BSD-3, 1 BSD-2, 1 0BSD, `dompurify` (MPL-2.0 o Apache-2.0, a elegir) y `lightningcss` (MPL-2.0, herramienta de compilación de Tailwind que no se distribuye en la app). Ninguna copyleft fuerte.
- **Conclusión**: Apache-2.0 es compatible. Al publicar, incluir un `THIRD_PARTY_NOTICES.md` generado (avisos de MPL/Apache/BSD).

## C. CI (T9.4 y T9.5)
**Principios**: permisos mínimos (`permissions: contents: read` por defecto), `concurrency` que cancela ejecuciones obsoletas de una misma rama, versiones de acciones fijadas por hash con el tag en comentario (Dependabot las actualiza; se fijaron las mayores ya probadas: checkout v4, setup-go v5, setup-node v4, upload/download-artifact v4, cache v4), sin secretos en los PR de forks.

| Workflow | Cuándo | Qué hace |
|---|---|---|
| `ci.yml` → job **`check`** (ubuntu) | PR, push a `main` | `go vet`, `go test -race ./...`, frontend (typecheck, lint, tests), E2E Playwright, bindings sin cambios (`wails3 generate` + `git status` limpio) |
| `ci.yml` → job **`integration`** (ubuntu + Docker) | PR, push a `main` | `-tags integration` contra MySQL 8.0/8.4 y MariaDB 10.11 |
| `ci.yml` → job **`build`** | push a `main` y manual, **no** en PR; tras `check` + `integration` | llama a `build.yml` (matriz macOS, Windows, ubuntu): compila, empaqueta y sube artefactos (14 días) |
| `build.yml` | `workflow_call` | reutilizable; lo usan `ci.yml` y `release.yml` |
| `security.yml` | PR, push a `main`, lunes 06:00 UTC, manual | `go mod verify`, `govulncheck`, `npm audit`; `dependency-review` en PR solo si el repo es público (en privados exige GitHub Advanced Security) |
| `release.yml` | etiqueta `v*` (no se dispara también `ci.yml`) | `verify`: la etiqueta coincide con `info.version` de `build/config.yml` y el commit está en `main`; `build` (3 sistemas); `publish` (único job con `contents: write`): renombra con la versión, genera `SHA256SUMS` y crea la *release* con las notas de `CHANGELOG.md` o, si no hay sección, generadas desde los commits; las etiquetas con guion (`v0.2.0-rc.1`) salen como *prerelease* |
| `.github/actions/setup` | acción local | Go, Node, dependencias de Linux (GTK4 + WebKitGTK 6.0), `wails3` de la versión de `go.mod` y `frontend/dist` (lo embebe `main.go`) |
| `dependabot.yml` | semanal | `gomod`, `npm` (`/frontend`) y `github-actions`, agrupados; ignora `wails/v3` (fijado por ADR-001) y `monaco-editor`/`dompurify` (ver AGENTS.md) |

- **Detección de cambios**: el job `changes` salta lo que no hace falta (docs, licencias, `.claude/` no ejecutan nada; solo Go no ejecuta el frontend ni E2E; solo frontend no ejecuta Go ni integración). Se filtra con `if` y **no** con `paths:` del workflow porque un workflow filtrado deja pendientes los checks obligatorios y bloquea PR de solo documentación; un job saltado cuenta como superado. Es una lista de **exclusión**: un archivo desconocido ejecuta todo. Lógica en `.github/scripts/changed-areas.sh`. Los push nuevos, force-push, la ejecución manual y la semanal lo ejecutan todo.
- **Caché**: módulos de Go, `npm` y navegadores de Playwright; reduce minutos y tiempo.
- **Instalación de `wails3`**: la versión exacta de `go.mod` (como hoy); en Linux, las dependencias GTK/WebKit; en Windows, WebView2 viene con el runner.
- **Artefactos**: `.app` comprimido (macOS), `.exe` (Windows), `tar.gz` (Linux). **Sin firmar** al principio: macOS mostrará Gatekeeper (la firma es *ad-hoc*, sin Developer ID ni notarización: en macOS 15+ hay que usar Ajustes → Privacidad y seguridad → «Abrir igualmente», o `xattr -dr com.apple.quarantine`; el clic derecho → Abrir solo funciona en macOS 14 o anterior) y Windows, SmartScreen. Firmar requiere un certificado de Apple Developer (99 USD/año) y uno de firma de código en Windows: decisión posterior, no bloquea.
- **Instaladores** (`.dmg`, NSIS, AppImage, `.deb`): fuera de alcance de esta fase.
- **Riesgos conocidos**: (1) lo de Windows y Linux **nunca se ha ejecutado**: es probable que el primer ciclo falle y haya que iterar; (2) `TestProbeViaSSH` es intermitente (C2): hay que estabilizarlo **antes** de exigir checks en `main` (T9.3); (3) los E2E usan el Chrome del sistema en local y `chromium` instalado por Playwright en CI.

## D. Orden de ejecución
`T9.1` auditoría y limpieza → `T9.3` estabilizar C2 → `T9.2` renombrar y migrar (con tus datos reales probados en macOS) → `T9.4` reestructurar CI y añadir `release.yml` → `T9.5` repositorio **privado**, primer push y bucle hasta verde en los tres sistemas → `T9.6` licencia, README, protección de `main`, hacerlo **público** y publicar `v0.1.0`.
El renombrado va antes del primer push para que el historial público ya diga Dbacus en el código (los mensajes de commit antiguos conservan el nombre viejo; se aceptan).

## Pendiente del usuario
1. **Dueño de GitHub** (usuario u organización) y nombre del repositorio (propuesto `dbacus`): define el módulo Go.
2. **Licencia** (propuesta: Apache-2.0).
3. **Correo de los commits**: dejar el personal o reescribir con el `noreply` de GitHub antes de publicar.
4. **Instalar `gh`** (GitHub CLI) y autenticarlo, o crear el repositorio desde la web y darme la URL; el `push` lo hace tu credencial, no yo.
5. Firma de código (puede esperar).

## Criterios de aceptación
- [ ] Tras actualizar, la app abre con los perfiles, el historial, los ajustes y las contraseñas de antes sin pedir nada (probado con datos reales en macOS) y los datos viejos siguen intactos.
- [ ] `grep -ri "tabularis"` solo encuentra menciones al proyecto original como referencia y a la migración.
- [ ] El CI está en verde en macOS, Windows y Linux en `main`, y un PR solo ejecuta el job Linux.
- [ ] Una etiqueta `v0.1.0` publica una *release* con los tres artefactos y `SHA256SUMS`; los checksums coinciden con lo descargado.
- [ ] `main` exige PR y checks; secret scanning y Dependabot activos; no hay credenciales ni datos internos en el repositorio.
