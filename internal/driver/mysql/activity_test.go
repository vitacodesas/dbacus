package mysql

import (
	"errors"
	"strings"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

// Todo lo que el monitor consulta es de solo lectura (spec 22 ⚑D8). Los SET de preparación de la conexión fijan el modo
// de sesión (incluido READ ONLY) y no son consultas.
func TestMonitorSQLIsReadOnlySafe(t *testing.T) {
	status, err := statusQuery([]string{"Uptime", "Threads_running", "Com_select"})
	require.NoError(t, err)
	stmts := []string{
		sqlProcesslist, sqlProcesslistFallback, sqlConnectionID, sqlMaxConnections, sqlDigests, sqlVersion, sqlCurrentUser, sqlServerUUID,
		sqlServerID, sqlStatusProbe, sqlDistinctUsers, sqlHasProcessPriv, sqlPerfSchemaOn, sqlDigestProbe, sqlDigestConsumer,
		status, digestTextsQuery(1), digestTextsQuery(digestTextBatch),
	}
	for _, s := range stmts {
		require.True(t, sqlsplit.IsReadOnlySafe(s), s)
	}
	for _, s := range []string{sqlSetReadOnly, sqlSetTimeoutMySQL, sqlSetTimeoutMariaDB} {
		require.False(t, sqlsplit.IsReadOnlySafe(s), "los SET de preparación no son consultas: %s", s)
	}
}

func TestProcesslistQueryUsesExplicitColumns(t *testing.T) {
	require.NotContains(t, sqlProcesslist, "*")
	require.Contains(t, sqlProcesslist, "INFORMATION_SCHEMA.PROCESSLIST")
	require.Contains(t, sqlProcesslist, "ID <> CONNECTION_ID()")
	require.NotContains(t, sqlDigests, "DIGEST_TEXT", "el texto se pide aparte")
	require.NotContains(t, sqlDigests, "*")
}

func TestStatusQueryValidatesNames(t *testing.T) {
	q, err := statusQuery([]string{"Uptime", "Innodb_row_lock_waits"})
	require.NoError(t, err)
	require.Equal(t, "SHOW GLOBAL STATUS WHERE Variable_name IN ('Uptime', 'Innodb_row_lock_waits')", q)
	for _, bad := range []string{"", "a'b", "a b", "x);DROP", "1abc", "a-b"} {
		_, err := statusQuery([]string{"Uptime", bad})
		require.Error(t, err, bad)
	}
	_, err = statusQuery(nil)
	require.Error(t, err)
}

func TestDigestTextsQueryPlaceholders(t *testing.T) {
	require.Equal(t, 3, strings.Count(digestTextsQuery(3), "?"))
	require.Equal(t, 1, strings.Count(digestTextsQuery(1), "?"))
}

func TestTruncateUTF8(t *testing.T) {
	s, cut := truncateUTF8("abc", 10)
	require.Equal(t, "abc", s)
	require.False(t, cut)
	// «é» ocupa 2 bytes: cortar en medio no puede dejar un carácter partido.
	s, cut = truncateUTF8("aé", 2)
	require.Equal(t, "a", s)
	require.True(t, cut)
	s, _ = truncateUTF8(strings.Repeat("😀", 1000), infoMaxBytes)
	require.LessOrEqual(t, len(s), infoMaxBytes)
	require.Zero(t, len(s)%4)
}

func TestGrantTarget(t *testing.T) {
	require.Equal(t, "'app'@'10.0.%'", grantTarget("app@10.0.%"))
	require.Equal(t, "'o''brien'@'h'", grantTarget("o'brien@h"))
	require.Equal(t, "'a@b'@'host'", grantTarget("a@b@host"))
	require.Equal(t, "'solo'@'%'", grantTarget("solo"))
}

func TestPrivilegeErrorClassification(t *testing.T) {
	for _, n := range []uint16{1044, 1142, 1143, 1227, 1370} {
		require.True(t, isPrivilegeError(&gomysql.MySQLError{Number: n}), n)
	}
	require.False(t, isPrivilegeError(&gomysql.MySQLError{Number: 1045}), "1045 es autenticación")
	require.False(t, isPrivilegeError(errors.New("x")))
	require.True(t, isUnknownObject(&gomysql.MySQLError{Number: 1146}))

	hint := "GRANT SELECT ON performance_schema.* TO 'u'@'h';"
	require.Equal(t, "no_privilege", classify(&gomysql.MySQLError{Number: 1142}, hint).Reason)
	require.Equal(t, hint, classify(&gomysql.MySQLError{Number: 1142}, hint).Hint)
	require.Equal(t, "unsupported", classify(&gomysql.MySQLError{Number: 1146}, hint).Reason)
	require.Equal(t, "error", classify(errors.New("x"), hint).Reason)
}

func TestPoolImplementsActivityReader(t *testing.T) {
	var _ driver.ActivityReader = (*pool)(nil)
}
