package model

import "time"

// DTOs de la migración de schema (spec 23). Los id de job viajan como texto. Nada de esto lleva datos de filas.

// Estados de un job.
const (
	MigPlanning   = "planning"
	MigRunning    = "running"
	MigCancelling = "cancelling"
	MigDone       = "done"
	MigFailed     = "failed"
	MigCanceled   = "canceled"
	MigPartial    = "partial"
)

// Fases de un job (spec 23 ⚑D3).
const (
	PhaseSchema   = "schema"
	PhaseTables   = "tables"
	PhaseData     = "data"
	PhaseKeys     = "keys"
	PhaseRoutines = "routines"
	PhaseViews    = "views"
	PhaseTriggers = "triggers"
	PhaseVerify   = "verify"
)

// Estados de una tabla dentro del job.
const (
	TblPending  = "pending"
	TblCreating = "creating"
	TblCopying  = "copying"
	TblDone     = "done"
	TblFailed   = "failed"
	TblSkipped  = "skipped"
)

// Claves de troceo (spec 23 ⚑D7).
const (
	ChunkPK        = "pk"
	ChunkUnique    = "unique"
	ChunkPartition = "partition"
	ChunkIndex     = "index"
	ChunkNone      = "none"
)

// Rutas de copia (spec 23 ⚑D2).
const (
	RouteServer = "server"
	RouteGo     = "go"
)

const (
	MigDefaultWorkers = 4
	MigMaxWorkers     = 16
	MigMinChunkRows   = 1000
	MigMaxChunkRows   = 1_000_000
)

type MigrationEndpoint struct {
	SessionID string `json:"sessionId"`
	Database  string `json:"database"`
}

// RoutineRef identifica una rutina: procedimiento y función pueden repetir nombre.
type RoutineRef struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // procedure | function
}

// MigrationObjects elige qué se migra. Un campo nil/ausente = todos los de su tipo; un slice vacío no nulo = ninguno.
type MigrationObjects struct {
	Tables []string `json:"tables"`
	// StructureOnly son tablas de `Tables` que se crean vacías: sin copiar sus filas (spec 23 ⚑D20).
	StructureOnly []string     `json:"structureOnly"`
	Views         []string     `json:"views"`
	Routines      []RoutineRef `json:"routines"`
	Triggers      []string     `json:"triggers"`
}

type MigrationOptions struct {
	Workers           int  `json:"workers"`   // 0 = por defecto
	ChunkRows         int  `json:"chunkRows"` // 0 = automático
	SkipExisting      bool `json:"skipExisting"`
	VerifyForeignKeys bool `json:"verifyForeignKeys"`
	VerifyRowCounts   bool `json:"verifyRowCounts"`
}

type MigrationRequest struct {
	Source  MigrationEndpoint `json:"source"`
	Target  MigrationEndpoint `json:"target"`
	Objects MigrationObjects  `json:"objects"`
	Options MigrationOptions  `json:"options"`
}

// ---- Lo que el driver lee del origen ----

// ChunkKeySpec es la clave por la que se parte una tabla (⚑D7).
type ChunkKeySpec struct {
	Kind       string   `json:"kind"`
	Columns    []string `json:"columns"`
	Partitions []string `json:"partitions"`
}

type MigrationTable struct {
	Name           string       `json:"name"`
	EstRows        int64        `json:"estRows"`
	EstBytes       int64        `json:"estBytes"`
	AvgRowLen      int64        `json:"avgRowLen"`
	Columns        []string     `json:"columns"`        // las que se copian (incluye invisibles, excluye generadas)
	SkippedColumns []string     `json:"skippedColumns"` // generadas
	CreateSQL      string       `json:"-"`              // SHOW CREATE TABLE; no viaja a Vue (se ve en Statements)
	ForeignKeys    []ForeignKey `json:"foreignKeys"`
	Key            ChunkKeySpec `json:"key"`
	HasPrimaryKey  bool         `json:"hasPrimaryKey"`
	Spatial        []string     `json:"spatial"` // columnas espaciales (no se copian entre servidores)
	AutoIncrement  int64        `json:"autoIncrement"`
	AutoIncColumn  string       `json:"autoIncColumn"`
	Charsets       []string     `json:"charsets"` // collations usadas por la tabla y sus columnas
	Partitioned    bool         `json:"partitioned"`
	KeyCardinality int64        `json:"keyCardinality"` // valores distintos de la columna líder (clave de troceo no única)
}

// MigrationCode es una vista, rutina o trigger tal como lo guarda el servidor.
type MigrationCode struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`  // view | procedure | function | trigger
	Table     string `json:"table"` // solo triggers
	CreateSQL string `json:"-"`
	Definer   string `json:"definer"`
	SQLMode   string `json:"-"`
	Charset   string `json:"-"` // character_set_client
	Collation string `json:"-"` // collation_connection
}

type MigrationSource struct {
	Database      string           `json:"database"`
	Charset       string           `json:"charset"`
	Collation     string           `json:"collation"`
	ServerVersion string           `json:"serverVersion"`
	MariaDB       bool             `json:"mariaDb"`
	SQLMode       string           `json:"-"`
	Tables        []MigrationTable `json:"tables"`
	Views         []MigrationCode  `json:"views"`
	Routines      []MigrationCode  `json:"routines"`
	Triggers      []MigrationCode  `json:"triggers"`
}

// ObjectRef nombra una vista, rutina o trigger por su tipo (`view`, `procedure`, `function`, `trigger`) y su nombre.
type ObjectRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// MigrationPreflightInput es lo que el destino comprueba antes de crear nada.
type MigrationPreflightInput struct {
	Database     string   `json:"database"`
	SourceName   string   `json:"sourceName"` // para comparar nombres canónicos si es el mismo servidor
	SameServer   bool     `json:"sameServer"`
	Collations   []string `json:"collations"`
	NoPKTables   []string `json:"noPkTables"`
	NeedsRoutine bool     `json:"needsRoutine"`
	NeedsTrigger bool     `json:"needsTrigger"`
	NeedsView    bool     `json:"needsView"`
	Workers      int      `json:"workers"`
	// Objects son las vistas, rutinas y triggers que se van a crear: el destino dice cuáles ya tiene y con qué código (⚑D21).
	Objects []ObjectRef `json:"objects"`
}

type MigrationPreflight struct {
	CanonicalName   string   `json:"canonicalName"` // nombre del destino tal como lo guarda el servidor ("" si no existe)
	Exists          bool     `json:"exists"`
	ExistingTables  []string `json:"existingTables"`
	ExistingObjects int      `json:"existingObjects"` // tablas, vistas, rutinas y triggers
	// ExistingCode es, de los `Objects` pedidos, lo que el destino ya tiene, con su CREATE (vacío si no se pudo leer).
	ExistingCode     []MigrationCode `json:"existingCode"`
	SameAsSource     bool            `json:"sameAsSource"`
	MaxConnections   int             `json:"maxConnections"`
	FreeConnections  int             `json:"freeConnections"`
	RequirePK        bool            `json:"requirePk"`
	MissingCollation []string        `json:"missingCollations"`
	Missing          []string        `json:"missing"` // privilegios que faltan, con el GRANT exacto como texto fijo
	MaxPacket        int64           `json:"maxPacket"`
	ServerVersion    string          `json:"serverVersion"`
	MariaDB          bool            `json:"mariaDb"`
	CurrentUser      string          `json:"currentUser"` // CURRENT_USER() de la conexión del destino, para avisar de DEFINER ajenos
}

// ---- Plan, job y resultado ----

type MigrationWarning struct {
	Code    string `json:"code"`
	Level   string `json:"level"` // info | warning | danger
	Object  string `json:"object"`
	Message string `json:"message"`
}

type PlanTable struct {
	Name           string   `json:"name"`
	EstRows        int64    `json:"estRows"`
	EstBytes       int64    `json:"estBytes"`
	Chunks         int      `json:"chunks"`
	ChunkKey       string   `json:"chunkKey"`
	Columns        int      `json:"columns"`
	SkippedColumns []string `json:"skippedColumns"`
	Skipped        bool     `json:"skipped"`
	StructureOnly  bool     `json:"structureOnly"` // se crea vacía: sin filas ni trozos
	Warnings       []string `json:"warnings"`
}

type PlanObject struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Table string `json:"table"`
	// Skipped: no se migra. O ya existe en el destino y «omitir lo que ya existe» lo deja como está, y Compare dice si su código es
	// igual (`same`), distinto (`different`) o no se pudo comparar (`unknown`); o el usuario no puede leer su código en el origen
	// (Compare = `unreadable`: hace falta SHOW_ROUTINE global, SELECT sobre mysql.proc en MariaDB, SHOW VIEW o ser el definer).
	Skipped bool   `json:"skipped"`
	Compare string `json:"compare"`
}

type ServerCaps struct {
	MaxConnections  int `json:"maxConnections"`
	FreeConnections int `json:"freeConnections"`
}

type MigrationPlan struct {
	PlanHash   string             `json:"planHash"`
	Route      string             `json:"route"`
	Tables     []PlanTable        `json:"tables"`
	Views      []PlanObject       `json:"views"`
	Routines   []PlanObject       `json:"routines"`
	Triggers   []PlanObject       `json:"triggers"`
	Workers    int                `json:"workers"`
	Warnings   []MigrationWarning `json:"warnings"`
	Statements []string           `json:"statements"` // DDL exacto de las fases A, D, E y F
	ServerCaps ServerCaps         `json:"serverCaps"`
	TargetNew  bool               `json:"targetNew"` // el schema destino se crea
}

type MigrationTableState struct {
	Name          string `json:"name"`
	State         string `json:"state"`
	StructureOnly bool   `json:"structureOnly"`
	RowsDone      int64  `json:"rowsDone"`
	RowsEst       int64  `json:"rowsEst"`
	ChunksDone    int    `json:"chunksDone"`
	ChunksAll     int    `json:"chunksAll"`
	Error         string `json:"error,omitempty"`
}

type MigrationObjectState struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	State string `json:"state"` // pending | done | failed | skipped
	Error string `json:"error,omitempty"`
}

type MigrationJob struct {
	ID         string                 `json:"id"`
	State      string                 `json:"state"`
	Phase      string                 `json:"phase"`
	Route      string                 `json:"route"`
	StartedAt  time.Time              `json:"startedAt"`
	ElapsedMs  int64                  `json:"elapsedMs"`
	Rows       int64                  `json:"rows"`
	EstRows    int64                  `json:"estRows"`
	Bytes      int64                  `json:"bytes"`
	TablesDone int                    `json:"tablesDone"`
	TablesAll  int                    `json:"tablesAll"`
	Tables     []MigrationTableState  `json:"tables"`
	Objects    []MigrationObjectState `json:"objects"`
	Error      string                 `json:"error,omitempty"`
}

type MigrationFailure struct {
	Object string `json:"object"`
	Kind   string `json:"kind"` // table | foreignKey | view | routine | trigger | schema
	Error  string `json:"error"`
}

type MigrationCreated struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // database | table | view | procedure | function | trigger
}

type OrphanRow struct {
	ForeignKey string `json:"foreignKey"`
	Table      string `json:"table"`
	Count      int64  `json:"count"`
	AtLeast    bool   `json:"atLeast"` // el recuento se cortó en el tope de la consulta
}

type RowCountDiff struct {
	Table  string `json:"table"`
	Source int64  `json:"source"`
	Target int64  `json:"target"`
}

// ObjectDiff es el resultado de comparar una vista, rutina o trigger creado con lo que hay en el destino: `same`, `missing` (no está),
// `different` (otro código) o `unreadable` (no se pudo leer uno de los dos y no se puede afirmar nada). Salen todos, también los iguales.
type ObjectDiff struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type MigrationVerification struct {
	Orphans        []OrphanRow    `json:"orphans"`
	RowCounts      []RowCountDiff `json:"rowCounts"`
	Objects        []ObjectDiff   `json:"objects"`
	ObjectsChecked int            `json:"objectsChecked"` // vistas, rutinas y triggers comparados (= len(Objects))
	Checked        bool           `json:"checked"`
}

type VerifyOptions struct {
	ForeignKeys bool `json:"foreignKeys"`
	RowCounts   bool `json:"rowCounts"`
	Objects     bool `json:"objects"` // compara el código de las vistas, rutinas y triggers creados (⚑D22)
}

type MigrationResult struct {
	Job          MigrationJob           `json:"job"`
	Failed       []MigrationFailure     `json:"failed"`
	Created      []MigrationCreated     `json:"created"`
	Warnings     []MigrationWarning     `json:"warnings"`
	Verification *MigrationVerification `json:"verification"`
}
