package sqlsplit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInjectLimit(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"simple", "SELECT * FROM t", "SELECT * FROM t LIMIT 100", true},
		{"minúsculas", "select id from t where x = 1", "select id from t where x = 1 LIMIT 100", true},
		{"sin FROM", "SELECT 1", "SELECT 1 LIMIT 100", true},
		{"comentario final de línea", "SELECT * FROM t -- todo", "SELECT * FROM t LIMIT 100 -- todo", true},
		{"comentario final de bloque", "SELECT * FROM t /* x */", "SELECT * FROM t LIMIT 100 /* x */", true},
		{"comentario inicial", "-- cabecera\nSELECT * FROM t", "-- cabecera\nSELECT * FROM t LIMIT 100", true},
		{"subconsulta con LIMIT dentro", "SELECT * FROM (SELECT * FROM t LIMIT 5) x", "SELECT * FROM (SELECT * FROM t LIMIT 5) x LIMIT 100", true},
		{"UNION dentro de paréntesis", "SELECT * FROM (SELECT 1 UNION SELECT 2) x", "SELECT * FROM (SELECT 1 UNION SELECT 2) x LIMIT 100", true},
		{"palabra en string no cuenta", "SELECT 'a limit b' FROM t", "SELECT 'a limit b' FROM t LIMIT 100", true},
		{"palabra en backticks no cuenta", "SELECT `limit`, `for` FROM t", "SELECT `limit`, `for` FROM t LIMIT 100", true},
		{"ya tiene LIMIT", "SELECT * FROM t LIMIT 10", "", false},
		{"LIMIT con offset", "SELECT * FROM t LIMIT 5, 10", "", false},
		{"LIMIT en minúsculas", "select * from t limit 10", "", false},
		{"UNION", "SELECT 1 UNION SELECT 2", "", false},
		{"INTO OUTFILE", "SELECT * FROM t INTO OUTFILE '/tmp/x'", "", false},
		{"FOR UPDATE", "SELECT * FROM t FOR UPDATE", "", false},
		{"LOCK IN SHARE MODE", "SELECT * FROM t LOCK IN SHARE MODE", "", false},
		{"WITH", "WITH c AS (SELECT 1) SELECT * FROM c", "", false},
		{"entre paréntesis", "(SELECT 1)", "", false},
		{"INSERT", "INSERT INTO t VALUES (1)", "", false},
		{"SHOW", "SHOW TABLES", "", false},
		{"vacío", "", "", false},
		{"solo comentario", "-- nada", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := InjectLimit(c.in, 100)
			require.Equal(t, c.ok, ok)
			if c.ok {
				require.Equal(t, c.want, got)
			} else {
				require.Equal(t, c.in, got, "sin cambios si no se aplica")
			}
		})
	}
}

func TestInjectLimitDisabledForZero(t *testing.T) {
	got, ok := InjectLimit("SELECT 1", 0)
	require.False(t, ok)
	require.Equal(t, "SELECT 1", got)
}
