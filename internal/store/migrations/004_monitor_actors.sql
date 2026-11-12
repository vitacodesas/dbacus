-- Quién se vio ejecutando cada digest (spec 22 ⚑D17). Por muestreo del processlist: aproximado, nunca guarda INFO.
-- Un contador por hora y (digest, usuario, host); `seen` = nº de muestras en que se vio; `last_ts` en milisegundos.
-- El digest es el del servidor (sin esquema), igual que STATEMENT_DIGEST. Se borra con el perfil.
CREATE TABLE monitor_digest_actor (
    profile_id TEXT NOT NULL REFERENCES connection_profiles(id) ON DELETE CASCADE,
    digest     TEXT NOT NULL,
    user       TEXT NOT NULL,
    host       TEXT NOT NULL,
    hour_ts    INTEGER NOT NULL,
    seen       INTEGER NOT NULL,
    last_ts    INTEGER NOT NULL,
    PRIMARY KEY (profile_id, digest, user, host, hour_ts)
) WITHOUT ROWID;
CREATE INDEX idx_monitor_digest_actor_hour ON monitor_digest_actor (profile_id, hour_ts);
