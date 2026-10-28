package secrets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"tabulariswails/internal/apperr"
)

func stores(t *testing.T) map[string]Store {
	keyring.MockInit()
	return map[string]Store{"keyring": NewKeyring(), "memory": NewMemory()}
}

func TestStoreContract(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_, ok, err := s.Get("p1", Password)
			require.NoError(t, err)
			require.False(t, ok)

			require.NoError(t, s.Set("p1", Password, "pa:ss/wörd"))
			require.NoError(t, s.Set("p1", SSHPassphrase, "pp"))
			require.NoError(t, s.Set("p2", Password, "other"))

			v, ok, err := s.Get("p1", Password)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, "pa:ss/wörd", v.Reveal())

			require.NoError(t, s.Delete("p1", Password))
			require.NoError(t, s.Delete("p1", Password), "idempotente")
			_, ok, _ = s.Get("p1", Password)
			require.False(t, ok)

			require.NoError(t, s.DeleteAll("p1"))
			_, ok, _ = s.Get("p1", SSHPassphrase)
			require.False(t, ok)
			_, ok, _ = s.Get("p2", Password)
			require.True(t, ok, "no toca otros perfiles")
		})
	}
}

func TestSecretNeverLeaks(t *testing.T) {
	s := Secret("hunter2")
	require.NotContains(t, fmt.Sprintf("%v %s %+v %#v", s, s, s, s), "hunter2")
	b, err := json.Marshal(map[string]any{"s": s})
	require.NoError(t, err)
	require.NotContains(t, string(b), "hunter2")

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "value", s)
	require.NotContains(t, buf.String(), "hunter2")
}

func TestKeyringErrorMapsToUnavailable(t *testing.T) {
	keyring.MockInitWithError(fmt.Errorf("no secret service"))
	err := NewKeyring().Set("p", Password, "x")
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, apperr.KeyringUnavailable, ae.Code)
	_, _, err = NewKeyring().Get("p", Password)
	require.ErrorAs(t, err, &ae)
}
