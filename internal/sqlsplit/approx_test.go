package sqlsplit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrap(t *testing.T) {
	ok := []struct{ in, want string }{
		{"SELECT a.id FROM a JOIN b ON a.id = b.id;", "SELECT a.id FROM a JOIN b ON a.id = b.id"},
		{"  select 1 ; ", "select 1"},
		{"SELECT DISTINCT x FROM t ORDER BY x", "SELECT DISTINCT x FROM t ORDER BY x"},
		{"SELECT x FROM t UNION SELECT y FROM u", "SELECT x FROM t UNION SELECT y FROM u"},
		{"SELECT x FROM (SELECT x FROM t LIMIT 5) AS s", "SELECT x FROM (SELECT x FROM t LIMIT 5) AS s"}, // LIMIT de una subconsulta
		{"SELECT 'LIMIT' AS a FROM t", "SELECT 'LIMIT' AS a FROM t"},
		{"SELECT x FROM t -- nota", "SELECT x FROM t -- nota"},
	}
	for _, c := range ok {
		got, reason := Wrap(c.in)
		require.Empty(t, reason, c.in)
		require.Equal(t, c.want, got, c.in)
	}
	bad := []struct{ in, reason string }{
		{"SHOW TABLES", ReasonNotSelect},
		{"WITH c AS (SELECT 1) SELECT * FROM c", ReasonNotSelect},
		{"UPDATE t SET a = 1", ReasonNotSelect},
		{"SELECT SQL_CALC_FOUND_ROWS * FROM t", ReasonNotSelect},
		{"", ReasonNotSelect},
		{"SELECT * FROM t LIMIT 10", ReasonUserLimit},
		{"SELECT * FROM a JOIN b ON 1 LIMIT 5 OFFSET 2", ReasonUserLimit},
		{"SELECT * FROM t INTO OUTFILE '/tmp/x'", ReasonIntoLock},
		{"SELECT * FROM t FOR UPDATE", ReasonIntoLock},
		{"SELECT * FROM t LOCK IN SHARE MODE", ReasonIntoLock},
	}
	for _, c := range bad {
		_, reason := Wrap(c.in)
		require.Equal(t, c.reason, reason, c.in)
	}
}
