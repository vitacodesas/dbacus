//go:build integration

package services

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/alter"
	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/testutil/mysqlbox"
)

type altStore struct{ p model.ConnectionProfile }

func (s altStore) GetProfile(context.Context, string) (model.ConnectionProfile, error) {
	return s.p, nil
}

type alterBox struct {
	t        *testing.T
	sessions *session.Manager
	sess     *session.Session
	svc      *AlterService
}

func newAlterBox(t *testing.T, in mysqlbox.Instance, readOnly bool) *alterBox {
	t.Helper()
	sessions := session.NewManager(session.Options{})
	t.Cleanup(sessions.CloseAll)
	prof := model.ConnectionProfile{ID: "p", Driver: "mysql", Host: in.Host, Port: in.Port, User: mysqlbox.User, Database: mysqlbox.Database, ReadOnly: readOnly, TLS: model.TLSConfig{Mode: "preferred"}}
	s, err := sessions.Open(context.Background(), session.OpenRequest{Profile: prof, Password: mysqlbox.Password})
	require.NoError(t, err)
	return &alterBox{t: t, sessions: sessions, sess: s,
		svc: NewAlterService(AlterDeps{Sessions: sessions, Schema: NewSchemaService(sessions, nil), Store: altStore{prof}})}
}

func (b *alterBox) sql(stmt string) {
	b.t.Helper()
	c, err := b.sess.AcquireConn(context.Background(), "setup")
	require.NoError(b.t, err)
	r, err := c.Run(context.Background(), stmt)
	require.NoError(b.t, err, stmt)
	require.NoError(b.t, r.Close())
}

func (b *alterBox) model() model.AlterModel {
	b.t.Helper()
	m, err := b.svc.GetModel(context.Background(), b.sess.ID, mysqlbox.Database, "orders")
	require.NoError(b.t, err)
	return m
}

func (b *alterBox) apply(ops ...model.AlterOp) model.AlterResult {
	b.t.Helper()
	ctx := context.Background()
	cs := model.ChangeSet{Database: mysqlbox.Database, Table: "orders", Fingerprint: b.model().Fingerprint, Ops: ops}
	p, err := b.svc.Preview(ctx, b.sess.ID, cs)
	require.NoError(b.t, err)
	res, err := b.svc.Apply(ctx, b.sess.ID, cs, p.SQLHash)
	require.NoError(b.t, err)
	require.Nil(b.t, res.Failed, "falló: %v\n%s", res.Failed, strings.Join(p.Statements, ";\n"))
	require.Equal(b.t, p.Statements, res.Applied)
	return res
}

func col(m model.AlterModel, name string) (model.Column, bool) {
	for _, c := range m.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return model.Column{}, false
}

func has[T any](items []T, f func(T) bool) bool {
	for _, i := range items {
		if f(i) {
			return true
		}
	}
	return false
}

const alterFixture = `
CREATE TABLE customers (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, name VARCHAR(50) NOT NULL);
CREATE TABLE orders (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  customer_id INT UNSIGNED NOT NULL,
  note VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL COMMENT 'it''s libre',
  nickname VARCHAR(40) NOT NULL DEFAULT 'anon',
  status ENUM('new','paid') NOT NULL DEFAULT 'new',
  total DECIMAL(10,2) UNSIGNED NOT NULL DEFAULT 0,
  flag TINYINT(1) NOT NULL DEFAULT 0,
  body TEXT NULL,
  uid CHAR(36) NOT NULL DEFAULT (UUID()),
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  gen INT GENERATED ALWAYS AS (flag + 1) VIRTUAL,
  KEY idx_customer (customer_id),
  KEY idx_status_total (status, total),
  CONSTRAINT fk_customer FOREIGN KEY (customer_id) REFERENCES customers (id) ON DELETE CASCADE
)`

func forEachImage(t *testing.T, readOnly bool, fn func(t *testing.T, b *alterBox)) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			in := mysqlbox.Start(t, image)
			// El fixture siempre se crea con una sesión de escritura, aunque el test sea de solo lectura.
			setup := newAlterBox(t, in, false)
			for _, stmt := range strings.Split(alterFixture, ";\n") {
				setup.sql(stmt)
			}
			if readOnly {
				fn(t, newAlterBox(t, in, true))
				return
			}
			fn(t, setup)
		})
	}
}

// Regenerar una columna sin tocarla no debe cambiar nada: es la prueba de que la lectura recoge todo lo que el
// MODIFY necesita (collation, comentario, DEFAULT con expresión, ON UPDATE, enteros sin signo…).
func TestModifyColumnPreservesPropertiesIntegration(t *testing.T) {
	forEachImage(t, false, func(t *testing.T, b *alterBox) {
		before := b.model()
		var ops []model.AlterOp
		for _, c := range before.Columns {
			if c.IsGenerated || c.IsPrimaryKey {
				continue
			}
			ops = append(ops, model.AlterOp{Kind: model.OpModifyColumn, Name: c.Name})
		}
		require.NotEmpty(t, ops)
		after := *b.apply(ops...).Model
		require.Equal(t, before.Fingerprint, after.Fingerprint, "ANTES:\n%s\nDESPUÉS:\n%s", before.CreateSQL, after.CreateSQL)
		note, _ := col(after, "note")
		require.Equal(t, "utf8mb4_bin", note.Collation)
		require.Equal(t, "it's libre", note.Comment)
		upd, _ := col(after, "updated_at")
		require.Equal(t, "CURRENT_TIMESTAMP", strings.ToUpper(strings.TrimSuffix(upd.OnUpdate, "()")))
	})
}

func TestAlterApplyIntegration(t *testing.T) {
	forEachImage(t, false, func(t *testing.T, b *alterBox) {
		t.Run("columnas: cambiar largo conserva collation; renombrar; mover; añadir; borrar", func(t *testing.T) {
			res := b.apply(
				model.AlterOp{Kind: model.OpModifyColumn, Name: "note", Column: &model.ColumnSpec{Type: "varchar(300)", Nullable: true, Comment: "nuevo"}},
				model.AlterOp{Kind: model.OpModifyColumn, Name: "nickname", NewName: "alias"},
				model.AlterOp{Kind: model.OpModifyColumn, Name: "flag", First: true},
				model.AlterOp{Kind: model.OpAddColumn, Name: "qty", After: "total", Column: &model.ColumnSpec{Type: "int unsigned", Default: sp("1")}},
				model.AlterOp{Kind: model.OpDropColumn, Name: "body"},
			)
			m := *res.Model
			note, _ := col(m, "note")
			require.Equal(t, "varchar(300)", note.ColumnType)
			require.Equal(t, "utf8mb4_bin", note.Collation, "la collation sobrevive al MODIFY")
			require.Equal(t, "nuevo", note.Comment)
			alias, ok := col(m, "alias")
			require.True(t, ok)
			require.Equal(t, "anon", *alias.Default, "CHANGE conserva el DEFAULT")
			_, old := col(m, "nickname")
			require.False(t, old)
			require.Equal(t, "flag", m.Columns[0].Name)
			qty, ok := col(m, "qty")
			require.True(t, ok)
			require.Equal(t, "1", *qty.Default)
			total, _ := col(m, "total")
			require.Equal(t, total.Ordinal+1, qty.Ordinal, "AFTER total")
			_, gone := col(m, "body")
			require.False(t, gone)
		})

		// El editor emite AFTER con el nombre NUEVO de una columna renombrada en el mismo ALTER (spec 19): hay que saber
		// si MySQL/MariaDB lo aceptan, o habría que usar el nombre viejo.
		t.Run("AFTER de una columna renombrada en el mismo ALTER", func(t *testing.T) {
			res := b.apply(
				model.AlterOp{Kind: model.OpModifyColumn, Name: "status", NewName: "state"},
				model.AlterOp{Kind: model.OpAddColumn, Name: "after_state", After: "state", Column: &model.ColumnSpec{Type: "int", Nullable: true}},
			)
			st, _ := col(*res.Model, "state")
			af, ok := col(*res.Model, "after_state")
			require.True(t, ok)
			require.Equal(t, st.Ordinal+1, af.Ordinal)
		})

		t.Run("índices: crear único con prefijo, renombrar, borrar", func(t *testing.T) {
			res := b.apply(
				model.AlterOp{Kind: model.OpAddIndex, Name: "uq_alias_note", Index: &model.IndexSpec{Kind: "unique", Columns: []model.IndexColumn{{Name: "alias"}, {Name: "note", Prefix: 20}}}},
				model.AlterOp{Kind: model.OpRenameIndex, Name: "idx_status_total", NewName: "idx_st"},
			)
			m := *res.Model
			require.True(t, has(m.Indexes, func(i model.Index) bool {
				return i.Name == "uq_alias_note" && i.Unique && i.Prefix && len(i.Columns) == 2 && i.PrefixLens[1] == 20
			}))
			require.True(t, has(m.Indexes, func(i model.Index) bool { return i.Name == "idx_st" }))
			require.False(t, has(m.Indexes, func(i model.Index) bool { return i.Name == "idx_status_total" }))
			res = b.apply(model.AlterOp{Kind: model.OpDropIndex, Name: "uq_alias_note"})
			require.False(t, has(res.Model.Indexes, func(i model.Index) bool { return i.Name == "uq_alias_note" }))
		})

		t.Run("claves foráneas: modificar una (mismo nombre) en dos sentencias", func(t *testing.T) {
			cs := model.ChangeSet{Database: mysqlbox.Database, Table: "orders", Ops: []model.AlterOp{
				{Kind: model.OpDropFK, Name: "fk_customer"},
				{Kind: model.OpAddFK, Name: "fk_customer", FK: &model.FKSpec{Columns: []string{"customer_id"}, RefTable: "customers", RefColumns: []string{"id"}, OnDelete: "SET NULL"}},
			}}
			p, err := b.svc.Preview(context.Background(), b.sess.ID, cs)
			require.NoError(t, err)
			require.Len(t, p.Statements, 2)
			// customer_id es NOT NULL: SET NULL lo rechaza el servidor, y el resultado cuenta lo que pasó.
			res, err := b.svc.Apply(context.Background(), b.sess.ID, cs, p.SQLHash)
			require.NoError(t, err)
			require.Len(t, res.Applied, 1, "la primera sentencia ya se confirmó: no hay rollback")
			require.NotNil(t, res.Failed)
			require.Equal(t, 1, res.Failed.Index)
			require.Empty(t, res.Model.ForeignKeys, "el modelo devuelto es el real: la FK ya no está")

			res2 := b.apply(model.AlterOp{Kind: model.OpAddFK, Name: "fk_customer", FK: &model.FKSpec{Columns: []string{"customer_id"}, RefTable: "customers", RefColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "RESTRICT"}})
			require.Len(t, res2.Model.ForeignKeys, 1)
			require.Equal(t, "CASCADE", res2.Model.ForeignKeys[0].OnDelete)
		})

		t.Run("borrar FK, índice y columna a la vez", func(t *testing.T) {
			res := b.apply(
				model.AlterOp{Kind: model.OpDropFK, Name: "fk_customer"},
				model.AlterOp{Kind: model.OpDropIndex, Name: "idx_customer"},
				model.AlterOp{Kind: model.OpDropColumn, Name: "customer_id"},
			)
			_, ok := col(*res.Model, "customer_id")
			require.False(t, ok)
			require.Empty(t, res.Model.ForeignKeys)
		})

		t.Run("la tabla alterada por otra sesión da stale_model", func(t *testing.T) {
			fp := b.model().Fingerprint
			b.sql("ALTER TABLE orders ADD COLUMN extra INT NULL")
			_, err := b.svc.Preview(context.Background(), b.sess.ID, model.ChangeSet{Database: mysqlbox.Database, Table: "orders", Fingerprint: fp,
				Ops: []model.AlterOp{{Kind: model.OpDropColumn, Name: "extra"}}})
			require.Equal(t, "stale_model", detail(t, err))
		})

		t.Run("un INSERT no cambia la huella aunque suba AUTO_INCREMENT", func(t *testing.T) {
			fp := b.model().Fingerprint
			b.sql("INSERT INTO customers (name) VALUES ('x')")
			b.sql("INSERT INTO orders (alias, state) VALUES ('a', 'new')")
			require.Equal(t, fp, b.model().Fingerprint)
		})
	})
}

func TestAlterReadOnlyServerLayerIntegration(t *testing.T) {
	forEachImage(t, true, func(t *testing.T, b *alterBox) {
		ctx := context.Background()
		// 1) El servicio rechaza antes de enviar nada.
		cs := model.ChangeSet{Database: mysqlbox.Database, Table: "orders", Ops: []model.AlterOp{{Kind: model.OpDropColumn, Name: "body"}}}
		_, err := b.svc.Preview(ctx, b.sess.ID, cs)
		require.Equal(t, "readonly", detail(t, err))
		_, err = b.svc.Apply(ctx, b.sess.ID, cs, "x")
		require.Equal(t, "readonly", detail(t, err))

		// 2) Aunque el guardia de Go se saltara, el servidor lo rechaza: se ejecuta un ALTER directamente por la conexión.
		conn, err := b.sess.Pool.AcquireEdit(ctx)
		require.NoError(t, err)
		defer conn.Release()
		for _, stmt := range []string{
			"ALTER TABLE `" + mysqlbox.Database + "`.`orders` DROP COLUMN `body`",
			"ALTER TABLE `" + mysqlbox.Database + "`.`orders` ADD COLUMN `zz` INT NULL",
			"ALTER TABLE `" + mysqlbox.Database + "`.`orders` DROP FOREIGN KEY `fk_customer`",
		} {
			_, _, err := conn.Exec(ctx, stmt)
			require.Error(t, err, stmt)
			require.Contains(t, err.Error(), "READ ONLY", stmt)
		}
		// La estructura no cambió.
		d, err := b.sess.Pool.Schema().Describe(ctx, mysqlbox.Database, "orders")
		require.NoError(t, err)
		_, ok := col(model.AlterModel{Columns: d.Columns}, "body")
		require.True(t, ok)
		require.Len(t, d.ForeignKeys, 1)
		require.NotEmpty(t, alter.Fingerprint(d.CreateSQL))
	})
}

var _ = apperr.Validation
