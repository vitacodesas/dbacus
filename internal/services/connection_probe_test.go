package services

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/testutil/sshsrv"
)

func stepNames(r TestResult) []string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.Name)
	}
	return out
}

func step(t *testing.T, r TestResult, name string) TestStep {
	t.Helper()
	for _, s := range r.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("paso %q no existe en %v", name, stepNames(r))
	return TestStep{}
}

// listener hace de "servidor de base de datos": acepta TCP y nada más (el driver es falso).
func listener(t *testing.T) (host string, port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	return "127.0.0.1", l.Addr().(*net.TCPAddr).Port
}

func (e *env) probeProfile(t *testing.T) model.ConnectionProfile {
	h, port := listener(t)
	p := e.profile("probe")
	p.Host, p.Port = h, port
	p.TLS.Mode = "preferred"
	return p
}

func TestProbeSuccess(t *testing.T) {
	e := newEnv(t)
	r, err := e.svc.Test(context.Background(), e.probeProfile(t), ConnectionSecrets{Password: sp("x")})
	require.NoError(t, err)
	require.True(t, r.OK, "%+v", r.Error)
	require.Equal(t, []string{StepTCP, StepTLS, StepAuth, StepQuery}, stepNames(r))
	for _, s := range r.Steps {
		require.True(t, s.OK, s.Name)
		require.False(t, s.Skipped)
	}
	require.Equal(t, "fake-1.0", r.ServerVersion)
	require.Equal(t, "x", e.drv.LastCfg.Password.Reveal())
	require.True(t, e.drv.Pools[0].Closed.Load(), "la prueba no deja pools abiertos")
}

func TestProbeTLSDisabledOmitsTLSStep(t *testing.T) {
	e := newEnv(t)
	p := e.probeProfile(t)
	p.TLS.Mode = "disable"
	r, _ := e.svc.Test(context.Background(), p, ConnectionSecrets{})
	require.True(t, r.OK)
	require.Equal(t, []string{StepTCP, StepAuth, StepQuery}, stepNames(r))
}

func TestProbeDoesNotSaveAnything(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.Test(context.Background(), e.probeProfile(t), ConnectionSecrets{Password: sp("x")})
	require.NoError(t, err)
	list, _ := e.svc.List(context.Background())
	require.Empty(t, list)
}

func TestProbeInvalidProfileReturnsError(t *testing.T) {
	e := newEnv(t)
	p := e.probeProfile(t)
	p.Host = ""
	_, err := e.svc.Test(context.Background(), p, ConnectionSecrets{})
	require.Equal(t, apperr.Validation, code(t, err))
}

func TestProbeDistinctMessagesPerFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	t.Run("puerto cerrado", func(t *testing.T) {
		p := e.probeProfile(t)
		p.Port = freePort(t)
		r, _ := e.svc.Test(ctx, p, ConnectionSecrets{})
		require.False(t, r.OK)
		require.Contains(t, r.Error.Message, "cerrado")
		require.True(t, step(t, r, StepAuth).Skipped, "los pasos siguientes quedan omitidos")
	})
	t.Run("host no resoluble", func(t *testing.T) {
		p := e.probeProfile(t)
		p.Host = "no-existe.invalid"
		r, _ := e.svc.Test(ctx, p, ConnectionSecrets{})
		require.False(t, step(t, r, StepTCP).OK)
		require.Contains(t, r.Error.Message, "resolver")
	})
	t.Run("credenciales", func(t *testing.T) {
		e.drv.PingErr = apperr.New(apperr.AuthFailed, "Usuario o contraseña incorrectos")
		defer func() { e.drv.PingErr = nil }()
		r, _ := e.svc.Test(ctx, e.probeProfile(t), ConnectionSecrets{})
		require.True(t, step(t, r, StepTCP).OK)
		require.True(t, step(t, r, StepTLS).OK)
		require.False(t, step(t, r, StepAuth).OK)
		require.Equal(t, apperr.AuthFailed, r.Error.Code)
		require.True(t, step(t, r, StepQuery).Skipped)
	})
	t.Run("tls", func(t *testing.T) {
		e.drv.PingErr = apperr.New(apperr.TLSError, "certificado no confiable")
		defer func() { e.drv.PingErr = nil }()
		r, _ := e.svc.Test(ctx, e.probeProfile(t), ConnectionSecrets{})
		require.False(t, step(t, r, StepTLS).OK)
		require.True(t, step(t, r, StepAuth).Skipped)
		require.Equal(t, apperr.TLSError, r.Error.Code)
	})
}

func TestProbeUsesStoredPasswordWhenFieldEmpty(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h, port := listener(t)
	p := e.profile("p")
	p.Host, p.Port = h, port
	saved, err := e.svc.Save(ctx, p, ConnectionSecrets{Password: sp("del-llavero")})
	require.NoError(t, err)

	_, err = e.svc.Test(ctx, saved, ConnectionSecrets{})
	require.NoError(t, err)
	require.Equal(t, "del-llavero", e.drv.LastCfg.Password.Reveal())

	_, err = e.svc.Test(ctx, saved, ConnectionSecrets{Password: sp("escrita")})
	require.NoError(t, err)
	require.Equal(t, "escrita", e.drv.LastCfg.Password.Reveal(), "lo escrito en el formulario manda")
}

func TestProbeViaSSH(t *testing.T) {
	e := newEnv(t)
	srv := sshsrv.Start(t)
	ctx := context.Background()
	h, port := listener(t)
	p := sshProfile(e, srv, "ssh")
	p.Host, p.Port = h, port

	r, _ := e.svc.Test(ctx, p, ConnectionSecrets{SSHPassword: sp(sshsrv.Password)})
	require.False(t, r.OK)
	require.Equal(t, []string{StepSSH, StepTCP, StepTLS, StepAuth, StepQuery}, stepNames(r))
	require.Equal(t, apperr.SSHHostKeyUnknown, r.Error.Code)
	require.Equal(t, srv.Fingerprint, r.Error.Detail)
	require.True(t, step(t, r, StepTCP).Skipped)

	require.NoError(t, e.kh.Trust(srv.Host, srv.Port, srv.Fingerprint))
	r, _ = e.svc.Test(ctx, p, ConnectionSecrets{SSHPassword: sp("mala")})
	require.Equal(t, apperr.SSHAuthFailed, r.Error.Code)

	r, _ = e.svc.Test(ctx, p, ConnectionSecrets{SSHPassword: sp(sshsrv.Password)})
	require.True(t, r.OK, "%+v", r.Error)
	require.NotNil(t, e.drv.LastDial, "el driver usa el túnel")

	// Destino inalcanzable desde el servidor SSH.
	p.Port = freePort(t)
	r, _ = e.svc.Test(ctx, p, ConnectionSecrets{SSHPassword: sp(sshsrv.Password)})
	require.True(t, step(t, r, StepSSH).OK)
	require.False(t, step(t, r, StepTCP).OK)
	require.Contains(t, r.Error.Message, "servidor SSH")
}

func TestProbeCancelReturnsQuickly(t *testing.T) {
	e := newEnv(t)
	// Servidor SSH "colgado": acepta y no responde al handshake.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	p := e.probeProfile(t)
	p.SSH = model.SSHConfig{Enabled: true, Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, User: "u", AuthMethod: "password"}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	r, err := e.svc.Test(ctx, p, ConnectionSecrets{SSHPassword: sp("x")})
	require.NoError(t, err)
	require.False(t, r.OK)
	require.Less(t, time.Since(start), time.Second, "cancelar un Test colgado termina en menos de 1 s")
}

// closeEOFConn simula un extremo que ya cerró cuando se llama a Close (p. ej. un canal SSH cerrado por el servidor remoto).
type closeEOFConn struct{ net.Conn }

func (closeEOFConn) Close() error { return io.EOF }

func TestProbeTCPIgnoresACloseErrorAfterConnecting(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return closeEOFConn{c1}, nil
	}
	require.NoError(t, probeTCP(context.Background(), dial, "db:3306", model.ConnectionProfile{Host: "db", Port: 3306}),
		"si se pudo conectar, el EOF al cerrar no es un fallo")
}
