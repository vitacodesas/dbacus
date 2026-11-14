package mysql

import (
	"strings"
	"sync"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/sqllex"
)

// writeGuard valida cada sentencia de la migración ANTES de enviarla (spec 23 ⚑D19). Es la garantía de «el origen nunca se escribe»
// en la ruta dentro del servidor, donde una sola conexión lee el origen y escribe el destino y por tanto no puede ser READ ONLY.
// Falla cerrado: lo que no reconoce no se envía. En la ruta por Go las conexiones del origen son además READ ONLY en el servidor.
type writeGuard struct {
	role string
	src  string // schema que se lee ("" si el rol no lo lee junto con el destino)
	dst  string // único schema en el que se puede escribir

	mu sync.Mutex
	ok map[string]struct{} // sentencias ya validadas: los INSERT por lotes repiten forma
}

func newWriteGuard(role, src, dst string) *writeGuard {
	return &writeGuard{role: role, src: src, dst: dst, ok: map[string]struct{}{}}
}

var (
	readVerbs  = map[string]bool{"SELECT": true, "SHOW": true}
	writeVerbs = map[string]bool{"INSERT": true, "CREATE": true, "ALTER": true, "DROP": true, "DELETE": true, "TRUNCATE": true}
)

func blocked(format string, args ...any) error {
	return apperr.Newf(apperr.QueryFailed, "Sentencia rechazada por el guardia de la migración: "+format, args...).WithDetail("source_write_blocked")
}

// check devuelve nil si la sentencia puede enviarse con el rol de la conexión.
func (g *writeGuard) check(stmt string) error {
	g.mu.Lock()
	_, seen := g.ok[stmt]
	g.mu.Unlock()
	if seen {
		return nil
	}
	if err := g.validate(stmt); err != nil {
		return err
	}
	g.mu.Lock()
	if len(g.ok) < 512 {
		g.ok[stmt] = struct{}{}
	}
	g.mu.Unlock()
	return nil
}

func (g *writeGuard) validate(stmt string) error {
	var toks []sqllex.Token
	for _, t := range sqllex.Lex(stmt) {
		if t.Kind != sqllex.Other {
			toks = append(toks, t)
		}
	}
	if len(toks) == 0 || toks[0].Kind != sqllex.Word {
		return blocked("sentencia vacía o sin verbo")
	}
	verb := strings.ToUpper(toks[0].Text)
	isRead, isWrite := readVerbs[verb], writeVerbs[verb]
	switch {
	case !isRead && !isWrite:
		return blocked("el verbo %s no está permitido", verb)
	case isWrite && g.role == driver.RoleSource:
		return blocked("las conexiones del origen solo leen (%s)", verb)
	}

	// Cuerpos de rutinas y triggers: llevan «;» y sentencias propias. El resto es de una sola sentencia.
	compound := false
	if verb == "CREATE" {
		for i := 1; i < len(toks) && i < 16; i++ {
			if toks[i].Kind == sqllex.Word {
				switch strings.ToUpper(toks[i].Text) {
				case "PROCEDURE", "FUNCTION", "TRIGGER":
					compound = true
				}
			}
		}
	}
	for i, t := range toks {
		if t.Kind == sqllex.Sym && t.Text == ";" && !(i == len(toks)-1) && !compound {
			return blocked("varias sentencias en una")
		}
	}

	// Lecturas con bloqueo y escrituras a disco.
	for i, t := range toks {
		if t.Kind != sqllex.Word {
			continue
		}
		w := strings.ToUpper(t.Text)
		next := ""
		if i+1 < len(toks) && toks[i+1].Kind == sqllex.Word {
			next = strings.ToUpper(toks[i+1].Text)
		}
		switch {
		case !compound && w == "FOR" && (next == "UPDATE" || next == "SHARE"),
			!compound && w == "LOCK" && next == "IN",
			!compound && w == "INTO" && (next == "OUTFILE" || next == "DUMPFILE"):
			return blocked("%s %s no está permitido", w, next)
		}
	}

	// El schema origen solo puede aparecer como lectura.
	if g.src != "" {
		sel := -1
		for i, t := range toks {
			if t.Kind == sqllex.Word && strings.EqualFold(t.Text, "SELECT") {
				sel = i
				break
			}
		}
		for i, t := range toks {
			if !t.IsName() || !strings.EqualFold(t.Text, g.src) || i+1 >= len(toks) || toks[i+1].Kind != sqllex.Sym || toks[i+1].Text != "." {
				continue
			}
			if i > 0 && toks[i-1].Kind == sqllex.Sym && toks[i-1].Text == "." {
				continue // segundo nivel de un nombre: `x`.`origen`.`y`
			}
			switch {
			case isRead:
			case verb == "INSERT" && sel >= 0 && i > sel: // INSERT … SELECT … FROM `origen`.`t`
			default:
				return blocked("una sentencia que escribe nombra el schema origen")
			}
		}
	}

	// Lo que se escribe va al destino: calificado con él, o sin calificar (la base por defecto de la conexión es siempre el destino).
	if isWrite && g.dst != "" {
		if q, ok := writeTargetQualifier(toks, verb); ok && q != g.dst {
			return blocked("escribe en %q y solo se puede escribir en %q", q, g.dst)
		}
	}
	return nil
}

// writeTargetQualifier devuelve el schema que califica el objeto que escribe la sentencia ("" si no está calificado).
func writeTargetQualifier(toks []sqllex.Token, verb string) (string, bool) {
	// Palabra tras la que viene el objeto.
	anchors := map[string]map[string]bool{
		"INSERT":   {"INTO": true},
		"DELETE":   {"FROM": true},
		"TRUNCATE": {"TABLE": true},
		"ALTER":    {"TABLE": true},
		"DROP":     {"TABLE": true, "VIEW": true, "PROCEDURE": true, "FUNCTION": true, "TRIGGER": true},
		"CREATE":   {"TABLE": true, "VIEW": true, "PROCEDURE": true, "FUNCTION": true, "TRIGGER": true},
	}[verb]
	skip := map[string]bool{"IF": true, "NOT": true, "EXISTS": true}
	for i, t := range toks {
		if t.Kind != sqllex.Word || !anchors[strings.ToUpper(t.Text)] {
			continue
		}
		j := i + 1
		for j < len(toks) && toks[j].Kind == sqllex.Word && skip[strings.ToUpper(toks[j].Text)] {
			j++
		}
		if j+1 < len(toks) && toks[j].IsName() && toks[j+1].Kind == sqllex.Sym && toks[j+1].Text == "." {
			return toks[j].Text, true
		}
		return "", false
	}
	return "", false
}
