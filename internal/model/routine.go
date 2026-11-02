package model

// DTOs de procedimientos y funciones almacenadas (spec 21). Vue envía el texto del CREATE y una operación tipada; nunca arma SQL.

// Tipos de rutina.
const (
	KindProcedure = "procedure"
	KindFunction  = "function"
)

// Operaciones de RoutineChange.
const (
	RoutineCreate  = "create"
	RoutineReplace = "replace"
	RoutineDrop    = "drop"
)

// Routine es una entrada de la lista de una base: sin cuerpo ni parámetros (una base puede tener cientos).
type Routine struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"` // procedure | function
	Definer      string `json:"definer"`
	Comment      string `json:"comment"`
	SecurityType string `json:"securityType"` // DEFINER | INVOKER
	LastAltered  string `json:"lastAltered"`
}

type RoutineParam struct {
	Name    string `json:"name"`
	Mode    string `json:"mode"` // IN | OUT | INOUT ("" en funciones)
	Type    string `json:"type"`
	Ordinal int    `json:"ordinal"`
}

// RoutineDetail es la lectura fresca de una rutina: el CREATE completo y lo que hace falta para recrearla igual.
type RoutineDetail struct {
	Routine
	Database            string         `json:"database"`
	Params              []RoutineParam `json:"params"`
	Returns             string         `json:"returns"` // solo funciones
	CreateSQL           string         `json:"createSql"`
	SQLMode             string         `json:"sqlMode"`
	CharsetClient       string         `json:"charsetClient"`
	CollationConnection string         `json:"collationConnection"`
	DatabaseCollation   string         `json:"databaseCollation"`
	// BodyHidden: el usuario no tiene privilegio para ver el cuerpo (CreateSQL vacío); no es un error.
	BodyHidden  bool   `json:"bodyHidden"`
	Fingerprint string `json:"fingerprint"`
}

// RoutineChange es lo que Vue pide: crear, reemplazar o borrar una rutina. SQL es el CREATE completo (vacío en drop).
type RoutineChange struct {
	Database    string `json:"database"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Op          string `json:"op"`
	SQL         string `json:"sql"`
	Fingerprint string `json:"fingerprint"`
	// RecreateDefiner quita la cláusula DEFINER del CREATE para que la rutina quede a nombre del usuario actual.
	RecreateDefiner bool `json:"recreateDefiner"`
}

type RoutinePreview struct {
	Statements  []string       `json:"statements"`
	SQLHash     string         `json:"sqlHash"`
	Warnings    []AlterWarning `json:"warnings"`
	Destructive bool           `json:"destructive"`
	NoRollback  bool           `json:"noRollback"`
}

// RoutineResult: un fallo de la base no es error de la llamada, llega en Failed. Restored dice que la original se
// recuperó tras un CREATE fallido; BackupSQL es la copia previa cuando ni la restauración funcionó.
type RoutineResult struct {
	Applied   []string       `json:"applied"`
	Failed    *AlterFailure  `json:"failed"`
	Restored  bool           `json:"restored"`
	BackupSQL string         `json:"backupSql"`
	Detail    *RoutineDetail `json:"detail"`
}
