// Package logging configura slog con redacción de secretos (spec 10).
package logging

import (
	"io"
	"log/slog"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

const redacted = "***"

var secretKeys = []string{"password", "passphrase", "secret", "token", "apikey"}

func isSecretKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range secretKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// sqlMax es el tope de SQL en logs; el SQL completo no se registra salvo en debug y truncado.
const sqlMax = 200

func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	switch {
	case isSecretKey(a.Key):
		return slog.String(a.Key, redacted)
	case a.Key == "sql":
		s := a.Value.String()
		if len(s) > sqlMax {
			s = s[:sqlMax] + "…"
		}
		return slog.String(a.Key, s)
	}
	return a
}

// NewHandler escribe JSON en w con redacción aplicada.
func NewHandler(w io.Writer, level slog.Leveler) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: replaceAttr})
}

// Setup instala el logger por defecto escribiendo a un archivo rotado y devuelve su cierre.
func Setup(file string, level slog.Leveler) (io.Closer, error) {
	rot := &lumberjack.Logger{Filename: file, MaxSize: 5, MaxBackups: 3, MaxAge: 14}
	slog.SetDefault(slog.New(NewHandler(rot, level)))
	return rot, nil
}
