package sqlsplit

import "testing"

func TestParseRoutineHead(t *testing.T) {
	cases := []struct {
		name, sql            string
		ok                   bool
		kind, rname, db, def string
		ifNotExists          bool
		nameText             string // lo que hay entre NameStart y NameEnd
	}{
		{"simple", "CREATE PROCEDURE p() BEGIN SELECT 1; END", true, "procedure", "p", "", "", false, "p"},
		{"function", "create function f(a int) returns int return a", true, "function", "f", "", "", false, "f"},
		{"backticks", "CREATE PROCEDURE `mi proc`() SELECT 1", true, "procedure", "mi proc", "", "", false, "`mi proc`"},
		{"backtick doblado", "CREATE PROCEDURE `a``b`() SELECT 1", true, "procedure", "a`b", "", "", false, "`a``b`"},
		{"con base", "CREATE PROCEDURE shop.p() SELECT 1", true, "procedure", "p", "shop", "", false, "p"},
		{"base entre backticks", "CREATE PROCEDURE `my db`.`p` () SELECT 1", true, "procedure", "p", "my db", "", false, "`p`"},
		{"definer entre comillas", "CREATE DEFINER='root'@'localhost' PROCEDURE p() SELECT 1", true, "procedure", "p", "", "'root'@'localhost'", false, "p"},
		{"definer con espacios", "CREATE DEFINER = `a b`@`h` FUNCTION f() RETURNS INT RETURN 1", true, "function", "f", "", "`a b`@`h`", false, "f"},
		{"definer current_user", "CREATE DEFINER=CURRENT_USER() PROCEDURE p() SELECT 1", true, "procedure", "p", "", "CURRENT_USER()", false, "p"},
		{"if not exists", "CREATE PROCEDURE IF NOT EXISTS p() SELECT 1", true, "procedure", "p", "", "", true, "p"},
		{"comentarios previos", "-- hola\n/* x */ # y\nCREATE /* z */ PROCEDURE p() SELECT 1", true, "procedure", "p", "", "", false, "p"},
		{"or replace", "CREATE OR REPLACE PROCEDURE p() SELECT 1", false, "", "", "", "", false, ""},
		{"aggregate", "CREATE AGGREGATE FUNCTION f() RETURNS INT", false, "", "", "", "", false, ""},
		{"tabla", "CREATE TABLE t (id int)", false, "", "", "", "", false, ""},
		{"drop", "DROP PROCEDURE p", false, "", "", "", "", false, ""},
		{"vacío", "", false, "", "", "", "", false, ""},
		{"sin nombre", "CREATE PROCEDURE ()", false, "", "", "", "", false, ""},
		{"backtick sin cerrar", "CREATE PROCEDURE `p() SELECT 1", false, "", "", "", "", false, ""},
		{"definer sin igual", "CREATE DEFINER root PROCEDURE p() SELECT 1", false, "", "", "", "", false, ""},
		{"if sin not", "CREATE PROCEDURE IF p() SELECT 1", false, "", "", "", "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := ParseRoutineHead(c.sql)
			if h.OK != c.ok {
				t.Fatalf("OK = %v, quería %v (%+v)", h.OK, c.ok, h)
			}
			if !c.ok {
				return
			}
			if h.Kind != c.kind || h.Name != c.rname || h.Database != c.db || h.Definer != c.def || h.IfNotExists != c.ifNotExists {
				t.Fatalf("cabecera = %+v", h)
			}
			if got := c.sql[h.NameStart:h.NameEnd]; got != c.nameText {
				t.Fatalf("span del nombre = %q, quería %q", got, c.nameText)
			}
			if c.def != "" {
				if got := c.sql[h.DefinerStart:h.DefinerEnd]; got[:7] != "DEFINER" {
					t.Fatalf("span del definer = %q", got)
				}
			} else if h.DefinerStart != -1 {
				t.Fatalf("DefinerStart = %d sin definer", h.DefinerStart)
			}
		})
	}
}

func TestIsRoutineDDL(t *testing.T) {
	yes := []string{
		"CREATE PROCEDURE p() SELECT 1", "create definer=x@y function f() returns int return 1",
		"DROP PROCEDURE IF EXISTS p", "drop function f", "ALTER PROCEDURE p COMMENT 'x'", "/* c */ DROP FUNCTION f",
	}
	no := []string{"CREATE TABLE t (a int)", "DROP TABLE t", "ALTER TABLE t ADD c int", "SELECT 1", "CALL p()", "CREATE VIEW v AS SELECT 1", ""}
	for _, s := range yes {
		if !IsRoutineDDL(s) {
			t.Errorf("IsRoutineDDL(%q) = false", s)
		}
	}
	for _, s := range no {
		if IsRoutineDDL(s) {
			t.Errorf("IsRoutineDDL(%q) = true", s)
		}
	}
}

func TestHasDelimiterLine(t *testing.T) {
	if !HasDelimiterLine("DELIMITER $$\nCREATE PROCEDURE p() BEGIN END$$\nDELIMITER ;") {
		t.Error("no detecta DELIMITER")
	}
	if !HasDelimiterLine("  delimiter //") {
		t.Error("no detecta delimiter en minúsculas con sangría")
	}
	if HasDelimiterLine("CREATE PROCEDURE p() BEGIN\n  SELECT 'delimiter x';\nEND") {
		t.Error("falso positivo dentro de un literal")
	}
}

func FuzzParseRoutineHead(f *testing.F) {
	for _, s := range []string{"CREATE PROCEDURE p() SELECT 1", "CREATE DEFINER='a'@'b' FUNCTION `x`.`y`()", "`", "CREATE DEFINER=", "/*"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		h := ParseRoutineHead(s)
		if h.OK && (h.NameStart < 0 || h.NameEnd > len(s) || h.NameStart >= h.NameEnd) {
			t.Fatalf("offsets inválidos %+v para %q", h, s)
		}
		_ = IsRoutineDDL(s)
		_ = HasDelimiterLine(s)
	})
}
