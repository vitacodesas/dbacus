package mysql

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"database/sql/driver"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"tabulariswails/internal/apperr"
	drv "tabulariswails/internal/driver"
	"tabulariswails/internal/model"
)

func code(t *testing.T, err error) string {
	t.Helper()
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	return ae.Code
}

func TestRegistered(t *testing.T) {
	d, ok := drv.Get("mysql")
	require.True(t, ok)
	require.Equal(t, 3306, d.DefaultPort())
	require.True(t, d.Capabilities().KillQuery)
}

func TestBuildConfigBasics(t *testing.T) {
	cfg := drv.ConnConfig{Profile: model.ConnectionProfile{Host: "db.local", Port: 3307, User: "u", Database: "d",
		Params: map[string]string{"sql_mode": "ANSI", "timeout": "3s", "readTimeout": "1m"}}, Password: "p@ss"}
	mc, err := buildConfig(cfg, nil)
	require.NoError(t, err)
	require.Equal(t, "db.local:3307", mc.Addr)
	require.Equal(t, "p@ss", mc.Passwd)
	require.Equal(t, 3*time.Second, mc.Timeout)
	require.Equal(t, time.Minute, mc.ReadTimeout)
	require.Equal(t, map[string]string{"sql_mode": "ANSI"}, mc.Params)
	require.False(t, mc.MultiStatements)
	require.False(t, mc.ParseTime)
	require.False(t, mc.ColumnsWithAlias)
}

func TestBuildConfigIPv6AndDefaultPort(t *testing.T) {
	mc, err := buildConfig(drv.ConnConfig{Profile: model.ConnectionProfile{Host: "[::1]"}}, nil)
	require.NoError(t, err)
	require.Equal(t, "[::1]:3306", mc.Addr)
}

func TestBuildConfigInjectsDialer(t *testing.T) {
	called := false
	dial := func(context.Context, string, string) (net.Conn, error) { called = true; return nil, io.EOF }
	mc, err := buildConfig(drv.ConnConfig{Profile: model.ConnectionProfile{Host: "h"}}, dial)
	require.NoError(t, err)
	_, _ = mc.DialFunc(context.Background(), "tcp", "h:1")
	require.True(t, called)
}

func TestBuildConfigValidation(t *testing.T) {
	cases := map[string]model.ConnectionProfile{
		"host vacío":        {Host: " "},
		"puerto alto":       {Host: "h", Port: 70000},
		"socket":            {Host: "/tmp/mysql.sock"},
		"param reservado":   {Host: "h", Params: map[string]string{"multiStatements": "true"}},
		"param tls":         {Host: "h", Params: map[string]string{"tls": "false"}},
		"clave inválida":    {Host: "h", Params: map[string]string{"a b": "1"}},
		"valor con ;":       {Host: "h", Params: map[string]string{"sql_mode": "x; DROP"}},
		"duración inválida": {Host: "h", Params: map[string]string{"timeout": "abc"}},
		"modo tls":          {Host: "h", TLS: model.TLSConfig{Mode: "raro"}},
		"cert sin key":      {Host: "h", TLS: model.TLSConfig{Mode: "required", CertPath: "/x"}},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := buildConfig(drv.ConnConfig{Profile: p}, nil)
			require.Error(t, err)
			require.Contains(t, []string{apperr.Validation, apperr.Unsupported}, code(t, err))
		})
	}
}

func writeCA(t *testing.T) (caPath string, cert *x509.Certificate, key *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err = x509.ParseCertificate(der)
	require.NoError(t, err)
	caPath = filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	return caPath, cert, key
}

func TestBuildTLSModes(t *testing.T) {
	ca, _, _ := writeCA(t)

	c, fb, err := buildTLS(model.TLSConfig{Mode: "disable"}, "h")
	require.NoError(t, err)
	require.Nil(t, c)
	require.False(t, fb)

	c, fb, err = buildTLS(model.TLSConfig{}, "h")
	require.NoError(t, err)
	require.True(t, c.InsecureSkipVerify)
	require.True(t, fb, "preferred permite texto plano")
	require.Equal(t, uint16(tls.VersionTLS12), c.MinVersion)

	c, fb, err = buildTLS(model.TLSConfig{Mode: "required"}, "h")
	require.NoError(t, err)
	require.True(t, c.InsecureSkipVerify)
	require.False(t, fb)

	c, _, err = buildTLS(model.TLSConfig{Mode: "verify-ca", CAPath: ca}, "h")
	require.NoError(t, err)
	require.True(t, c.InsecureSkipVerify)
	require.NotNil(t, c.VerifyPeerCertificate)

	c, _, err = buildTLS(model.TLSConfig{Mode: "verify-full", CAPath: ca}, "db.example")
	require.NoError(t, err)
	require.False(t, c.InsecureSkipVerify)
	require.Equal(t, "db.example", c.ServerName)
	require.NotNil(t, c.RootCAs)
}

func TestBuildTLSBadCA(t *testing.T) {
	_, _, err := buildTLS(model.TLSConfig{Mode: "verify-full", CAPath: "/no/existe"}, "h")
	require.Equal(t, apperr.TLSError, code(t, err))

	bad := filepath.Join(t.TempDir(), "bad.pem")
	require.NoError(t, os.WriteFile(bad, []byte("no es pem"), 0o600))
	_, _, err = buildTLS(model.TLSConfig{Mode: "verify-full", CAPath: bad}, "h")
	require.Equal(t, apperr.TLSError, code(t, err))
}

func TestVerifyChainOnlyIgnoresHostname(t *testing.T) {
	_, caCert, caKey := writeCA(t)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "otro-nombre"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"otro-nombre"}}
	der, err := x509.CreateCertificate(rand.Reader, leafTpl, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)

	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	require.NoError(t, verifyChainOnly(roots)([][]byte{der}, nil))

	other := x509.NewCertPool()
	require.Error(t, verifyChainOnly(other)([][]byte{der}, nil), "CA distinta debe fallar")
	require.Error(t, verifyChainOnly(roots)(nil, nil))
}

func TestMapError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"auth", &gomysql.MySQLError{Number: 1045, Message: "Access denied"}, apperr.AuthFailed},
		{"db inexistente", &gomysql.MySQLError{Number: 1049, Message: "Unknown database"}, apperr.Validation},
		{"sintaxis", &gomysql.MySQLError{Number: 1064, Message: "You have an error"}, apperr.QuerySyntax},
		{"interrumpida", &gomysql.MySQLError{Number: 1317, Message: "interrupted"}, apperr.QueryCanceled},
		{"lock timeout", &gomysql.MySQLError{Number: 1205, Message: "Lock wait"}, apperr.Timeout},
		{"otro", &gomysql.MySQLError{Number: 1146, Message: "no table"}, apperr.QueryFailed},
		{"cancel", context.Canceled, apperr.QueryCanceled},
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), apperr.Timeout},
		{"sin tls", gomysql.ErrNoTLS, apperr.TLSError},
		{"x509", x509.UnknownAuthorityError{}, apperr.TLSError},
		{"bad conn", driver.ErrBadConn, apperr.SessionClosed},
		{"conexión ya cerrada por database/sql", sql.ErrConnDone, apperr.SessionClosed},
		{"eof", io.EOF, apperr.SessionClosed},
		{"dns", &net.DNSError{Err: "no such host", Name: "x"}, apperr.NetworkUnreachable},
		{"refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, apperr.NetworkUnreachable},
		{"ya tipado", apperr.New(apperr.SSHAuthFailed, "ssh"), apperr.SSHAuthFailed},
		{"desconocido", errors.New("raro"), apperr.QueryFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, code(t, mapError(c.err))) })
	}
	require.NoError(t, mapError(nil))
}

func TestMapErrorDoesNotLeakPasswordInMessage(t *testing.T) {
	err := mapError(&gomysql.MySQLError{Number: 1045, Message: "Access denied for user 'u'@'h' (using password: YES)"})
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.NotContains(t, ae.Message, "Access denied for user")
}

func TestParseDSN(t *testing.T) {
	d := &Driver{}
	t.Run("url completa", func(t *testing.T) {
		c, err := d.ParseDSN("mysql://root:p%40ss@db.local:3307/shop?charset=utf8mb4&ssl-mode=VERIFY_IDENTITY")
		require.NoError(t, err)
		p := c.Profile
		require.Equal(t, []any{"db.local", 3307, "root", "shop"}, []any{p.Host, p.Port, p.User, p.Database})
		require.Equal(t, "p@ss", c.Password.Reveal())
		require.Equal(t, "verify-full", p.TLS.Mode)
		require.Equal(t, map[string]string{"charset": "utf8mb4"}, p.Params)
	})
	t.Run("defaults e IPv6", func(t *testing.T) {
		c, err := d.ParseDSN("mariadb://u@[::1]/")
		require.NoError(t, err)
		require.Equal(t, "::1", c.Profile.Host)
		require.Equal(t, 3306, c.Profile.Port)
		require.Empty(t, c.Password)
		require.Empty(t, c.Profile.Database)
	})
	t.Run("formato del driver Go", func(t *testing.T) {
		c, err := d.ParseDSN("u:pw@tcp(10.0.0.5:3310)/app?tls=skip-verify&time_zone=%27%2B00%3A00%27")
		require.NoError(t, err)
		require.Equal(t, "10.0.0.5", c.Profile.Host)
		require.Equal(t, 3310, c.Profile.Port)
		require.Equal(t, "required", c.Profile.TLS.Mode)
		require.Equal(t, "pw", c.Password.Reveal())
	})
	for name, in := range map[string]string{
		"vacío": "", "esquema": "postgres://u@h/db", "sin host": "mysql:///db", "puerto": "mysql://u@h:abc/db",
		"basura": "esto no es un dsn", "unix": "u@unix(/tmp/s.sock)/db",
	} {
		t.Run("inválido "+name, func(t *testing.T) {
			_, err := d.ParseDSN(in)
			require.Error(t, err)
			require.Contains(t, []string{apperr.Validation, apperr.Unsupported}, code(t, err))
		})
	}
}

func TestQuoteIdent(t *testing.T) {
	require.Equal(t, "`a`", QuoteIdent("a"))
	require.Equal(t, "`a``b; DROP`", QuoteIdent("a`b; DROP"))
}

func TestOpenDoesNotTouchNetwork(t *testing.T) {
	p, err := (&Driver{}).Open(context.Background(), drv.ConnConfig{Profile: model.ConnectionProfile{Host: "203.0.113.1"}}, nil)
	require.NoError(t, err)
	require.NoError(t, p.Close())
}

func TestPingUnreachableMapsToNetworkError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close()) // puerto cerrado

	p, err := (&Driver{}).Open(context.Background(), drv.ConnConfig{Profile: model.ConnectionProfile{Host: "127.0.0.1", Port: port, TLS: model.TLSConfig{Mode: "disable"}}}, nil)
	require.NoError(t, err)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.Equal(t, apperr.NetworkUnreachable, code(t, p.Ping(ctx)))
}

func TestMariaDefault(t *testing.T) {
	require.Equal(t, "anon", mariaDefault("'anon'"))
	require.Equal(t, "it's", mariaDefault("'it''s'"))
	require.Equal(t, "current_timestamp(6)", mariaDefault("current_timestamp(6)"))
	require.Equal(t, "0", mariaDefault("0"))
	require.Equal(t, "'", mariaDefault("'"))
}
