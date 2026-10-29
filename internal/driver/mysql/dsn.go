package mysql

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/secrets"
)

// ParseDSN acepta URLs (mysql://u:p@h:3306/db?x=y, mariadb://…) y el formato del driver Go (u:p@tcp(h:3306)/db?x=y).
func (*Driver) ParseDSN(dsn string) (driver.ConnConfig, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return driver.ConnConfig{}, invalidDSN("vacío")
	}
	if strings.Contains(dsn, "://") {
		return parseURL(dsn)
	}
	return parseGoDSN(dsn)
}

func invalidDSN(why string) error {
	return apperr.Newf(apperr.Validation, "La cadena de conexión no es válida (%s)", why).WithDetail("dsn")
}

func parseURL(raw string) (driver.ConnConfig, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return driver.ConnConfig{}, invalidDSN("URL mal formada")
	}
	if u.Scheme != "mysql" && u.Scheme != "mariadb" {
		return driver.ConnConfig{}, invalidDSN("esquema «" + u.Scheme + "» no soportado")
	}
	if u.Hostname() == "" {
		return driver.ConnConfig{}, invalidDSN("falta el host")
	}
	port := 3306
	if ps := u.Port(); ps != "" {
		if port, err = strconv.Atoi(ps); err != nil || port < 1 || port > 65535 {
			return driver.ConnConfig{}, invalidDSN("puerto inválido")
		}
	}
	cfg := newConfig(u.Hostname(), port)
	if u.User != nil {
		cfg.Profile.User = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			cfg.Password = secrets.Secret(pw)
		}
	}
	cfg.Profile.Database = strings.TrimPrefix(u.Path, "/")
	for k, vs := range u.Query() {
		applyParam(&cfg, k, vs[len(vs)-1])
	}
	return cfg, nil
}

func parseGoDSN(raw string) (driver.ConnConfig, error) {
	mc, err := gomysql.ParseDSN(raw)
	if err != nil {
		return driver.ConnConfig{}, invalidDSN("formato no reconocido")
	}
	if mc.Net != "tcp" {
		return driver.ConnConfig{}, apperr.New(apperr.Unsupported, "Solo se soportan conexiones TCP").WithDetail("dsn")
	}
	host, portStr, err := net.SplitHostPort(mc.Addr)
	if err != nil {
		host, portStr = mc.Addr, "3306"
	}
	port, _ := strconv.Atoi(portStr)
	cfg := newConfig(host, port)
	cfg.Profile.User, cfg.Profile.Database = mc.User, mc.DBName
	cfg.Password = secrets.Secret(mc.Passwd)
	if mc.TLSConfig != "" {
		applyParam(&cfg, "tls", mc.TLSConfig)
	}
	for k, v := range mc.Params {
		applyParam(&cfg, k, v)
	}
	return cfg, nil
}

func newConfig(host string, port int) driver.ConnConfig {
	return driver.ConnConfig{Profile: model.ConnectionProfile{
		Driver: "mysql", Host: host, Port: port,
		TLS: model.TLSConfig{Mode: "preferred"}, Params: map[string]string{},
	}}
}

// applyParam separa los parámetros de TLS del resto, que quedan como Params de sesión.
func applyParam(cfg *driver.ConnConfig, k, v string) {
	switch strings.ToLower(k) {
	case "tls", "ssl-mode", "sslmode", "ssl_mode":
		if m := tlsModeFromDSN(v); m != "" {
			cfg.Profile.TLS.Mode = m
			return
		}
	case "ssl-ca", "sslca":
		cfg.Profile.TLS.CAPath = v
		return
	case "ssl-cert", "sslcert":
		cfg.Profile.TLS.CertPath = v
		return
	case "ssl-key", "sslkey":
		cfg.Profile.TLS.KeyPath = v
		return
	}
	cfg.Profile.Params[k] = v
}

func tlsModeFromDSN(v string) string {
	switch strings.ToLower(strings.ReplaceAll(v, "_", "-")) {
	case "false", "disable", "disabled":
		return "disable"
	case "preferred", "prefer":
		return "preferred"
	case "skip-verify", "required", "require":
		return "required"
	case "verify-ca":
		return "verify-ca"
	case "true", "verify-full", "verify-identity":
		return "verify-full"
	}
	return ""
}
