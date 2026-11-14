//go:build integration

package mysql

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/migrate"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/testutil/mysqlbox"
)

// Migración de schema (spec 23) contra MySQL 8.0/8.4 y MariaDB 10.11 reales.

func rowsOf(t *testing.T, p driver.Pool, stmt string) [][]any {
	t.Helper()
	c, err := p.Acquire(context.Background())
	require.NoError(t, err)
	defer c.Release()
	r, err := c.Run(context.Background(), stmt)
	require.NoError(t, err, stmt)
	return collect(t, r)
}

func scalar(t *testing.T, p driver.Pool, stmt string) string {
	t.Helper()
	rows := rowsOf(t, p, stmt)
	require.NotEmpty(t, rows, stmt)
	return fmt.Sprint(asText(rows[0][0]))
}

func asText(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// execSession ejecuta sentencias en UNA conexión (las variables de sesión persisten entre ellas).
func execSession(t *testing.T, p driver.Pool, stmts ...string) {
	t.Helper()
	c, err := p.Acquire(context.Background())
	require.NoError(t, err)
	defer c.Release()
	for _, s := range stmts {
		r, err := c.Run(context.Background(), s)
		require.NoError(t, err, s)
		require.NoError(t, r.Close())
	}
}

// rowCount del fixture: ajustable para pruebas más pesadas.
func itRows(def int) int {
	if v, err := strconv.Atoi(os.Getenv("DBACUS_IT_ROWS")); err == nil && v > 0 {
		return v
	}
	return def
}

const fixtureSchema = "msrc"

// buildFixture arma `msrc` con lo que la migración tiene que sortear.
func buildFixture(t *testing.T, p driver.Pool, maria bool) {
	t.Helper()
	nopk := itRows(60000)
	execSession(t, p,
		"SET SESSION sql_mode=''", "SET FOREIGN_KEY_CHECKS=0",
		"DROP DATABASE IF EXISTS mnum", "CREATE DATABASE mnum", "CREATE TABLE mnum.n (n INT PRIMARY KEY)",
		"INSERT INTO mnum.n VALUES (1),(2),(3),(4),(5),(6),(7),(8)",
		"INSERT INTO mnum.n SELECT n + 8 FROM mnum.n", "INSERT INTO mnum.n SELECT n + 16 FROM mnum.n", "INSERT INTO mnum.n SELECT n + 32 FROM mnum.n",
		"INSERT INTO mnum.n SELECT n + 64 FROM mnum.n", "INSERT INTO mnum.n SELECT n + 128 FROM mnum.n", "INSERT INTO mnum.n SELECT n + 256 FROM mnum.n",
		"INSERT INTO mnum.n SELECT n + 512 FROM mnum.n", "INSERT INTO mnum.n SELECT n + 1024 FROM mnum.n", "INSERT INTO mnum.n SELECT n + 2048 FROM mnum.n",
		"INSERT INTO mnum.n SELECT n + 4096 FROM mnum.n", "INSERT INTO mnum.n SELECT n + 8192 FROM mnum.n", "INSERT INTO mnum.n SELECT n + 16384 FROM mnum.n",
		"INSERT INTO mnum.n SELECT n + 32768 FROM mnum.n", // 65 536 números

		"DROP DATABASE IF EXISTS "+fixtureSchema, "CREATE DATABASE "+fixtureSchema+" CHARACTER SET utf8mb4",
		"USE "+fixtureSchema,
		"CREATE TABLE customers (id INT NOT NULL AUTO_INCREMENT, name VARCHAR(50) NOT NULL, email VARCHAR(80) NULL, last_order INT NULL, PRIMARY KEY (id), UNIQUE KEY uq_email (email), KEY (last_order)) ENGINE=InnoDB",
		"CREATE TABLE orders (id INT NOT NULL AUTO_INCREMENT, cid INT NULL, parent INT NULL, total DECIMAL(12,2) NULL, note TEXT NULL, PRIMARY KEY (id), KEY (cid), KEY (parent), "+
			"CONSTRAINT fk_o_c FOREIGN KEY (cid) REFERENCES customers (id) ON DELETE CASCADE, CONSTRAINT fk_o_p FOREIGN KEY (parent) REFERENCES orders (id)) ENGINE=InnoDB",
		"ALTER TABLE customers ADD CONSTRAINT fk_c_o FOREIGN KEY (last_order) REFERENCES orders (id) ON DELETE SET NULL", // ciclo customers ↔ orders
		"CREATE TABLE audit (id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, msg VARCHAR(100)) ENGINE=InnoDB",
		"CREATE TRIGGER trg_orders_ai AFTER INSERT ON orders FOR EACH ROW INSERT INTO audit (msg) VALUES (CONCAT('nuevo pedido ', NEW.id))",
		"INSERT INTO customers (name, email) SELECT CONCAT('c', n), CONCAT('c', n, '@x.com') FROM mnum.n WHERE n <= 300",
		"INSERT INTO orders (cid, parent, total, note) SELECT 1 + (n % 300), IF(n > 10, n - 5, NULL), n / 3, REPEAT('x', n % 50) FROM mnum.n WHERE n <= 2500",
		"UPDATE customers SET last_order = id * 3",
		"DELETE FROM orders WHERE id = 2500", // deja el AUTO_INCREMENT por encima del máximo

		"CREATE TABLE composite (a INT NOT NULL, b VARCHAR(20) NOT NULL, c INT, PRIMARY KEY (a, b)) ENGINE=InnoDB",
		"INSERT INTO composite SELECT n % 50, CONCAT('k', n), n FROM mnum.n WHERE n <= 5000",
		"CREATE TABLE strpk (code VARCHAR(30) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL, v INT, PRIMARY KEY (code)) ENGINE=InnoDB",
		"INSERT INTO strpk SELECT IF(n % 2 = 0, CONCAT('K', LPAD(n, 6, '0')), CONCAT('k', LPAD(n, 6, '0'))), n FROM mnum.n WHERE n <= 3000",
		// Sin clave primaria: un índice no único con muchos repetidos y NULL.
		"CREATE TABLE nopk (g INT NULL, v VARCHAR(10), KEY ix_g (g)) ENGINE=InnoDB",
		fmt.Sprintf("INSERT INTO nopk SELECT IF(a.n %% 500 = 0, NULL, a.n %% 7), CONCAT('v', a.n) FROM mnum.n a WHERE a.n <= 65536 LIMIT %d", nopk),
		// Sin ningún índice.
		"CREATE TABLE noidx (x INT, y VARCHAR(10)) ENGINE=InnoDB",
		"INSERT INTO noidx SELECT n, CONCAT('y', n) FROM mnum.n WHERE n <= 8000",
		// Particionada sin clave.
		"CREATE TABLE part_nopk (id INT, v INT) ENGINE=InnoDB PARTITION BY HASH (id) PARTITIONS 4",
		"INSERT INTO part_nopk SELECT n, n * 2 FROM mnum.n WHERE n <= 4000",
		// Columnas generadas e invisibles.
		"CREATE TABLE gen (id INT NOT NULL PRIMARY KEY, a INT, b INT AS (a * 2) VIRTUAL, c INT AS (a + 1) STORED, hid INT INVISIBLE DEFAULT 7) ENGINE=InnoDB",
		"INSERT INTO gen (id, a, hid) SELECT n, n, n + 1000 FROM mnum.n WHERE n <= 100",
		// Tipos.
		"CREATE TABLE types_t (id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, f FLOAT, d DOUBLE, dec_ DECIMAL(30,10), dt DATETIME(6), ts TIMESTAMP NULL, j JSON, b BLOB, bits BIT(8), e ENUM('a','b'), s SET('x','y'), y YEAR, u BIGINT UNSIGNED, t TEXT, vb VARBINARY(10), z DATE) ENGINE=InnoDB",
		"INSERT INTO types_t (f, d, dec_, dt, ts, j, b, bits, e, s, y, u, t, vb, z) VALUES "+
			"(3.14159274, 2.718281828459045, 12345678901234567890.1234567891, '2024-02-29 23:59:59.123456', '2020-01-01 00:00:01', '{\"a\": [1, 2, {\"b\": null}]}', 0x00FF80, b'10101010', 'b', 'x,y', 2024, 18446744073709551615, '😀 ñandú', 0x0001FF, '0000-00-00'),"+
			"(NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),"+
			"(-1.5e-30, 1e300, -0.0000000001, '1000-01-01 00:00:00.000001', '2038-01-19 03:14:07', '[]', '', b'0', 'a', '', 1901, 0, '', '', '9999-12-31')",
		// Vistas (una depende de otra) y rutinas.
		"CREATE VIEW v_big AS SELECT o.id AS oid, c.name AS cname FROM orders o JOIN customers c ON c.id = o.cid",
		"CREATE VIEW v_nested AS SELECT oid FROM v_big WHERE oid < 100",
		"CREATE FUNCTION fn_orders() RETURNS INT READS SQL DATA RETURN (SELECT COUNT(*) FROM "+fixtureSchema+".orders)",
		"CREATE PROCEDURE p_touch() INSERT INTO audit (msg) VALUES ('touch')",
	)
}

func tablesOf(t *testing.T, p driver.Pool, db string) []string {
	var out []string
	for _, r := range rowsOf(t, p, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = '"+db+"' AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME") {
		out = append(out, fmt.Sprint(asText(r[0])))
	}
	return out
}

func checksum(t *testing.T, p driver.Pool, db, table string) string {
	t.Helper()
	rows := rowsOf(t, p, "CHECKSUM TABLE "+QuoteIdent(db)+"."+QuoteIdent(table))
	return fmt.Sprint(asText(rows[0][1]))
}

// ioCounters lee las escrituras acumuladas por tabla del schema (performance_schema); vacío si no está activo.
func ioCounters(t *testing.T, p driver.Pool, db string) map[string]string {
	t.Helper()
	out := map[string]string{}
	c, err := p.Acquire(context.Background())
	require.NoError(t, err)
	defer c.Release()
	r, err := c.Run(context.Background(), "SELECT OBJECT_NAME, COUNT_INSERT, COUNT_UPDATE, COUNT_DELETE FROM performance_schema.table_io_waits_summary_by_table WHERE OBJECT_SCHEMA = '"+db+"'")
	if err != nil {
		return out
	}
	for _, row := range collect(t, r) {
		out[fmt.Sprint(asText(row[0]))] = fmt.Sprint(row[1:])
	}
	return out
}

func updateTimes(t *testing.T, p driver.Pool, db string) string {
	t.Helper()
	var parts []string
	c, err := p.Acquire(context.Background())
	require.NoError(t, err)
	defer c.Release()
	_, _ = c.Run(context.Background(), "SET SESSION information_schema_stats_expiry=0")
	r, err := c.Run(context.Background(), "SELECT TABLE_NAME, IFNULL(UPDATE_TIME, '-') FROM information_schema.TABLES WHERE TABLE_SCHEMA = '"+db+"' ORDER BY TABLE_NAME")
	require.NoError(t, err)
	for _, row := range collect(t, r) {
		parts = append(parts, fmt.Sprint(asText(row[0]), "=", asText(row[1])))
	}
	return strings.Join(parts, ";")
}

type migEnv struct {
	src, dst driver.Pool
	same     bool
}

func (e migEnv) run(t *testing.T, target string, opts model.MigrationOptions, mut func(*migrate.Prepared)) (*migrate.Job, *migrate.Prepared) {
	t.Helper()
	ctx := context.Background()
	req := model.MigrationRequest{
		Source: model.MigrationEndpoint{Database: fixtureSchema}, Target: model.MigrationEndpoint{Database: target}, Options: opts,
	}
	p, err := migrate.Plan(ctx, migrate.PlanDeps{Source: e.src.(driver.SchemaMigrator), Target: e.dst.(driver.SchemaMigrator), SameSession: e.same}, req)
	require.NoError(t, err)
	if mut != nil {
		mut(p)
	}
	j := migrate.NewJob("it-"+target, p, migrate.Callbacks{})
	require.NoError(t, j.Start(ctx))
	j.Wait()
	return j, p
}

func TestMigrateIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			in := mysqlbox.Start(t, image)
			maria := strings.HasPrefix(image, "mariadb")
			root := openPool(t, in, nil)
			buildFixture(t, root, maria)
			other := openPool(t, in, nil) // segunda sesión del mismo servidor (ruta por Go)

			srcSums := map[string]string{}
			for _, tb := range tablesOf(t, root, fixtureSchema) {
				srcSums[tb] = checksum(t, root, fixtureSchema, tb)
			}
			const genContent = "SELECT SUM(id), SUM(a), SUM(b), SUM(c), SUM(hid) FROM " + fixtureSchema + ".gen"
			genBefore := scalar(t, root, genContent)
			before := ioCounters(t, root, fixtureSchema)
			timesBefore := updateTimes(t, root, fixtureSchema)

			t.Run("ruta en el servidor", func(t *testing.T) {
				testMigrateServerRoute(t, migEnv{src: root, dst: root, same: true}, root, srcSums, maria)
			})
			t.Run("ruta por Go entre dos sesiones", func(t *testing.T) {
				testMigrateGoRoute(t, migEnv{src: root, dst: other}, root, srcSums)
			})
			t.Run("el origen nunca se escribe", func(t *testing.T) {
				for _, tb := range tablesOf(t, root, fixtureSchema) {
					if tb == "gen" { // CHECKSUM TABLE no es estable con columnas generadas en MariaDB: se compara el contenido
						require.Equal(t, genBefore, scalar(t, root, genContent), "contenido de gen en el origen")
						continue
					}
					require.Equal(t, srcSums[tb], checksum(t, root, fixtureSchema, tb), "tabla %s del origen", tb)
				}
				require.Equal(t, timesBefore, updateTimes(t, root, fixtureSchema), "UPDATE_TIME del origen")
				if len(before) > 0 {
					require.Equal(t, before, ioCounters(t, root, fixtureSchema), "contadores de escritura de performance_schema sobre el origen")
				} else {
					t.Log("performance_schema sin datos: solo se comprueban los checksums")
				}
			})
			t.Run("las conexiones del origen son de solo lectura", func(t *testing.T) { testSourceConnsReadOnly(t, root) })
			t.Run("una reconexión conserva las variables de sesión", func(t *testing.T) { testReconnectKeepsSessionVars(t, root) })
			t.Run("un sql_mode de 5.7 con NO_AUTO_CREATE_USER", func(t *testing.T) { testLegacySQLMode(t, root) })
			t.Run("rutinas ilegibles para un usuario con solo EXECUTE", func(t *testing.T) { testUnreadableRoutines(t, in, root) })
			t.Run("el hash del plan sobrevive a inserts en el origen", func(t *testing.T) { testPlanHashStable(t, root) })
			t.Run("rutinas que ya existen en el destino", func(t *testing.T) { testExistingRoutines(t, root) })
			t.Run("tablas solo estructura", func(t *testing.T) { testStructureOnly(t, root) })
			t.Run("reintento de un trozo sin duplicar", func(t *testing.T) { testClearRangeRetry(t, root) })
			t.Run("la copia dentro del servidor no bloquea el origen", func(t *testing.T) { testNoLockOnSource(t, root, maria) })
			t.Run("verificación y limpieza", func(t *testing.T) { testVerifyAndCleanup(t, migEnv{src: root, dst: root, same: true}, root) })
			t.Run("sql_require_primary_key", func(t *testing.T) { testRequirePK(t, root, maria) })
			t.Run("mismo schema", func(t *testing.T) {
				_, err := migrate.Plan(context.Background(), migrate.PlanDeps{Source: root.(driver.SchemaMigrator), Target: root.(driver.SchemaMigrator), SameSession: true},
					model.MigrationRequest{Source: model.MigrationEndpoint{Database: fixtureSchema}, Target: model.MigrationEndpoint{Database: fixtureSchema}})
				var ae *apperr.AppError
				require.ErrorAs(t, err, &ae)
				require.Equal(t, "same_schema", ae.Detail)
			})
		})
	}
}

// assertSameSchema compara tablas, filas, claves foráneas, AUTO_INCREMENT y objetos entre origen y destino.
func assertSameSchema(t *testing.T, p driver.Pool, dst string, srcSums map[string]string) {
	t.Helper()
	require.Equal(t, tablesOf(t, p, fixtureSchema), tablesOf(t, p, dst), "mismas tablas")
	for tb, sum := range srcSums {
		if tb != "gen" { // CHECKSUM TABLE con columnas generadas no es comparable entre dos tablas en MariaDB: se compara por contenido más abajo
			require.Equal(t, sum, checksum(t, p, dst, tb), "datos de %s", tb)
		}
		a := scalar(t, p, "SELECT COUNT(*) FROM "+QuoteIdent(fixtureSchema)+"."+QuoteIdent(tb))
		b := scalar(t, p, "SELECT COUNT(*) FROM "+QuoteIdent(dst)+"."+QuoteIdent(tb))
		require.Equal(t, a, b, "filas de %s", tb)
	}
	fks := func(db string) []string {
		var out []string
		for _, r := range rowsOf(t, p, "SELECT TABLE_NAME, CONSTRAINT_NAME, REFERENCED_TABLE_NAME, UPDATE_RULE, DELETE_RULE, REFERENCED_TABLE_SCHEMA FROM information_schema.REFERENTIAL_CONSTRAINTS rc JOIN (SELECT DISTINCT CONSTRAINT_SCHEMA, CONSTRAINT_NAME, REFERENCED_TABLE_SCHEMA FROM information_schema.KEY_COLUMN_USAGE) k USING (CONSTRAINT_SCHEMA, CONSTRAINT_NAME) WHERE CONSTRAINT_SCHEMA = '"+db+"' ORDER BY 1, 2") {
			sch := fmt.Sprint(asText(r[5]))
			if sch == fixtureSchema || sch == dst {
				sch = "<este>"
			}
			// En InnoDB RESTRICT y NO ACTION se comportan igual y MariaDB muestra uno por el otro: se comparan como equivalentes.
			rule := func(v any) string { return strings.NewReplacer("NO ACTION", "RESTRICT").Replace(fmt.Sprint(asText(v))) }
			out = append(out, fmt.Sprint(asText(r[0]), asText(r[1]), asText(r[2]), rule(r[3]), rule(r[4]), sch))
		}
		return out
	}
	require.Equal(t, fks(fixtureSchema), fks(dst), "las claves foráneas del destino apuntan al destino")
	require.GreaterOrEqual(t, len(fks(dst)), 3)
	autoInc := func(db, tb string) int64 {
		rows := rowsOf(t, p, "SHOW CREATE TABLE "+QuoteIdent(db)+"."+QuoteIdent(tb))
		s := fmt.Sprint(asText(rows[0][1]))
		if m := autoIncOption.FindStringSubmatch(s); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			return n
		}
		return 0
	}
	for _, tb := range []string{"customers", "orders", "audit", "types_t"} {
		require.GreaterOrEqual(t, autoInc(dst, tb), autoInc(fixtureSchema, tb), "AUTO_INCREMENT de %s no baja del origen", tb)
	}
	require.Equal(t, "2500", scalar(t, p, "SELECT MAX(id) + 1 FROM "+QuoteIdent(dst)+".orders"), "el máximo +1 conserva el contador subido por borrados")
	for _, kind := range []struct{ q, name string }{
		{"SELECT COUNT(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA = '" + dst + "'", "vistas"},
		{"SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = '" + dst + "'", "rutinas"},
		{"SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = '" + dst + "'", "triggers"},
	} {
		require.Equal(t, strings.Replace(kind.q, dst, fixtureSchema, 1), strings.Replace(kind.q, dst, fixtureSchema, 1))
		require.Equal(t, scalar(t, p, strings.Replace(kind.q, dst, fixtureSchema, 1)), scalar(t, p, kind.q), kind.name)
	}
	// El texto de las vistas y rutinas apunta al destino.
	require.Contains(t, fmt.Sprint(asText(rowsOf(t, p, "SHOW CREATE VIEW "+QuoteIdent(dst)+".v_big")[0][1])), "`"+dst+"`.`orders`")
	fnText := fmt.Sprint(asText(rowsOf(t, p, "SHOW CREATE FUNCTION "+QuoteIdent(dst)+".fn_orders")[0][2]))
	require.Contains(t, fnText, "`"+dst+"`.", "la rutina apunta al schema nuevo")
	require.NotContains(t, fnText, fixtureSchema+".", "y no al origen")
	require.Equal(t, scalar(t, p, "SELECT "+QuoteIdent(dst)+".fn_orders()"), scalar(t, p, "SELECT COUNT(*) FROM "+QuoteIdent(dst)+".orders"))
	// Los triggers no se dispararon durante la carga: la auditoría del destino tiene lo mismo que la del origen.
	require.Equal(t, scalar(t, p, "SELECT COUNT(*) FROM "+QuoteIdent(fixtureSchema)+".audit"), scalar(t, p, "SELECT COUNT(*) FROM "+QuoteIdent(dst)+".audit"), "TestMigrateTriggersCreatedLast")
	// Columnas invisibles y generadas.
	require.Equal(t, scalar(t, p, "SELECT SUM(hid) FROM "+QuoteIdent(fixtureSchema)+".gen"), scalar(t, p, "SELECT SUM(hid) FROM "+QuoteIdent(dst)+".gen"), "columna invisible")
	require.Equal(t, scalar(t, p, "SELECT SUM(id), SUM(a), SUM(b), SUM(c), SUM(hid) FROM "+QuoteIdent(fixtureSchema)+".gen"), scalar(t, p, "SELECT SUM(id), SUM(a), SUM(b), SUM(c), SUM(hid) FROM "+QuoteIdent(dst)+".gen"), "contenido de la tabla con columnas generadas e invisibles")
	require.Equal(t, scalar(t, p, "SELECT SUM(b + c) FROM "+QuoteIdent(dst)+".gen"), scalar(t, p, "SELECT SUM(a * 2 + a + 1) FROM "+QuoteIdent(dst)+".gen"), "columnas generadas")
}

func jobSummary(r model.MigrationResult) string {
	return fmt.Sprintf("estado=%s fallos=%+v", r.Job.State, r.Failed)
}

func testMigrateServerRoute(t *testing.T, e migEnv, p driver.Pool, srcSums map[string]string, maria bool) {
	j, prep := e.run(t, "mdst_srv", model.MigrationOptions{Workers: 4, ChunkRows: 1000, VerifyForeignKeys: true, VerifyRowCounts: true}, nil)
	r := j.Result()
	require.Equal(t, model.MigDone, r.Job.State, jobSummary(r))
	require.Equal(t, model.RouteServer, prep.Plan.Route)
	byName := map[string]model.PlanTable{}
	for _, tb := range prep.Plan.Tables {
		byName[tb.Name] = tb
	}
	require.Equal(t, model.ChunkPK, byName["orders"].ChunkKey)
	require.Equal(t, model.ChunkPK, byName["composite"].ChunkKey)
	require.Equal(t, model.ChunkIndex, byName["nopk"].ChunkKey, "sin PK pero con índice")
	require.Equal(t, model.ChunkNone, byName["noidx"].ChunkKey)
	require.Equal(t, model.ChunkPartition, byName["part_nopk"].ChunkKey)
	require.Equal(t, []string{"b", "c"}, byName["gen"].SkippedColumns, "las generadas no se insertan")
	assertSameSchema(t, p, "mdst_srv", srcSums)
	require.NotNil(t, j.Result().Verification)
	require.Empty(t, j.Result().Verification.Orphans)
	require.Empty(t, j.Result().Verification.RowCounts)
	// El código de rutinas, vistas (una anidada) y el trigger coincide con el del origen tras un viaje real por SHOW CREATE.
	ov, err := j.Verify(context.Background(), model.VerifyOptions{Objects: true})
	require.NoError(t, err)
	require.Equal(t, 5, ov.ObjectsChecked, "fn_orders, p_touch, v_big, v_nested y trg_orders_ai")
	require.Len(t, ov.Objects, 5)
	for _, o := range ov.Objects {
		require.Equal(t, "same", o.Status, "%s %s: sin falsas diferencias", o.Kind, o.Name)
	}
	// La tabla sin PK se partió en varios trozos y quedó con las mismas filas (TestMigrateNoPrimaryKeyTableParallel).
	for _, tb := range r.Job.Tables {
		if tb.Name == "nopk" {
			require.Greater(t, tb.ChunksAll, 2, "el índice no único reparte la tabla en varios trozos")
		}
	}
}

func testMigrateGoRoute(t *testing.T, e migEnv, p driver.Pool, srcSums map[string]string) {
	j, prep := e.run(t, "mdst_go", model.MigrationOptions{Workers: 4, ChunkRows: 1000}, func(p *migrate.Prepared) {
		// Los tipos espaciales no hay: el fixture no los trae.
	})
	r := j.Result()
	require.Equal(t, model.RouteGo, prep.Plan.Route)
	require.Equal(t, model.MigDone, r.Job.State, jobSummary(r))
	assertSameSchema(t, p, "mdst_go", srcSums)
	// Fidelidad de tipos: CHECKSUM TABLE ya los comparó; se mira también un valor delicado a ojo.
	require.Equal(t, scalar(t, p, "SELECT HEX(b), BIN(bits+0), u, t FROM msrc.types_t WHERE id = 1"), scalar(t, p, "SELECT HEX(b), BIN(bits+0), u, t FROM mdst_go.types_t WHERE id = 1"))
	require.Equal(t, scalar(t, p, "SELECT f FROM msrc.types_t WHERE id = 1"), scalar(t, p, "SELECT f FROM mdst_go.types_t WHERE id = 1"), "FLOAT exacto")
	require.Equal(t, scalar(t, p, "SELECT UNIX_TIMESTAMP(ts) FROM msrc.types_t WHERE id = 1"), scalar(t, p, "SELECT UNIX_TIMESTAMP(ts) FROM mdst_go.types_t WHERE id = 1"), "TIMESTAMP no cambia de zona")
}

func testSourceConnsReadOnly(t *testing.T, p driver.Pool) {
	m := p.(driver.SchemaMigrator)
	s, err := m.OpenMigration(context.Background(), driver.MigrationOpen{Role: driver.RoleSource, Database: fixtureSchema, Size: 1})
	require.NoError(t, err)
	defer s.Close()
	c, err := s.Acquire(context.Background())
	require.NoError(t, err)
	defer c.Release()
	// Saltándose el guardia (que ya lo rechaza), el servidor también impone el solo lectura: error 1792.
	_, err = c.(*migConn).c.ExecContext(context.Background(), "INSERT INTO "+fixtureSchema+".audit (msg) VALUES ('no')")
	require.Error(t, err)
	require.Contains(t, err.Error(), "1792", "Cannot execute statement in a READ ONLY transaction")
	require.Error(t, c.Exec(context.Background(), "INSERT INTO `"+fixtureSchema+"`.`audit` (msg) VALUES ('no')"), "el guardia lo rechaza antes de enviarlo")
	require.Equal(t, "ON", scalar(t, p, "SELECT IF(1=1,'ON','OFF')"))
}

func testReconnectKeepsSessionVars(t *testing.T, p driver.Pool) {
	m := p.(driver.SchemaMigrator)
	execSession(t, p, "DROP DATABASE IF EXISTS mrecon", "CREATE DATABASE mrecon")
	s, err := m.OpenMigration(context.Background(), driver.MigrationOpen{Role: driver.RoleServer, Database: "mrecon", Source: fixtureSchema, Size: 1})
	require.NoError(t, err)
	defer s.Close()
	vars := func(c driver.MigrationConn) string {
		row := c.(*migConn).c.QueryRowContext(context.Background(), "SELECT @@foreign_key_checks, @@unique_checks, @@time_zone")
		var a, b, tz string
		require.NoError(t, row.Scan(&a, &b, &tz))
		return a + "|" + b + "|" + tz
	}
	c1, err := s.Acquire(context.Background())
	require.NoError(t, err)
	require.Equal(t, "0|0|+00:00", vars(c1))
	id := c1.ThreadID()
	_, err = p.(*pool).aux.ExecContext(context.Background(), "KILL "+strconv.FormatUint(id, 10)) // se cae la conexión del worker
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)
	require.Error(t, c1.Exec(context.Background(), "TRUNCATE TABLE `mrecon`.`x`"), "la conexión caída falla")
	_ = c1.Release()
	c2, err := s.Acquire(context.Background())
	require.NoError(t, err)
	defer c2.Release()
	require.NotEqual(t, id, c2.ThreadID(), "el pool abrió otra conexión")
	require.Equal(t, "0|0|+00:00", vars(c2), "la conexión nueva ya nace con las foráneas apagadas (TestMigrateReconnectKeepsSessionVars)")
}

func testClearRangeRetry(t *testing.T, p driver.Pool) {
	ctx := context.Background()
	execSession(t, p, "DROP DATABASE IF EXISTS mretry", "CREATE DATABASE mretry", "CREATE TABLE mretry.orders LIKE "+fixtureSchema+".orders")
	m := p.(driver.SchemaMigrator)
	s, err := m.OpenMigration(ctx, driver.MigrationOpen{Role: driver.RoleServer, Database: "mretry", Source: fixtureSchema, Size: 1})
	require.NoError(t, err)
	defer s.Close()
	c, err := s.Acquire(ctx)
	require.NoError(t, err)
	defer c.Release()
	tbl := model.MigrationTable{Name: "orders", Columns: []string{"id", "cid", "parent", "total", "note"}, Key: model.ChunkKeySpec{Kind: model.ChunkPK, Columns: []string{"id"}}}
	ch := driver.MigrationChunk{Lo: []any{int64(100)}, Hi: []any{int64(300)}}
	copyOnce := func() int64 {
		require.NoError(t, c.Begin(ctx))
		n, err := c.CopyChunk(ctx, tbl, ch)
		require.NoError(t, err)
		require.NoError(t, c.Commit(ctx))
		return n
	}
	require.EqualValues(t, 200, copyOnce())
	// El COMMIT se aplicó pero se perdió el acuse: se reintenta; sin ClearRange habría una clave duplicada.
	require.Error(t, func() error {
		require.NoError(t, c.Begin(ctx))
		_, err := c.CopyChunk(ctx, tbl, ch)
		_ = c.Rollback(ctx)
		return err
	}(), "duplicaría las filas")
	require.NoError(t, c.ClearRange(ctx, tbl, ch))
	require.Equal(t, "0", scalar(t, p, "SELECT COUNT(*) FROM mretry.orders"))
	require.EqualValues(t, 200, copyOnce())
	require.Equal(t, "200", scalar(t, p, "SELECT COUNT(*) FROM mretry.orders"), "TestMigrateRetryClearsChunkRange: ni duplicadas ni perdidas")
	require.NoError(t, c.ClearRange(ctx, tbl, driver.MigrationChunk{Whole: true}))
	require.Equal(t, "0", scalar(t, p, "SELECT COUNT(*) FROM mretry.orders"), "un trozo entero vacía la tabla")
}

// Con READ COMMITTED el INSERT … SELECT lee el origen sin bloquearlo; con REPEATABLE READ pone bloqueos compartidos (control).
func testNoLockOnSource(t *testing.T, p driver.Pool, maria bool) {
	ctx := context.Background()
	execSession(t, p, "DROP DATABASE IF EXISTS mlock", "CREATE DATABASE mlock", "CREATE TABLE mlock.t LIKE "+fixtureSchema+".customers")
	tbl := model.MigrationTable{Name: "t", Columns: []string{"id", "name", "email", "last_order"}, Key: model.ChunkKeySpec{Kind: model.ChunkPK, Columns: []string{"id"}}}
	m := p.(driver.SchemaMigrator)
	s, err := m.OpenMigration(ctx, driver.MigrationOpen{Role: driver.RoleServer, Database: "mlock", Source: fixtureSchema, Size: 1})
	require.NoError(t, err)
	defer s.Close()
	c, err := s.Acquire(ctx)
	require.NoError(t, err)
	defer c.Release()
	require.NoError(t, c.Begin(ctx))
	// Se copia con el nombre de la tabla del origen: customers.
	srcTbl := tbl
	_, err = c.(*migConn).exec(ctx, "INSERT INTO `mlock`.`t` (`id`, `name`, `email`, `last_order`) SELECT `id`, `name`, `email`, `last_order` FROM `"+fixtureSchema+"`.`customers` WHERE `id` >= ? AND `id` < ?", int64(1), int64(200))
	require.NoError(t, err)
	_ = srcTbl
	// Con la transacción de la copia abierta, otra sesión escribe en el origen: no espera.
	other, err := p.Acquire(ctx)
	require.NoError(t, err)
	defer other.Release()
	_, _ = other.Run(ctx, "SET SESSION innodb_lock_wait_timeout = 2")
	start := time.Now()
	r, err := other.Run(ctx, "UPDATE "+fixtureSchema+".customers SET name = name WHERE id = 5")
	require.NoError(t, err, "TestSameServerCopyDoesNotLockSource: la copia en READ COMMITTED no bloquea al origen")
	_ = r.Close()
	require.Less(t, time.Since(start), 1500*time.Millisecond)
	require.NoError(t, c.Rollback(ctx))

	// Control: una transacción REPEATABLE READ corriente sí bloquea esas filas (comportamiento documentado de InnoDB).
	ctl, err := p.Acquire(ctx)
	require.NoError(t, err)
	defer ctl.Release()
	require.NoError(t, ctl.Begin(ctx))
	_, _, err = ctl.Exec(ctx, "INSERT INTO mlock.t (id, name, email, last_order) SELECT id, name, email, last_order FROM "+fixtureSchema+".customers WHERE id < 200")
	require.NoError(t, err)
	start = time.Now()
	_, err = other.Run(ctx, "UPDATE "+fixtureSchema+".customers SET name = name WHERE id = 5")
	if err == nil {
		t.Log("control: el servidor no bloqueó con REPEATABLE READ en esta versión; la propiedad de READ COMMITTED no se contrasta")
	} else {
		require.GreaterOrEqual(t, time.Since(start), time.Second, "control: con REPEATABLE READ la escritura espera al bloqueo compartido")
	}
	require.NoError(t, ctl.Rollback(ctx))
}

func testVerifyAndCleanup(t *testing.T, e migEnv, p driver.Pool) {
	ctx := context.Background()
	execSession(t, p, "DROP DATABASE IF EXISTS mdst_ver")
	j, _ := e.run(t, "mdst_ver", model.MigrationOptions{Workers: 2, ChunkRows: 1000}, nil)
	require.Equal(t, model.MigDone, j.Result().Job.State, jobSummary(j.Result()))
	// Alguien inserta un huérfano en el destino (con las foráneas apagadas, como hace la propia migración).
	execSession(t, p, "SET FOREIGN_KEY_CHECKS=0", "INSERT INTO mdst_ver.orders (id, cid) VALUES (999999, 424242)", "DELETE FROM mdst_ver.noidx WHERE x < 10")
	v, err := j.Verify(ctx, model.VerifyOptions{ForeignKeys: true, RowCounts: true})
	require.NoError(t, err)
	require.Len(t, v.Orphans, 1, "TestVerifyFindsOrphans")
	require.Equal(t, "fk_o_c", v.Orphans[0].ForeignKey)
	require.EqualValues(t, 1, v.Orphans[0].Count)
	var diffs []string
	for _, d := range v.RowCounts {
		diffs = append(diffs, d.Table)
	}
	sort.Strings(diffs)
	require.Equal(t, []string{"audit", "noidx", "orders"}, diffs, "TestVerifyRowCounts (el trigger ya existe en el destino y audita el huérfano insertado)")

	// Limpieza: solo lo creado por el job; el schema (lo creó la migración) también se borra porque queda vacío.
	execSession(t, p, "CREATE TABLE mdst_ver.ajena (a INT)")
	require.NoError(t, j.Cleanup(ctx))
	require.Equal(t, []string{"ajena"}, tablesOf(t, p, "mdst_ver"), "TestCleanupOnlyDropsCreatedObjects: lo ajeno sigue ahí")
	require.Equal(t, "1", scalar(t, p, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = 'mdst_ver'"), "el schema no se borra: no quedó vacío")
	execSession(t, p, "DROP DATABASE mdst_ver")
}

func testRequirePK(t *testing.T, p driver.Pool, maria bool) {
	if maria {
		t.Skip("MariaDB no tiene sql_require_primary_key")
	}
	ctx := context.Background()
	execSession(t, p, "SET GLOBAL sql_require_primary_key = ON")
	defer execSession(t, p, "SET GLOBAL sql_require_primary_key = OFF")
	m := p.(driver.SchemaMigrator)
	_, err := migrate.Plan(ctx, migrate.PlanDeps{Source: m, Target: m, SameSession: true},
		model.MigrationRequest{Source: model.MigrationEndpoint{Database: fixtureSchema}, Target: model.MigrationEndpoint{Database: "mdst_rpk"}})
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "incompatible", ae.Detail)
	require.Contains(t, ae.Message, "nopk")
	require.Equal(t, "0", scalar(t, p, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = 'mdst_rpk'"), "no se creó nada")
	// Sin las tablas sin PK, el plan pasa.
	_, err = migrate.Plan(ctx, migrate.PlanDeps{Source: m, Target: m, SameSession: true}, model.MigrationRequest{
		Source: model.MigrationEndpoint{Database: fixtureSchema}, Target: model.MigrationEndpoint{Database: "mdst_rpk"},
		Objects: model.MigrationObjects{Tables: []string{"customers", "orders"}}})
	require.NoError(t, err)
}

// Entre dos servidores distintos (dos contenedores): ruta por Go, tipos intactos y collations comprobadas en el plan.
func TestMigrateAcrossServersIntegration(t *testing.T) {
	images := mysqlbox.Images()
	srcImage := images[0]
	srcIn := mysqlbox.Start(t, srcImage)
	src := openPool(t, srcIn, nil)
	buildFixture(t, src, strings.HasPrefix(srcImage, "mariadb"))
	sums := map[string]string{}
	for _, tb := range tablesOf(t, src, fixtureSchema) {
		if tb != "gen" {
			sums[tb] = checksum(t, src, fixtureSchema, tb)
		}
	}

	t.Run("mismo motor, otro servidor", func(t *testing.T) {
		dstIn := mysqlbox.Start(t, srcImage)
		dst := openPool(t, dstIn, nil)
		ctx := context.Background()
		p, err := migrate.Plan(ctx, migrate.PlanDeps{Source: src.(driver.SchemaMigrator), Target: dst.(driver.SchemaMigrator)},
			model.MigrationRequest{Source: model.MigrationEndpoint{Database: fixtureSchema}, Target: model.MigrationEndpoint{Database: "remoto"}, Options: model.MigrationOptions{Workers: 4, ChunkRows: 1000}})
		require.NoError(t, err)
		require.Equal(t, model.RouteGo, p.Plan.Route)
		j := migrate.NewJob("it-remote", p, migrate.Callbacks{})
		require.NoError(t, j.Start(ctx))
		j.Wait()
		r := j.Result()
		require.Equal(t, model.MigDone, r.Job.State, jobSummary(r))
		for tb, sum := range sums {
			require.Equal(t, sum, checksum(t, dst, "remoto", tb), "datos de %s en otro servidor", tb)
		}
		require.Equal(t, scalar(t, src, "SELECT COUNT(*) FROM "+fixtureSchema+".audit"), scalar(t, dst, "SELECT COUNT(*) FROM remoto.audit"), "los triggers no se dispararon en la carga")
		require.Equal(t, "1", scalar(t, dst, "SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = 'remoto'"))
	})

	t.Run("collations que el destino no tiene", func(t *testing.T) {
		var other string
		for _, im := range images {
			if strings.HasPrefix(im, "mariadb") != strings.HasPrefix(srcImage, "mariadb") {
				other = im
				break
			}
		}
		if other == "" {
			t.Skip("hace falta una imagen de la otra familia (MySQL ↔ MariaDB)")
		}
		dstIn := mysqlbox.Start(t, other)
		dst := openPool(t, dstIn, nil)
		_, err := migrate.Plan(context.Background(), migrate.PlanDeps{Source: src.(driver.SchemaMigrator), Target: dst.(driver.SchemaMigrator)},
			model.MigrationRequest{Source: model.MigrationEndpoint{Database: fixtureSchema}, Target: model.MigrationEndpoint{Database: "x"}})
		var ae *apperr.AppError
		require.ErrorAs(t, err, &ae, "TestPlanRejectsUnknownCollation")
		require.Equal(t, "incompatible", ae.Detail)
		require.Contains(t, ae.Message, "utf8mb4_0900_ai_ci")
		require.Equal(t, "0", scalar(t, dst, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = 'x'"), "no se creó nada")
	})
}

// Medición (spec 23, ADR-008): DBACUS_BENCH_MIGRATE=1 DBACUS_IT_ROWS=1000000. Tabla de ~1 M de filas con un índice secundario.
func TestMigrateBench(t *testing.T) {
	if os.Getenv("DBACUS_BENCH_MIGRATE") == "" {
		t.Skip("DBACUS_BENCH_MIGRATE=1 para medir")
	}
	rows := itRows(1_048_576)
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			in := mysqlbox.Start(t, image)
			root := openPool(t, in, nil)
			other := openPool(t, in, nil)
			execSession(t, root, "SET SESSION sql_mode=''",
				"DROP DATABASE IF EXISTS mnum", "CREATE DATABASE mnum", "CREATE TABLE mnum.n (n INT PRIMARY KEY)", "INSERT INTO mnum.n VALUES (1),(2),(3),(4),(5),(6),(7),(8)")
			for i := 8; i <= 32768; i *= 2 {
				execSession(t, root, fmt.Sprintf("INSERT INTO mnum.n SELECT n + %d FROM mnum.n", i))
			}
			execSession(t, root, "DROP DATABASE IF EXISTS "+fixtureSchema, "CREATE DATABASE "+fixtureSchema,
				"CREATE TABLE "+fixtureSchema+".bench (id BIGINT NOT NULL AUTO_INCREMENT, a INT, b VARCHAR(40), c DECIMAL(10,2), d DATETIME, PRIMARY KEY (id), KEY ix_a (a)) ENGINE=InnoDB",
				fmt.Sprintf("INSERT INTO %s.bench (a, b, c, d) SELECT x.n %% 1000, CONCAT('fila-', x.n, '-', y.n), x.n / 7, NOW() FROM mnum.n x JOIN (SELECT n FROM mnum.n WHERE n <= 16) y LIMIT %d", fixtureSchema, rows))
			t.Logf("%s: %s filas en el origen", image, scalar(t, root, "SELECT COUNT(*) FROM "+fixtureSchema+".bench"))
			type cfg struct {
				route   string
				workers int
			}
			var cfgs []cfg
			for _, w := range []int{1, 2, 4, 8, 16} {
				cfgs = append(cfgs, cfg{model.RouteServer, w})
			}
			for _, w := range []int{1, 2, 4, 8, 16} {
				cfgs = append(cfgs, cfg{model.RouteGo, w})
			}
			only := os.Getenv("DBACUS_BENCH_ONLY") // p. ej. "go:16" para repetir una sola configuración
			for _, c := range cfgs {
				if only != "" && only != fmt.Sprintf("%s:%d", c.route, c.workers) {
					continue
				}
				dst := fmt.Sprintf("mbench_%s_%d", c.route, c.workers)
				e := migEnv{src: root, dst: root, same: c.route == model.RouteServer}
				if c.route == model.RouteGo {
					e.dst = other
				}
				waits := func() string { // esperas de InnoDB por redo log lleno o buffer pool sin páginas libres
					out := ""
					for _, v := range []string{"Innodb_log_waits", "Innodb_buffer_pool_wait_free", "Innodb_row_lock_waits"} {
						if rows := rowsOf(t, root, "SHOW GLOBAL STATUS LIKE '"+v+"'"); len(rows) > 0 {
							out += fmt.Sprintf(" %s=%s", strings.TrimPrefix(v, "Innodb_"), asText(rows[0][1]))
						}
					}
					return out
				}
				w0 := waits()
				j, _ := e.run(t, dst, model.MigrationOptions{Workers: c.workers, ChunkRows: 100_000}, nil)
				r := j.Result()
				require.Equal(t, model.MigDone, r.Job.State, jobSummary(r))
				secs := float64(r.Job.ElapsedMs) / 1000
				t.Logf("MEDICIÓN %-14s ruta=%-6s workers=%-2d %7.2f s  %9.0f filas/s | antes:%s | después:%s", image, c.route, c.workers, secs, float64(r.Job.Rows)/secs, w0, waits())
				execSession(t, root, "DROP DATABASE "+dst)
			}
		})
	}
}

// testLegacySQLMode: un origen 5.7 trae NO_AUTO_CREATE_USER, que un 8.x rechaza con el error 1231; la migración lo descarta en lugar de fallar.
func testLegacySQLMode(t *testing.T, p driver.Pool) {
	m := p.(driver.SchemaMigrator)
	execSession(t, p, "DROP DATABASE IF EXISTS mmode", "CREATE DATABASE mmode")
	legacy := "ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_AUTO_CREATE_USER,NO_ENGINE_SUBSTITUTION"
	s, err := m.OpenMigration(context.Background(), driver.MigrationOpen{Role: driver.RoleTarget, Database: "mmode", Size: 1, SQLMode: legacy})
	require.NoError(t, err)
	defer s.Close()
	c, err := s.Acquire(context.Background())
	require.NoError(t, err)
	defer c.Release()
	require.NoError(t, c.CreateObject(context.Background(),
		model.MigrationCode{Kind: "view", Name: "v", SQLMode: legacy}, "CREATE VIEW `mmode`.`v` AS SELECT 1 AS a"))
	require.Equal(t, "1", scalar(t, p, "SELECT a FROM mmode.v"))
}

// testStructureOnly: las tablas marcadas se crean vacías (con su estructura y sin el contador del origen) y el resto se copia completo.
func testStructureOnly(t *testing.T, p driver.Pool) {
	ctx := context.Background()
	execSession(t, p, "DROP DATABASE IF EXISTS mstruct")
	m := p.(driver.SchemaMigrator)
	prep, err := migrate.Plan(ctx, migrate.PlanDeps{Source: m, Target: m, SameSession: true}, model.MigrationRequest{
		Source:  model.MigrationEndpoint{Database: fixtureSchema},
		Target:  model.MigrationEndpoint{Database: "mstruct"},
		Objects: model.MigrationObjects{StructureOnly: []string{"customers", "strpk"}},
		Options: model.MigrationOptions{Workers: 2, VerifyRowCounts: true},
	})
	require.NoError(t, err)
	j := migrate.NewJob("it-mstruct", prep, migrate.Callbacks{})
	require.NoError(t, j.Start(ctx))
	j.Wait()
	r := j.Result()
	require.Equal(t, model.MigDone, r.Job.State, jobSummary(r))

	require.Equal(t, "0", scalar(t, p, "SELECT COUNT(*) FROM mstruct.customers"))
	require.Equal(t, "0", scalar(t, p, "SELECT COUNT(*) FROM mstruct.strpk"))
	for _, tb := range []string{"orders", "composite", "nopk", "noidx"} {
		require.Equal(t, scalar(t, p, "SELECT COUNT(*) FROM "+fixtureSchema+"."+tb), scalar(t, p, "SELECT COUNT(*) FROM mstruct."+tb), tb)
	}
	// Estructura intacta: columnas, índices y foráneas existen aunque no haya filas.
	require.Equal(t, scalar(t, p, "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema='"+fixtureSchema+"' AND table_name='customers'"),
		scalar(t, p, "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema='mstruct' AND table_name='customers'"))
	// Una tabla vacía empieza en 1, no en el contador del origen.
	execSession(t, p, "INSERT INTO mstruct.strpk (code, v) VALUES ('z', 1)", "INSERT INTO mstruct.customers (name) VALUES ('nuevo')")
	require.Equal(t, "1", scalar(t, p, "SELECT MAX(id) FROM mstruct.customers"))

	v, err := j.Verify(ctx, model.VerifyOptions{RowCounts: true})
	require.NoError(t, err)
	require.Empty(t, v.RowCounts, "las tablas vacías a propósito no cuentan como diferencia")
	require.NoError(t, j.Cleanup(ctx))
}

// testUnreadableRoutines: con solo SELECT y EXECUTE, SHOW CREATE PROCEDURE devuelve el cuerpo como NULL; el plan omite esas rutinas con un aviso.
func testUnreadableRoutines(t *testing.T, in mysqlbox.Instance, root driver.Pool) {
	ctx := context.Background()
	execSession(t, root, "DROP USER IF EXISTS 'lim'@'%'", "CREATE USER 'lim'@'%' IDENTIFIED BY 'limpass'",
		"GRANT SELECT, SHOW VIEW, TRIGGER, EXECUTE ON "+fixtureSchema+".* TO 'lim'@'%'")
	execSession(t, root, "DROP DATABASE IF EXISTS mlim", "CREATE DATABASE mlim", "GRANT ALL ON mlim.* TO 'lim'@'%'") // destino vacío donde sí puede escribir
	t.Cleanup(func() { execSession(t, root, "DROP DATABASE IF EXISTS mlim", "DROP USER IF EXISTS 'lim'@'%'") })
	lim := openPool(t, in, func(c *driver.ConnConfig) {
		c.Profile.User, c.Password, c.Profile.Database = "lim", "limpass", fixtureSchema
	})
	m := lim.(driver.SchemaMigrator)

	src, err := m.MigrationInspect(ctx, fixtureSchema)
	require.NoError(t, err)
	require.NotEmpty(t, src.Routines)
	for _, r := range src.Routines {
		require.Empty(t, strings.TrimSpace(r.CreateSQL), "%s: con solo EXECUTE el servidor no entrega el cuerpo", r.Name)
	}

	prep, err := migrate.Plan(ctx, migrate.PlanDeps{Source: m, Target: m, SameSession: true},
		model.MigrationRequest{Source: model.MigrationEndpoint{Database: fixtureSchema}, Target: model.MigrationEndpoint{Database: "mlim"}})
	require.NoError(t, err)
	require.Len(t, prep.Plan.Routines, len(src.Routines), "siguen en el plan, para que se vean en el paso de contenido")
	for _, o := range prep.Plan.Routines {
		require.True(t, o.Skipped, "%s no se puede elegir", o.Name)
		require.Equal(t, "unreadable", o.Compare, o.Name)
	}
	require.Empty(t, prep.Routines, "ninguna rutina ilegible entra en lo que se crea")
	var warned int
	for _, w := range prep.Plan.Warnings {
		if w.Code == "unreadable_code" {
			warned++
		}
	}
	require.Equal(t, len(src.Routines), warned)
}

// testPlanHashStable: un insert en el origen entre «Revisar» y «Migrar» sube el AUTO_INCREMENT del SHOW CREATE; no debe dar stale_plan.
func testPlanHashStable(t *testing.T, p driver.Pool) {
	ctx := context.Background()
	execSession(t, p, "DROP DATABASE IF EXISTS mhash", "CREATE DATABASE mhash",
		"CREATE TABLE mhash.t (id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, v INT) ENGINE=InnoDB", "INSERT INTO mhash.t (v) VALUES (1),(2),(3)")
	t.Cleanup(func() { execSession(t, p, "DROP DATABASE IF EXISTS mhash") })
	m := p.(driver.SchemaMigrator)
	plan := func() string {
		prep, err := migrate.Plan(ctx, migrate.PlanDeps{Source: m, Target: m, SameSession: true},
			model.MigrationRequest{Source: model.MigrationEndpoint{Database: "mhash"}, Target: model.MigrationEndpoint{Database: "mhash_copia"}})
		require.NoError(t, err)
		return prep.Plan.PlanHash
	}
	before := plan()
	execSession(t, p, "INSERT INTO mhash.t (v) VALUES (4),(5),(6),(7)")
	require.Equal(t, before, plan(), "AUTO_INCREMENT subió y el plan sigue siendo el mismo")
	execSession(t, p, "ALTER TABLE mhash.t ADD COLUMN extra INT NULL")
	require.NotEqual(t, before, plan(), "una columna nueva sí invalida el plan")
}

// testExistingRoutines: lo que ya existe en el destino se compara y se omite (⚑D21), sin reemplazarlo. La primera pasada omite una rutina
// distinta y crea la otra; la segunda ve la creada como «igual» (comprueba la normalización contra el SHOW CREATE real).
func testExistingRoutines(t *testing.T, p driver.Pool) {
	ctx := context.Background()
	execSession(t, p, "DROP DATABASE IF EXISTS mexist", "CREATE DATABASE mexist",
		"CREATE PROCEDURE mexist.p_touch() SELECT 'versión del destino'")
	t.Cleanup(func() { execSession(t, p, "DROP DATABASE IF EXISTS mexist") })
	m := p.(driver.SchemaMigrator)
	req := model.MigrationRequest{
		Source:  model.MigrationEndpoint{Database: fixtureSchema},
		Target:  model.MigrationEndpoint{Database: "mexist"},
		Objects: model.MigrationObjects{Tables: []string{"audit"}, Views: []string{}, Routines: []model.RoutineRef{{Kind: "procedure", Name: "p_touch"}, {Kind: "function", Name: "fn_orders"}}, Triggers: []string{}},
		Options: model.MigrationOptions{Workers: 2},
	}
	_, err := migrate.Plan(ctx, migrate.PlanDeps{Source: m, Target: m, SameSession: true}, req)
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "exists", ae.Detail)
	require.Contains(t, ae.Message, "p_touch", "el rechazo nombra la rutina que choca")

	req.Options.SkipExisting = true
	compare := func(prep *migrate.Prepared) map[string]string {
		got := map[string]string{}
		for _, o := range prep.Plan.Routines {
			if o.Skipped {
				got[o.Name] = o.Compare
			}
		}
		return got
	}
	prep, err := migrate.Plan(ctx, migrate.PlanDeps{Source: m, Target: m, SameSession: true}, req)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"p_touch": "different"}, compare(prep))
	j := migrate.NewJob("it-mexist", prep, migrate.Callbacks{})
	require.NoError(t, j.Start(ctx))
	j.Wait()
	r := j.Result()
	require.Equal(t, model.MigDone, r.Job.State, jobSummary(r))
	require.Empty(t, r.Failed)
	require.Contains(t, scalar(t, p, "SELECT ROUTINE_DEFINITION FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='mexist' AND ROUTINE_NAME='p_touch'"), "destino", "la rutina existente no se toca")
	require.Equal(t, "1", scalar(t, p, "SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='mexist' AND ROUTINE_NAME='fn_orders'"), "la que no existía sí se crea")

	req.Options.SkipExisting = true
	prep, err = migrate.Plan(ctx, migrate.PlanDeps{Source: m, Target: m, SameSession: true}, req)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"p_touch": "different", "fn_orders": "same"}, compare(prep), "la copiada en la primera pasada es idéntica")
}
