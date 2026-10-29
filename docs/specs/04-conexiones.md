# 04 — Conexiones (foco principal de etapa 1)

## Objetivo
Configurar, probar, guardar y abrir conexiones MySQL con la menor fricción posible y diagnósticos claros cuando falle.

## Pantalla "Conexiones"
- **Lista** (sidebar/launcher): tarjetas con nombre, color, `user@host:port/db`, indicador de estado (desconectada/conectada), menú: Conectar, Editar, Clonar, Eliminar. Búsqueda por texto. Doble clic = conectar.
- **Botón "Nueva conexión"** abre el formulario en un panel/modal grande.

## Formulario (secciones)
1. **General**: Nombre* (único), Driver* (selector; solo MySQL activo, otros "Próximamente"), Color, Host* (default `127.0.0.1`), Puerto* (default `3306`, 1–65535), Usuario*, Contraseña (con ojo mostrar/ocultar), checkbox "Guardar contraseña en el llavero", checkbox "No guardar el historial" (spec 07), checkbox "Solo lectura" (spec 10; la barra de estado lo señala), Base de datos (opcional).
2. **SSL/TLS**: Modo (`disable|preferred|required|verify-ca|verify-full`), rutas CA/Cert/Key con selector de archivo nativo (diálogo en Go).
3. **Túnel SSH**: toggle; Host*, Puerto (22), Usuario*, Método (Contraseña | Clave privada); contraseña o ruta de clave + passphrase opcional.
4. **Avanzado**: tabla clave/valor de `Params` (charset, `timeout`, `readTimeout`, `sql_mode` de sesión, etc.) con sugerencias.
5. **Importar**: campo "Pegar URL" (`mysql://user:pass@host:3306/db?param=x`) → llama `ParseDSN` y rellena el formulario.

Pie: **Probar conexión**, **Guardar**, **Guardar y conectar**, **Cancelar**.

## Validación (cliente UX + servidor autoritativo)
- Frontend valida campos requeridos/rango para feedback inmediato; **Go repite toda validación** (`VALIDATION` con `detail` por campo).
- Nombre duplicado → error en el campo. Host no vacío, sin espacios. Si SSH activo, exige sus campos. Rutas de archivo deben existir y ser legibles (Go).
- Si `verify-*` sin CA y no hay CA del sistema aplicable → error accionable.

## Probar conexión
`ConnectionService.Test` ejecuta pasos en orden y los reporta (`TestStep`): `ssh` → `tcp` → `tls` → `auth` → `query` (`SELECT VERSION()`). TLS y autenticación ocurren dentro del mismo handshake del protocolo MySQL, así que el paso (`tls` o `auth`) se atribuye por el tipo de error devuelto (`TLS_ERROR` → tls; `AUTH_FAILED`/BD inexistente → auth). Con modo `preferred`, `tls ✓` no garantiza que la sesión esté cifrada (puede haber caído a texto plano). UI muestra checklist (✓/✗, ms) y el mensaje del primer paso fallido con sugerencia (p. ej. "Puerto cerrado: ¿MySQL escucha en 3306?"). Timeout total 15 s; botón Cancelar. No guarda nada. Usa la contraseña del formulario (no la del llavero) si fue escrita; si el perfil existe y el campo está vacío, usa la del llavero.

## Persistencia y secretos
- Perfil (sin secretos) en SQLite `connection_profiles`; secretos en el llavero: servicio `dbacus`, cuenta `profile:<id>:password|ssh_password|ssh_passphrase`. Secretos del servicio anterior `tabularis-clon`: se leen como respaldo y se copian al servicio `dbacus` en la primera lectura; el original se conserva hasta eliminar el perfil (se borra de ambos).
- `SavePassword=false` → no se guarda; se pide en un diálogo al conectar y vive solo en memoria de la sesión.
- Edición: campos secretos muestran "••••• (guardada)"; vacío = sin cambios; "Borrar contraseña" explícito.
- Llavero no disponible → `KEYRING_UNAVAILABLE`; ofrecer "pedir cada vez" (no hay fallback en texto plano).
- Eliminar perfil: confirmar; borrar secretos; cerrar sesiones activas; conservar historial opcionalmente (preguntar).
- Clonar: copia perfil y secretos con nuevo ID.

## Túnel SSH
- `internal/tunnel` abre `ssh.Client` y expone `DialFunc` que hace `client.DialContext("tcp", host:port)` hacia MySQL; sin puerto local abierto (no hay listener).
- Verificación de host key con `known_hosts` propio de la app (`UserConfigDir/…/known_hosts`): desconocida → `SSH_HOSTKEY_UNKNOWN` con huella SHA256; la UI muestra diálogo "¿Confiar?" → `TrustHostKey`; cambiada → `SSH_HOSTKEY_MISMATCH` (bloquea, sin opción de ignorar en un clic).
- Keepalive cada 30 s; si cae, cierra sesión y emite `session:closed{reason:"ssh_tunnel_down"}`.
- Autenticación por clave: soporta ed25519/rsa/ecdsa, passphrase; no usa ssh-agent en etapa 1 (anotado como futuro).

## Conectar (ciclo de sesión)
1. `Connect(id)` → resuelve secretos → abre túnel (si aplica) → `driver.Open` → `Ping` → registra `Session`.
2. UI: sesión aparece en el panel con el explorador de esquema y botón "Nueva consulta".
3. Reconexión: si el ping falla al ejecutar, la pestaña muestra "Conexión perdida — Reconectar" (re-`Acquire`; no reintenta automáticamente sentencias).
4. Desconectar libera pestañas, conexiones, túnel y buffers.

## Criterios de aceptación
- [ ] Crear perfil válido, guardarlo, reiniciar app, aparece en la lista sin contraseña en SQLite ni en logs.
- [ ] Test muestra pasos y falla con mensaje distinto para: host inalcanzable, puerto cerrado, credenciales inválidas, TLS inválido, SSH auth fallida, host key desconocida.
- [ ] Editar sin tocar contraseña no la pierde; "Borrar contraseña" la elimina del llavero.
- [ ] Clonar crea perfil independiente (editar uno no afecta al otro).
- [ ] Conectar vía túnel SSH a un MySQL no expuesto funciona y se cierra limpio al desconectar (sin goroutines huérfanas: test con `goleak`).
- [ ] `ParseDSN` acepta `mysql://u:p%40ss@h:3307/db?charset=utf8mb4` y rellena el formulario.
- [ ] Cancelar un Test colgado termina en < 1 s.
- [ ] Todas las validaciones del formulario existen también en Go (tests unitarios).

## Casos borde
IPv6 (`[::1]`), contraseñas con caracteres especiales/unicode, sockets Unix (futuro; `Host` que empiece por `/` → `UNSUPPORTED` por ahora), MariaDB, `caching_sha2_password`, servidores con `require_secure_transport`.
