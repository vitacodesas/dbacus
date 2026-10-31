package model

// DTOs del editor de estructura de tabla (spec 19). Vue envía operaciones tipadas y nunca arma SQL.

// AlterModel es la lectura fresca (sin caché) de una tabla que se va a editar.
type AlterModel struct {
	Database    string       `json:"database"`
	Table       string       `json:"table"`
	Columns     []Column     `json:"columns"`
	Indexes     []Index      `json:"indexes"`
	ForeignKeys []ForeignKey `json:"foreignKeys"`
	IncomingFKs []ForeignKey `json:"incomingFks"`
	CreateSQL   string       `json:"createSql"`
	// TableCollation es la collation por defecto de la tabla.
	TableCollation string `json:"tableCollation"`
	// Fingerprint cambia cuando cambia la estructura (no con el contador AUTO_INCREMENT); Preview y Apply lo comparan.
	Fingerprint string `json:"fingerprint"`
	RowEstimate *int64 `json:"rowEstimate"`
}

// Tipos de operación de AlterOp.
const (
	OpAddColumn    = "addColumn"
	OpModifyColumn = "modifyColumn" // también renombra (NewName) y reordena (First/After)
	OpDropColumn   = "dropColumn"
	OpAddIndex     = "addIndex"
	OpDropIndex    = "dropIndex"
	OpRenameIndex  = "renameIndex"
	OpAddFK        = "addFK"
	OpDropFK       = "dropFK"
)

// ColumnSpec es el estado deseado de una columna. Las propiedades que Dbacus no edita (collation, charset, ON UPDATE)
// no viajan: Go las toma de la columna original.
type ColumnSpec struct {
	Type        string  `json:"type"` // COLUMN_TYPE: varchar(255), bigint unsigned, enum('a','b')…
	Nullable    bool    `json:"nullable"`
	Default     *string `json:"default"` // nil = sin cláusula DEFAULT
	DefaultExpr bool    `json:"defaultExpr"`
	AutoInc     bool    `json:"autoInc"`
	Comment     string  `json:"comment"`
}

type IndexColumn struct {
	Name   string `json:"name"`
	Desc   bool   `json:"desc"`
	Prefix int    `json:"prefix"` // longitud del prefijo; 0 = columna entera
}

type IndexSpec struct {
	Kind    string        `json:"kind"` // index|unique|fulltext
	Columns []IndexColumn `json:"columns"`
}

type FKSpec struct {
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"` // "" = la misma base
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnUpdate   string   `json:"onUpdate"`
	OnDelete   string   `json:"onDelete"`
}

// AlterOp es una operación del ChangeSet. Name es el nombre actual (modify/drop/rename) o el nuevo (add).
type AlterOp struct {
	Kind    string      `json:"kind"`
	Name    string      `json:"name"`
	NewName string      `json:"newName"` // modifyColumn y renameIndex
	Column  *ColumnSpec `json:"column"`  // addColumn; modifyColumn (nil = conservar la actual)
	First   bool        `json:"first"`   // addColumn/modifyColumn: moverla al principio
	After   string      `json:"after"`   // addColumn/modifyColumn: moverla tras esta columna
	Index   *IndexSpec  `json:"index"`
	FK      *FKSpec     `json:"fk"`
}

type ChangeSet struct {
	Database    string    `json:"database"`
	Table       string    `json:"table"`
	Fingerprint string    `json:"fingerprint"` // el de GetModel; "" = no comprobar
	Ops         []AlterOp `json:"ops"`
}

type AlterWarning struct {
	Code     string `json:"code"` // drop_column|drop_index|drop_fk|lossy_change|large_table|no_primary_key
	Message  string `json:"message"`
	Severity string `json:"severity"` // info|warning|danger
}

type AlterPreview struct {
	Statements    []string       `json:"statements"`
	SQLHash       string         `json:"sqlHash"`
	Warnings      []AlterWarning `json:"warnings"`
	Destructive   bool           `json:"destructive"`
	NoRollback    bool           `json:"noRollback"`
	EstimatedRows *int64         `json:"estimatedRows"`
}

type AlterFailure struct {
	Index   int    `json:"index"` // posición de la sentencia que falló
	Message string `json:"message"`
}

type AlterResult struct {
	Applied []string      `json:"applied"`
	Failed  *AlterFailure `json:"failed"`
	Model   *AlterModel   `json:"model"`
}
