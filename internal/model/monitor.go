package model

import "time"

// DTOs del monitoreo DBA (spec 22). Los del driver (MonitorCaps, Process, DigestCounters) son lo que entrega
// driver.ActivityReader; el resto son las peticiones y respuestas de MonitorService. Lo persistente (ejecuciones,
// muestras, ajustes) vive en el store, no aquí.

// Estado de una fuente de datos del monitor.
const (
	SourceOK          = "ok"
	SourceDegraded    = "degraded"
	SourceUnavailable = "unavailable"
)

// Motivos de SourceState.Reason.
const (
	ReasonOK            = "ok"
	ReasonOwnOnly       = "own_only"        // sin PROCESS: solo se ven los hilos del propio usuario
	ReasonPerfSchemaOff = "perf_schema_off" // performance_schema desactivado (requiere reiniciar el servidor)
	ReasonConsumerOff   = "consumer_off"    // performance_schema activo pero el consumidor del digest está apagado
	ReasonNoPrivilege   = "no_privilege"
	ReasonUnsupported   = "unsupported"
	ReasonError         = "error"
)

// SourceState describe si una fuente (procesos, digest, estado global) se puede leer. Una fuente no disponible es un
// estado, no un error (spec 22 ⚑D9). Hint es texto fijo que explica cómo habilitarla; la app nunca lo ejecuta.
type SourceState struct {
	State  string `json:"state"`  // ok | degraded | unavailable
	Reason string `json:"reason"` // ver Reason*
	Hint   string `json:"hint"`
}

type MonitorCaps struct {
	ServerVersion string      `json:"serverVersion"`
	IsMariaDB     bool        `json:"isMariaDB"`
	ServerID      string      `json:"serverId"` // @@server_uuid o, si no existe, "id:<@@server_id>"
	Processlist   SourceState `json:"processlist"`
	Digest        SourceState `json:"digest"`
	Status        SourceState `json:"status"`
}

// Process es una fila de INFORMATION_SCHEMA.PROCESSLIST. Info solo vive en memoria (spec 22 ⚑D7): nunca se persiste.
type Process struct {
	ID            uint64 `json:"id"`
	User          string `json:"user"`
	Host          string `json:"host"`
	DB            string `json:"db"`
	Command       string `json:"command"`
	TimeSec       int64  `json:"timeSec"`
	State         string `json:"state"`
	Info          string `json:"info"` // truncado a 2 KiB
	InfoTruncated bool   `json:"infoTruncated"`
}

// DigestCounters son los contadores acumulados de un digest (sin texto). Se leen como uint64: SUM_TIMER_WAIT (picosegundos)
// puede superar el máximo de un int64. Digest "" es la fila de desbordamiento (tabla de digests llena, DIGEST NULL).
type DigestCounters struct {
	Schema        string `json:"schema"`
	Digest        string `json:"digest"`
	Execs         uint64 `json:"execs"`
	TimerPs       uint64 `json:"timerPs"`
	MaxTimerPs    uint64 `json:"maxTimerPs"`
	RowsExamined  uint64 `json:"rowsExamined"`
	RowsSent      uint64 `json:"rowsSent"`
	NoIndex       uint64 `json:"noIndex"` // SUM_NO_INDEX_USED + SUM_NO_GOOD_INDEX_USED
	TmpDiskTables uint64 `json:"tmpDiskTables"`
	Errors        uint64 `json:"errors"`
}

// Criterios de orden del ranking (TopRequest.OrderBy).
const (
	OrderTotalTime = "total_time"
	OrderExecs     = "execs"
	OrderAvgTime   = "avg_time"
	OrderRowsRatio = "rows_ratio"
	OrderNoIndex   = "no_index"
	OrderTmpDisk   = "tmp_disk"
	OrderErrors    = "errors"
)

type MonitorConfig struct {
	IntervalSec int  `json:"intervalSec"`
	TopK        int  `json:"topK"`
	Processes   bool `json:"processes"`
	Status      bool `json:"status"`
	Digest      bool `json:"digest"`
}

type ProcessSummary struct {
	Total       int            `json:"total"`
	Running     int            `json:"running"` // COMMAND <> 'Sleep'
	Sleeping    int            `json:"sleeping"`
	WaitingLock int            `json:"waitingLock"`
	LongestSec  int64          `json:"longestSec"`
	ByCommand   map[string]int `json:"byCommand"`
	ByUser      map[string]int `json:"byUser"`
}

// ProcessSnapshot es lo que recibe la vista En vivo; solo en memoria.
type ProcessSnapshot struct {
	At      time.Time      `json:"at"`
	Rows    []Process      `json:"rows"`
	Summary ProcessSummary `json:"summary"`
	// MaxConnections es @@global.max_connections, leído una vez por vigilancia; 0 si no se pudo leer.
	MaxConnections uint64 `json:"maxConnections"`
}

type DigestStat struct {
	Digest        string      `json:"digest"`
	Schema        string      `json:"schema"`
	Text          string      `json:"text"`
	Execs         uint64      `json:"execs"`
	TotalMs       float64     `json:"totalMs"`
	AvgMs         float64     `json:"avgMs"`
	MaxObservedMs float64     `json:"maxObservedMs"`
	RowsExamined  uint64      `json:"rowsExamined"`
	RowsSent      uint64      `json:"rowsSent"`
	NoIndex       uint64      `json:"noIndex"`
	TmpDisk       uint64      `json:"tmpDisk"`
	Errors        uint64      `json:"errors"`
	Prev          *DigestStat `json:"prev,omitempty"`
	ChangePct     float64     `json:"changePct"`
	IsNew         bool        `json:"isNew"`
	Regression    bool        `json:"regression"` // ChangePct ≥ RegressionMinPct con ≥ RegressionMinExecs ejecuciones en ambos periodos (spec 22 ⚑D14)
	Approx        bool        `json:"approx"`
}

type TopRequest struct {
	ProfileID     string    `json:"profileId"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	Schema        string    `json:"schema"`
	OrderBy       string    `json:"orderBy"`
	Limit         int       `json:"limit"`
	Compare       bool      `json:"compare"`
	IncludeSystem bool      `json:"includeSystem"`
}

type SeriesRequest struct {
	ProfileID string    `json:"profileId"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Metrics   []string  `json:"metrics"`
	MaxPoints int       `json:"maxPoints"`
}

type SeriesPoint struct {
	T   time.Time `json:"t"`
	V   float64   `json:"v"`
	Min *float64  `json:"min,omitempty"`
	Max *float64  `json:"max,omitempty"`
}

type TimeGap struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Annotation es una nota del usuario sobre la línea de tiempo (p. ej. «deploy v2.14»).
type Annotation struct {
	ID        string    `json:"id"`
	ProfileID string    `json:"profileId"`
	At        time.Time `json:"at"`
	Text      string    `json:"text"` // ≤ 200 caracteres
}

type Series struct {
	Metric      string        `json:"metric"`
	Level       string        `json:"level"` // raw | hour | day
	Points      []SeriesPoint `json:"points"`
	Gaps        []TimeGap     `json:"gaps"`
	Annotations []Annotation  `json:"annotations"`
}
