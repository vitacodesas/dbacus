# ADR-003 — SQLite local para metadatos y llavero para secretos

**Estado**: aceptada

**Decisión**: perfiles, historial, ajustes y estado de UI en SQLite (`modernc.org/sqlite`, sin CGO, simplifica cross-compile) con migraciones embebidas. Secretos únicamente en el llavero del SO vía `zalando/go-keyring`.

**Consecuencias**
- Linux sin Secret Service (headless/CI) → `KEYRING_UNAVAILABLE`; en tests se usa `keyring.MockInit()`.
- Modo WAL; una sola conexión de escritura serializada por mutex.
- Exportar/importar perfiles (futuro) deberá excluir secretos o cifrarlos con passphrase del usuario.
