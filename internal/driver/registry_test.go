package driver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type stub struct{ name string }

func (s stub) Name() string                                           { return s.name }
func (s stub) DisplayName() string                                    { return s.name }
func (stub) Capabilities() Capabilities                               { return Capabilities{} }
func (stub) DefaultPort() int                                         { return 0 }
func (stub) QuoteIdent(n string) string                               { return n }
func (stub) QuoteString(s string) string                              { return s }
func (stub) ParseDSN(string) (ConnConfig, error)                      { return ConnConfig{}, nil }
func (stub) Open(context.Context, ConnConfig, DialFunc) (Pool, error) { return nil, nil }

func TestRegistry(t *testing.T) {
	t.Cleanup(func() { Unregister("b"); Unregister("a") })
	Register(stub{"b"})
	Register(stub{"a"})

	d, ok := Get("a")
	require.True(t, ok)
	require.Equal(t, "a", d.Name())
	_, ok = Get("zzz")
	require.False(t, ok)

	var names []string
	for _, d := range List() {
		names = append(names, d.Name())
	}
	require.Equal(t, []string{"a", "b"}, names)

	require.Panics(t, func() { Register(stub{"a"}) })
}
