package edit

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/model"
)

func col(name, typ string, mut ...func(*model.Column)) model.Column {
	c := model.Column{Name: name, DataType: typ, ColumnType: typ}
	for _, m := range mut {
		m(&c)
	}
	return c
}

func nullable(c *model.Column) { c.Nullable = true }
func auto(c *model.Column)     { c.IsAutoInc = true }
func gen(c *model.Column)      { c.IsGenerated = true }

func metas(names ...string) []model.ColumnMeta {
	out := make([]model.ColumnMeta, len(names))
	for i, n := range names {
		out[i] = model.ColumnMeta{Name: n, DataType: "varchar"}
	}
	return out
}

func detail(typ string, cols []model.Column, idx ...model.Index) model.TableDetail {
	return model.TableDetail{Table: model.Table{Name: "t", Schema: "db", Type: typ}, Columns: cols, Indexes: idx}
}

func analyze(sql string, d model.TableDetail, cols []model.ColumnMeta, mut ...func(*Input)) Plan {
	in := Input{SQL: sql, DefaultDB: "db", Cols: cols, Describe: func(string, string) (model.TableDetail, error) { return d, nil }}
	for _, m := range mut {
		if m != nil {
			m(&in)
		}
	}
	return Analyze(in)
}

var pkID = model.Index{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}

func actor() model.TableDetail {
	return detail("table", []model.Column{
		col("id", "int", auto), col("name", "varchar"), col("note", "text", nullable), col("blob", "blob", nullable),
		col("full", "varchar", gen), col("seq", "int", auto),
	}, pkID)
}

func TestEditableStarWithPrimaryKey(t *testing.T) {
	p := analyze("SELECT * FROM t", actor(), metas("id", "name", "note", "blob", "full", "seq"))
	require.True(t, p.Editable, p.Reason)
	require.Equal(t, "db", p.Database)
	require.Equal(t, "t", p.Table)
	require.Equal(t, []int{0}, p.KeyColumns)
	require.Equal(t, []string{"id"}, p.KeyTableCols)

	reasons := []string{ColKey, "", "", ColBinary, ColGenerated, ColAutoIncrement}
	for i, want := range reasons {
		require.Equal(t, want, p.Columns[i].Reason, "columna %d", i)
		require.Equal(t, want == "", p.Columns[i].Editable, "columna %d", i)
	}
	require.True(t, p.Columns[2].Nullable)
}

func TestNamedBaseOverridesDefault(t *testing.T) {
	var got [2]string
	p := analyze("SELECT * FROM `other`.`t`", actor(), metas("id", "name"), func(in *Input) {
		in.Describe = func(db, table string) (model.TableDetail, error) { got = [2]string{db, table}; return actor(), nil }
	})
	require.True(t, p.Editable)
	require.Equal(t, [2]string{"other", "t"}, got)
}

func TestNotEditable(t *testing.T) {
	cols := metas("id", "name")
	cases := []struct {
		name   string
		sql    string
		d      model.TableDetail
		cols   []model.ColumnMeta
		mut    func(*Input)
		reason string
	}{
		{"JOIN", "SELECT * FROM t JOIN u ON 1", actor(), cols, nil, ReasonNoSingleTable},
		{"alias de tabla", "SELECT * FROM t x", actor(), cols, nil, ReasonNoSingleTable},
		{"sin base", "SELECT * FROM t", actor(), cols, func(in *Input) { in.DefaultDB = "" }, ReasonNoSingleTable},
		{"perfil de solo lectura", "SELECT * FROM t", actor(), cols, func(in *Input) { in.ReadOnly = true }, ReasonReadOnly},
		{"no devuelve filas", "SELECT * FROM t", actor(), nil, nil, ReasonNotRows},
		{"vista", "SELECT * FROM t", detail("view", actor().Columns, pkID), cols, nil, ReasonView},
		{"tabla inexistente", "SELECT * FROM t", actor(), cols, func(in *Input) {
			in.Describe = func(string, string) (model.TableDetail, error) { return model.TableDetail{}, errors.New("no existe") }
		}, ReasonTableNotFound},
		{"sin clave", "SELECT * FROM t", detail("table", actor().Columns), cols, nil, ReasonNoKey},
		{"clave no seleccionada", "SELECT name FROM t", actor(), metas("name"), nil, ReasonKeyNotSelected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := analyze(c.sql, c.d, c.cols, c.mut)
			require.False(t, p.Editable)
			require.Equal(t, c.reason, p.Reason)
			require.Len(t, p.Columns, len(c.cols), "siempre hay una entrada por columna")
		})
	}
}

func TestCompositeKey(t *testing.T) {
	d := detail("table", []model.Column{col("a", "int"), col("b", "int"), col("v", "varchar")},
		model.Index{Name: "PRIMARY", Unique: true, Columns: []string{"a", "b"}})
	p := analyze("SELECT * FROM t", d, metas("a", "b", "v"))
	require.True(t, p.Editable)
	require.Equal(t, []string{"a", "b"}, p.KeyTableCols)
	require.Equal(t, []int{0, 1}, p.KeyColumns)

	p = analyze("SELECT a, v FROM t", d, metas("a", "v"))
	require.Equal(t, ReasonKeyNotSelected, p.Reason, "con la clave a medias no se puede identificar la fila")
}

func TestUniqueIndexIsUsedOnlyWhenSafe(t *testing.T) {
	cols := []model.Column{col("code", "varchar"), col("opt", "varchar", nullable), col("name", "varchar")}
	cols2 := metas("code", "opt", "name")

	t.Run("único NOT NULL sirve de clave", func(t *testing.T) {
		d := detail("table", cols, model.Index{Name: "uq_code", Unique: true, Columns: []string{"code"}})
		p := analyze("SELECT * FROM t", d, cols2)
		require.True(t, p.Editable)
		require.Equal(t, []string{"code"}, p.KeyTableCols)
	})
	t.Run("único con columna NULL no sirve", func(t *testing.T) {
		d := detail("table", cols, model.Index{Name: "uq_opt", Unique: true, Columns: []string{"opt"}})
		require.Equal(t, ReasonNoKey, analyze("SELECT * FROM t", d, cols2).Reason)
	})
	t.Run("único con prefijo no sirve", func(t *testing.T) {
		d := detail("table", cols, model.Index{Name: "uq_code", Unique: true, Prefix: true, Columns: []string{"code"}})
		require.Equal(t, ReasonNoKey, analyze("SELECT * FROM t", d, cols2).Reason)
	})
	t.Run("índice no único no sirve", func(t *testing.T) {
		d := detail("table", cols, model.Index{Name: "ix", Columns: []string{"code"}})
		require.Equal(t, ReasonNoKey, analyze("SELECT * FROM t", d, cols2).Reason)
	})
	t.Run("índice funcional no sirve", func(t *testing.T) {
		d := detail("table", cols, model.Index{Name: "fx", Unique: true, Columns: []string{"(expresión)"}})
		require.Equal(t, ReasonNoKey, analyze("SELECT * FROM t", d, cols2).Reason)
	})
	t.Run("si la PK no está seleccionada se usa el único que sí", func(t *testing.T) {
		d := detail("table", append([]model.Column{col("id", "int")}, cols...), pkID, model.Index{Name: "uq_code", Unique: true, Columns: []string{"code"}})
		p := analyze("SELECT code, name FROM t", d, metas("code", "name"))
		require.True(t, p.Editable)
		require.Equal(t, []string{"code"}, p.KeyTableCols)
	})
}

func TestExplicitColumnsExpressionsAndDuplicates(t *testing.T) {
	d := actor()
	p := analyze("SELECT id, name, UPPER(name), name FROM t", d, metas("id", "name", "UPPER(name)", "name"))
	require.True(t, p.Editable)
	require.Equal(t, "", p.Columns[1].Reason)
	require.Equal(t, ColExpression, p.Columns[2].Reason)
	require.Equal(t, ColDuplicate, p.Columns[3].Reason, "la misma columna dos veces: solo la primera se edita")

	p = analyze("SELECT id, name AS n FROM t", d, metas("id", "n"))
	require.True(t, p.Editable)
	require.Equal(t, ColExpression, p.Columns[1].Reason, "un alias no se puede mapear")

	p = analyze("SELECT * FROM t", d, metas("id", "extra"))
	require.Equal(t, ColUnmapped, p.Columns[1].Reason)
}

func TestColumnNamesAreCaseInsensitive(t *testing.T) {
	p := analyze("SELECT ID, NAME FROM t", actor(), metas("ID", "NAME"))
	require.True(t, p.Editable)
	require.Equal(t, []string{"id", "name"}, p.TableCols, "se normaliza al nombre real de la tabla")
}

func str(s string) *string { return &s }

func TestInsertEligibilityAndRequiredColumns(t *testing.T) {
	d := detail("table", []model.Column{
		col("id", "int", auto),
		col("email", "varchar"),          // NOT NULL sin DEFAULT: obligatoria
		col("name", "varchar", nullable), // opcional
		col("created", "datetime", func(c *model.Column) { c.Default = str("CURRENT_TIMESTAMP") }), // tiene DEFAULT
		col("blob", "blob", nullable),
		col("full", "varchar", gen),
	}, pkID)

	t.Run("con todo en el resultado se puede insertar y se marcan las obligatorias", func(t *testing.T) {
		p := analyze("SELECT * FROM t", d, metas("id", "email", "name", "created", "blob", "full"))
		require.True(t, p.CanInsert, p.InsertReason)
		require.Empty(t, p.InsertMissing)
		want := []struct{ insertable, required bool }{
			{false, false}, // AUTO_INCREMENT: lo pone el servidor
			{true, true},   // email: NOT NULL sin DEFAULT
			{true, false},
			{true, false},  // con DEFAULT no es obligatoria
			{false, false}, // binaria: no se escribe desde aquí
			{false, false}, // generada
		}
		for i, w := range want {
			require.Equal(t, w.insertable, p.Columns[i].Insertable, "insertable %d", i)
			require.Equal(t, w.required, p.Columns[i].Required, "required %d", i)
		}
	})

	t.Run("si falta una columna obligatoria no se ofrece insertar y se dice cuál", func(t *testing.T) {
		p := analyze("SELECT id, name FROM t", d, metas("id", "name"))
		require.True(t, p.Editable)
		require.False(t, p.CanInsert)
		require.Equal(t, InsertMissingRequired, p.InsertReason)
		require.Equal(t, []string{"email"}, p.InsertMissing)
	})

	t.Run("una columna obligatoria que no se puede escribir (binaria) impide insertar", func(t *testing.T) {
		d2 := detail("table", []model.Column{col("id", "int", auto), col("raw", "blob")}, pkID)
		p := analyze("SELECT * FROM t", d2, metas("id", "raw"))
		require.False(t, p.CanInsert)
		require.Equal(t, []string{"raw"}, p.InsertMissing)
	})

	t.Run("una clave no autoincremental es insertable y obligatoria", func(t *testing.T) {
		d3 := detail("table", []model.Column{col("code", "varchar"), col("v", "int", nullable)},
			model.Index{Name: "PRIMARY", Unique: true, Columns: []string{"code"}})
		p := analyze("SELECT * FROM t", d3, metas("code", "v"))
		require.True(t, p.CanInsert)
		require.True(t, p.Columns[0].Insertable)
		require.True(t, p.Columns[0].Required)
		require.False(t, p.Columns[0].Editable, "pero no se edita en filas existentes (D4)")
	})
}

func TestDeleteAffectsListsCascadingTables(t *testing.T) {
	d := actor()
	d.IncomingFKs = []model.ForeignKey{
		{Name: "fk1", RefSchema: "db", RefTable: "orders", OnDelete: "CASCADE"},
		{Name: "fk2", RefSchema: "db", RefTable: "orders", OnDelete: "CASCADE"}, // misma tabla: una sola vez
		{Name: "fk3", RefSchema: "db", RefTable: "logs", OnDelete: "SET NULL"},
		{Name: "fk4", RefSchema: "db", RefTable: "audit", OnDelete: "RESTRICT"}, // RESTRICT falla, no arrastra
		{Name: "fk5", RefSchema: "db", RefTable: "notes", OnDelete: "NO ACTION"},
	}
	p := analyze("SELECT * FROM t", d, metas("id", "name", "note", "blob", "full", "seq"))
	require.Equal(t, []string{"db.orders", "db.logs"}, p.DeleteAffects)
}
