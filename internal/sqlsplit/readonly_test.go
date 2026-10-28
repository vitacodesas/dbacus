package sqlsplit

import "testing"

func TestIsReadOnlySafe(t *testing.T) {
	cases := map[string]bool{
		"SELECT * FROM t":                        true,
		"  select 1":                             true,
		"/* c */ SELECT 1":                       true,
		"(SELECT 1) UNION (SELECT 2)":            true,
		"WITH c AS (SELECT 1) SELECT * FROM c":   true,
		"SHOW TABLES":                            true,
		"DESCRIBE t":                             true,
		"EXPLAIN SELECT 1":                       true,
		"USE app":                                true,
		"SELECT 'INTO' AS x, `into` FROM t":      true, // INTO dentro de string o identificador
		"SELECT * FROM t -- INTO OUTFILE":        true,
		"SELECT (SELECT 1) AS a":                 true,
		"DROP TABLE t":                           false,
		"drop table t":                           false,
		"/* SELECT */ DROP TABLE t":              false,
		"INSERT INTO t VALUES (1)":               false,
		"UPDATE t SET a = 1":                     false,
		"DELETE FROM t":                          false,
		"TRUNCATE t":                             false,
		"CREATE TABLE t (a INT)":                 false,
		"CALL p()":                               false,
		"SET SESSION TRANSACTION READ WRITE":     false, // reabriría la escritura
		"START TRANSACTION READ WRITE":           false,
		"SELECT * FROM t INTO OUTFILE '/tmp/x'":  false,
		"SELECT 1 INTO DUMPFILE '/tmp/x'":        false,
		"SELECT * FROM (SELECT 1) s INTO @v":     false,
		"":                                       false,
		"-- solo un comentario":                  false,
		"REPLACE INTO t VALUES (1)":              false,
		"LOAD DATA INFILE '/tmp/x' INTO TABLE t": false,
		"GRANT ALL ON *.* TO 'x'":                false,
		"FLUSH TABLES":                           false,
	}
	for sql, want := range cases {
		if got := IsReadOnlySafe(sql); got != want {
			t.Errorf("IsReadOnlySafe(%q) = %v, quiero %v", sql, got, want)
		}
	}
}
