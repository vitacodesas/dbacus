// Package migrate orquesta la copia de un schema a otro (spec 23, ADR-008). No conoce el SQL de ningún motor: pide al driver
// (driver.SchemaMigrator) inspeccionar, crear y copiar, y se ocupa de las goroutines, los trozos, los reintentos, la
// cancelación y el progreso. Aquí solo hay SQL en la reescritura de vistas, rutinas y triggers (identificadores con backticks).
package migrate

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vitacodesas/dbacus/internal/sqllex"
)

func quote(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

// RewriteResult es el SQL reescrito y lo que el usuario debe saber.
type RewriteResult struct {
	SQL      string
	Replaced int      // referencias al schema origen cambiadas al destino
	Warnings []string // códigos: dynamic_sql, string_mentions_source
}

// RewriteSchema cambia `origen`.objeto por `destino`.objeto solo en identificadores (nunca en cadenas ni comentarios) y solo
// cuando objeto es un nombre que existe en el origen (known): una tabla con alias igual al schema (`origen.columna`) no se toca.
// Las referencias a otros schemas no se tocan. Avisa de PREPARE/EXECUTE y de cadenas que citan el schema origen (no se reescriben).
func RewriteSchema(sql, src, dst string, known map[string]bool) RewriteResult {
	toks := sqllex.Lex(sql)
	var b strings.Builder
	res := RewriteResult{}
	dyn, mention := false, false
	prevSig := func(i int) int { // índice del token significativo anterior
		for j := i - 1; j >= 0; j-- {
			if toks[j].Kind != sqllex.Other {
				return j
			}
		}
		return -1
	}
	nextSig := func(i int) int {
		for j := i + 1; j < len(toks); j++ {
			if toks[j].Kind != sqllex.Other {
				return j
			}
		}
		return -1
	}
	for i, t := range toks {
		out := sql[t.Start:t.End]
		switch {
		case t.IsName() && t.Text == src:
			if p := prevSig(i); p >= 0 && toks[p].Kind == sqllex.Sym && toks[p].Text == "." {
				break // `x`.`origen`: es el segundo nivel, no un schema
			}
			d := nextSig(i)
			if d >= 0 && toks[d].Kind == sqllex.Sym && toks[d].Text == "." {
				if o := nextSig(d); o >= 0 && toks[o].IsName() && known[toks[o].Text] {
					out = quote(dst)
					res.Replaced++
				}
			}
		case t.Kind == sqllex.Word && (strings.EqualFold(t.Text, "PREPARE") || strings.EqualFold(t.Text, "EXECUTE")):
			dyn = true
		case t.Kind == sqllex.Str && src != "" && mentions(t.Text, src):
			mention = true
		}
		b.WriteString(out)
	}
	res.SQL = b.String()
	if dyn {
		res.Warnings = append(res.Warnings, "dynamic_sql")
	}
	if mention {
		res.Warnings = append(res.Warnings, "string_mentions_source")
	}
	return res
}

// mentions dice si s contiene name como palabra completa (sin letras, dígitos ni _ a los lados).
func mentions(s, name string) bool {
	for from := 0; ; {
		i := strings.Index(s[from:], name)
		if i < 0 {
			return false
		}
		i += from
		before, after := i == 0, i+len(name) >= len(s)
		if !before {
			r, _ := utf8.DecodeLastRuneInString(s[:i])
			before = !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
		}
		if !after {
			r, _ := utf8.DecodeRuneInString(s[i+len(name):])
			after = !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
		}
		if before && after {
			return true
		}
		from = i + len(name)
	}
}

// StripDefiner quita la cláusula `DEFINER = usuario@host` (spec 23 ⚑D15) y devuelve el definidor original ("" si no había).
// No toca `SQL SECURITY DEFINER`: solo `DEFINER` seguido de `=`.
func StripDefiner(sql string) (string, string) {
	toks := sqllex.Lex(sql)
	for i, t := range toks {
		if t.Kind != sqllex.Word || !strings.EqualFold(t.Text, "DEFINER") {
			continue
		}
		j := i + 1
		for j < len(toks) && toks[j].Kind == sqllex.Other {
			j++
		}
		if j >= len(toks) || toks[j].Kind != sqllex.Sym || toks[j].Text != "=" {
			continue
		}
		j++
		for j < len(toks) && toks[j].Kind == sqllex.Other {
			j++
		}
		if j >= len(toks) {
			return sql, ""
		}
		end := j
		switch {
		case toks[j].Kind == sqllex.Word && strings.EqualFold(toks[j].Text, "CURRENT_USER"):
			end = j
			if k := j + 1; k+1 < len(toks) && toks[k].Kind == sqllex.Sym && toks[k].Text == "(" && toks[k+1].Kind == sqllex.Sym && toks[k+1].Text == ")" {
				end = k + 1
			}
		case toks[j].IsName() || toks[j].Kind == sqllex.Str:
			end = j
			if k := j + 1; k < len(toks) && toks[k].Kind == sqllex.Sym && toks[k].Text == "@" {
				if m := k + 1; m < len(toks) && (toks[m].IsName() || toks[m].Kind == sqllex.Str) {
					end = m
				} else if m < len(toks) && toks[m].Kind == sqllex.Sym && toks[m].Text == "%" {
					end = m
				}
			}
		default:
			return sql, ""
		}
		definer := sql[toks[i].Start:toks[end].End]
		definer = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(definer[len("DEFINER"):]), "="))
		cut := sql[:toks[i].Start] + strings.TrimLeft(sql[toks[end].End:], " \t")
		return cut, definer
	}
	return sql, ""
}
