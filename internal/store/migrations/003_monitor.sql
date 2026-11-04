-- Monitoreo DBA (spec 22, ADR-007). Los borrados por perfil son en cascada (foreign_keys=1).
-- Tiempos en milisegundos desde epoch; duraciones de digest en microsegundos; las métricas de servidor son tasas por segundo
-- (Threads_* y proc_* son valores instantáneos). NULL = fuente no disponible en esa muestra.

CREATE TABLE monitor_run (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id     TEXT NOT NULL REFERENCES connection_profiles(id) ON DELETE CASCADE,
    started_at     INTEGER NOT NULL,
    ended_at       INTEGER,
    reason         TEXT NOT NULL DEFAULT 'running',
    interval_sec   INTEGER NOT NULL,
    server_version TEXT NOT NULL DEFAULT '',
    server_id      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_monitor_run_profile ON monitor_run (profile_id, started_at DESC);

CREATE TABLE monitor_sample (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id     INTEGER NOT NULL REFERENCES monitor_run(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES connection_profiles(id) ON DELETE CASCADE,
    ts         INTEGER NOT NULL,
    span_ms    INTEGER NOT NULL DEFAULT 0,
    after_gap  INTEGER NOT NULL DEFAULT 0,
    qps REAL,
    com_select REAL,
    com_insert REAL,
    com_update REAL,
    com_delete REAL,
    slow_queries REAL,
    aborted_connects REAL,
    aborted_clients REAL,
    row_lock_waits REAL,
    row_lock_time_ms REAL,
    bp_reads REAL,
    bp_read_requests REAL,
    tmp_tables REAL,
    tmp_disk_tables REAL,
    select_full_join REAL,
    bytes_received REAL,
    bytes_sent REAL,
    threads_connected REAL,
    threads_running REAL,
    proc_total REAL,
    proc_running REAL,
    proc_sleeping REAL,
    proc_waiting_lock REAL,
    proc_longest_sec REAL,
    proc_long_count REAL,
    proc_json  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_monitor_sample_profile_ts ON monitor_sample (profile_id, ts);
CREATE INDEX idx_monitor_sample_run ON monitor_sample (run_id);
CREATE INDEX idx_monitor_sample_ts ON monitor_sample (ts);

CREATE TABLE monitor_digest (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id  TEXT NOT NULL REFERENCES connection_profiles(id) ON DELETE CASCADE,
    digest      TEXT NOT NULL,
    schema_name TEXT NOT NULL DEFAULT '',
    text        TEXT NOT NULL DEFAULT '',
    first_seen  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    UNIQUE (profile_id, digest, schema_name)
);

CREATE TABLE monitor_digest_sample (
    sample_id       INTEGER NOT NULL REFERENCES monitor_sample(id) ON DELETE CASCADE,
    digest_id       INTEGER NOT NULL REFERENCES monitor_digest(id) ON DELETE CASCADE,
    ts              INTEGER NOT NULL,
    execs           INTEGER NOT NULL,
    total_us        INTEGER NOT NULL,
    max_observed_us INTEGER NOT NULL DEFAULT 0,
    rows_examined   INTEGER NOT NULL DEFAULT 0,
    rows_sent       INTEGER NOT NULL DEFAULT 0,
    no_index        INTEGER NOT NULL DEFAULT 0,
    tmp_disk        INTEGER NOT NULL DEFAULT 0,
    errors          INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (sample_id, digest_id)
) WITHOUT ROWID;
CREATE INDEX idx_monitor_digest_sample_digest ON monitor_digest_sample (digest_id, ts);

CREATE TABLE monitor_rollup_hour (
    profile_id TEXT NOT NULL REFERENCES connection_profiles(id) ON DELETE CASCADE,
    hour_ts INTEGER NOT NULL,
    samples INTEGER NOT NULL,
    first_ts INTEGER NOT NULL,
    last_ts INTEGER NOT NULL,
    span_ms INTEGER NOT NULL,
    qps_avg REAL, qps_min REAL, qps_max REAL,
    com_select_avg REAL, com_select_min REAL, com_select_max REAL,
    com_insert_avg REAL, com_insert_min REAL, com_insert_max REAL,
    com_update_avg REAL, com_update_min REAL, com_update_max REAL,
    com_delete_avg REAL, com_delete_min REAL, com_delete_max REAL,
    slow_queries_avg REAL, slow_queries_min REAL, slow_queries_max REAL,
    aborted_connects_avg REAL, aborted_connects_min REAL, aborted_connects_max REAL,
    aborted_clients_avg REAL, aborted_clients_min REAL, aborted_clients_max REAL,
    row_lock_waits_avg REAL, row_lock_waits_min REAL, row_lock_waits_max REAL,
    row_lock_time_ms_avg REAL, row_lock_time_ms_min REAL, row_lock_time_ms_max REAL,
    bp_reads_avg REAL, bp_reads_min REAL, bp_reads_max REAL,
    bp_read_requests_avg REAL, bp_read_requests_min REAL, bp_read_requests_max REAL,
    tmp_tables_avg REAL, tmp_tables_min REAL, tmp_tables_max REAL,
    tmp_disk_tables_avg REAL, tmp_disk_tables_min REAL, tmp_disk_tables_max REAL,
    select_full_join_avg REAL, select_full_join_min REAL, select_full_join_max REAL,
    bytes_received_avg REAL, bytes_received_min REAL, bytes_received_max REAL,
    bytes_sent_avg REAL, bytes_sent_min REAL, bytes_sent_max REAL,
    threads_connected_avg REAL, threads_connected_min REAL, threads_connected_max REAL,
    threads_running_avg REAL, threads_running_min REAL, threads_running_max REAL,
    proc_total_avg REAL, proc_total_min REAL, proc_total_max REAL,
    proc_running_avg REAL, proc_running_min REAL, proc_running_max REAL,
    proc_sleeping_avg REAL, proc_sleeping_min REAL, proc_sleeping_max REAL,
    proc_waiting_lock_avg REAL, proc_waiting_lock_min REAL, proc_waiting_lock_max REAL,
    proc_longest_sec_avg REAL, proc_longest_sec_min REAL, proc_longest_sec_max REAL,
    proc_long_count_avg REAL, proc_long_count_min REAL, proc_long_count_max REAL,
    PRIMARY KEY (profile_id, hour_ts)
) WITHOUT ROWID;

CREATE TABLE monitor_rollup_hour_digest (
    profile_id TEXT NOT NULL REFERENCES connection_profiles(id) ON DELETE CASCADE,
    hour_ts INTEGER NOT NULL,
    digest_id INTEGER NOT NULL REFERENCES monitor_digest(id) ON DELETE CASCADE,
    execs INTEGER NOT NULL,
    total_us INTEGER NOT NULL,
    max_observed_us INTEGER NOT NULL DEFAULT 0,
    rows_examined INTEGER NOT NULL DEFAULT 0,
    rows_sent INTEGER NOT NULL DEFAULT 0,
    no_index INTEGER NOT NULL DEFAULT 0,
    tmp_disk INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (profile_id, hour_ts, digest_id)
) WITHOUT ROWID;
CREATE INDEX idx_monitor_rollup_hour_digest_digest ON monitor_rollup_hour_digest (digest_id, hour_ts);

CREATE TABLE monitor_rollup_day (
    profile_id TEXT NOT NULL REFERENCES connection_profiles(id) ON DELETE CASCADE,
    day_ts INTEGER NOT NULL,
    samples INTEGER NOT NULL,
    first_ts INTEGER NOT NULL,
    last_ts INTEGER NOT NULL,
    span_ms INTEGER NOT NULL,
    qps_avg REAL, qps_min REAL, qps_max REAL,
    com_select_avg REAL, com_select_min REAL, com_select_max REAL,
    com_insert_avg REAL, com_insert_min REAL, com_insert_max REAL,
    com_update_avg REAL, com_update_min REAL, com_update_max REAL,
    com_delete_avg REAL, com_delete_min REAL, com_delete_max REAL,
    slow_queries_avg REAL, slow_queries_min REAL, slow_queries_max REAL,
    aborted_connects_avg REAL, aborted_connects_min REAL, aborted_connects_max REAL,
    aborted_clients_avg REAL, aborted_clients_min REAL, aborted_clients_max REAL,
    row_lock_waits_avg REAL, row_lock_waits_min REAL, row_lock_waits_max REAL,
    row_lock_time_ms_avg REAL, row_lock_time_ms_min REAL, row_lock_time_ms_max REAL,
    bp_reads_avg REAL, bp_reads_min REAL, bp_reads_max REAL,
    bp_read_requests_avg REAL, bp_read_requests_min REAL, bp_read_requests_max REAL,
    tmp_tables_avg REAL, tmp_tables_min REAL, tmp_tables_max REAL,
    tmp_disk_tables_avg REAL, tmp_disk_tables_min REAL, tmp_disk_tables_max REAL,
    select_full_join_avg REAL, select_full_join_min REAL, select_full_join_max REAL,
    bytes_received_avg REAL, bytes_received_min REAL, bytes_received_max REAL,
    bytes_sent_avg REAL, bytes_sent_min REAL, bytes_sent_max REAL,
    threads_connected_avg REAL, threads_connected_min REAL, threads_connected_max REAL,
    threads_running_avg REAL, threads_running_min REAL, threads_running_max REAL,
    proc_total_avg REAL, proc_total_min REAL, proc_total_max REAL,
    proc_running_avg REAL, proc_running_min REAL, proc_running_max REAL,
    proc_sleeping_avg REAL, proc_sleeping_min REAL, proc_sleeping_max REAL,
    proc_waiting_lock_avg REAL, proc_waiting_lock_min REAL, proc_waiting_lock_max REAL,
    proc_longest_sec_avg REAL, proc_longest_sec_min REAL, proc_longest_sec_max REAL,
    proc_long_count_avg REAL, proc_long_count_min REAL, proc_long_count_max REAL,
    PRIMARY KEY (profile_id, day_ts)
) WITHOUT ROWID;

CREATE TABLE monitor_rollup_day_digest (
    profile_id TEXT NOT NULL REFERENCES connection_profiles(id) ON DELETE CASCADE,
    day_ts INTEGER NOT NULL,
    digest_id INTEGER NOT NULL REFERENCES monitor_digest(id) ON DELETE CASCADE,
    execs INTEGER NOT NULL,
    total_us INTEGER NOT NULL,
    max_observed_us INTEGER NOT NULL DEFAULT 0,
    rows_examined INTEGER NOT NULL DEFAULT 0,
    rows_sent INTEGER NOT NULL DEFAULT 0,
    no_index INTEGER NOT NULL DEFAULT 0,
    tmp_disk INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (profile_id, day_ts, digest_id)
) WITHOUT ROWID;
CREATE INDEX idx_monitor_rollup_day_digest_digest ON monitor_rollup_day_digest (digest_id, day_ts);

CREATE TABLE monitor_annotation (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id TEXT NOT NULL REFERENCES connection_profiles(id) ON DELETE CASCADE,
    at         INTEGER NOT NULL,
    text       TEXT NOT NULL
);
CREATE INDEX idx_monitor_annotation_profile ON monitor_annotation (profile_id, at);
