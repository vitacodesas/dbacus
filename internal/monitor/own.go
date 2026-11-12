package monitor

import "strings"

// IsOwnStatement dice si el texto normalizado de un digest (DIGEST_TEXT) es de una sentencia fija del propio monitor
// (spec 22 ⚑D16). Compara sin espacios, sin comillas invertidas y en minúsculas, porque el servidor reformatea el texto
// (`SELECT `ID` , `USER` …`). Es una lista cerrada y deliberadamente estrecha: las consultas genéricas del Probe
// (`SELECT VERSION()`, `CURRENT_USER()`…) NO se filtran, porque un usuario real las ejecuta también. Límite conocido:
// otro cliente que ejecute exactamente la misma forma (p. ej. un exporter con `SHOW GLOBAL STATUS WHERE Variable_name IN`)
// tampoco aparece en el ranking.
func IsOwnStatement(text string) bool {
	n := normalizeDigestText(text)
	switch n {
	case "setsessiontransactionreadonly",
		"setsessionmax_execution_time=?",
		"setsessionmax_statement_time=?",
		"selectconnection_id()",
		"selectstatement_digest(?)", // sonda de ProcessDigest (spec 22 ⚑D17)
		"select@@global.max_connections":
		return true
	}
	switch {
	// Procesos: columnas y filtro exactos de la consulta del monitor.
	// La lista de columnas no se compara entera: el servidor reescribe palabras reservadas (USER sale como SYSTEM_USER).
	case strings.HasPrefix(n, "selectid,") && strings.Contains(n, "frominformation_schema.processlistwhereid") &&
		strings.Contains(n, "connection_id()"):
		return true
	// Estado global: la consulta con la lista IN (el servidor colapsa la lista en `(...)`).
	case strings.HasPrefix(n, "showglobalstatuswherevariable_namein"):
		return true
	// Digest: contadores (sin texto), texto por lotes y sonda.
	case strings.Contains(n, "events_statements_summary_by_digest") &&
		(strings.HasPrefix(n, "selectschema_name,digest,count_star,sum_timer_wait,max_timer_wait") ||
			strings.HasPrefix(n, "selectdigest,digest_textfrom") ||
			strings.HasPrefix(n, "select?from")):
		return true
	}
	return false
}

func normalizeDigestText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '\t', '\n', '\r', '`':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
