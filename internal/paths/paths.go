// Package paths resuelve dónde vive el estado local de la app (spec 01).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const appDir = "Tabularis-Clon"

// EnvHome permite aislar todo el estado en una carpeta (tests, depuración).
const EnvHome = "TABULARIS_CLON_HOME"

type Paths struct {
	Config string // app.db, known_hosts
	Logs   string
}

// Resolve calcula las rutas y crea los directorios.
func Resolve() (Paths, error) {
	var p Paths
	if home := os.Getenv(EnvHome); home != "" {
		p = Paths{Config: filepath.Join(home, "config"), Logs: filepath.Join(home, "logs")}
	} else {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, fmt.Errorf("config dir: %w", err)
		}
		cache, err := os.UserCacheDir()
		if err != nil {
			return Paths{}, fmt.Errorf("cache dir: %w", err)
		}
		p = Paths{Config: filepath.Join(cfg, appDir), Logs: filepath.Join(cache, appDir, "logs")}
	}
	for _, d := range []string{p.Config, p.Logs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return Paths{}, fmt.Errorf("crear %s: %w", d, err)
		}
	}
	return p, nil
}

func (p Paths) DB() string         { return filepath.Join(p.Config, "app.db") }
func (p Paths) KnownHosts() string { return filepath.Join(p.Config, "known_hosts") }
func (p Paths) LogFile() string    { return filepath.Join(p.Logs, "app.log") }
