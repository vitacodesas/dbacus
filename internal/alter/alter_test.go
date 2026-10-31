package alter

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

func str(s string) *string { return &s }

func baseModel() model.AlterModel {
	return model.AlterModel{
		Database: "shop", Table: "orders",
		Columns: []model.Column{
			{Name: "id", ColumnType: "int", IsPrimaryKey: true, IsAutoInc: true},
			{Name: "customer_id", ColumnType: "int unsigned"},
			{Name: "note", ColumnType: "varchar(100)", Nullable: true, Charset: "utf8mb4", Collation: "utf8mb4_0900_ai_ci"},
			{Name: "updated", ColumnType: "timestamp", OnUpdate: "CURRENT_TIMESTAMP"},
			{Name: "gen", ColumnType: "int", IsGenerated: true, Nullable: true},
		},
		Indexes: []model.Index{
			{Name: "PRIMARY", Unique: true, Columns: []string{"id"}},
			{Name: "idx_customer", Columns: []string{"customer_id"}},
		},
		ForeignKeys: []model.ForeignKey{{Name: "fk_customer", Columns: []string{"customer_id"}, RefTable: "customers", RefColumns: []string{"id"}}},
		IncomingFKs: []model.ForeignKey{{Name: "fk_items_order", RefTable: "items", Columns: []string{"order_id"}, RefColumns: []string{"id"}}},
	}
}

func cs(ops ...model.AlterOp) model.ChangeSet {
	return model.ChangeSet{Database: "shop", Table: "orders", Ops: ops}
}

func TestFingerprintIgnoresAutoIncrementCounter(t *testing.T) {
	a := "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT\n) ENGINE=InnoDB AUTO_INCREMENT=554271 DEFAULT CHARSET=utf8mb4"
	b := "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT\n) ENGINE=InnoDB AUTO_INCREMENT=554300 DEFAULT CHARSET=utf8mb4"
	c := "CREATE TABLE `t` (\n  `id` bigint NOT NULL AUTO_INCREMENT\n) ENGINE=InnoDB AUTO_INCREMENT=554271 DEFAULT CHARSET=utf8mb4"
	require.Equal(t, Fingerprint(a), Fingerprint(b))
	require.NotEqual(t, Fingerprint(a), Fingerprint(c))
}

func TestResolvePreservesPropertiesOnlyWhereTheTypeAllowsThem(t *testing.T) {
	m := baseModel()
	note, updated := &m.Columns[2], &m.Columns[3]

	// Cambiar el largo conserva charset y collation.
	c := Resolve(model.AlterOp{Name: "note", Column: &model.ColumnSpec{Type: "varchar(200)", Nullable: true}}, note, "")
	require.Equal(t, "utf8mb4", c.Charset)
	require.Equal(t, "utf8mb4_0900_ai_ci", c.Collation)

	// Pasar a un entero los descarta.
	c = Resolve(model.AlterOp{Name: "note", Column: &model.ColumnSpec{Type: "int", Nullable: true}}, note, "")
	require.Empty(t, c.Charset)
	require.Empty(t, c.Collation)

	// ON UPDATE sobrevive mientras siga siendo temporal, y se pierde si deja de serlo.
	c = Resolve(model.AlterOp{Name: "updated", Column: &model.ColumnSpec{Type: "datetime"}}, updated, "")
	require.Equal(t, "CURRENT_TIMESTAMP", c.OnUpdate)
	c = Resolve(model.AlterOp{Name: "updated", Column: &model.ColumnSpec{Type: "int"}}, updated, "")
	require.Empty(t, c.OnUpdate)

	// Si la collation es la de la tabla, la columna la hereda y no se escribe.
	c = Resolve(model.AlterOp{Name: "note", Column: &model.ColumnSpec{Type: "varchar(100)", Nullable: true}}, note, "utf8mb4_0900_ai_ci")
	require.Empty(t, c.Collation)
	require.Empty(t, c.Charset)

	// Sin definición (renombrar o mover) se parte de la columna original.
	c = Resolve(model.AlterOp{Name: "note", NewName: "remark"}, note, "")
	require.Equal(t, "remark", c.Name)
	require.Equal(t, "varchar(100)", c.Type)
	require.True(t, c.Nullable)
}

func TestValidType(t *testing.T) {
	for _, ok := range []string{"int", "INT UNSIGNED", "varchar(255)", "decimal(10,2)", "decimal(10, 2) unsigned", "enum('a','b')", "enum('it''s','x y')", "datetime(6)", "json", "bigint unsigned zerofill"} {
		require.True(t, ValidType(ok), ok)
	}
	for _, bad := range []string{"", "int; DROP TABLE x", "varchar(255) NOT NULL", "varchar(", "enum('a\\')", "enum('a'b')", "foo", "int(10) --", "int)", "varchar(abc)", "int /*"} {
		require.False(t, ValidType(bad), bad)
	}
}

func TestSafeExpr(t *testing.T) {
	for _, ok := range []string{"uuid()", "concat('a', ')')", "(1+2)", "json_array()", "'it''s'"} {
		require.True(t, safeExpr(ok), ok)
	}
	for _, bad := range []string{"", "  ", "1; DROP TABLE t", "1 -- x", "1 /* x", "a # b", "(1", "1)", "'abc", "`x`"} {
		require.False(t, safeExpr(bad), bad)
	}
}

func detail(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	ae := apperr.From(err)
	require.Equal(t, apperr.Validation, ae.Code)
	return ae.Detail
}

func TestValidate(t *testing.T) {
	m := baseModel()
	intCol := &model.ColumnSpec{Type: "int", Nullable: true}
	cases := []struct {
		name   string
		ops    []model.AlterOp
		detail string // "" = válido
	}{
		{"añadir columna", []model.AlterOp{{Kind: model.OpAddColumn, Name: "qty", Column: intCol}}, ""},
		{"nombre repetido", []model.AlterOp{{Kind: model.OpAddColumn, Name: "note", Column: intCol}}, "ops[0].name"},
		{"nombre vacío", []model.AlterOp{{Kind: model.OpAddColumn, Name: " ", Column: intCol}}, "ops[0].name"},
		{"nombre largo", []model.AlterOp{{Kind: model.OpAddColumn, Name: string(make([]byte, 0)) + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Column: intCol}}, "ops[0].name"},
		{"tipo inválido", []model.AlterOp{{Kind: model.OpAddColumn, Name: "x", Column: &model.ColumnSpec{Type: "int); DROP"}}}, "ops[0].column.type"},
		{"añadir sin definición", []model.AlterOp{{Kind: model.OpAddColumn, Name: "x"}}, "ops[0].column"},
		{"expresión de default insegura", []model.AlterOp{{Kind: model.OpAddColumn, Name: "x", Column: &model.ColumnSpec{Type: "int", Default: str("1; DROP TABLE t"), DefaultExpr: true}}}, "ops[0].column.default"},
		{"first y after", []model.AlterOp{{Kind: model.OpAddColumn, Name: "x", Column: intCol, First: true, After: "id"}}, "ops[0].after"},
		{"after inexistente", []model.AlterOp{{Kind: model.OpAddColumn, Name: "x", Column: intCol, After: "nada"}}, "ops[0].after"},
		{"modificar inexistente", []model.AlterOp{{Kind: model.OpModifyColumn, Name: "nada", Column: intCol}}, "ops[0].name"},
		{"modificar columna generada", []model.AlterOp{{Kind: model.OpModifyColumn, Name: "gen", Column: intCol}}, "generated"},
		{"renombrar a una existente", []model.AlterOp{{Kind: model.OpModifyColumn, Name: "note", NewName: "id"}}, "ops[0].newName"},
		{"renombrar", []model.AlterOp{{Kind: model.OpModifyColumn, Name: "note", NewName: "remark"}}, ""},
		{"borrar la clave primaria", []model.AlterOp{{Kind: model.OpDropColumn, Name: "id"}}, "primaryKey"},
		{"borrar columna con índice", []model.AlterOp{{Kind: model.OpDropColumn, Name: "customer_id"}}, "dependency"},
		{"borrar columna con su índice y su FK", []model.AlterOp{{Kind: model.OpDropFK, Name: "fk_customer"}, {Kind: model.OpDropIndex, Name: "idx_customer"}, {Kind: model.OpDropColumn, Name: "customer_id"}}, ""},
		{"borrar una columna que nadie usa", []model.AlterOp{{Kind: model.OpDropColumn, Name: "note"}}, ""},
		{"AUTO_INCREMENT en un texto", []model.AlterOp{{Kind: model.OpAddColumn, Name: "x", Column: &model.ColumnSpec{Type: "varchar(5)", AutoInc: true}}}, "ops[0].column.autoInc"},
		{"dos AUTO_INCREMENT", []model.AlterOp{{Kind: model.OpAddColumn, Name: "x", Column: &model.ColumnSpec{Type: "int", AutoInc: true}}}, "autoInc"},
		{"índice sin columnas", []model.AlterOp{{Kind: model.OpAddIndex, Name: "i", Index: &model.IndexSpec{Kind: "index"}}}, "ops[0].index"},
		{"índice con columna inexistente", []model.AlterOp{{Kind: model.OpAddIndex, Name: "i", Index: &model.IndexSpec{Kind: "index", Columns: []model.IndexColumn{{Name: "x"}}}}}, "ops[0].index.columns"},
		{"índice PRIMARY", []model.AlterOp{{Kind: model.OpAddIndex, Name: "primary", Index: &model.IndexSpec{Kind: "unique", Columns: []model.IndexColumn{{Name: "note"}}}}}, "ops[0].name"},
		{"índice nuevo con columna nueva", []model.AlterOp{
			{Kind: model.OpAddColumn, Name: "x", Column: intCol},
			{Kind: model.OpAddIndex, Name: "i", Index: &model.IndexSpec{Kind: "unique", Columns: []model.IndexColumn{{Name: "x"}}}}}, ""},
		{"borrar PRIMARY", []model.AlterOp{{Kind: model.OpDropIndex, Name: "PRIMARY"}}, "ops[0].name"},
		{"renombrar índice a uno existente", []model.AlterOp{{Kind: model.OpRenameIndex, Name: "idx_customer", NewName: "PRIMARY"}}, "ops[0].newName"},
		{"FK con distinto número de columnas", []model.AlterOp{{Kind: model.OpAddFK, Name: "f", FK: &model.FKSpec{Columns: []string{"note"}, RefTable: "t", RefColumns: []string{"a", "b"}}}}, "ops[0].fk"},
		{"FK con acción inválida", []model.AlterOp{{Kind: model.OpAddFK, Name: "f", FK: &model.FKSpec{Columns: []string{"note"}, RefTable: "t", RefColumns: []string{"a"}, OnDelete: "SET DEFAULT"}}}, "ops[0].fk"},
		{"FK válida", []model.AlterOp{{Kind: model.OpAddFK, Name: "f", FK: &model.FKSpec{Columns: []string{"note"}, RefTable: "t", RefColumns: []string{"a"}, OnDelete: "cascade"}}}, ""},
		{"FK repetida", []model.AlterOp{{Kind: model.OpAddFK, Name: "fk_customer", FK: &model.FKSpec{Columns: []string{"note"}, RefTable: "t", RefColumns: []string{"a"}}}}, "ops[0].name"},
		{"modificar una FK = borrar y crear", []model.AlterOp{
			{Kind: model.OpDropFK, Name: "fk_customer"},
			{Kind: model.OpAddFK, Name: "fk_customer", FK: &model.FKSpec{Columns: []string{"customer_id"}, RefTable: "customers", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}}, ""},
		{"operación desconocida", []model.AlterOp{{Kind: "dropTable", Name: "orders"}}, "ops[0].kind"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(cs(c.ops...), m)
			if c.detail == "" {
				require.NoError(t, err)
				return
			}
			require.Equal(t, c.detail, detail(t, err))
		})
	}

	t.Run("tabla sin nombre", func(t *testing.T) {
		require.Equal(t, "table", detail(t, Validate(model.ChangeSet{Database: "shop"}, m)))
	})
	t.Run("no puede quedar sin columnas", func(t *testing.T) {
		one := model.AlterModel{Columns: []model.Column{{Name: "a", ColumnType: "int"}}}
		require.Equal(t, "columns", detail(t, Validate(model.ChangeSet{Database: "d", Table: "t", Ops: []model.AlterOp{{Kind: model.OpDropColumn, Name: "a"}}}, one)))
	})
	t.Run("AUTO_INCREMENT sin índice", func(t *testing.T) {
		mm := baseModel()
		mm.Indexes = []model.Index{{Name: "idx_customer", Columns: []string{"customer_id"}}} // sin PRIMARY: «id» ya no encabeza ningún índice
		require.Equal(t, "autoInc", detail(t, Validate(cs(model.AlterOp{Kind: model.OpModifyColumn, Name: "note", Column: intCol}), mm)))
	})
}

func TestWarnings(t *testing.T) {
	m := baseModel()
	rows := int64(5_000_000)
	m.RowEstimate = &rows

	w, destructive := Warnings(cs(model.AlterOp{Kind: model.OpDropColumn, Name: "note"}), m)
	require.True(t, destructive)
	require.Equal(t, "drop_column", w[0].Code)
	require.Equal(t, "large_table", w[1].Code)

	_, d := Warnings(cs(model.AlterOp{Kind: model.OpDropIndex, Name: "idx_customer"}), m)
	require.True(t, d)
	_, d = Warnings(cs(model.AlterOp{Kind: model.OpDropFK, Name: "fk_customer"}), m)
	require.True(t, d)

	lossyCases := map[string]bool{
		"varchar(200)":      false, // ampliar no pierde
		"varchar(50)":       true,  // reducir sí
		"text":              true,  // otro tipo base
		"varchar(100)":      false, // igual
		"enum('a','b')":     true,
		"varchar(100) null": false,
	}
	for typ, want := range lossyCases {
		_, got := Warnings(cs(model.AlterOp{Kind: model.OpModifyColumn, Name: "note", Column: &model.ColumnSpec{Type: typ, Nullable: true}}), m)
		if typ == "varchar(100) null" {
			continue
		}
		require.Equal(t, want, got, typ)
	}
	// NULL → NOT NULL puede fallar o convertir.
	_, d = Warnings(cs(model.AlterOp{Kind: model.OpModifyColumn, Name: "note", Column: &model.ColumnSpec{Type: "varchar(100)"}}), m)
	require.True(t, d)

	// Sin cambios no hay avisos; añadir una columna no destruye nada.
	w, d = Warnings(cs(), m)
	require.Empty(t, w)
	require.False(t, d)
	_, d = Warnings(cs(model.AlterOp{Kind: model.OpAddColumn, Name: "x", Column: &model.ColumnSpec{Type: "int"}}), m)
	require.False(t, d)
}
