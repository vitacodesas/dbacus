package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// fakeServer imita un servidor que no conoce ciertos modos (error 1231, como un 8.x ante NO_AUTO_CREATE_USER).
func fakeServer(unknown ...string) func(context.Context, string) error {
	return func(_ context.Context, stmt string) error {
		v := strings.TrimSuffix(strings.TrimPrefix(stmt, "SET SESSION sql_mode='"), "'")
		for _, m := range strings.Split(v, ",") {
			for _, u := range unknown {
				if m == u {
					return &gomysql.MySQLError{Number: 1231, Message: "Variable 'sql_mode' can't be set to the value of '" + u + "'"}
				}
			}
		}
		return nil
	}
}

func TestCompatibleModesDropsUnknownModes(t *testing.T) {
	list := "ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_AUTO_CREATE_USER,NO_ENGINE_SUBSTITUTION"
	require.Equal(t, "ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION", compatibleModes(context.Background(), fakeServer("NO_AUTO_CREATE_USER"), list))
	require.Equal(t, list, compatibleModes(context.Background(), fakeServer(), list), "un servidor que los acepta todos no cambia nada")
}

func TestCompatibleModesKeepsListOnOtherErrors(t *testing.T) {
	boom := func(context.Context, string) error { return errors.New("conexión caída") }
	require.Equal(t, "A,B", compatibleModes(context.Background(), boom, "A,B"), "solo 1231 descarta modos")
}
