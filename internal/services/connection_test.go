package services

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/driver/fake"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/secrets"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/store"
	"github.com/vitacodesas/dbacus/internal/testutil/sshsrv"
	"github.com/vitacodesas/dbacus/internal/tunnel"
)

type env struct {
	dbPath   string
	svc      *ConnectionService
	st       *store.Store
	sec      *secrets.Memory
	drv      *fake.Driver
	kh       *tunnel.KnownHosts
	sessions *session.Manager
	closed   chan SessionClosed
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	st, err := store.Open(context.Background(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	drv := fake.New("fake-" + t.Name())
	driver.Register(drv)
	t.Cleanup(func() { driver.Unregister(drv.Name()) })

	e := &env{dbPath: dbPath, st: st, sec: secrets.NewMemory(), drv: drv, closed: make(chan SessionClosed, 4),
		kh: tunnel.NewKnownHosts(filepath.Join(t.TempDir(), "known_hosts"))}
	e.sessions = session.NewManager(session.Options{KnownHosts: e.kh, OnClosed: func(id, reason string) { e.closed <- SessionClosed{id, reason} }})
	t.Cleanup(e.sessions.CloseAll)
	e.svc = NewConnectionService(ConnectionDeps{Store: st, Secrets: e.sec, Sessions: e.sessions, KnownHosts: e.kh})
	return e
}

func (e *env) profile(name string) model.ConnectionProfile {
	return model.ConnectionProfile{Name: name, Driver: e.drv.Name(), Host: "127.0.0.1", Port: 3306, User: "root", SavePassword: true}
}

func sp(s string) *string { return &s }

func code(t *testing.T, err error) string {
	t.Helper()
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	return ae.Code
}

func detail(t *testing.T, err error) string {
	t.Helper()
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	return ae.Detail
}

func TestSaveCreateStoresSecretOutsideSQLite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	saved, err := e.svc.Save(ctx, e.profile("Local"), ConnectionSecrets{Password: sp("pa$$")})
	require.NoError(t, err)
	require.NotEmpty(t, saved.ID)
	require.True(t, saved.HasPassword)

	pw, ok, _ := e.sec.Get(saved.ID, secrets.Password)
	require.True(t, ok)
	require.Equal(t, "pa$$", pw.Reveal())

	// La contraseña no aparece en lo persistido ni en lo que se devuelve a Vue.
	got, err := e.svc.Get(ctx, saved.ID)
	require.NoError(t, err)
	require.True(t, got.HasPassword)
	raw, err := os.ReadFile(e.dbPath)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "pa$$", "la contraseña no está en el archivo SQLite")
	if wal, err := os.ReadFile(e.dbPath + "-wal"); err == nil {
		require.NotContains(t, string(wal), "pa$$")
	}
}

func TestSaveDefaultsAndClientCannotForgeFlags(t *testing.T) {
	e := newEnv(t)
	p := e.profile("  Con espacios  ")
	p.Port = 0
	p.HasPassword = true // intento de falsificar el estado
	saved, err := e.svc.Save(context.Background(), p, ConnectionSecrets{})
	require.NoError(t, err)
	require.Equal(t, "Con espacios", saved.Name)
	require.Equal(t, 1, saved.Port, "el puerto por defecto sale del driver")
	require.False(t, saved.HasPassword)
	require.Equal(t, "preferred", saved.TLS.Mode)
}

func TestSaveEditKeepsAndClearsPassword(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	saved, _ := e.svc.Save(ctx, e.profile("A"), ConnectionSecrets{Password: sp("uno")})

	saved.Host = "10.0.0.9"
	edited, err := e.svc.Save(ctx, saved, ConnectionSecrets{}) // sin tocar contraseña
	require.NoError(t, err)
	require.True(t, edited.HasPassword)
	pw, _, _ := e.sec.Get(saved.ID, secrets.Password)
	require.Equal(t, "uno", pw.Reveal())

	edited, err = e.svc.Save(ctx, edited, ConnectionSecrets{Password: sp("dos")})
	require.NoError(t, err)
	pw, _, _ = e.sec.Get(saved.ID, secrets.Password)
	require.Equal(t, "dos", pw.Reveal())

	edited, err = e.svc.Save(ctx, edited, ConnectionSecrets{Clear: []string{"password"}})
	require.NoError(t, err)
	require.False(t, edited.HasPassword)
	_, ok, _ := e.sec.Get(saved.ID, secrets.Password)
	require.False(t, ok)
}

func TestSaveWithoutSavePasswordDropsStoredSecrets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	saved, _ := e.svc.Save(ctx, e.profile("A"), ConnectionSecrets{Password: sp("x")})
	saved.SavePassword = false
	edited, err := e.svc.Save(ctx, saved, ConnectionSecrets{Password: sp("ignorada")})
	require.NoError(t, err)
	require.False(t, edited.HasPassword)
	_, ok, _ := e.sec.Get(saved.ID, secrets.Password)
	require.False(t, ok)
}

func TestSaveDuplicateNameAndUnknownID(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.svc.Save(ctx, e.profile("Dup"), ConnectionSecrets{})
	require.NoError(t, err)
	_, err = e.svc.Save(ctx, e.profile("dup"), ConnectionSecrets{})
	require.Equal(t, apperr.Validation, code(t, err))
	require.Equal(t, "name", detail(t, err))

	p := e.profile("X")
	p.ID = "no-existe"
	_, err = e.svc.Save(ctx, p, ConnectionSecrets{})
	require.Equal(t, apperr.NotFound, code(t, err))
}

type failingSecrets struct{ secrets.Store }

func (failingSecrets) Set(string, secrets.Kind, secrets.Secret) error {
	return apperr.New(apperr.KeyringUnavailable, "sin llavero")
}

func TestSaveKeyringFailureLeavesNoHalfCreatedProfile(t *testing.T) {
	e := newEnv(t)
	e.svc.d.Secrets = failingSecrets{e.sec}
	_, err := e.svc.Save(context.Background(), e.profile("A"), ConnectionSecrets{Password: sp("x")})
	require.Equal(t, apperr.KeyringUnavailable, code(t, err))
	list, _ := e.svc.List(context.Background())
	require.Empty(t, list)
}

func TestValidation(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "f.pem")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	cases := []struct {
		name  string
		mut   func(*model.ConnectionProfile)
		field string
	}{
		{"sin nombre", func(p *model.ConnectionProfile) { p.Name = "  " }, "name"},
		{"nombre largo", func(p *model.ConnectionProfile) { p.Name = string(make([]rune, 101)) + "x" }, "name"},
		{"driver", func(p *model.ConnectionProfile) { p.Driver = "oracle" }, "driver"},
		{"host vacío", func(p *model.ConnectionProfile) { p.Host = "" }, "host"},
		{"host con espacio", func(p *model.ConnectionProfile) { p.Host = "a b" }, "host"},
		{"puerto", func(p *model.ConnectionProfile) { p.Port = 70000 }, "port"},
		{"usuario", func(p *model.ConnectionProfile) { p.User = "" }, "user"},
		{"color", func(p *model.ConnectionProfile) { p.Color = "rojo" }, "color"},
		{"tls modo", func(p *model.ConnectionProfile) { p.TLS.Mode = "x" }, "tls.mode"},
		{"verify-ca sin CA", func(p *model.ConnectionProfile) { p.TLS.Mode = "verify-ca" }, "tls.caPath"},
		{"CA inexistente", func(p *model.ConnectionProfile) { p.TLS.CAPath = "/no/existe" }, "tls.caPath"},
		{"CA es carpeta", func(p *model.ConnectionProfile) { p.TLS.CAPath = dir }, "tls.caPath"},
		{"cert sin key", func(p *model.ConnectionProfile) { p.TLS.CertPath = file }, "tls.certPath"},
		{"ssh sin host", func(p *model.ConnectionProfile) { p.SSH = model.SSHConfig{Enabled: true, User: "u"} }, "ssh.host"},
		{"ssh sin usuario", func(p *model.ConnectionProfile) { p.SSH = model.SSHConfig{Enabled: true, Host: "h"} }, "ssh.user"},
		{"ssh puerto", func(p *model.ConnectionProfile) {
			p.SSH = model.SSHConfig{Enabled: true, Host: "h", User: "u", Port: 99999}
		}, "ssh.port"},
		{"ssh método", func(p *model.ConnectionProfile) {
			p.SSH = model.SSHConfig{Enabled: true, Host: "h", User: "u", AuthMethod: "otp"}
		}, "ssh.authMethod"},
		{"ssh clave sin ruta", func(p *model.ConnectionProfile) {
			p.SSH = model.SSHConfig{Enabled: true, Host: "h", User: "u", AuthMethod: "key"}
		}, "ssh.keyPath"},
		{"ssh clave inexistente", func(p *model.ConnectionProfile) {
			p.SSH = model.SSHConfig{Enabled: true, Host: "h", User: "u", AuthMethod: "key", KeyPath: "/no/existe"}
		}, "ssh.keyPath"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := e.profile("V-" + c.name)
			c.mut(&p)
			_, err := e.svc.Save(context.Background(), p, ConnectionSecrets{})
			require.Equal(t, apperr.Validation, code(t, err))
			require.Equal(t, c.field, detail(t, err))
		})
	}

	ok := e.profile("Válido")
	ok.TLS = model.TLSConfig{Mode: "verify-full", CAPath: file}
	ok.SSH = model.SSHConfig{Enabled: true, Host: "bastion", User: "u"}
	saved, err := e.svc.Save(context.Background(), ok, ConnectionSecrets{})
	require.NoError(t, err)
	require.Equal(t, 22, saved.SSH.Port)
	require.Equal(t, "password", saved.SSH.AuthMethod)
}

func TestClone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src, _ := e.svc.Save(ctx, e.profile("Prod"), ConnectionSecrets{Password: sp("pw")})

	c1, err := e.svc.Clone(ctx, src.ID)
	require.NoError(t, err)
	require.Equal(t, "Prod (copia)", c1.Name)
	require.NotEqual(t, src.ID, c1.ID)
	require.True(t, c1.HasPassword)
	c2, err := e.svc.Clone(ctx, src.ID)
	require.NoError(t, err)
	require.Equal(t, "Prod (copia 2)", c2.Name)

	pw, ok, _ := e.sec.Get(c1.ID, secrets.Password)
	require.True(t, ok)
	require.Equal(t, "pw", pw.Reveal())

	// Independientes: borrar el original no toca la copia, ni editar la copia al original.
	c1.Host = "otro"
	_, err = e.svc.Save(ctx, c1, ConnectionSecrets{Clear: []string{"password"}})
	require.NoError(t, err)
	_, ok, _ = e.sec.Get(src.ID, secrets.Password)
	require.True(t, ok)
	require.NoError(t, e.svc.Delete(ctx, src.ID, false))
	_, err = e.svc.Get(ctx, c2.ID)
	require.NoError(t, err)
	_, ok, _ = e.sec.Get(c2.ID, secrets.Password)
	require.True(t, ok)

	_, err = e.svc.Clone(ctx, "nada")
	require.Equal(t, apperr.NotFound, code(t, err))
}

func TestDeleteRemovesSecretsClosesSessionsAndOptionallyHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p, _ := e.svc.Save(ctx, e.profile("A"), ConnectionSecrets{Password: sp("x")})
	info, err := e.svc.Connect(ctx, p.ID, nil)
	require.NoError(t, err)
	_, err = e.st.AddHistory(ctx, model.HistoryEntry{ProfileID: p.ID, SQL: "SELECT 1", OK: true}, 0)
	require.NoError(t, err)

	require.NoError(t, e.svc.Delete(ctx, p.ID, false))
	_, ok, _ := e.sec.Get(p.ID, secrets.Password)
	require.False(t, ok)
	_, err = e.sessions.Get(info.SessionID)
	require.Error(t, err, "la sesión se cerró")
	hist, _, _ := e.st.ListHistory(ctx, store.HistoryQuery{ProfileID: p.ID})
	require.Len(t, hist, 1, "el historial se conserva si no se pide borrarlo")

	require.Equal(t, apperr.NotFound, code(t, e.svc.Delete(ctx, p.ID, true)))
	p2, _ := e.svc.Save(ctx, e.profile("B"), ConnectionSecrets{})
	_, _ = e.st.AddHistory(ctx, model.HistoryEntry{ProfileID: p2.ID, SQL: "SELECT 2", OK: true}, 0)
	require.NoError(t, e.svc.Delete(ctx, p2.ID, true))
	hist, _, _ = e.st.ListHistory(ctx, store.HistoryQuery{ProfileID: p2.ID})
	require.Empty(t, hist)
}

func TestConnectUsesStoredPasswordAndOverrides(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p, _ := e.svc.Save(ctx, e.profile("A"), ConnectionSecrets{Password: sp("guardada")})

	info, err := e.svc.Connect(ctx, p.ID, nil)
	require.NoError(t, err)
	require.Equal(t, "fake-1.0", info.ServerVersion)
	require.Equal(t, "guardada", e.drv.LastCfg.Password.Reveal())
	require.Len(t, e.svc.ListSessions(), 1)

	_, err = e.svc.Connect(ctx, p.ID, &ConnectionSecrets{Password: sp("temporal")})
	require.NoError(t, err)
	require.Equal(t, "temporal", e.drv.LastCfg.Password.Reveal())

	require.NoError(t, e.svc.Disconnect(info.SessionID))
	require.Len(t, e.svc.ListSessions(), 1)
	require.Equal(t, apperr.SessionClosed, code(t, e.svc.Disconnect(info.SessionID)))
}

func TestConnectAsksPasswordWhenNotSaved(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	prof := e.profile("A")
	prof.SavePassword = false
	p, _ := e.svc.Save(ctx, prof, ConnectionSecrets{})

	_, err := e.svc.Connect(ctx, p.ID, nil)
	require.Equal(t, apperr.PasswordRequired, code(t, err))
	require.Equal(t, "password", detail(t, err))

	_, err = e.svc.Connect(ctx, p.ID, &ConnectionSecrets{Password: sp("")})
	require.NoError(t, err, "una contraseña vacía escrita explícitamente es válida")
	_, ok, _ := e.sec.Get(p.ID, secrets.Password)
	require.False(t, ok, "lo escrito al conectar nunca se guarda")

	// Con SavePassword=true y sin contraseña guardada se conecta con contraseña vacía.
	p2, _ := e.svc.Save(ctx, e.profile("B"), ConnectionSecrets{})
	_, err = e.svc.Connect(ctx, p2.ID, nil)
	require.NoError(t, err)
}

func TestConnectErrorsMapped(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p, _ := e.svc.Save(ctx, e.profile("A"), ConnectionSecrets{})
	e.drv.PingErr = apperr.New(apperr.AuthFailed, "no")
	_, err := e.svc.Connect(ctx, p.ID, nil)
	require.Equal(t, apperr.AuthFailed, code(t, err))
	_, err = e.svc.Connect(ctx, "nada", nil)
	require.Equal(t, apperr.NotFound, code(t, err))
}

func sshProfile(e *env, srv *sshsrv.Server, name string) model.ConnectionProfile {
	p := e.profile(name)
	p.SSH = model.SSHConfig{Enabled: true, Host: srv.Host, Port: srv.Port, User: sshsrv.User, AuthMethod: "password"}
	return p
}

func TestConnectViaSSHHostKeyFlow(t *testing.T) {
	e := newEnv(t)
	srv := sshsrv.Start(t)
	ctx := context.Background()
	p, err := e.svc.Save(ctx, sshProfile(e, srv, "ssh"), ConnectionSecrets{SSHPassword: sp(sshsrv.Password)})
	require.NoError(t, err)
	require.True(t, p.HasSSHPassword)

	_, err = e.svc.Connect(ctx, p.ID, nil)
	require.Equal(t, apperr.SSHHostKeyUnknown, code(t, err))
	fp := detail(t, err)
	require.Equal(t, srv.Fingerprint, fp)

	require.NoError(t, e.svc.TrustHostKey(srv.Host, srv.Port, fp))
	info, err := e.svc.Connect(ctx, p.ID, nil)
	require.NoError(t, err)

	// Caída del túnel → evento de sesión cerrada.
	srv.DropConnections()
	select {
	case ev := <-e.closed:
		require.Equal(t, SessionClosed{info.SessionID, session.ReasonTunnelDown}, ev)
	case <-time.After(3 * time.Second):
		t.Fatal("no llegó session:closed")
	}
}

func TestParseDSN(t *testing.T) {
	e := newEnv(t)
	e.drv.ParseFn = func(dsn string) (driver.ConnConfig, error) {
		return driver.ConnConfig{Profile: model.ConnectionProfile{Host: "h", Port: 1234, User: "u"}, Password: "p@ss"}, nil
	}
	got, err := e.svc.ParseDSN("fake://u:p%40ss@h:1234/db")
	require.NoError(t, err)
	require.Equal(t, "h", got.Profile.Host)
	require.Equal(t, "p@ss", *got.Secrets.Password)
	e.drv.ParseFn = nil
	_, err = e.svc.ParseDSN("nada")
	require.Error(t, err)
}

func TestListDriversAndPickFile(t *testing.T) {
	e := newEnv(t)
	names := []string{}
	for _, d := range e.svc.ListDrivers() {
		names = append(names, d.Name)
	}
	require.Contains(t, names, e.drv.Name())

	_, err := e.svc.PickFile("x")
	require.Equal(t, apperr.Unsupported, code(t, err))
	e.svc.d.PickFile = func(title string) (string, error) { return "/tmp/" + title, nil }
	got, err := e.svc.PickFile("a.pem")
	require.NoError(t, err)
	require.Equal(t, "/tmp/a.pem", got)
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
