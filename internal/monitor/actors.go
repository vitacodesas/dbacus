package monitor

import (
	"net"
	"strconv"
	"strings"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/store"
)

// internalUsers son hilos del servidor, no de una persona o aplicación (spec 22 ⚑D17).
var internalUsers = map[string]bool{"system user": true, "event_scheduler": true, "unauthenticated user": true}

// NormalizeHost quita el puerto de HOST de PROCESSLIST (`192.168.1.5:53422` → `192.168.1.5`). Un nombre sin puerto se respeta.
// [Suposición] MySQL muestra IPv6 como `::1:53422` o `[::1]:53422`; solo se recorta el último `:dígitos` si lo que queda sigue siendo una IP.
func NormalizeHost(h string) string {
	h = strings.TrimSpace(h)
	if strings.HasPrefix(h, "[") {
		if host, _, err := net.SplitHostPort(h); err == nil {
			return host
		}
		return h
	}
	i := strings.LastIndexByte(h, ':')
	if i <= 0 || i == len(h)-1 {
		return h
	}
	if _, err := strconv.ParseUint(h[i+1:], 10, 16); err != nil {
		return h
	}
	rest := h[:i]
	if strings.Contains(rest, ":") && net.ParseIP(rest) == nil {
		return h
	}
	return rest
}

// ActorsOf devuelve, sin repetidos, quién ejecutaba cada digest en esta lectura del processlist. Los procesos sin digest
// (hilos dormidos, servidores sin STATEMENT_DIGEST), los hilos internos y los de host vacío no cuentan. Solo salen el hash,
// el usuario y el host: INFO no sale de aquí (⚑D7).
func ActorsOf(rows []model.Process) []store.ActorSeen {
	var out []store.ActorSeen
	seen := map[store.ActorSeen]bool{}
	for _, p := range rows {
		if p.Digest == "" || p.User == "" || internalUsers[strings.ToLower(p.User)] {
			continue
		}
		host := NormalizeHost(p.Host)
		if host == "" {
			continue
		}
		a := store.ActorSeen{Digest: p.Digest, User: p.User, Host: host}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}
