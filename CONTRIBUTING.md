# Cómo contribuir a Dbacus

Gracias por querer aportar. Esta guía va de cero a un pull request aceptado. Puedes escribir issues y PR en español o en inglés.

## 1. Lo que debes saber antes de tocar nada

- **El spec manda.** `docs/specs/` es la fuente de verdad. Si vas a cambiar un comportamiento, **actualiza el spec primero** (y si el spec está mal, corrígelo en el mismo PR y explica por qué).
- **Go hace el trabajo pesado; Vue solo pinta.** Nada de parseo SQL, exportación, filtrado de resultados ni red en el frontend, y Vue nunca habla con la base de datos ni lee secretos. Las reglas completas están en [AGENTS.md](AGENTS.md); léelas, aplican a personas y a agentes.
- **Los contratos Go↔Vue** viven en `docs/specs/02-contrato-go-vue.md`. Cambiar un DTO o un evento empieza por ese archivo.
- **Nunca** registres contraseñas, *passphrases* ni SQL con literales en logs, ni los subas al repositorio.

## 2. Preparar el entorno

Requisitos: Go ≥ 1.26, Node ≥ 24, `wails3` v3.0.0 y, para los tests de integración, Docker. En Linux: `libgtk-4-dev` y `libwebkitgtk-6.0-dev`.

```bash
git clone https://github.com/vitacodesas/dbacus.git && cd dbacus
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
(cd frontend && npm ci)
wails3 dev        # app con recarga en caliente
```

Sin ventana nativa (útil para ver la interfaz en el navegador): `wails3 task build:server DEV=true && ./bin/dbacus-server`, y abre `http://localhost:8080`.

## 3. Elegir qué hacer

1. Mira el [backlog](docs/specs/12-backlog.md): cada tarea tiene alcance, **fuera de alcance** y *Definition of Done*.
2. Si la idea es nueva, abre un issue («Funcionalidad») antes de escribir código, para acordar el diseño.
3. Haz **solo** lo que dice la tarea. Lo que descubras de paso, anótalo o abre otro issue.

## 4. Trabajar

1. Crea una rama desde `main`: `git switch -c feat/lo-que-sea`.
2. Spec primero, código después, **tests incluidos** (sin tests no está hecho). Imita el estilo del código de alrededor; los comentarios solo explican el «por qué» no obvio.
3. Si cambias un servicio o un evento de Go, regenera los bindings y súbelos: `wails3 generate bindings -clean=true -ts -i`. El CI comprueba que no haya diferencias.
4. Si cambias la interfaz, actualiza `src/mock/services.ts` (el *typecheck* no detecta que el mock se desvíe) y las cadenas de `es.ts` **y** `en.ts`.
5. Commits pequeños y descriptivos, en español, explicando el porqué.

## 5. Probar antes de abrir el PR

```bash
scripts/smoke.sh                       # bindings, go vet, go test -race, typecheck, lint, vitest y build
cd frontend && npx playwright test     # E2E con el backend simulado (PW_CHANNEL=chrome para usar tu Chrome)
DOCKER_HOST=... go test -tags=integration ./...   # contra MySQL/MariaDB reales (ver AGENTS.md)
```

Si tocaste la interfaz, pruébala también en la app real (`wails3 dev`) y, si afecta al arranque o al empaquetado, con `wails3 build`. `docs/smoke-manual.md` lista las comprobaciones manuales.

## 6. Abrir el pull request

- Rellena la plantilla: qué cambia, por qué y cómo lo probaste.
- **El CI decide qué ejecutar**: un PR solo de documentación no corre nada; uno solo de Go no corre el frontend ni E2E; y en un PR solo se ejecuta Linux. macOS y Windows se compilan al fusionar con `main`. No te asustes si ves jobs «skipped»: es lo esperado.
- Responde a la revisión con commits nuevos (no reescribas el historial durante la revisión).
- Cuando esté aprobado y el CI en verde, se fusiona a `main`.

## 7. Cómo llega tu cambio a los usuarios

Tras fusionar, quien mantiene el proyecto sube la versión en `build/config.yml` y el `CHANGELOG.md`, etiqueta `vX.Y.Z` y el CI publica la *release* en tres sistemas con `SHA256SUMS`. Las apps ya instaladas se actualizan con el actualizador integrado, por sistema. El detalle completo (firma y notarización en macOS, instaladores, Windows y Linux) está en [docs/specs/16-distribucion-y-actualizaciones.md](docs/specs/16-distribucion-y-actualizaciones.md). Hoy el actualizador **está especificado pero no implementado**: mientras tanto, los usuarios descargan la *release* a mano.

## 8. Seguridad y licencia

- Vulnerabilidades: **no** abras un issue público; sigue [SECURITY.md](SECURITY.md).
- Al contribuir aceptas que tu aporte se publique bajo la licencia [Apache-2.0](LICENSE) del proyecto.
