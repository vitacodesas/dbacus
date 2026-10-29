package paging

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

func col(name, dataType string, nullable bool) model.Column {
	return model.Column{Name: name, DataType: dataType, Nullable: nullable}
}

func pk(cols ...string) model.Index { return model.Index{Name: "PRIMARY", Columns: cols, Unique: true} }

// tabla de ejemplo: id (PK), price decimal, qty int, ref int único, note texto, opt int que admite NULL, ratio double.
func detail(indexes ...model.Index) model.TableDetail {
	return model.TableDetail{
		Table: model.Table{Name: "t", Type: "table"},
		Columns: []model.Column{
			col("id", "bigint", false), col("price", "decimal", false), col("qty", "int", false), col("ref", "int", false),
			col("note", "varchar", false), col("opt", "int", true), col("ratio", "double", false),
		},
		Indexes: indexes,
	}
}

func decide(t *testing.T, sql string, d model.TableDetail) Decision {
	t.Helper()
	parts, reason := sqlsplit.SplitSelect(sql)
	if reason != "" {
		return Decision{Mode: ModeNone, Reason: reason}
	}
	return Decide(parts, d, 20)
}

func TestDecideKeysetOrders(t *testing.T) {
	d := detail(pk("id"))
	cases := []struct {
		name, sql string
		order     []Col
	}{
		{"sin ORDER BY usa la clave", "SELECT * FROM t", []Col{{Name: "id"}}},
		{"ORDER BY por la clave se respeta", "SELECT * FROM t ORDER BY id DESC", []Col{{Name: "id", Desc: true}}},
		{"ORDER BY del usuario más la clave de desempate", "SELECT * FROM t ORDER BY price DESC", []Col{{Name: "price", Desc: true}, {Name: "id"}}},
		{"varias columnas con direcciones mixtas", "SELECT * FROM t ORDER BY qty, price DESC", []Col{{Name: "qty"}, {Name: "price", Desc: true}, {Name: "id"}}},
		{"los nombres se toman de la tabla, no de lo escrito", "SELECT * FROM t ORDER BY PRICE", []Col{{Name: "price"}, {Name: "id"}}},
		{"lista de columnas con la clave", "SELECT id, note FROM t", []Col{{Name: "id"}}},
		{"lista de columnas en otro orden y mayúsculas", "SELECT NOTE, ID FROM t", []Col{{Name: "id"}}},
		{"la lista puede llevar expresiones si la clave va suelta", "SELECT id, UPPER(note) AS n FROM t", []Col{{Name: "id"}}},
		{"con WHERE", "SELECT * FROM t WHERE qty > 3", []Col{{Name: "id"}}},
		{"tipos enteros y decimales", "SELECT * FROM t ORDER BY price, qty", []Col{{Name: "price"}, {Name: "qty"}, {Name: "id"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decide(t, c.sql, d)
			require.Equal(t, ModeKeyset, got.Mode, got.Reason)
			require.Equal(t, c.order, got.Plan.Order)
			require.Equal(t, 20, got.Plan.PageSize)
		})
	}
}

func TestDecideKeepsTheOriginalPartsOfTheStatement(t *testing.T) {
	got := decide(t, "SELECT id, price, note FROM `db`.`t` WHERE qty > 3 ORDER BY price DESC", detail(pk("id")))
	require.Equal(t, ModeKeyset, got.Mode)
	require.Equal(t, "id, price, note", got.Plan.List)
	require.Equal(t, "`db`.`t`", got.Plan.From)
	require.Equal(t, "qty > 3", got.Plan.Where)
}

func TestDecideCompositeAndAlternativeKeys(t *testing.T) {
	composite := detail(pk("qty", "id"))
	got := decide(t, "SELECT * FROM t", composite)
	require.Equal(t, []Col{{Name: "qty"}, {Name: "id"}}, got.Plan.Order, "clave compuesta en el orden de la clave")
	got = decide(t, "SELECT * FROM t ORDER BY id DESC", composite)
	require.Equal(t, []Col{{Name: "id", Desc: true}, {Name: "qty"}}, got.Plan.Order, "solo se añade lo que falta")

	// Sin PRIMARY: un índice único completo sobre columnas NOT NULL.
	uniq := detail(model.Index{Name: "uq_ref", Columns: []string{"ref"}, Unique: true})
	got = decide(t, "SELECT * FROM t", uniq)
	require.Equal(t, ModeKeyset, got.Mode)
	require.Equal(t, []Col{{Name: "ref"}}, got.Plan.Order)

	// La primaria no está en la lista pero otra clave única sí: se usa esa.
	both := detail(pk("id"), model.Index{Name: "uq_ref", Columns: []string{"ref"}, Unique: true})
	got = decide(t, "SELECT ref, note FROM t", both)
	require.Equal(t, ModeKeyset, got.Mode)
	require.Equal(t, []Col{{Name: "ref"}}, got.Plan.Order)
	got = decide(t, "SELECT * FROM t", both)
	require.Equal(t, []Col{{Name: "id"}}, got.Plan.Order, "con ambas disponibles, la primaria")
}

func TestDecideReasons(t *testing.T) {
	d := detail(pk("id"))
	view := detail(pk("id"))
	view.Type = "view"
	textKey := detail(pk("note"))
	floatKey := detail(pk("ratio"))
	nullableUnique := detail(model.Index{Name: "uq_opt", Columns: []string{"opt"}, Unique: true})
	prefixUnique := detail(model.Index{Name: "uq_ref", Columns: []string{"ref"}, Unique: true, Prefix: true})

	cases := []struct {
		name, sql string
		d         model.TableDetail
		reason    string
	}{
		{"no es un SELECT", "SHOW TABLES", d, sqlsplit.ReasonNotSelect},
		{"LIMIT propio", "SELECT * FROM t LIMIT 5", d, sqlsplit.ReasonUserLimit},
		{"JOIN", "SELECT * FROM t JOIN u ON 1", d, sqlsplit.ReasonMultiTable},
		{"GROUP BY", "SELECT qty FROM t GROUP BY qty", d, sqlsplit.ReasonGrouped},
		{"UNION", "SELECT * FROM t UNION SELECT * FROM t", d, sqlsplit.ReasonCompound},
		{"FOR UPDATE", "SELECT * FROM t FOR UPDATE", d, sqlsplit.ReasonIntoLock},
		{"ORDER BY con expresión", "SELECT * FROM t ORDER BY price + 1", d, sqlsplit.ReasonOrderUnsupported},
		{"ORDER BY por una columna que no existe", "SELECT * FROM t ORDER BY nope", d, sqlsplit.ReasonOrderUnsupported},
		{"vista", "SELECT * FROM t", view, ReasonView},
		{"sin clave única", "SELECT * FROM t", detail(), ReasonNoKey},
		{"índice único que admite NULL no sirve", "SELECT * FROM t", nullableUnique, ReasonNoKey},
		{"índice único con prefijo no sirve", "SELECT * FROM t", prefixUnique, ReasonNoKey},
		{"clave de texto", "SELECT * FROM t", textKey, ReasonKeyType},
		{"clave FLOAT/DOUBLE no es exacta", "SELECT * FROM t", floatKey, ReasonKeyType},
		{"ORDER BY de texto", "SELECT * FROM t ORDER BY note", d, ReasonKeyType},
		{"ORDER BY de DOUBLE", "SELECT * FROM t ORDER BY ratio", d, ReasonKeyType},
		{"ORDER BY por una columna que admite NULL", "SELECT * FROM t ORDER BY opt", d, ReasonNullableOrder},
		{"la clave no está en la lista", "SELECT note FROM t", d, ReasonKeyNotSelected},
		{"la clave está pero con alias", "SELECT id AS x, note FROM t", d, ReasonKeyNotSelected},
		{"la clave está dentro de una expresión", "SELECT id + 0, note FROM t", d, ReasonKeyNotSelected},
		{"una columna del ORDER BY no está en la lista", "SELECT id FROM t ORDER BY price", d, ReasonKeyNotSelected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decide(t, c.sql, c.d)
			require.Equal(t, ModeNone, got.Mode)
			require.Equal(t, c.reason, got.Reason)
			require.Empty(t, got.Plan.Order, "sin paginar no hay plan")
		})
	}
}

func TestDecideAcceptsEveryExactNumericType(t *testing.T) {
	for _, typ := range []string{"tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "BIGINT"} {
		d := model.TableDetail{Columns: []model.Column{col("id", typ, false)}, Indexes: []model.Index{pk("id")}}
		got := decide(t, "SELECT * FROM t", d)
		require.Equal(t, ModeKeyset, got.Mode, typ)
	}
	for _, typ := range []string{"float", "double", "real", "bit", "year", "varchar", "char", "date", "datetime", "timestamp", "binary", "json", "enum"} {
		d := model.TableDetail{Columns: []model.Column{col("id", typ, false)}, Indexes: []model.Index{pk("id")}}
		got := decide(t, "SELECT * FROM t", d)
		require.Equal(t, ReasonKeyType, got.Reason, typ)
	}
}

func TestKeyColumns(t *testing.T) {
	got := decide(t, "SELECT * FROM t ORDER BY price DESC", detail(pk("id")))
	require.Equal(t, []string{"price", "id"}, got.Plan.KeyColumns())
}
