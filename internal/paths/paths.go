// Package paths resuelve dónde vive el estado local de la app (spec 01).
package paths

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const appDir = "Dbacus"

// legacyAppDir es el nombre previo al renombrado (spec 15 §A); solo se lee para migrar.
const legacyAppDir = "Tabularis-Clon"

// migratedMarker evita volver a migrar aunque el usuario borre luego app.db.
const migratedMarker = "migrated-from-legacy"

// EnvHome permite aislar todo el estado en una carpeta (tests, depuración).
const EnvHome = "DBACUS_HOME"

type Paths struct {
	Config string // app.db, known_hosts
	Logs   string
	// Queries es la carpeta de consultas guardadas por defecto (spec 20); se crea al primer uso, no aquí.
	Queries string

	// MigratedFrom no está vacío si en este arranque se copió el estado de la carpeta antigua.
	MigratedFrom string
	// MigrationErr es un fallo de la migración; no impide arrancar, pero el llamador debe registrarlo.
	MigrationErr error
}

// Resolve calcula las rutas y crea los directorios.
func Resolve() (Paths, error) {
	var p Paths
	if home := os.Getenv(EnvHome); home != "" {
		p = Paths{Config: filepath.Join(home, "config"), Logs: filepath.Join(home, "logs"), Queries: filepath.Join(home, "queries")}
	} else {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, fmt.Errorf("config dir: %w", err)
		}
		cache, err := os.UserCacheDir()
		if err != nil {
			return Paths{}, fmt.Errorf("cache dir: %w", err)
		}
		p = Paths{Config: filepath.Join(cfg, appDir), Logs: filepath.Join(cache, appDir, "logs"), Queries: defaultQueries()}
	}
	for _, d := range []string{p.Config, p.Logs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return Paths{}, fmt.Errorf("crear %s: %w", d, err)
		}
	}
	if os.Getenv(EnvHome) == "" {
		// Solo con la ubicación del SO: con DBACUS_HOME el estado está aislado a propósito.
		cfg, _ := os.UserConfigDir()
		p.MigratedFrom, p.MigrationErr = migrateLegacy(filepath.Join(cfg, legacyAppDir), p.Config)
	}
	return p, nil
}

// migrateLegacy copia (nunca mueve ni borra) el estado de la carpeta antigua si la nueva aún no tiene datos.
func migrateLegacy(oldDir, newDir string) (string, error) {
	if _, err := os.Stat(filepath.Join(newDir, migratedMarker)); err == nil {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(newDir, "app.db")); err == nil {
		return "", nil // ya hay datos nuevos: no se pisan
	}
	if _, err := os.Stat(filepath.Join(oldDir, "app.db")); err != nil {
		return "", nil // instalación limpia
	}
	// -wal/-shm solo existen si la app antigua no se cerró limpia; sin ellos SQLite perdería esas escrituras.
	names := []string{"app.db", "app.db-wal", "app.db-shm", "known_hosts"}
	for _, name := range names {
		if err := copyIfExists(filepath.Join(oldDir, name), filepath.Join(newDir, name)); err != nil {
			// Sin copias parciales: el siguiente arranque reintenta desde cero.
			for _, n := range names {
				_ = os.Remove(filepath.Join(newDir, n))
			}
			return "", fmt.Errorf("migrar %s: %w", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(newDir, migratedMarker), []byte(oldDir+"\n"), 0o600); err != nil {
		return oldDir, fmt.Errorf("marcador de migración: %w", err)
	}
	return oldDir, nil
}

func copyIfExists(src, dst string) error {
	in, err := os.Open(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func (p Paths) DB() string         { return filepath.Join(p.Config, "app.db") }
func (p Paths) KnownHosts() string { return filepath.Join(p.Config, "known_hosts") }
func (p Paths) LogFile() string    { return filepath.Join(p.Logs, "app.log") }

// defaultQueries es Documentos/Dbacus/Consultas. Sigue XDG_DOCUMENTS_DIR si está definida; no se consulta
// la carpeta Documentos redirigida de Windows (OneDrive): [no verificado] en ese caso.
func defaultQueries() string {
	docs := os.Getenv("XDG_DOCUMENTS_DIR")
	if docs == "" || !filepath.IsAbs(docs) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		docs = filepath.Join(home, "Documents")
	}
	return filepath.Join(docs, appDir, "Consultas")
}
