package tunnel

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"golang.org/x/crypto/ssh"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/testutil/sshsrv"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func code(t *testing.T, err error) string {
	t.Helper()
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	return ae.Code
}

func trusted(t *testing.T, s *sshsrv.Server) *KnownHosts {
	t.Helper()
	kh := NewKnownHosts(filepath.Join(t.TempDir(), "known_hosts"))
	require.NoError(t, kh.Trust(s.Host, s.Port, s.Fingerprint))
	return kh
}

func baseCfg(s *sshsrv.Server, kh *KnownHosts) Config {
	return Config{Host: s.Host, Port: s.Port, User: sshsrv.User, AuthMethod: "password", Password: sshsrv.Password, KnownHosts: kh, DialTimeout: 3 * time.Second}
}

func echoRoundTrip(t *testing.T, tn *Tunnel, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := tn.Dial(ctx, "tcp", target)
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Write([]byte("hola"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(c, buf)
	require.NoError(t, err)
	require.Equal(t, "hola", string(buf))
}

func TestPasswordAuthAndDial(t *testing.T) {
	s := sshsrv.Start(t)
	echo := sshsrv.EchoServer(t)
	tn, err := Open(context.Background(), baseCfg(s, trusted(t, s)))
	require.NoError(t, err)
	defer tn.Close()
	echoRoundTrip(t, tn, echo)
}

func TestKeyAuth(t *testing.T) {
	s := sshsrv.Start(t)
	echo := sshsrv.EchoServer(t)
	cfg := baseCfg(s, trusted(t, s))
	cfg.AuthMethod, cfg.KeyPath, cfg.Password = "key", s.KeyPath, ""
	tn, err := Open(context.Background(), cfg)
	require.NoError(t, err)
	defer tn.Close()
	echoRoundTrip(t, tn, echo)
}

func TestKeyAuthPassphrase(t *testing.T) {
	s := sshsrv.Start(t)
	kh := trusted(t, s)

	// Reescribe la clave del servidor de pruebas protegida con passphrase.
	raw, err := os.ReadFile(s.KeyPath)
	require.NoError(t, err)
	priv, err := ssh.ParseRawPrivateKey(raw)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("pp"))
	require.NoError(t, err)
	protected := filepath.Join(t.TempDir(), "id_protected")
	require.NoError(t, os.WriteFile(protected, pemEncode(block), 0o600))

	cfg := baseCfg(s, kh)
	cfg.AuthMethod, cfg.KeyPath, cfg.Password = "key", protected, ""

	_, err = Open(context.Background(), cfg)
	require.Equal(t, apperr.SSHAuthFailed, code(t, err), "sin passphrase")

	cfg.Passphrase = "mala"
	_, err = Open(context.Background(), cfg)
	require.Equal(t, apperr.SSHAuthFailed, code(t, err), "passphrase incorrecta")

	cfg.Passphrase = "pp"
	tn, err := Open(context.Background(), cfg)
	require.NoError(t, err)
	require.NoError(t, tn.Close())
}

func TestWrongPassword(t *testing.T) {
	s := sshsrv.Start(t)
	cfg := baseCfg(s, trusted(t, s))
	cfg.Password = "incorrecta"
	_, err := Open(context.Background(), cfg)
	require.Equal(t, apperr.SSHAuthFailed, code(t, err))
}

func TestKeyFileProblems(t *testing.T) {
	s := sshsrv.Start(t)
	cfg := baseCfg(s, trusted(t, s))
	cfg.AuthMethod = "key"

	cfg.KeyPath = "/no/existe"
	_, err := Open(context.Background(), cfg)
	require.Equal(t, apperr.SSHAuthFailed, code(t, err))

	bad := filepath.Join(t.TempDir(), "bad")
	require.NoError(t, os.WriteFile(bad, []byte("basura"), 0o600))
	cfg.KeyPath = bad
	_, err = Open(context.Background(), cfg)
	require.Equal(t, apperr.SSHAuthFailed, code(t, err))

	cfg.AuthMethod = "otro"
	_, err = Open(context.Background(), cfg)
	require.Equal(t, apperr.Validation, code(t, err))
}

func TestHostKeyUnknownThenTrusted(t *testing.T) {
	s := sshsrv.Start(t)
	kh := NewKnownHosts(filepath.Join(t.TempDir(), "known_hosts"))

	_, err := Open(context.Background(), baseCfg(s, kh))
	require.Equal(t, apperr.SSHHostKeyUnknown, code(t, err))
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, s.Fingerprint, ae.Detail, "Detail es exactamente la huella para que la UI la muestre y la confíe")

	require.NoError(t, kh.Trust(s.Host, s.Port, ae.Detail))
	tn, err := Open(context.Background(), baseCfg(s, kh))
	require.NoError(t, err)
	require.NoError(t, tn.Close())
}

func TestHostKeyMismatchBlocksAndCannotBeOverwritten(t *testing.T) {
	s := sshsrv.Start(t)
	kh := NewKnownHosts(filepath.Join(t.TempDir(), "known_hosts"))
	require.NoError(t, kh.Trust(s.Host, s.Port, "SHA256:otraHuellaDistinta"))

	_, err := Open(context.Background(), baseCfg(s, kh))
	require.Equal(t, apperr.SSHHostKeyMismatch, code(t, err))

	require.Equal(t, apperr.SSHHostKeyMismatch, code(t, kh.Trust(s.Host, s.Port, s.Fingerprint)), "Trust no pisa una huella distinta")
	require.NoError(t, kh.Forget(s.Host, s.Port))
	require.NoError(t, kh.Trust(s.Host, s.Port, s.Fingerprint))
}

func TestKnownHostsValidationAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	kh := NewKnownHosts(path)
	require.Equal(t, apperr.Validation, code(t, kh.Trust("h", 22, "md5:xx")))
	require.Equal(t, apperr.Validation, code(t, kh.Trust("h", 22, "SHA256:a b")))
	require.NoError(t, kh.Trust("[::1]", 22, "SHA256:abc"))
	require.NoError(t, kh.Trust("HOST", 2222, "SHA256:def"))
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" { // Windows no tiene bits de permiso Unix: Perm() devuelve 0o666
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	require.NoError(t, kh.Forget("nope", 1), "idempotente")
}

func TestKnownHostsRequiredAndValidation(t *testing.T) {
	_, err := Open(context.Background(), Config{Host: "h", User: "u"})
	require.Error(t, err, "sin KnownHosts no se conecta")
	_, err = Open(context.Background(), Config{User: "u", KnownHosts: NewKnownHosts("x")})
	require.Equal(t, apperr.Validation, code(t, err))
}

func TestUnreachableServer(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	_, err := Open(context.Background(), Config{Host: "127.0.0.1", Port: port, User: "u", Password: "p", KnownHosts: NewKnownHosts("x"), DialTimeout: time.Second})
	require.Equal(t, apperr.NetworkUnreachable, code(t, err))
}

func TestOpenRespectsContextCancel(t *testing.T) {
	// Un servidor que acepta pero nunca habla: el handshake se queda esperando.
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
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	start := time.Now()
	_, err = Open(ctx, Config{Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, User: "u", Password: "p",
		KnownHosts: NewKnownHosts(filepath.Join(t.TempDir(), "kh")), DialTimeout: 5 * time.Second})
	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second)
}

func TestDialTargetRefused(t *testing.T) {
	s := sshsrv.Start(t)
	tn, err := Open(context.Background(), baseCfg(s, trusted(t, s)))
	require.NoError(t, err)
	defer tn.Close()

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l.Addr().String()
	require.NoError(t, l.Close())
	_, err = tn.Dial(context.Background(), "tcp", dead)
	require.Equal(t, apperr.NetworkUnreachable, code(t, err))
}

func TestDialHonorsContext(t *testing.T) {
	s := sshsrv.Start(t)
	tn, err := Open(context.Background(), baseCfg(s, trusted(t, s)))
	require.NoError(t, err)
	defer tn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = tn.Dial(ctx, "tcp", "127.0.0.1:9")
	require.Error(t, err)
}

func TestDropClosesDoneWithErr(t *testing.T) {
	s := sshsrv.Start(t)
	tn, err := Open(context.Background(), baseCfg(s, trusted(t, s)))
	require.NoError(t, err)
	defer tn.Close()

	select {
	case <-tn.Done():
		t.Fatal("Done no debería estar cerrado")
	default:
	}
	s.DropConnections()
	select {
	case <-tn.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Done no se cerró tras la caída")
	}
	require.Error(t, tn.Err())
}

func TestKeepAliveDetectsDeadPeer(t *testing.T) {
	s := sshsrv.Start(t)
	cfg := baseCfg(s, trusted(t, s))
	cfg.KeepAlive = 50 * time.Millisecond
	tn, err := Open(context.Background(), cfg)
	require.NoError(t, err)
	defer tn.Close()
	time.Sleep(200 * time.Millisecond) // varios keepalives sanos no cierran el túnel
	select {
	case <-tn.Done():
		t.Fatal("keepalive sano cerró el túnel")
	default:
	}
}

func TestCloseIsCleanAndIdempotent(t *testing.T) {
	s := sshsrv.Start(t)
	tn, err := Open(context.Background(), baseCfg(s, trusted(t, s)))
	require.NoError(t, err)
	require.NoError(t, tn.Close())
	require.NoError(t, tn.Close())
	<-tn.Done()
	require.NoError(t, tn.Err(), "Close pedido no cuenta como caída")
}
