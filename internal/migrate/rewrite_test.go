package migrate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/sqllex"
)

var known = map[string]bool{"orders": true, "customers": true, "audit": true, "calc": true}

func TestRewriteSchemaIdentifiersOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		in, want string
		warns    []string
	}{
		"vista": {
			"CREATE VIEW `v` AS select `src`.`orders`.`id` AS `id` from `src`.`orders` join `src`.`customers` on 1=1",
			"CREATE VIEW `v` AS select `dst`.`orders`.`id` AS `id` from `dst`.`orders` join `dst`.`customers` on 1=1", nil,
		},
		"sin comillas": {"SELECT * FROM src.orders", "SELECT * FROM `dst`.orders", nil},
		"no toca cadenas ni comentarios": {
			"SELECT 'src.orders' /* src.orders */ -- src.orders\n, `src`.`orders`.id FROM `src`.`orders`",
			"SELECT 'src.orders' /* src.orders */ -- src.orders\n, `dst`.`orders`.id FROM `dst`.`orders`",
			[]string{"string_mentions_source"},
		},
		"otro schema se queda":      {"SELECT * FROM `other`.`orders` JOIN `src`.`customers`", "SELECT * FROM `other`.`orders` JOIN `dst`.`customers`", nil},
		"alias igual al schema":     {"SELECT src.id FROM orders AS src", "SELECT src.id FROM orders AS src", nil},
		"objeto desconocido":        {"SELECT * FROM `src`.`nope`", "SELECT * FROM `src`.`nope`", nil},
		"segundo nivel":             {"SELECT * FROM `x`.`src`.`orders`", "SELECT * FROM `x`.`src`.`orders`", nil},
		"sql dinámico":              {"BEGIN PREPARE s FROM 'SELECT 1'; EXECUTE s; END", "BEGIN PREPARE s FROM 'SELECT 1'; EXECUTE s; END", []string{"dynamic_sql"}},
		"cadena que cita el schema": {"SELECT 'copiar de src ahora'", "SELECT 'copiar de src ahora'", []string{"string_mentions_source"}},
		"no es palabra completa":    {"SELECT 'srcx'", "SELECT 'srcx'", nil},
		"backtick doble":            {"SELECT * FROM `src`.`orders` WHERE `a``b` = 1", "SELECT * FROM `dst`.`orders` WHERE `a``b` = 1", nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := RewriteSchema(tc.in, "src", "dst", known)
			require.Equal(t, tc.want, got.SQL)
			require.Equal(t, tc.warns, got.Warnings)
		})
	}
}

func TestRewriteDestinationNeedingQuotes(t *testing.T) {
	got := RewriteSchema("SELECT * FROM `src`.`orders`", "src", "mi base`x", known)
	require.Equal(t, "SELECT * FROM `mi base``x`.`orders`", got.SQL)
}

func TestStripDefiner(t *testing.T) {
	for name, tc := range map[string]struct{ in, sql, definer string }{
		"vista":             {"CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW `v` AS select 1", "CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select 1", "`root`@`localhost`"},
		"rutina":            {"CREATE DEFINER=`app`@`%` PROCEDURE `p`() BEGIN END", "CREATE PROCEDURE `p`() BEGIN END", "`app`@`%`"},
		"current":           {"CREATE DEFINER = CURRENT_USER() TRIGGER t BEFORE INSERT ON x FOR EACH ROW SET NEW.a = 1", "CREATE TRIGGER t BEFORE INSERT ON x FOR EACH ROW SET NEW.a = 1", "CURRENT_USER()"},
		"sin":               {"CREATE PROCEDURE `p`() BEGIN END", "CREATE PROCEDURE `p`() BEGIN END", ""},
		"solo SQL SECURITY": {"CREATE PROCEDURE p() SQL SECURITY DEFINER BEGIN END", "CREATE PROCEDURE p() SQL SECURITY DEFINER BEGIN END", ""},
		"en una cadena":     {"CREATE PROCEDURE p() BEGIN SELECT 'DEFINER=`x`@`y`'; END", "CREATE PROCEDURE p() BEGIN SELECT 'DEFINER=`x`@`y`'; END", ""},
	} {
		t.Run(name, func(t *testing.T) {
			sql, def := StripDefiner(tc.in)
			require.Equal(t, tc.sql, sql)
			require.Equal(t, tc.definer, def)
		})
	}
}

// El lexer conserva todos los bytes y la reescritura nunca cambia lo que hay dentro de cadenas o comentarios.
func FuzzRewriteKeepsLiterals(f *testing.F) {
	for _, s := range []string{"SELECT 'a' FROM `src`.`orders`", "/* `src`.`orders` */ SELECT 1", "SELECT \"src\".orders", "-- x\nSELECT '\\'' FROM src.orders", "`a"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		var b strings.Builder
		for _, tk := range sqllex.Lex(in) {
			b.WriteString(in[tk.Start:tk.End])
		}
		require.Equal(t, in, b.String(), "el lexer pierde bytes")
		out := RewriteSchema(in, "src", "dst", known).SQL
		// Lo que hay entre comillas simples o en comentarios sigue idéntico.
		strs := func(s string) []string {
			var r []string
			for _, tk := range sqllex.Lex(s) {
				if tk.Kind == sqllex.Str || (tk.Kind == sqllex.Other && strings.TrimSpace(s[tk.Start:tk.End]) != "") {
					r = append(r, s[tk.Start:tk.End])
				}
			}
			return r
		}
		require.Equal(t, strs(in), strs(out))
	})
}
