package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveWithHomeOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	p, err := Resolve()
	require.NoError(t, err)
	require.DirExists(t, p.Config)
	require.DirExists(t, p.Logs)
	require.Equal(t, filepath.Join(home, "config", "app.db"), p.DB())
	info, _ := os.Stat(p.Config)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

func TestMigrateLegacyCopiesAndKeepsOldData(t *testing.T) {
	root := t.TempDir()
	oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "new")
	writeFile(t, oldDir, "app.db", "DB")
	writeFile(t, oldDir, "known_hosts", "KH")
	require.NoError(t, os.MkdirAll(newDir, 0o700))

	from, err := migrateLegacy(oldDir, newDir)
	require.NoError(t, err)
	require.Equal(t, oldDir, from)
	require.FileExists(t, filepath.Join(newDir, migratedMarker))
	got, _ := os.ReadFile(filepath.Join(newDir, "app.db"))
	require.Equal(t, "DB", string(got))
	// No destructiva: lo antiguo sigue ahí por si hay que volver atrás.
	require.FileExists(t, filepath.Join(oldDir, "app.db"))
	require.NoFileExists(t, filepath.Join(newDir, "app.db-wal"))
}

func TestMigrateLegacyNeverOverwritesNewData(t *testing.T) {
	root := t.TempDir()
	oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "new")
	writeFile(t, oldDir, "app.db", "OLD")
	writeFile(t, newDir, "app.db", "NEW")

	from, err := migrateLegacy(oldDir, newDir)
	require.NoError(t, err)
	require.Empty(t, from)
	got, _ := os.ReadFile(filepath.Join(newDir, "app.db"))
	require.Equal(t, "NEW", string(got))
}

func TestMigrateLegacyRunsOnce(t *testing.T) {
	root := t.TempDir()
	oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "new")
	writeFile(t, oldDir, "app.db", "DB")
	require.NoError(t, os.MkdirAll(newDir, 0o700))
	_, err := migrateLegacy(oldDir, newDir)
	require.NoError(t, err)

	// El usuario borra la BD nueva a propósito: no se resucita desde la antigua.
	require.NoError(t, os.Remove(filepath.Join(newDir, "app.db")))
	from, err := migrateLegacy(oldDir, newDir)
	require.NoError(t, err)
	require.Empty(t, from)
	require.NoFileExists(t, filepath.Join(newDir, "app.db"))
}

func TestMigrateLegacyCleanInstall(t *testing.T) {
	root := t.TempDir()
	newDir := filepath.Join(root, "new")
	require.NoError(t, os.MkdirAll(newDir, 0o700))
	from, err := migrateLegacy(filepath.Join(root, "missing"), newDir)
	require.NoError(t, err)
	require.Empty(t, from)
	require.NoFileExists(t, filepath.Join(newDir, migratedMarker))
}

func TestMigrateLegacyFailureLeavesNoPartialCopy(t *testing.T) {
	root := t.TempDir()
	oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "new")
	writeFile(t, oldDir, "app.db", "DB")
	// known_hosts ilegible en origen (es un directorio) fuerza el fallo tras copiar app.db.
	require.NoError(t, os.MkdirAll(filepath.Join(oldDir, "known_hosts", "x"), 0o700))
	require.NoError(t, os.MkdirAll(newDir, 0o700))

	_, err := migrateLegacy(oldDir, newDir)
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(newDir, "app.db"))
	require.NoFileExists(t, filepath.Join(newDir, migratedMarker))
}
