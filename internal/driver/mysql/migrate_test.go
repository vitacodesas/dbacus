package mysql

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
)

func guardErr(t *testing.T, g *writeGuard, stmt string) *apperr.AppError {
	t.Helper()
	err := g.check(stmt)
	if err == nil {
		return nil
	}
	ae, ok := err.(*apperr.AppError)
	require.True(t, ok)
	require.Equal(t, "source_write_blocked", ae.Detail)
	return ae
}

// Spec 23 ⚑D19: el guardia deja pasar solo lo que escribe en el destino y lee del origen, y no envía nada más.
func TestWriteGuardRejectsAnythingNotTargetingDestination(t *testing.T) {
	g := newWriteGuard(driver.RoleServer, "src", "dst")
	allowed := []string{
		"INSERT INTO `dst`.`t` (`a`,`b`) SELECT `a`,`b` FROM `src`.`t` WHERE (`a` >= ?) AND (`a` < ?)",
		"INSERT INTO `dst`.`t` (`a`) SELECT `a` FROM `src`.`t` PARTITION (`p0`)",
		"INSERT INTO `dst`.`t` (`a`) VALUES (?),(?)",
		"CREATE TABLE `t` (\n  `id` int NOT NULL,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB AUTO_INCREMENT=5 /*!50100 PARTITION BY HASH (`id`) PARTITIONS 4 */",
		"ALTER TABLE `dst`.`orders` ADD CONSTRAINT `fk` FOREIGN KEY (`c`) REFERENCES `dst`.`customers` (`id`) ON DELETE CASCADE ON UPDATE RESTRICT",
		"ALTER TABLE `dst`.`orders` ADD CONSTRAINT `fk` FOREIGN KEY (`c`) REFERENCES `otro`.`customers` (`id`)",
		"DELETE FROM `dst`.`t` WHERE (`a` >= ?)",
		"TRUNCATE TABLE `dst`.`t`",
		"DROP TABLE IF EXISTS `dst`.`t`",
		"SELECT COUNT(*) FROM `src`.`t`",
		"SELECT `a` FROM `src`.`t` ORDER BY `a` LIMIT 1 OFFSET 1000",
		"SHOW CREATE TABLE `src`.`t`",
		"CREATE VIEW `v` AS select `dst`.`t`.`a` AS `a` from `dst`.`t`",
		"CREATE PROCEDURE `p`() BEGIN INSERT INTO `dst`.`audit` VALUES (1); SELECT 1; END",
		"CREATE TRIGGER `trg` BEFORE INSERT ON `t` FOR EACH ROW SET NEW.a = 1;",
		"CREATE TABLE `t` (`ts` timestamp ON UPDATE CURRENT_TIMESTAMP, `c` text COMMENT 'FOR UPDATE de src.t')", // «FOR UPDATE» y src dentro de una cadena
	}
	for _, s := range allowed {
		require.NoError(t, g.check(s), s)
	}
	denied := map[string]string{
		"INSERT al origen":                       "INSERT INTO `src`.`t` (`a`) VALUES (1)",
		"INSERT … SELECT del origen al origen":   "INSERT INTO `src`.`t` SELECT * FROM `dst`.`t`",
		"INSERT que nombra el origen sin SELECT": "INSERT INTO `dst`.`t` (`a`) VALUES (`src`.`t`.`a`)",
		"origen antes del SELECT":                "INSERT INTO `dst`.`t` (`src`.`t`.`a`) SELECT 1",
		"ALTER del origen":                       "ALTER TABLE `src`.`t` ADD COLUMN `x` int",
		"DROP del origen":                        "DROP TABLE `src`.`t`",
		"DELETE del origen":                      "DELETE FROM `src`.`t`",
		"TRUNCATE del origen":                    "TRUNCATE TABLE `src`.`t`",
		"FK que referencia al origen":            "ALTER TABLE `dst`.`o` ADD CONSTRAINT `fk` FOREIGN KEY (`c`) REFERENCES `src`.`c` (`id`)",
		"vista que referencia al origen":         "CREATE VIEW `v` AS select * from `src`.`t`",
		"escribe en un tercer schema":            "INSERT INTO `otro`.`t` (`a`) VALUES (1)",
		"DROP de un tercer schema":               "DROP TABLE `otro`.`t`",
		"mayúsculas distintas":                   "INSERT INTO `SRC`.`t` (`a`) VALUES (1)",
		"USE":                                    "USE `src`",
		"UPDATE":                                 "UPDATE `dst`.`t` SET a = 1",
		"REPLACE":                                "REPLACE INTO `dst`.`t` VALUES (1)",
		"FOR UPDATE":                             "SELECT `a` FROM `src`.`t` FOR UPDATE",
		"FOR SHARE":                              "SELECT `a` FROM `src`.`t` FOR SHARE",
		"LOCK IN SHARE MODE":                     "SELECT `a` FROM `src`.`t` LOCK IN SHARE MODE",
		"INTO OUTFILE":                           "SELECT `a` FROM `src`.`t` INTO OUTFILE '/tmp/x'",
		"ANALYZE":                                "ANALYZE TABLE `src`.`t`",
		"OPTIMIZE":                               "OPTIMIZE TABLE `src`.`t`",
		"FLUSH":                                  "FLUSH TABLES WITH READ LOCK",
		"LOCK TABLES":                            "LOCK TABLES `src`.`t` READ",
		"CALL":                                   "CALL `src`.`p`()",
		"SET GLOBAL":                             "SET GLOBAL max_connections = 1",
		"GRANT":                                  "GRANT SELECT ON *.* TO 'a'@'b'",
		"KILL":                                   "KILL 5",
		"RENAME":                                 "RENAME TABLE `src`.`t` TO `dst`.`t`",
		"dos sentencias":                         "SELECT 1; DROP TABLE `src`.`t`",
		"sin verbo":                              "",
		"solo un comentario":                     "/* SELECT 1 */",
		"verbo escondido tras un comentario":     "/* x */ UPDATE `src`.`t` SET a = 1",
	}
	for name, s := range denied {
		t.Run(name, func(t *testing.T) { require.NotNil(t, guardErr(t, g, s), s) })
	}
}

func TestWriteGuardSourceConnectionsOnlyRead(t *testing.T) {
	g := newWriteGuard(driver.RoleSource, "", "")
	for _, s := range []string{"SELECT `a` FROM `src`.`t`", "SHOW CREATE TABLE `src`.`t`", "SELECT COUNT(*) FROM `src`.`t` WHERE `a` > ?"} {
		require.NoError(t, g.check(s), s)
	}
	for _, s := range []string{"INSERT INTO `dst`.`t` VALUES (1)", "CREATE TABLE x (a int)", "DROP TABLE `dst`.`t`", "DELETE FROM `src`.`t`", "SELECT 1 FOR UPDATE"} {
		require.NotNil(t, guardErr(t, g, s), s)
	}
}

// Nombres hostiles: el nombre del schema se escapa con backticks y no puede abrir una sentencia nueva.
func TestWriteGuardHostileNames(t *testing.T) {
	evil := "a`; DROP TABLE `src`.`t`; -- "
	g := newWriteGuard(driver.RoleServer, "src", evil)
	require.NoError(t, g.check("INSERT INTO "+QuoteIdent(evil)+".`t` (`a`) SELECT `a` FROM `src`.`t`"))
	require.NotNil(t, guardErr(t, g, "INSERT INTO `src`.`t` (`a`) VALUES (1)"))
	// El origen con comillas raras sigue detectándose.
	g = newWriteGuard(driver.RoleServer, "s`rc", "dst")
	require.NotNil(t, guardErr(t, g, "ALTER TABLE "+QuoteIdent("s`rc")+".`t` ADD COLUMN `x` int"))
}

func FuzzWriteGuardNeverAllowsWritingTheSource(f *testing.F) {
	for _, s := range []string{"INSERT INTO `src`.`t` VALUES (1)", "DELETE FROM src.t", "SELECT 1", "INSERT INTO dst.t SELECT * FROM src.t", "/* a */ DROP TABLE `src`.`t`", "ALTER TABLE `dst`.`t` ADD CONSTRAINT x FOREIGN KEY (a) REFERENCES `src`.`p` (id)"} {
		f.Add(s)
	}
	g := newWriteGuard(driver.RoleServer, "src", "dst")
	f.Fuzz(func(t *testing.T, stmt string) {
		if g.validate(stmt) != nil {
			return
		}
		// Lo que pasa el guardia nunca nombra el origen en una posición de escritura.
		up := strings.ToUpper(stmt)
		for _, bad := range []string{"INTO `SRC`.", "INTO SRC.", "TABLE `SRC`.", "TABLE SRC.", "UPDATE `SRC`."} {
			if strings.Contains(up, bad) && !strings.HasPrefix(strings.TrimSpace(up), "SELECT") && !strings.HasPrefix(strings.TrimSpace(up), "SHOW") {
				t.Fatalf("pasó una sentencia que escribe en el origen: %q", stmt)
			}
		}
	})
}

func TestStripForeignKeys(t *testing.T) {
	create := "CREATE TABLE `orders` (\n  `id` int NOT NULL,\n  `cid` int DEFAULT NULL,\n  `parent` int DEFAULT NULL,\n  PRIMARY KEY (`id`),\n  KEY `fk_cust` (`cid`),\n  KEY `fk_self` (`parent`),\n  CONSTRAINT `fk_cust` FOREIGN KEY (`cid`) REFERENCES `customers` (`id`) ON DELETE CASCADE,\n  CONSTRAINT `fk_self` FOREIGN KEY (`parent`) REFERENCES `orders` (`id`)\n) ENGINE=InnoDB AUTO_INCREMENT=7 DEFAULT CHARSET=utf8mb4"
	fks := []model.ForeignKey{{Name: "fk_cust"}, {Name: "fk_self"}}
	got, err := stripForeignKeys(create, fks)
	require.NoError(t, err)
	require.NotContains(t, got, "FOREIGN KEY")
	require.Contains(t, got, "  KEY `fk_self` (`parent`)\n)", "se quita la coma de la línea anterior al paréntesis")
	require.Contains(t, got, "AUTO_INCREMENT=7")

	// Una FK en medio de otras definiciones no toca las comas.
	mid := "CREATE TABLE `t` (\n  `a` int,\n  CONSTRAINT `x` FOREIGN KEY (`a`) REFERENCES `p` (`id`),\n  KEY `k` (`a`)\n) ENGINE=InnoDB"
	got, err = stripForeignKeys(mid, []model.ForeignKey{{Name: "x"}})
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE `t` (\n  `a` int,\n  KEY `k` (`a`)\n) ENGINE=InnoDB", got)

	// Backtick dentro del nombre.
	got, err = stripForeignKeys("CREATE TABLE `t` (\n  `a` int,\n  CONSTRAINT `a``b` FOREIGN KEY (`a`) REFERENCES `p` (`id`)\n) ENGINE=InnoDB", []model.ForeignKey{{Name: "a`b"}})
	require.NoError(t, err)
	require.NotContains(t, got, "FOREIGN")

	// Falla cerrado: una FK que information_schema no conoce, o una que no se reconoce en el texto.
	_, err = stripForeignKeys(create, fks[:1])
	require.Error(t, err)
	_, err = stripForeignKeys(create, []model.ForeignKey{{Name: "otra"}})
	require.Error(t, err)
	_, err = stripForeignKeys("CREATE TABLE t (a int, FOREIGN KEY (a) REFERENCES p (id))", nil)
	require.Error(t, err)
	ok, err := stripForeignKeys("CREATE TABLE t (a int)", nil)
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE t (a int)", ok)
}

func TestChooseKeyLadder(t *testing.T) {
	notNull := []bool{false}
	pk := idxInfo{name: "PRIMARY", unique: true, typ: "BTREE", cols: []string{"id"}, nullable: notNull}
	uq := idxInfo{name: "uq", unique: true, typ: "BTREE", cols: []string{"a", "b"}, nullable: []bool{false, false}}
	uqNull := idxInfo{name: "uqn", unique: true, typ: "BTREE", cols: []string{"n"}, nullable: []bool{true}}
	ix1 := idxInfo{name: "i1", typ: "BTREE", cols: []string{"c1", "c2"}, nullable: []bool{false, false}, card: 10}
	ix2 := idxInfo{name: "i2", typ: "BTREE", cols: []string{"c3"}, nullable: []bool{true}, card: 500}
	ft := idxInfo{name: "ft", typ: "FULLTEXT", cols: []string{"txt"}, card: 9999}
	prefix := idxInfo{name: "pf", typ: "BTREE", cols: []string{"s"}, prefix: true, card: 9999}
	fn := idxInfo{name: "fn", typ: "BTREE", cols: []string{""}, functional: true, card: 9999}

	k, hasPK, _ := chooseKey([]idxInfo{ix1, pk, uq}, nil)
	require.Equal(t, model.ChunkPK, k.Kind)
	require.True(t, hasPK)

	k, hasPK, _ = chooseKey([]idxInfo{ix1, uq}, []string{"p0"})
	require.Equal(t, model.ChunkUnique, k.Kind, "una única NOT NULL gana a las particiones")
	require.False(t, hasPK)
	require.Equal(t, []string{"a", "b"}, k.Columns)

	k, _, _ = chooseKey([]idxInfo{uqNull, ix1}, []string{"p0", "p1"})
	require.Equal(t, model.ChunkPartition, k.Kind, "una única con columnas nulables no sirve de clave")
	require.Equal(t, []string{"p0", "p1"}, k.Partitions)

	k, _, card := chooseKey([]idxInfo{ix1, ix2, ft, prefix, fn, uqNull}, nil)
	require.Equal(t, model.ChunkIndex, k.Kind)
	require.Equal(t, []string{"c3"}, k.Columns, "el índice de mayor cardinalidad, sin FULLTEXT, prefijos ni funcionales")
	require.EqualValues(t, 500, card)

	k, _, _ = chooseKey([]idxInfo{ft, prefix, fn}, nil)
	require.Equal(t, model.ChunkNone, k.Kind)
	k, _, _ = chooseKey(nil, nil)
	require.Equal(t, model.ChunkNone, k.Kind)
}

func TestKeyClauses(t *testing.T) {
	c, a := keyClause([]string{"id"}, []any{int64(5)}, false, true)
	require.Equal(t, "`id` >= ?", c)
	require.Equal(t, []any{int64(5)}, a)
	c, _ = keyClause([]string{"id"}, []any{int64(5)}, true, false)
	require.Equal(t, "`id` < ?", c)

	c, a = keyClause([]string{"a", "b"}, []any{1, 2}, false, true)
	require.Equal(t, "((`a` > ?) OR (`a` = ? AND `b` > ?) OR (`a` = ? AND `b` = ?))", c)
	require.Equal(t, []any{1, 1, 2, 1, 2}, a)
	require.Equal(t, strings.Count(c, "?"), len(a))
	c, a = keyClause([]string{"a", "b"}, []any{1, 2}, true, false)
	require.Equal(t, "((`a` < ?) OR (`a` = ? AND `b` < ?))", c)
	require.Equal(t, strings.Count(c, "?"), len(a))
}

func TestChunkWhere(t *testing.T) {
	pkT := model.MigrationTable{Name: "t", Key: model.ChunkKeySpec{Kind: model.ChunkPK, Columns: []string{"id"}}}
	p, w, a := chunkWhere(pkT, driver.MigrationChunk{Lo: []any{int64(10)}, Hi: []any{int64(20)}})
	require.Equal(t, "", p)
	require.Equal(t, " WHERE `id` >= ? AND `id` < ?", w)
	require.Equal(t, []any{int64(10), int64(20)}, a)
	_, w, a = chunkWhere(pkT, driver.MigrationChunk{})
	require.Equal(t, "", w, "el único trozo de una tabla no lleva WHERE")
	require.Empty(t, a)
	_, w, _ = chunkWhere(pkT, driver.MigrationChunk{Lo: []any{int64(10)}})
	require.Equal(t, " WHERE `id` >= ?", w)

	ixT := model.MigrationTable{Name: "t", Key: model.ChunkKeySpec{Kind: model.ChunkIndex, Columns: []string{"c"}}}
	_, w, _ = chunkWhere(ixT, driver.MigrationChunk{Lo: []any{"a"}, Hi: []any{"m"}})
	require.Equal(t, " WHERE `c` >= ? AND `c` < ? AND `c` IS NOT NULL", w)
	_, w, _ = chunkWhere(ixT, driver.MigrationChunk{})
	require.Equal(t, " WHERE `c` IS NOT NULL", w, "los NULL van solo en su trozo")
	_, w, a = chunkWhere(ixT, driver.MigrationChunk{Null: true})
	require.Equal(t, " WHERE `c` IS NULL", w)
	require.Empty(t, a)

	pt := model.MigrationTable{Name: "t", Key: model.ChunkKeySpec{Kind: model.ChunkPartition, Partitions: []string{"p0"}}}
	p, w, _ = chunkWhere(pt, driver.MigrationChunk{Partition: "p0", Whole: true})
	require.Equal(t, " PARTITION (`p0`)", p)
	require.Equal(t, "", w)
}

func TestMigSQLMode(t *testing.T) {
	got := migSQLMode("ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION")
	require.Equal(t, "ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION,NO_AUTO_VALUE_ON_ZERO", got)
	require.Equal(t, "NO_AUTO_VALUE_ON_ZERO", migSQLMode(""))
	require.Equal(t, "STRICT_ALL_TABLES,NO_AUTO_VALUE_ON_ZERO", migSQLMode("STRICT_ALL_TABLES,NO_AUTO_VALUE_ON_ZERO"))
	require.NotContains(t, migSQLMode("A'; DROP"), "'", "solo caracteres seguros")
}

func TestMigInitStatements(t *testing.T) {
	s, mode := migInitStatements(driver.MigrationOpen{Role: driver.RoleServer, SQLMode: "STRICT_TRANS_TABLES"})
	require.Len(t, s, 1)
	require.Contains(t, s[0], "foreign_key_checks=0")
	require.Contains(t, s[0], "unique_checks=0", "solo la ruta en el servidor")
	require.Contains(t, s[0], "time_zone='+00:00'")
	require.Contains(t, s[0], "lock_wait_timeout=30")
	require.Contains(t, mode, "NO_AUTO_VALUE_ON_ZERO")

	s, _ = migInitStatements(driver.MigrationOpen{Role: driver.RoleSource})
	require.Len(t, s, 2)
	require.NotContains(t, s[0], "unique_checks")
	require.Equal(t, "SET SESSION TRANSACTION READ ONLY", s[1], "el origen nace READ ONLY, impuesto por el servidor")

	s, _ = migInitStatements(driver.MigrationOpen{Role: driver.RoleTarget})
	require.Len(t, s, 1)
	require.NotContains(t, s[0], "unique_checks")
}

func TestGrantScopeCovers(t *testing.T) {
	for scope, want := range map[string]bool{
		"*.*": true, "`dst`.*": true, "`DST`.*": true, "`d%`.*": true, "`d_t`.*": true, "`otro`.*": false, "`dst`.`t`": false, "`d\\_st`.*": false,
	} {
		require.Equal(t, want, grantScopeCovers(scope, "dst"), scope)
	}
}

func TestInsertSQLShape(t *testing.T) {
	q := insertSQL("dst", model.MigrationTable{Name: "t", Columns: []string{"a", "b"}}, 3)
	require.Equal(t, "INSERT INTO `dst`.`t` (`a`, `b`) VALUES (?,?),(?,?),(?,?)", q)
	require.NoError(t, newWriteGuard(driver.RoleTarget, "", "dst").check(q))
}
