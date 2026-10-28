package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactsSecretKeys(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(NewHandler(&buf, slog.LevelDebug))
	l.Info("conn", "password", "hunter2", "ssh_passphrase", "p4ss", "apiKey", "k", "host", "db1")
	out := buf.String()
	require.NotContains(t, out, "hunter2")
	require.NotContains(t, out, "p4ss")
	require.Contains(t, out, `"host":"db1"`)
}

func TestTruncatesSQL(t *testing.T) {
	var buf bytes.Buffer
	slog.New(NewHandler(&buf, slog.LevelDebug)).Debug("q", "sql", strings.Repeat("x", 500))
	require.Less(t, buf.Len(), 400)
}
