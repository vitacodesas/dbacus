package session

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/driver/fake"
	"tabulariswails/internal/model"
	"tabulariswails/internal/testutil/sshsrv"
	"tabulariswails/internal/tunnel"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func newFake(t *testing.T) *fake.Driver {
	t.Helper()
	d := fake.New("fake-" + t.Name())
	driver.Register(d)
	t.Cleanup(func() { driver.Unregister(d.Name()) })
	return d
}

func profile(d *fake.Driver) model.ConnectionProfile {
	return model.ConnectionProfile{ID: "p1", Driver: d.Name(), Host: "db", Port: 1, User: "u", Database: "app"}
}

func code(t *testing.T, err error) string {
	t.Helper()
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	return ae.Code
}

func TestOpenRegistersSession(t *testing.T) {
	d := newFake(t)
	m := NewManager(Options{})
	defer m.CloseAll()

	s, err := m.Open(context.Background(), OpenRequest{Profile: profile(d), Password: "pw"})
	require.NoError(t, err)
	require.Equal(t, "fake-1.0", s.ServerVersion)
	require.Equal(t, "app", s.CurrentDatabase)
	require.Equal(t, "pw", d.LastCfg.Password.Reveal())
	require.Nil(t, d.LastDial, "sin SSH no se inyecta dialer")

	got, err := m.Get(s.ID)
	require.NoError(t, err)
	require.Same(t, s, got)

	require.NoError(t, m.Close(s.ID))
	_, err = m.Get(s.ID)
	require.Equal(t, apperr.SessionClosed, code(t, err))
	require.True(t, d.Pools[0].Closed.Load())
	require.Equal(t, apperr.SessionClosed, code(t, m.Close(s.ID)), "cerrar dos veces informa SESSION_CLOSED")
}

func TestOpenFailures(t *testing.T) {
	m := NewManager(Options{})
	_, err := m.Open(context.Background(), OpenRequest{Profile: model.ConnectionProfile{Driver: "no-existe"}})
	require.Equal(t, apperr.Unsupported, code(t, err))

	d := newFake(t)
	d.OpenErr = apperr.New(apperr.Validation, "mal")
	_, err = m.Open(context.Background(), OpenRequest{Profile: profile(d)})
	require.Equal(t, apperr.Validation, code(t, err))

	d.OpenErr = nil
	d.PingErr = apperr.New(apperr.AuthFailed, "auth")
	_, err = m.Open(context.Background(), OpenRequest{Profile: profile(d)})
	require.Equal(t, apperr.AuthFailed, code(t, err))
	require.True(t, d.Pools[0].Closed.Load(), "un Ping fallido cierra el pool")
}

func TestAcquireConnLazyAndShared(t *testing.T) {
	d := newFake(t)
	m := NewManager(Options{})
	defer m.CloseAll()
	s, err := m.Open(context.Background(), OpenRequest{Profile: profile(d)})
	require.NoError(t, err)
	pool := d.Pools[0]
	require.EqualValues(t, 0, pool.Acquired.Load(), "no se adquiere hasta que una pestaña lo pide")

	a, err := s.AcquireConn(context.Background(), "tab1")
	require.NoError(t, err)
	again, err := s.AcquireConn(context.Background(), "tab1")
	require.NoError(t, err)
	require.Same(t, a, again)
	b, err := s.AcquireConn(context.Background(), "tab2")
	require.NoError(t, err)
	require.NotSame(t, a, b)
	require.Equal(t, 2, s.TabCount())

	got, ok := s.TabConn("tab1")
	require.True(t, ok)
	require.Same(t, a, got)
	_, ok = s.TabConn("nada")
	require.False(t, ok)

	require.NoError(t, s.CloseTab("tab1"))
	require.NoError(t, s.CloseTab("tab1"), "idempotente")
	require.EqualValues(t, 1, pool.Open.Load())
}

func TestAcquireConnConcurrentSameTabAcquiresOnce(t *testing.T) {
	d := newFake(t)
	d.AcquireFn = func(context.Context) error { time.Sleep(50 * time.Millisecond); return nil }
	m := NewManager(Options{})
	defer m.CloseAll()
	s, _ := m.Open(context.Background(), OpenRequest{Profile: profile(d)})

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.AcquireConn(context.Background(), "tab")
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, d.Pools[0].Acquired.Load())
}

func TestAcquireFailureAllowsRetry(t *testing.T) {
	d := newFake(t)
	m := NewManager(Options{})
	defer m.CloseAll()
	s, _ := m.Open(context.Background(), OpenRequest{Profile: profile(d)})

	d.AcquireFn = func(context.Context) error { return errors.New("sin conexiones") }
	_, err := s.AcquireConn(context.Background(), "tab")
	require.Error(t, err)
	require.Equal(t, 0, s.TabCount())

	d.AcquireFn = nil
	_, err = s.AcquireConn(context.Background(), "tab")
	require.NoError(t, err)
}

func TestMaxTabs(t *testing.T) {
	d := newFake(t)
	m := NewManager(Options{MaxTabs: 2})
	defer m.CloseAll()
	s, _ := m.Open(context.Background(), OpenRequest{Profile: profile(d)})
	for _, id := range []string{"a", "b"} {
		_, err := s.AcquireConn(context.Background(), id)
		require.NoError(t, err)
	}
	_, err := s.AcquireConn(context.Background(), "c")
	require.Equal(t, apperr.Validation, code(t, err))
	_, err = s.AcquireConn(context.Background(), "a")
	require.NoError(t, err, "una pestaña existente no cuenta como nueva")
	require.NoError(t, s.CloseTab("a"))
	_, err = s.AcquireConn(context.Background(), "c")
	require.NoError(t, err)
}

func TestCloseReleasesEverythingAndRejectsNewWork(t *testing.T) {
	d := newFake(t)
	m := NewManager(Options{})
	s, _ := m.Open(context.Background(), OpenRequest{Profile: profile(d)})
	_, _ = s.AcquireConn(context.Background(), "a")
	_, _ = s.AcquireConn(context.Background(), "b")

	require.NoError(t, m.Close(s.ID))
	require.EqualValues(t, 0, d.Pools[0].Open.Load(), "todas las conexiones liberadas")
	require.True(t, d.Pools[0].Closed.Load())
	_, err := s.AcquireConn(context.Background(), "z")
	require.Equal(t, apperr.SessionClosed, code(t, err))
}

func TestConcurrentCloseIsSafe(t *testing.T) {
	d := newFake(t)
	m := NewManager(Options{})
	s, _ := m.Open(context.Background(), OpenRequest{Profile: profile(d)})
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.Close(s.ID) }()
	}
	wg.Wait()
}

func TestCloseByProfileAndCloseAll(t *testing.T) {
	d := newFake(t)
	closed := make(chan string, 4)
	m := NewManager(Options{OnClosed: func(id, reason string) { closed <- reason }})

	p1, p2 := profile(d), profile(d)
	p2.ID = "p2"
	s1, _ := m.Open(context.Background(), OpenRequest{Profile: p1})
	_, _ = m.Open(context.Background(), OpenRequest{Profile: p1})
	s3, _ := m.Open(context.Background(), OpenRequest{Profile: p2})

	m.CloseByProfile("p1")
	_, err := m.Get(s1.ID)
	require.Error(t, err)
	_, err = m.Get(s3.ID)
	require.NoError(t, err)

	m.CloseAll()
	_, err = m.Get(s3.ID)
	require.Error(t, err)
	require.Empty(t, closed, "cierres de usuario/shutdown no notifican OnClosed")
}

func sshProfile(d *fake.Driver, srv *sshsrv.Server) model.ConnectionProfile {
	p := profile(d)
	p.SSH = model.SSHConfig{Enabled: true, Host: srv.Host, Port: srv.Port, User: sshsrv.User, AuthMethod: "password"}
	return p
}

func trustedHosts(t *testing.T, srv *sshsrv.Server) *tunnel.KnownHosts {
	kh := tunnel.NewKnownHosts(filepath.Join(t.TempDir(), "known_hosts"))
	require.NoError(t, kh.Trust(srv.Host, srv.Port, srv.Fingerprint))
	return kh
}

func TestOpenThroughSSHTunnel(t *testing.T) {
	srv := sshsrv.Start(t)
	echo := sshsrv.EchoServer(t)
	d := newFake(t)
	m := NewManager(Options{KnownHosts: trustedHosts(t, srv)})

	s, err := m.Open(context.Background(), OpenRequest{Profile: sshProfile(d, srv), SSHPassword: sshsrv.Password})
	require.NoError(t, err)
	require.NotNil(t, d.LastDial, "el driver recibe el dialer del túnel")

	c, err := d.LastDial(context.Background(), "tcp", echo)
	require.NoError(t, err)
	_, _ = c.Write([]byte("ping"))
	buf := make([]byte, 4)
	_, err = io.ReadFull(c, buf)
	require.NoError(t, err)
	require.Equal(t, "ping", string(buf))
	_ = c.Close()

	require.NoError(t, m.Close(s.ID))
	require.True(t, s.tunnel != nil)
	select {
	case <-s.tunnel.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("el túnel debe cerrarse con la sesión")
	}
	m.CloseAll()
}

func TestSSHFailuresCleanUp(t *testing.T) {
	srv := sshsrv.Start(t)
	d := newFake(t)
	m := NewManager(Options{KnownHosts: tunnel.NewKnownHosts(filepath.Join(t.TempDir(), "kh"))})

	_, err := m.Open(context.Background(), OpenRequest{Profile: sshProfile(d, srv), SSHPassword: sshsrv.Password})
	require.Equal(t, apperr.SSHHostKeyUnknown, code(t, err))
	require.Empty(t, d.Pools, "no se abre el pool si el túnel falla")

	// Túnel OK pero el Ping del driver falla: el túnel debe cerrarse (goleak lo verifica).
	m2 := NewManager(Options{KnownHosts: trustedHosts(t, srv)})
	d.PingErr = apperr.New(apperr.AuthFailed, "x")
	_, err = m2.Open(context.Background(), OpenRequest{Profile: sshProfile(d, srv), SSHPassword: sshsrv.Password})
	require.Equal(t, apperr.AuthFailed, code(t, err))
}

func TestTunnelDropClosesSessionAndNotifies(t *testing.T) {
	srv := sshsrv.Start(t)
	d := newFake(t)
	type ev struct{ id, reason string }
	events := make(chan ev, 1)
	m := NewManager(Options{KnownHosts: trustedHosts(t, srv), OnClosed: func(id, reason string) { events <- ev{id, reason} }})
	defer m.CloseAll()

	s, err := m.Open(context.Background(), OpenRequest{Profile: sshProfile(d, srv), SSHPassword: sshsrv.Password})
	require.NoError(t, err)
	_, err = s.AcquireConn(context.Background(), "tab")
	require.NoError(t, err)

	srv.DropConnections()
	select {
	case e := <-events:
		require.Equal(t, ev{s.ID, ReasonTunnelDown}, e)
	case <-time.After(3 * time.Second):
		t.Fatal("no llegó session closed")
	}
	_, err = m.Get(s.ID)
	require.Equal(t, apperr.SessionClosed, code(t, err))
	require.True(t, d.Pools[0].Closed.Load())
	require.EqualValues(t, 0, d.Pools[0].Open.Load())
}
