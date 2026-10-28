package paths

import (
	"os"
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
	require.Equal(t, home+"/config/app.db", p.DB())
	info, _ := os.Stat(p.Config)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}
