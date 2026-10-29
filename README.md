# Dbacus

Cliente SQL de escritorio, rápido y ligero, inspirado en Tabularis. Go + Wails 3 en el backend; Vue 3, Pinia y Tailwind solo pintan la interfaz.

> **Estado: 0.1.0, en desarrollo.** Solo MySQL/MariaDB. Los binarios **no están firmados** (ver [Instalación](#instalación)).

## Qué hace

- **Conexiones**: perfiles con prueba paso a paso, TLS, túnel SSH (clave o contraseña, con verificación de `known_hosts`) y modo solo lectura. Las contraseñas viven en el llavero del sistema, nunca en disco ni en la interfaz.
- **Explorador de esquema**: bases, tablas, columnas, índices y claves foráneas, con carga bajo demanda.
- **Consultas**: editor Monaco con pestañas, varias sentencias, resultados paginados por Go, cancelación real (`KILL QUERY`) y autocompletado con el esquema.
- **Edición de resultados**: celdas, filas nuevas, duplicar y borrar, revisando el SQL antes de aplicarlo y con control de conflictos.
- **Historial y exportación** a CSV y JSON, en flujo, sin cargar todo en memoria.

## Instalación

Descarga el archivo de tu sistema desde [Releases](../../releases) y comprueba su huella con `SHA256SUMS`.

| Sistema | Archivo | Primera apertura |
|---|---|---|
| macOS | `.zip` con `dbacus.app` | macOS 15 o posterior: abrir una vez (saldrá el aviso) y luego *Ajustes del Sistema → Privacidad y seguridad → «Abrir igualmente»*. Alternativa: `xattr -dr com.apple.quarantine dbacus.app`. En macOS 14 o anterior basta clic derecho → Abrir |
| Windows | `.exe` | SmartScreen: «Más información» → «Ejecutar de todas formas» |
| Linux | `.tar.gz` | Requiere GTK 4 y WebKitGTK 6.0 |

Aún no hay instaladores ni actualización automática: para actualizar, descarga la nueva versión. Si vienes de la versión anterior (`Tabularis Clon`), Dbacus **copia** tus perfiles y contraseñas al primer arranque y no borra los datos viejos.

## Compilar

Requisitos: Go ≥ 1.26, Node ≥ 24, `wails3` v3.0.0 y, en Linux, `libgtk-4-dev` y `libwebkitgtk-6.0-dev`.

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
wails3 dev      # desarrollo con recarga
wails3 build    # binario en bin/
scripts/smoke.sh   # vet, tests, lint y build
```

Los tests de integración (Docker) y el resto de comandos están en [AGENTS.md](AGENTS.md). Las especificaciones, en [docs/specs](docs/specs).

## Documentación

Los specs, el CHANGELOG y la interfaz están en **español**. Contribuciones y reportes en español o inglés.

## English summary

Dbacus is a lightweight desktop SQL client (MySQL/MariaDB for now) inspired by Tabularis. Go + Wails 3 does the heavy lifting (queries, streaming, export, SSH tunnel, keychain); Vue 3 only renders. Features: connection profiles with a read-only mode, SSH tunnel and TLS, schema browser, Monaco editor with cancellable queries, in-grid editing with SQL review, history and CSV/JSON export.

**Status: 0.1.0, early.** Binaries are **unsigned** (macOS Gatekeeper and Windows SmartScreen will warn). Download from [Releases](../../releases) and verify with `SHA256SUMS`. Build: Go ≥ 1.26, Node ≥ 24, `wails3` v3.0.0 (Linux needs GTK 4 + WebKitGTK 6.0). Licensed under Apache-2.0.

## Licencia

[Apache-2.0](LICENSE). Avisos de terceros en [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
