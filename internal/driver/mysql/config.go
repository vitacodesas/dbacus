package mysql

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
)

const defaultDialTimeout = 10 * time.Second

var (
	paramKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// Opciones del driver Go que cambian su seguridad o semántica: la app no las deja configurar.
	reservedParams = map[string]bool{
		"multistatements": true, "allowallfiles": true, "allowoldpasswords": true, "allowcleartextpasswords": true,
		"parsetime": true, "interpolateparams": true, "columnswithalias": true, "tls": true,
		"allownativepasswords": true, "clientfoundrows": true, "rejectreadonly": true,
	}
)

// Validate comprueba el perfil sin tocar la red (implementa driver.Validator).
func (*Driver) Validate(cfg driver.ConnConfig) error {
	_, err := buildConfig(cfg, nil)
	return err
}

func buildConfig(cfg driver.ConnConfig, dial driver.DialFunc) (*gomysql.Config, error) {
	p := cfg.Profile
	if strings.TrimSpace(p.Host) == "" {
		return nil, apperr.New(apperr.Validation, "El host es obligatorio").WithDetail("host")
	}
	port := p.Port
	if port == 0 {
		port = 3306
	}
	if port < 1 || port > 65535 {
		return nil, apperr.New(apperr.Validation, "El puerto debe estar entre 1 y 65535").WithDetail("port")
	}
	if strings.HasPrefix(p.Host, "/") {
		return nil, apperr.New(apperr.Unsupported, "Los sockets Unix aún no están soportados").WithDetail("host")
	}

	mc := gomysql.NewConfig()
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(strings.Trim(p.Host, "[]"), strconv.Itoa(port))
	mc.User = p.User
	mc.Passwd = cfg.Password.Reveal()
	mc.DBName = p.Database
	mc.Timeout = defaultDialTimeout
	mc.Params = map[string]string{}
	if dial != nil {
		mc.DialFunc = dial
	}

	for k, v := range p.Params {
		switch strings.ToLower(k) {
		case "timeout", "readtimeout", "writetimeout":
			d, err := time.ParseDuration(v)
			if err != nil || d < 0 {
				return nil, apperr.Newf(apperr.Validation, "Duración inválida en «%s»: %q", k, v).WithDetail("params." + k)
			}
			switch strings.ToLower(k) {
			case "timeout":
				mc.Timeout = d
			case "readtimeout":
				mc.ReadTimeout = d
			default:
				mc.WriteTimeout = d
			}
			continue
		}
		if reservedParams[strings.ToLower(k)] {
			return nil, apperr.Newf(apperr.Validation, "El parámetro «%s» no está permitido", k).WithDetail("params." + k)
		}
		// El driver envía "SET k=v" sin escapar: se restringe la forma de clave y valor.
		if !paramKey.MatchString(k) || strings.ContainsAny(v, ";\n\r") {
			return nil, apperr.Newf(apperr.Validation, "Parámetro inválido: «%s»", k).WithDetail("params." + k)
		}
		mc.Params[k] = v
	}

	tlsCfg, fallback, err := buildTLS(p.TLS, strings.Trim(p.Host, "[]"))
	if err != nil {
		return nil, err
	}
	mc.TLS = tlsCfg
	mc.AllowFallbackToPlaintext = fallback
	return mc, nil
}

// buildTLS traduce el modo del perfil a tls.Config. nil = sin TLS. fallback permite texto plano (modo preferred).
func buildTLS(t model.TLSConfig, host string) (cfg *tls.Config, fallback bool, err error) {
	mode := t.Mode
	if mode == "" {
		mode = "preferred"
	}
	switch mode {
	case "disable":
		return nil, false, nil
	case "preferred", "required", "verify-ca", "verify-full":
	default:
		return nil, false, apperr.Newf(apperr.Validation, "Modo TLS desconocido: %q", t.Mode).WithDetail("tls.mode")
	}

	cfg = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}

	if (t.CertPath == "") != (t.KeyPath == "") {
		return nil, false, apperr.New(apperr.Validation, "El certificado y la clave de cliente deben indicarse juntos").WithDetail("tls.certPath")
	}
	if t.CertPath != "" {
		pair, err := tls.LoadX509KeyPair(t.CertPath, t.KeyPath)
		if err != nil {
			return nil, false, apperr.Wrap(apperr.TLSError, "No se pudo leer el certificado de cliente", err).WithDetail("tls.certPath")
		}
		cfg.Certificates = []tls.Certificate{pair}
	}

	var roots *x509.CertPool
	if t.CAPath != "" {
		pem, err := os.ReadFile(t.CAPath)
		if err != nil {
			return nil, false, apperr.Wrap(apperr.TLSError, "No se pudo leer el certificado CA", err).WithDetail("tls.caPath")
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, false, apperr.New(apperr.TLSError, "El archivo CA no contiene certificados PEM válidos").WithDetail("tls.caPath")
		}
	}

	switch mode {
	case "preferred":
		cfg.InsecureSkipVerify = true
		return cfg, true, nil
	case "required":
		cfg.InsecureSkipVerify = true
		return cfg, false, nil
	case "verify-ca":
		// Verifica la cadena pero no el nombre del host.
		cfg.InsecureSkipVerify = true
		cfg.RootCAs = roots
		cfg.VerifyPeerCertificate = verifyChainOnly(roots)
		return cfg, false, nil
	default: // verify-full
		cfg.RootCAs = roots // nil → raíces del sistema
		return cfg, false, nil
	}
}

func verifyChainOnly(roots *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return fmt.Errorf("el servidor no presentó certificado")
		}
		certs := make([]*x509.Certificate, len(raw))
		for i, r := range raw {
			c, err := x509.ParseCertificate(r)
			if err != nil {
				return err
			}
			certs[i] = c
		}
		inter := x509.NewCertPool()
		for _, c := range certs[1:] {
			inter.AddCert(c)
		}
		_, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter})
		return err
	}
}
