# 16 · Distribución y actualizaciones

Estado: **especificado, sin implementar** (Épica 11 del backlog). Supone que **ya existe una suscripción de Apple Developer**. No hay certificado de firma de código para Windows.

Leyenda: **[Verificado]** = leído en el código del repositorio o del módulo de Wails; **[No verificado]** = depende de algo que nadie ha ejecutado (secretos de Apple, el actualizador en marcha). T11.5 es el spike que lo comprueba.

## 1. De un cambio a una versión publicada

1. **Elegir** una tarea de `docs/specs/12-backlog.md` o abrir un issue. Si cambia comportamiento, **el spec se actualiza primero** (`AGENTS.md`).
2. **Rama** desde `main`, commits pequeños y descriptivos.
3. **Probar en local** con `scripts/smoke.sh` (bindings, `go vet`, `go test -race`, typecheck, lint, vitest, build). Si tocaste la interfaz, E2E con `cd frontend && npx playwright test`.
4. **Pull request** con la plantilla. El CI (`ci.yml`) calcula qué cambió (`changes`) y salta lo que no hace falta: un PR solo de documentación no ejecuta nada; uno solo de Go no corre el frontend ni E2E. En PR solo corre Linux. [Verificado]
5. **Revisión y merge** a `main` (protección de rama: PR y checks obligatorios, spec 15 §B). En `main` el job `build` compila y empaqueta en macOS, Windows y Linux y sube los artefactos 14 días.
6. **Preparar la versión**: subir `info.version` en `build/config.yml` (fuente única de la versión) y mover lo de «Sin publicar» a la nueva sección del `CHANGELOG.md`.
7. **Etiquetar** (`git tag vX.Y.Z` y subirla, lo hace una persona con permisos). `release.yml` comprueba que la etiqueta coincide con `build/config.yml` y que el commit está en `main`, compila en los tres sistemas, genera `SHA256SUMS` y crea la *release* con las notas del CHANGELOG. Etiquetas con guion (`v0.2.0-rc.1`) salen como *prerelease*. [Verificado]
8. **Los usuarios reciben la versión** según su sistema (§3).

## 2. Cómo se actualiza una app ya instalada

Se usa el **actualizador integrado de Wails 3** (`application.App.Updater`, paquete `pkg/updater`). [Verificado en el módulo `wails/v3@v3.0.0`; Dbacus todavía no lo usa.]

- **Qué hace**: consulta GitHub Releases, compara la versión (`semver`), descarga el artefacto de su sistema, verifica su suma en `SHA256SUMS` y su **firma ed25519**, lo descomprime, valida el contenido y un proceso auxiliar lo intercambia al cerrar la app, con copia de seguridad y reversión si falla. En macOS comprueba además `codesign --verify --deep --strict` del `.app` instalado.
- **Qué no hace**: no ejecuta instaladores (`.dmg`, NSIS, `.msix`, `.deb`, `.rpm`); solo reemplaza un binario o un `.app` por otro. No eleva permisos: si la app vive en una ruta protegida, no podrá escribirla.
- **Configuración**: proveedor `github` (`vitacodesas/dbacus`, `ChecksumAsset: "SHA256SUMS"`), `CurrentVersion` (ver §4), `PublicKey` ed25519, `CheckInterval`, ventana integrada (`BuiltinWindow`), canal estable. Con `PublicKey` fijada **falla cerrado**: una descarga sin firma válida se descarta.
- **Claves**: el par se genera una vez con `wails3 updater genkey`. La clave **pública** va embebida en la app; la **privada** es el secreto de GitHub `UPDATER_PRIVATE_KEY`, usado solo por `release.yml` para firmar cada artefacto (`wails3 updater sign`). Perder o filtrar la privada obliga a publicar una versión nueva con otra pública, y los usuarios tendrían que actualizar a mano una vez.
- **Ajuste de usuario**: «Buscar actualizaciones automáticamente» (activado por defecto) y botón «Buscar ahora». Nunca se instala sin que el usuario acepte en la ventana del actualizador.
- **Prerelease**: los `-rc` no se ofrecen en el canal estable.

## 3. Por sistema operativo

### macOS (con suscripción Apple Developer)
| | |
|---|---|
| **Primera instalación** | `.dmg` firmado con *Developer ID Application*, **notarizado y grapado** (`stapler`). Se abre sin avisos de Gatekeeper. Se genera con `wails3 task darwin:package:dmg` (la tarea existe; CI no la usa hoy). |
| **Actualización** | La app descarga el `.zip` del mismo `.app` (firmado y grapado), reemplaza `/Applications/Dbacus.app` y se relanza. Escribe en `/Applications` si el usuario es administrador o el `.app` es suyo; si no, la actualización falla con un mensaje y ofrece abrir la página de descarga. [No verificado] |
| **Arquitectura** | Un solo `.app` universal (`darwin:package:universal`, arm64 + amd64). `macos-latest` produce hoy solo arm64: hay que cambiarlo. |
| **Identificador** | `io.github.vitacodesas.dbacus` (`build/config.yml`, `Info.plist`). Queda ligado a la firma: **no cambiarlo** después de publicar. |

**Pasos en CI** (entre `wails3 package` y `ditto`, en `build.yml`) [No verificado]:
1. Importar el certificado *Developer ID Application* (`.p12`, en base64 en un secreto) a un **llavero temporal** del runner.
2. `xcrun notarytool store-credentials` con Apple ID, Team ID y contraseña específica de app (o una clave API) para crear el perfil de llavero que exige `wails3 tool sign --notarize`.
3. `wails3 task darwin:sign:notarize -- --identity "Developer ID Application: … (TEAMID)" --hardened-runtime --entitlements build/darwin/entitlements.plist` (el archivo de *entitlements* no existe aún; hay que crearlo y probar qué necesita WebKit).
4. Verificar: `codesign --verify --deep --strict`, `spctl -a -vv`, `xcrun stapler validate`. Si alguno falla, el job falla.
5. Comprimir con `ditto` **después** de grapar.

Secretos de GitHub: `APPLE_CERT_P12_BASE64`, `APPLE_CERT_PASSWORD`, `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_PASSWORD`, `KEYCHAIN_PASSWORD`. Riesgo conocido: el `codesign` de Wails no pasa `--timestamp`; la notarización lo exige, así que puede haber que añadirlo.

### Windows
| | |
|---|---|
| **Primera instalación** | Instalador NSIS **por usuario** (`INSTALL_SCOPE=user` → `%LOCALAPPDATA%\Programs\Dbacus`), nombre `dbacus-<arch>-installer.exe`. Sin certificado, SmartScreen mostrará «Windows protegió su PC» (Más información → Ejecutar de todas formas). |
| **Actualización** | La app descarga `dbacus_<versión>_windows_<arch>.exe`/`.zip`, lo valida y lo intercambia al cerrar. Funciona **porque** la instalación es por usuario: en `Program Files` no habría permisos. [No verificado] |
| **Integridad** | La garantiza la firma ed25519 del actualizador, no Authenticode. Un certificado de firma de código es una decisión posterior (las tareas `windows:sign` y `windows:sign:installer` ya existen). |
| **Dependencia** | WebView2 (el instalador NSIS la gestiona; en Windows 11 viene de serie). |

### Linux
| | |
|---|---|
| **Primera instalación** | **AppImage** (`dbacus_<versión>_linux_<arch>.AppImage`): descargar, `chmod +x`, ejecutar. Requiere GTK 4 y WebKitGTK 6.0 en el sistema. `.tar.gz` como alternativa manual. |
| **Actualización** | El AppImage se reemplaza a sí mismo con el actualizador. [No verificado] |
| **`.deb` / `.rpm`** | Se instalan en `/usr/local/bin` (root), así que **no se autoactualizan**: se reinstala el paquete nuevo. Un repositorio apt/rpm queda fuera de alcance. Los metadatos de `build/linux/nfpm/nfpm.yaml` están desactualizados (`license: MIT`, `homepage: https://wails.io`, versión fija) y hay que corregirlos antes de publicarlos. |
| **Firma** | PGP opcional para `.deb`/`.rpm` (`sign:packages`, ya existe); el AppImage va firmado por ed25519 del actualizador. |

## 4. Lo que hay que construir

- **Versión en la app** [hoy no existe: sin `-ldflags -X`, sin pantalla «Acerca de»]: paquete `internal/version` (`var Version = "dev"`), inyectado con `-ldflags "-X …/internal/version.Version=<info.version>"` desde `build/config.yml` en los Taskfile de los tres sistemas, y expuesto a Vue (contrato en el spec 02 antes de implementarlo). Se muestra en la barra de estado o en Ajustes.
- **Nombres de artefacto** compatibles con el selector por defecto del actualizador, que exige `darwin|windows|linux` y `amd64|arm64` en el nombre y descarta los que contienen `-installer.`: `dbacus_<versión>_<os>_<arch>.<ext>` (hoy son `dbacus-v0.1.0-macos.zip`, que **no** casan). Si el selector no reconoce `universal`, se escribe un `AssetMatcher` propio. [Leído en el código; no ejecutado.]
- **`release.yml`**: renombrar, firmar cada artefacto con `UPDATER_PRIVATE_KEY`, y publicar `SHA256SUMS` y las firmas.
- **Instaladores en CI** (hoy `build.yml` solo hace `wails3 build`/`package` sin instaladores): DMG en macOS, NSIS por usuario en Windows, AppImage en Linux.
- **Ajuste y UI** del actualizador (T11.4) y su test.

## 5. Riesgos y qué no está verificado

- Notarización con el `codesign` de Wails (sin `--timestamp`; *entitlements* inexistentes).
- Que un `.app` actualizado por HTTP (sin atributo de cuarentena) pase Gatekeeper y `codesign --verify`.
- Permisos de escritura al actualizar (`/Applications` sin administrador, `%LOCALAPPDATA%`, AppImage en carpetas de solo lectura).
- El alias de `universal` y el *parser* de `SHA256SUMS` del proveedor GitHub.
- Que el actualizador soporta bien el cierre y relanzado en las tres plataformas con los nombres definitivos.

**T11.5 (spike)** lo comprueba de extremo a extremo en el repositorio **privado** antes de hacerlo público: publicar `v0.1.0`, instalarla, publicar `v0.1.1` y verificar que la app instalada se actualiza en los tres sistemas, también el caso de firma inválida (debe rechazarse) y el de falta de permisos.

## 6. Criterios de aceptación

- [ ] `dbacus_<versión>_<os>_<arch>` en la *release*, con `SHA256SUMS` y firmas, y el selector del actualizador los reconoce.
- [ ] El `.dmg` abre en un Mac limpio sin avisos de Gatekeeper; `spctl` y `stapler validate` pasan.
- [ ] Una app `v0.1.0` instalada se actualiza a `v0.1.1` en macOS, Windows y Linux (AppImage).
- [ ] Un artefacto con la firma alterada se rechaza y la app instalada queda intacta.
- [ ] La versión se muestra en la app y coincide con la etiqueta.
- [ ] Ningún secreto aparece en logs ni en artefactos; el workflow no se ejecuta con secretos en PR de forks.
