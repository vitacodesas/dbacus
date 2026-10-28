package model

// DTOs de esquema y metadatos de columnas (spec 02/03).

type Database struct {
	Name     string `json:"name"`
	IsSystem bool   `json:"isSystem"`
}

type Table struct {
	Name        string `json:"name"`
	Schema      string `json:"schema"`
	Type        string `json:"type"` // table|view
	RowEstimate *int64 `json:"rowEstimate"`
	Comment     string `json:"comment"`
}

type Column struct {
	Name         string  `json:"name"`
	DataType     string  `json:"dataType"`
	ColumnType   string  `json:"columnType"`
	Nullable     bool    `json:"nullable"`
	Default      *string `json:"default"`
	IsPrimaryKey bool    `json:"isPrimaryKey"`
	IsAutoInc    bool    `json:"isAutoInc"`
	Comment      string  `json:"comment"`
	Ordinal      int     `json:"ordinal"`
}

type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	Type    string   `json:"type"`
}

type ForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnUpdate   string   `json:"onUpdate"`
	OnDelete   string   `json:"onDelete"`
}

type TableDetail struct {
	Table
	Columns     []Column     `json:"columns"`
	Indexes     []Index      `json:"indexes"`
	ForeignKeys []ForeignKey `json:"foreignKeys"`
	CreateSQL   string       `json:"createSql"`
}

// CompletionCatalog alimenta el autocompletado de Monaco (spec 05).
type CompletionCatalog struct {
	Tables []CatalogTable `json:"tables"`
}

type CatalogTable struct {
	Name    string          `json:"name"`
	Columns []CatalogColumn `json:"columns"`
}

type CatalogColumn struct {
	Name     string `json:"name"`
	DataType string `json:"dataType"`
}

type ColumnMeta struct {
	Name      string `json:"name"`
	DataType  string `json:"dataType"`
	Nullable  bool   `json:"nullable"`
	Precision *int   `json:"precision"`
	Scale     *int   `json:"scale"`
}
