package monitor

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/store"
)

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.5:53422":    "192.168.1.5",
		"app-1.internal:40000": "app-1.internal",
		"localhost:3306":       "localhost",
		"localhost":            "localhost",
		"192.168.1.5":          "192.168.1.5",
		"[::1]:53422":          "::1",
		"::1:53422":            "::1",
		"fe80::1:53422":        "fe80::1",
		"host:name":            "host:name", // lo que sigue a «:» no es un puerto
		"":                     "",
		"  10.0.0.9:1  ":       "10.0.0.9",
	} {
		require.Equal(t, want, NormalizeHost(in), in)
	}
}

func TestActorsOf(t *testing.T) {
	rows := []model.Process{
		{ID: 1, User: "app", Host: "10.0.0.5:5001", Digest: "d1", Info: "SELECT 1"},
		{ID: 2, User: "app", Host: "10.0.0.5:5002", Digest: "d1"}, // mismo actor con otro puerto: una sola vez
		{ID: 3, User: "ana", Host: "10.0.0.7:5003", Digest: "d1"},
		{ID: 4, User: "app", Host: "10.0.0.5:5004"},                       // sin digest (dormido)
		{ID: 5, User: "system user", Host: "", Digest: "d2"},              // hilo interno
		{ID: 6, User: "event_scheduler", Host: "localhost", Digest: "d2"}, // hilo interno
		{ID: 7, User: "app", Host: "", Digest: "d3"},                      // sin host
		{ID: 8, User: "root", Host: "localhost:41000", Digest: "d4"},      // un cliente local legítimo
	}
	require.Equal(t, []store.ActorSeen{
		{Digest: "d1", User: "app", Host: "10.0.0.5"},
		{Digest: "d1", User: "ana", Host: "10.0.0.7"},
		{Digest: "d4", User: "root", Host: "localhost"},
	}, ActorsOf(rows))
}
