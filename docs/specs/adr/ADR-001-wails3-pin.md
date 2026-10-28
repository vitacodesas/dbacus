# ADR-001 — Wails 3 con versión exacta pineada

**Estado**: aceptada (actualizada en T0.1, 2026-10-28)

**Contexto**: al redactar el spec se asumió que Wails 3 seguía en beta. En T0.1 se verificó con `go list -m -versions` que **`v3.0.0` estable se publicó el 2026-10-26**. Se usa esa versión en lugar de una beta.

**Decisión**: `github.com/wailsapp/wails/v3 v3.0.0` en `go.mod` y `@wailsio/runtime` `3.0.0` en `frontend/package.json` (ambos exactos, sin rangos). El CLI debe coincidir: `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0`.

**Consecuencias**
- Wails queda aislado en `internal/app`, `internal/services` (solo diálogos/eventos) y `frontend/src/api`; el resto no lo importa.
- Actualizar versión = tarea explícita que corra `scripts/smoke.sh` (bindings, typecheck, lint, tests, build).
- Plantilla generada con `wails3 init -t vue`; se eliminaron los directorios `build/ios` y `build/android` (solo escritorio).

**Verificado en el spike T0.1 (macOS arm64)**
- Bindings TS generados desde servicios Go (`wails3 generate bindings -ts -i`) y consumidos solo desde `frontend/src/api`.
- Evento Go→Vue tipado (`ping:tick`) con goroutine ligada al `ctx` de `ServiceStartup`.
- Llamada Vue→Go (`PingService.Hello`) por el puente.
- `wails3 build` produce binario de escritorio; modo servidor (`-tags server`) sirve para pruebas de UI en navegador.
- **No verificado**: diálogo nativo `PickFile` (requiere interacción en la ventana nativa; el entorno no permite capturarla) y empaquetado de Monaco (se prueba en T4.4).

**Notas**
- El linker de macOS avisa de que los objetos se compilaron para macOS 26 y se enlazan con 11.0; es inocuo, pero conviene fijar `MACOSX_DEPLOYMENT_TARGET` en el empaquetado (T6.3).
- Docker no estaba corriendo al verificar; los tests de integración (T1.3+) lo necesitan.
