package queryfiles

import (
	"sort"
	"strings"
)

const headerPrefix = "-- @dbacus "

// maxHeaderLine evita interpretar como cabecera una primera línea gigante.
const maxHeaderLine = 1024

// Meta es la conexión asociada a una consulta, guardada en la primera línea del archivo (spec 20 ⚑D5).
type Meta struct {
	ProfileID   string
	ProfileName string
	Database    string
	// Extra conserva las claves que esta versión no conoce, para no perderlas al reescribir.
	Extra map[string]string
}

func (m Meta) empty() bool {
	return m.ProfileID == "" && m.ProfileName == "" && m.Database == "" && len(m.Extra) == 0
}

// splitHeader separa la cabecera del cuerpo. body usa los mismos saltos de línea que s.
// Devuelve ok=false si no hay cabecera (entonces body == s).
func splitHeader(s string) (m Meta, body string, ok bool) {
	rest := s
	// Líneas en blanco iniciales no cuentan: la cabecera es la primera línea no vacía.
	for {
		nl := strings.IndexByte(rest, '\n')
		line := rest
		if nl >= 0 {
			line = rest[:nl]
		}
		if strings.TrimSpace(line) != "" {
			break
		}
		if nl < 0 {
			return Meta{}, s, false
		}
		rest = rest[nl+1:]
	}
	nl := strings.IndexByte(rest, '\n')
	line, after := rest, ""
	if nl >= 0 {
		line, after = rest[:nl], rest[nl+1:]
	}
	line = strings.TrimSuffix(line, "\r")
	if len(line) > maxHeaderLine || !strings.HasPrefix(line, headerPrefix) {
		return Meta{}, s, false
	}
	kv := parsePairs(line[len(headerPrefix):])
	for k, v := range kv {
		switch k {
		case "profile":
			m.ProfileID = v
		case "name":
			m.ProfileName = v
		case "database":
			m.Database = v
		default:
			if m.Extra == nil {
				m.Extra = map[string]string{}
			}
			m.Extra[k] = v
		}
	}
	// Se consume una sola línea en blanco tras la cabecera (la que escribe formatHeader).
	if strings.HasPrefix(after, "\r\n") {
		after = after[2:]
	} else if strings.HasPrefix(after, "\n") {
		after = after[1:]
	}
	return m, after, true
}

// parsePairs lee `clave=valor` o `clave="valor con \"escapes\""` separados por espacios.
func parsePairs(s string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(s) {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		start := i
		for i < len(s) && s[i] != '=' && s[i] != ' ' {
			i++
		}
		key := s[start:i]
		if i >= len(s) || s[i] != '=' || key == "" {
			continue // palabra suelta: se ignora
		}
		i++
		var val strings.Builder
		if i < len(s) && s[i] == '"' {
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				val.WriteByte(s[i])
				i++
			}
			i++ // comilla de cierre
		} else {
			for i < len(s) && s[i] != ' ' {
				val.WriteByte(s[i])
				i++
			}
		}
		out[key] = val.String()
	}
	return out
}

// formatHeader devuelve la línea de cabecera sin salto final ("" si no hay nada que escribir).
func formatHeader(m Meta) string {
	if m.empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(headerPrefix, " "))
	add := func(k, v string) {
		if v == "" {
			return
		}
		b.WriteString(" " + k + "=" + quote(v))
	}
	add("profile", m.ProfileID)
	add("name", m.ProfileName)
	add("database", m.Database)
	keys := make([]string, 0, len(m.Extra))
	for k := range m.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(k, m.Extra[k])
	}
	return b.String()
}

// quote escribe el valor sin comillas si es "simple"; si no, entre comillas con `\` y `"` escapados.
// Los caracteres de control se sustituyen por espacios: un salto de línea rompería la cabecera.
func quote(v string) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, v)
	if v != "" && !strings.ContainsAny(v, ` "\=`) {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}
