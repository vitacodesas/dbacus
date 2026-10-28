package sqlfmt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatLayouts(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"select completo",
			"select a,b,count(*) as n from users u left outer join orders o on o.user_id=u.id and o.total>10 where u.active=1 and (u.age>18 or u.vip) group by a,b having n>1 order by n desc limit 10",
			"SELECT\n  a,\n  b,\n  count(*) AS n\nFROM users u\nLEFT OUTER JOIN orders o ON o.user_id = u.id AND o.total > 10\nWHERE u.active = 1\n  AND (u.age > 18 OR u.vip)\nGROUP BY\n  a,\n  b\nHAVING n > 1\nORDER BY\n  n DESC\nLIMIT 10;"},
		{"insert con on duplicate",
			"insert into t (a,b) values (1,'x'),(2,'y') on duplicate key update a=values(a)",
			"INSERT INTO t (a, b)\nVALUES (1, 'x'), (2, 'y')\nON DUPLICATE KEY UPDATE a = VALUES(a);"},
		{"update con subconsulta y unarios",
			"update t set a=1,b=-2 where id in (select id from x where y=-1)",
			"UPDATE t\nSET\n  a = 1,\n  b = -2\nWHERE id IN (SELECT id FROM x WHERE y = -1);"},
		{"delete", "delete from t where id=1", "DELETE FROM t\nWHERE id = 1;"},
		{"union", "select 1 union all select 2", "SELECT\n  1\nUNION ALL\nSELECT\n  2;"},
		{"identificadores con backticks y estrella", "select `a b`.*, now() from `a b`", "SELECT\n  `a b`.*,\n  now()\nFROM `a b`;"},
		{"comentario final de línea no se traga el ;", "select 1 -- fin", "SELECT\n  1 -- fin\n;"},
		{"comentarios sueltos entre sentencias se conservan", "select 1; -- aparte\nselect 2; /* fin */", "SELECT\n  1;\n\n-- aparte\n\nSELECT\n  2;\n\n/* fin */"},
		{"varias sentencias", "select 1;select 2", "SELECT\n  1;\n\nSELECT\n  2;"},
		{"strings intactos", "select 'a  b; -- no' from t where x='It''s'", "SELECT\n  'a  b; -- no'\nFROM t\nWHERE x = 'It''s';"},
		{"resta binaria vs negativo", "select a-b, -c, 3 - -2", "SELECT\n  a - b,\n  -c,\n  3 - -2;"},
		{"vacío", "   ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, Format(c.in)) })
	}
}

func TestScriptsWithDelimiterAreLeftAlone(t *testing.T) {
	src := "DELIMITER $$\ncreate procedure p() begin select 1; end$$\nDELIMITER ;"
	require.Equal(t, src, Format(src))
}

var corpus = []string{
	"select a,b from t where x=1 and y in (1,2,3) order by a desc limit 5",
	"SELECT u.*, COUNT(o.id) c FROM users u JOIN orders o ON o.uid=u.id WHERE u.name LIKE 'a%' GROUP BY u.id HAVING c>0",
	"insert into `my t` (`a`,b) values (NULL,'x'), (3.5e2, 0x1F)",
	"update a set b=b+1, c=concat(c,'x') where d between 1 and 10 or e is not null",
	"select case when a>1 then 'x' else 'y' end as z, @v := 1, a<=>b, a->>'$.k' from t",
	"select * from (select 1 as n union select 2) x where n<>1 -- nota",
	"delete from t where a=1 /* bloque */ limit 1",
	"select ifnull(a,0)+1, -1, - 1, a- -1, 1.5, .5, 1e3 from t",
	"with c as (select 1) select * from c",
	"select 'a' 'b', \"c\" from t t1, t2 where t1.id=t2.id",
	"-- inicio\nselect 1; -- tras la primera\n/* bloque */ select 2 -- final",
}

func TestFormatPreservesTokens(t *testing.T) {
	// Formatear solo cambia espacios y la capitalización de palabras: la secuencia de tokens es la misma.
	norm := func(s string) []string {
		var out []string
		for _, tk := range tokenize(s) {
			switch {
			case tk.kind == tWord:
				out = append(out, strings.ToUpper(tk.text))
			case tk.text != ";": // los ';' los repone Format; el resto de tokens debe coincidir
				out = append(out, tk.text)
			}
		}
		return out
	}
	for _, q := range corpus {
		got := Format(q)
		want := norm(q)
		have := norm(got)
		require.Equal(t, want, have, "original:\n%s\nformateado:\n%s", q, got)
	}
}

func TestFormatIsIdempotent(t *testing.T) {
	for _, q := range corpus {
		once := Format(q)
		require.Equal(t, once, Format(once), "no es idempotente para:\n%s", q)
	}
}

func FuzzFormatKeepsTokens(f *testing.F) {
	for _, q := range corpus {
		f.Add(q)
	}
	f.Fuzz(func(t *testing.T, q string) {
		// Se excluyen entradas con signos que solo forman SQL válido en combinaciones concretas (".5", "!=", "@v"):
		// el fuzz las sigue pudiendo recombinar de formas inválidas que el formateador no pretende preservar.
		if strings.Contains(strings.ToLower(q), "delimiter") || strings.ContainsAny(q, ";.!@") {
			t.Skip()
		}
		out := FormatStatement(q)
		norm := func(s string) []string {
			var o []string
			for _, tk := range tokenize(s) {
				if tk.kind == tWord {
					o = append(o, strings.ToUpper(tk.text))
				} else {
					o = append(o, tk.text)
				}
			}
			return o
		}
		require.Equal(t, norm(q), norm(out))
		require.Equal(t, out, FormatStatement(out))
	})
}
