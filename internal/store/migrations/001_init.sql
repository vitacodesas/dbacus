CREATE TABLE connection_profiles (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
    driver     TEXT NOT NULL,
    data       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE query_history (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    id            TEXT NOT NULL UNIQUE,
    profile_id    TEXT NOT NULL,
    sql           TEXT NOT NULL,
    database      TEXT NOT NULL DEFAULT '',
    executed_at   INTEGER NOT NULL,
    elapsed_ms    INTEGER NOT NULL DEFAULT 0,
    ok            INTEGER NOT NULL,
    row_count     INTEGER NOT NULL DEFAULT 0,
    affected_rows INTEGER NOT NULL DEFAULT 0,
    error_message TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_history_profile ON query_history (profile_id, seq DESC);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE ui_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
