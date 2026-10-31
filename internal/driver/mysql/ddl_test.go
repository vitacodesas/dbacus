package mysql

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

func s(v string) *string { return &v }

func ddlModel() model.AlterModel {
	return model.AlterModel{
		Database: "shop", Table: "orders",
		Columns: []model.Column{
			{Name: "id", ColumnType: "int", IsPrimaryKey: true, IsAutoInc: true},
			{Name: "note", ColumnType: "varchar(100)", Nullable: true, Charset: "utf8mb4", Collation: "utf8mb4_0900_ai_ci", Comment: "libre"},
			{Name: "updated_at", ColumnType: "timestamp", Default: s("CURRENT_TIMESTAMP"), DefaultExpr: true, OnUpdate: "CURRENT_TIMESTAMP"},
		},
	}
}

func gen(t *testing.T, info driver.ServerInfo, ops ...model.AlterOp) []string {
	t.Helper()
	out, err := (&Driver{}).GenerateDDL(model.ChangeSet{Database: "shop", Table: "orders", Ops: ops}, ddlModel(), info)
	require.NoError(t, err)
	return out
}

var mysql8 = driver.ServerInfo{Version: "8.0.36"}

func TestGenerateDDL(t *testing.T) {
	const head = "ALTER TABLE `shop`.`orders`\n  "
	cases := []struct {
		name string
		info driver.ServerInfo
		ops  []model.AlterOp
		want []string
	}{
		{"sin operaciones", mysql8, nil, nil},
		{"añadir columna al final", mysql8,
			[]model.AlterOp{{Kind: model.OpAddColumn, Name: "qty", Column: &model.ColumnSpec{Type: "int unsigned", Default: s("0")}}},
			[]string{head + "ADD COLUMN `qty` int unsigned NOT NULL DEFAULT '0'"}},
		{"añadir columna con comentario tras otra", mysql8,
			[]model.AlterOp{{Kind: model.OpAddColumn, Name: "tag", After: "id", Column: &model.ColumnSpec{Type: "varchar(20)", Nullable: true, Comment: "it's"}}},
			[]string{head + "ADD COLUMN `tag` varchar(20) NULL COMMENT 'it\\'s' AFTER `id`"}},
		{"añadir columna al principio con expresión", mysql8,
			[]model.AlterOp{{Kind: model.OpAddColumn, Name: "uid", First: true, Column: &model.ColumnSpec{Type: "char(36)", Default: s("uuid()"), DefaultExpr: true}}},
			[]string{head + "ADD COLUMN `uid` char(36) NOT NULL DEFAULT (uuid()) FIRST"}},
		{"cambiar el largo conserva charset, collation y comentario", mysql8,
			[]model.AlterOp{{Kind: model.OpModifyColumn, Name: "note", Column: &model.ColumnSpec{Type: "varchar(200)", Nullable: true, Comment: "libre"}}},
			[]string{head + "MODIFY COLUMN `note` varchar(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL COMMENT 'libre'"}},
		{"cambiar una columna ON UPDATE no pierde ON UPDATE", mysql8,
			[]model.AlterOp{{Kind: model.OpModifyColumn, Name: "updated_at", Column: &model.ColumnSpec{Type: "datetime", Default: s("CURRENT_TIMESTAMP"), DefaultExpr: true}}},
			[]string{head + "MODIFY COLUMN `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP"}},
		{"renombrar usa CHANGE y parte de la definición actual", mysql8,
			[]model.AlterOp{{Kind: model.OpModifyColumn, Name: "note", NewName: "remark"}},
			[]string{head + "CHANGE COLUMN `note` `remark` varchar(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL COMMENT 'libre'"}},
		{"mover sin otros cambios", mysql8,
			[]model.AlterOp{{Kind: model.OpModifyColumn, Name: "updated_at", First: true}},
			[]string{head + "MODIFY COLUMN `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP FIRST"}},
		{"borrar columna", mysql8,
			[]model.AlterOp{{Kind: model.OpDropColumn, Name: "note"}},
			[]string{head + "DROP COLUMN `note`"}},
		{"índice único con prefijo y orden", mysql8,
			[]model.AlterOp{{Kind: model.OpAddIndex, Name: "uq_note", Index: &model.IndexSpec{Kind: "unique", Columns: []model.IndexColumn{{Name: "note", Prefix: 20, Desc: true}, {Name: "id"}}}}},
			[]string{head + "ADD UNIQUE INDEX `uq_note` (`note`(20) DESC, `id`)"}},
		{"índice fulltext", mysql8,
			[]model.AlterOp{{Kind: model.OpAddIndex, Name: "ft", Index: &model.IndexSpec{Kind: "fulltext", Columns: []model.IndexColumn{{Name: "note"}}}}},
			[]string{head + "ADD FULLTEXT INDEX `ft` (`note`)"}},
		{"renombrar y borrar índices", mysql8,
			[]model.AlterOp{{Kind: model.OpRenameIndex, Name: "a", NewName: "b"}, {Kind: model.OpDropIndex, Name: "c"}},
			[]string{head + "DROP INDEX `c`,\n  RENAME INDEX `a` TO `b`"}},
		{"clave foránea con acciones y base implícita", mysql8,
			[]model.AlterOp{{Kind: model.OpAddFK, Name: "fk_c", FK: &model.FKSpec{Columns: []string{"id"}, RefTable: "customers", RefColumns: []string{"id"}, OnUpdate: "restrict", OnDelete: "cascade"}}},
			[]string{head + "ADD CONSTRAINT `fk_c` FOREIGN KEY (`id`) REFERENCES `shop`.`customers` (`id`) ON UPDATE RESTRICT ON DELETE CASCADE"}},
		{"borrar una FK sola es un único ALTER", mysql8,
			[]model.AlterOp{{Kind: model.OpDropFK, Name: "fk_c"}, {Kind: model.OpAddColumn, Name: "x", Column: &model.ColumnSpec{Type: "int", Nullable: true}}},
			[]string{head + "DROP FOREIGN KEY `fk_c`,\n  ADD COLUMN `x` int NULL"}},
		{"borrar FK e índice: dos sentencias, las FK primero", mysql8,
			[]model.AlterOp{{Kind: model.OpDropIndex, Name: "idx_c"}, {Kind: model.OpDropFK, Name: "fk_c"}},
			[]string{head + "DROP FOREIGN KEY `fk_c`", head + "DROP INDEX `idx_c`"}},
		{"modificar una FK (mismo nombre): borrar y crear aparte", mysql8,
			[]model.AlterOp{{Kind: model.OpDropFK, Name: "fk_c"}, {Kind: model.OpAddFK, Name: "fk_c", FK: &model.FKSpec{Columns: []string{"id"}, RefTable: "customers", RefColumns: []string{"id"}}}},
			[]string{head + "DROP FOREIGN KEY `fk_c`", head + "ADD CONSTRAINT `fk_c` FOREIGN KEY (`id`) REFERENCES `shop`.`customers` (`id`)"}},
		{"los identificadores se escapan", mysql8,
			[]model.AlterOp{{Kind: model.OpAddColumn, Name: "a`b", Column: &model.ColumnSpec{Type: "int", Nullable: true}}},
			[]string{head + "ADD COLUMN `a``b` int NULL"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, gen(t, c.info, c.ops...)) })
	}
}

func TestRenameIndexNeedsAServerThatSupportsIt(t *testing.T) {
	op := model.AlterOp{Kind: model.OpRenameIndex, Name: "a", NewName: "b"}
	for _, ok := range []driver.ServerInfo{
		{Version: "5.7.44"}, {Version: "8.0.36"}, {Version: "8.4.0"},
		{Version: "10.11.6-MariaDB", MariaDB: true}, {Version: "10.5.2-MariaDB", MariaDB: true}, {Version: "5.5.5-10.6.12-MariaDB-log", MariaDB: true},
		{Version: "raro"}, // versión ilegible: se intenta
	} {
		_, err := (&Driver{}).GenerateDDL(model.ChangeSet{Database: "d", Table: "t", Ops: []model.AlterOp{op}}, ddlModel(), ok)
		require.NoError(t, err, ok.Version)
	}
	for _, bad := range []driver.ServerInfo{{Version: "5.6.51"}, {Version: "10.4.32-MariaDB", MariaDB: true}, {Version: "10.5.1-MariaDB", MariaDB: true}} {
		_, err := (&Driver{}).GenerateDDL(model.ChangeSet{Database: "d", Table: "t", Ops: []model.AlterOp{op}}, ddlModel(), bad)
		require.Equal(t, apperr.Unsupported, apperr.From(err).Code, bad.Version)
	}
}

// Una collation igual a la de la tabla no se escribe: el DDL queda como el que devuelve SHOW CREATE TABLE.
func TestColumnCollationEqualToTheTablesIsOmitted(t *testing.T) {
	m := ddlModel()
	m.TableCollation = "utf8mb4_0900_ai_ci"
	out, err := (&Driver{}).GenerateDDL(model.ChangeSet{Database: "shop", Table: "orders", Ops: []model.AlterOp{{Kind: model.OpModifyColumn, Name: "note", NewName: "remark"}}}, m, mysql8)
	require.NoError(t, err)
	require.Equal(t, []string{"ALTER TABLE `shop`.`orders`\n  CHANGE COLUMN `note` `remark` varchar(100) NULL COMMENT 'libre'"}, out)
}

func TestColumnWithOddCharsetIsRejected(t *testing.T) {
	m := ddlModel()
	m.Columns[1].Charset = "utf8mb4; DROP TABLE x"
	_, err := (&Driver{}).GenerateDDL(model.ChangeSet{Database: "d", Table: "t", Ops: []model.AlterOp{{Kind: model.OpModifyColumn, Name: "note", NewName: "n2"}}}, m, mysql8)
	require.Error(t, err)
}

// Un ALTER nunca es de lectura: si alguna vez IsReadOnlySafe lo dejara pasar, un perfil de solo lectura podría ejecutarlo.
func TestGeneratedDDLIsNeverReadOnlySafe(t *testing.T) {
	ops := [][]model.AlterOp{
		{{Kind: model.OpAddColumn, Name: "x", Column: &model.ColumnSpec{Type: "int", Nullable: true}}},
		{{Kind: model.OpModifyColumn, Name: "note", NewName: "n"}},
		{{Kind: model.OpDropColumn, Name: "note"}},
		{{Kind: model.OpAddIndex, Name: "i", Index: &model.IndexSpec{Kind: "index", Columns: []model.IndexColumn{{Name: "note"}}}}},
		{{Kind: model.OpDropIndex, Name: "i"}, {Kind: model.OpDropFK, Name: "f"}},
		{{Kind: model.OpAddFK, Name: "f", FK: &model.FKSpec{Columns: []string{"id"}, RefTable: "t", RefColumns: []string{"id"}}}},
	}
	for _, o := range ops {
		for _, stmt := range gen(t, mysql8, o...) {
			require.True(t, strings.HasPrefix(stmt, "ALTER TABLE"), stmt)
			require.False(t, sqlsplit.IsReadOnlySafe(stmt), stmt)
			require.Len(t, sqlsplit.Split(stmt), 1, "una sentencia por envío")
		}
	}
}
