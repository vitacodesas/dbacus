//go:build integration

package mysql

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/sqlsplit"
	"github.com/vitacodesas/dbacus/internal/testutil/mysqlbox"
)

func loadFixture(t *testing.T, p driver.Pool) {
	t.Helper()
	raw, err := os.ReadFile("testdata/schema.sql")
	require.NoError(t, err)
	ctx := context.Background()
	c, err := p.Acquire(ctx)
	require.NoError(t, err)
	defer c.Release()
	for _, s := range sqlsplit.Split(string(raw)) {
		r, err := c.Run(ctx, s.Text)
		require.NoError(t, err, s.Text)
		require.NoError(t, r.Close())
	}
}

func byName[T any](items []T, name func(T) string, want string) (T, bool) {
	for _, it := range items {
		if name(it) == want {
			return it, true
		}
	}
	var zero T
	return zero, false
}

func testSchema(t *testing.T, in mysqlbox.Instance) {
	p := openPool(t, in, nil)
	loadFixture(t, p)
	s := p.Schema()
	ctx := context.Background()

	t.Run("bases de datos", func(t *testing.T) {
		dbs, err := s.Databases(ctx)
		require.NoError(t, err)
		shop, ok := byName(dbs, func(d model.Database) string { return d.Name }, "shop")
		require.True(t, ok)
		require.False(t, shop.IsSystem)
		is, ok := byName(dbs, func(d model.Database) string { return d.Name }, "information_schema")
		require.True(t, ok)
		require.True(t, is.IsSystem)
	})

	t.Run("tablas y vistas", func(t *testing.T) {
		tables, err := s.Tables(ctx, "shop")
		require.NoError(t, err)
		names := []string{}
		for _, tb := range tables {
			names = append(names, tb.Name)
		}
		require.Equal(t, []string{"customers", "order_items", "orders", "paid_orders", "weird`name"}, names)
		c, _ := byName(tables, func(x model.Table) string { return x.Name }, "customers")
		require.Equal(t, "table", c.Type)
		require.Equal(t, "Clientes", c.Comment)
		require.Equal(t, "shop", c.Schema)
		v, _ := byName(tables, func(x model.Table) string { return x.Name }, "paid_orders")
		require.Equal(t, "view", v.Type)
		require.Empty(t, v.Comment, "el 'VIEW' que rellena MySQL no es un comentario")
		require.Nil(t, v.RowEstimate)
	})

	t.Run("describe: columnas", func(t *testing.T) {
		d, err := s.Describe(ctx, "shop", "customers")
		require.NoError(t, err)
		require.Equal(t, "table", d.Type)
		require.Len(t, d.Columns, 5)
		id, name, nick := d.Columns[0], d.Columns[2], d.Columns[3]
		require.Equal(t, "id", id.Name)
		require.Equal(t, "int", id.DataType)
		require.Regexp(t, `^int(\(\d+\))? unsigned$`, id.ColumnType, "MariaDB conserva el ancho; MySQL 8 no")
		require.True(t, id.IsPrimaryKey)
		require.True(t, id.IsAutoInc)
		require.False(t, id.Nullable)
		require.Equal(t, 1, id.Ordinal)
		require.Equal(t, "Nombre completo", name.Comment)
		require.True(t, name.Nullable)
		require.Nil(t, name.Default, "DEFAULT NULL no es un valor por defecto literal")
		require.NotNil(t, nick.Default)
		require.Equal(t, "anon", *nick.Default)
	})

	t.Run("describe: índices", func(t *testing.T) {
		d, _ := s.Describe(ctx, "shop", "orders")
		idx, ok := byName(d.Indexes, func(i model.Index) string { return i.Name }, "idx_status_total")
		require.True(t, ok)
		require.False(t, idx.Unique)
		require.Equal(t, []string{"status", "total"}, idx.Columns, "orden de columnas del índice compuesto")
		pk, _ := byName(d.Indexes, func(i model.Index) string { return i.Name }, "PRIMARY")
		require.True(t, pk.Unique)

		items, _ := s.Describe(ctx, "shop", "order_items")
		pk, _ = byName(items.Indexes, func(i model.Index) string { return i.Name }, "PRIMARY")
		require.Equal(t, []string{"order_id", "line"}, pk.Columns)

		c, _ := s.Describe(ctx, "shop", "customers")
		uq, _ := byName(c.Indexes, func(i model.Index) string { return i.Name }, "uq_email")
		require.True(t, uq.Unique)
	})

	t.Run("describe: claves foráneas", func(t *testing.T) {
		d, err := s.Describe(ctx, "shop", "orders")
		require.NoError(t, err)
		require.Len(t, d.ForeignKeys, 1)
		fk := d.ForeignKeys[0]
		require.Equal(t, "fk_orders_customer", fk.Name)
		require.Equal(t, []string{"customer_id"}, fk.Columns)
		require.Equal(t, "customers", fk.RefTable)
		require.Equal(t, "shop", fk.RefSchema)
		require.Equal(t, []string{"id"}, fk.RefColumns)
		require.Equal(t, "CASCADE", fk.OnDelete)
		require.Equal(t, "RESTRICT", fk.OnUpdate)

		c, _ := s.Describe(ctx, "shop", "customers")
		require.Empty(t, c.ForeignKeys)
		require.NotNil(t, c.ForeignKeys, "lista vacía, no null (el front itera sin comprobar)")
	})

	t.Run("describe: CREATE, vistas y nombres raros", func(t *testing.T) {
		c, _ := s.Describe(ctx, "shop", "customers")
		require.Contains(t, c.CreateSQL, "CREATE TABLE")
		v, err := s.Describe(ctx, "shop", "paid_orders")
		require.NoError(t, err)
		require.Equal(t, "view", v.Type)
		require.Contains(t, v.CreateSQL, "VIEW")
		w, err := s.Describe(ctx, "shop", "weird`name")
		require.NoError(t, err)
		require.Equal(t, "col one", w.Columns[0].Name)
		require.Equal(t, "select", w.Columns[1].Name)
		require.Contains(t, w.CreateSQL, "weird``name")
	})

	t.Run("tabla inexistente", func(t *testing.T) {
		_, err := s.Describe(ctx, "shop", "no_existe")
		require.Equal(t, apperr.NotFound, code(t, err))
		tables, err := s.Tables(ctx, "base_que_no_existe")
		require.NoError(t, err)
		require.Empty(t, tables)
	})

	t.Run("catálogo", func(t *testing.T) {
		cat, err := s.Catalog(ctx, "shop")
		require.NoError(t, err)
		o, ok := byName(cat.Tables, func(c model.CatalogTable) string { return c.Name }, "orders")
		require.True(t, ok)
		require.Equal(t, []string{"id", "customer_id", "status", "total"}, []string{o.Columns[0].Name, o.Columns[1].Name, o.Columns[2].Name, o.Columns[3].Name})
		require.Equal(t, "decimal", o.Columns[3].DataType)
		_, ok = byName(cat.Tables, func(c model.CatalogTable) string { return c.Name }, "paid_orders")
		require.True(t, ok, "las vistas también se autocompletan")
	})
}
