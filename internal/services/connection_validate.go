package services

import (
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
)

const maxNameLen = 100

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func invalid(field, msg string) error {
	return apperr.New(apperr.Validation, msg).WithDetail(field)
}

// validateGroup recorta y valida un grupo (spec 17). La unicidad del nombre la resuelve el almacén.
func validateGroup(g *model.ConnectionGroup) error {
	g.Name = strings.TrimSpace(g.Name)
	g.Color = strings.TrimSpace(g.Color)
	if g.Name == "" {
		return invalid("name", "El nombre del grupo es obligatorio")
	}
	if len([]rune(g.Name)) > maxNameLen {
		return invalid("name", "El nombre del grupo es demasiado largo")
	}
	if g.Color != "" && !colorRe.MatchString(g.Color) {
		return invalid("color", "El color debe tener formato #RRGGBB")
	}
	return nil
}

// normalizeAndValidate recorta y completa valores por defecto y valida el perfil (spec 04). Es la validación autoritativa:
// la del formulario es solo UX. El campo erróneo viaja en AppError.Detail con la ruta del DTO ("tls.caPath").
func (s *ConnectionService) normalizeAndValidate(p *model.ConnectionProfile, _ ConnectionSecrets, _ *model.ConnectionProfile) error {
	p.Name = strings.TrimSpace(p.Name)
	p.Host = strings.TrimSpace(p.Host)
	p.User = strings.TrimSpace(p.User)
	p.Database = strings.TrimSpace(p.Database)
	p.Color = strings.TrimSpace(p.Color)
	p.GroupID = strings.TrimSpace(p.GroupID)

	if p.Name == "" {
		return invalid("name", "El nombre es obligatorio")
	}
	if len([]rune(p.Name)) > maxNameLen {
		return invalid("name", "El nombre es demasiado largo")
	}
	drv, ok := driver.Get(p.Driver)
	if !ok {
		return invalid("driver", "El tipo de base de datos no está soportado")
	}
	if p.Host == "" {
		return invalid("host", "El host es obligatorio")
	}
	if strings.IndexFunc(p.Host, unicode.IsSpace) >= 0 {
		return invalid("host", "El host no puede contener espacios")
	}
	if p.Port == 0 {
		p.Port = drv.DefaultPort()
	}
	if p.Port < 1 || p.Port > 65535 {
		return invalid("port", "El puerto debe estar entre 1 y 65535")
	}
	if p.User == "" {
		return invalid("user", "El usuario es obligatorio")
	}
	if p.Color != "" && !colorRe.MatchString(p.Color) {
		return invalid("color", "El color debe tener formato #RRGGBB")
	}
	if p.Params == nil {
		p.Params = map[string]string{}
	}

	if err := validateTLS(&p.TLS); err != nil {
		return err
	}
	if err := validateSSH(&p.SSH); err != nil {
		return err
	}
	if v, ok := drv.(driver.Validator); ok {
		if err := v.Validate(driver.ConnConfig{Profile: *p}); err != nil {
			return toAppErr(err)
		}
	}
	return nil
}

func validateTLS(t *model.TLSConfig) error {
	if t.Mode == "" {
		t.Mode = "preferred"
	}
	switch t.Mode {
	case "disable", "preferred", "required", "verify-ca", "verify-full":
	default:
		return invalid("tls.mode", "Modo TLS desconocido")
	}
	if t.Mode == "verify-ca" && t.CAPath == "" {
		return invalid("tls.caPath", "El modo verify-ca necesita un certificado CA")
	}
	for _, f := range []struct{ path, field, label string }{
		{t.CAPath, "tls.caPath", "El certificado CA"},
		{t.CertPath, "tls.certPath", "El certificado de cliente"},
		{t.KeyPath, "tls.keyPath", "La clave de cliente"},
	} {
		if err := checkReadableFile(f.path, f.field, f.label); err != nil {
			return err
		}
	}
	if (t.CertPath == "") != (t.KeyPath == "") {
		return invalid("tls.certPath", "El certificado y la clave de cliente deben indicarse juntos")
	}
	return nil
}

func validateSSH(c *model.SSHConfig) error {
	if !c.Enabled {
		return nil
	}
	c.Host, c.User = strings.TrimSpace(c.Host), strings.TrimSpace(c.User)
	if c.Host == "" {
		return invalid("ssh.host", "El host SSH es obligatorio")
	}
	if strings.IndexFunc(c.Host, unicode.IsSpace) >= 0 {
		return invalid("ssh.host", "El host SSH no puede contener espacios")
	}
	if c.Port == 0 {
		c.Port = 22
	}
	if c.Port < 1 || c.Port > 65535 {
		return invalid("ssh.port", "El puerto SSH debe estar entre 1 y 65535")
	}
	if c.User == "" {
		return invalid("ssh.user", "El usuario SSH es obligatorio")
	}
	if c.AuthMethod == "" {
		c.AuthMethod = "password"
	}
	switch c.AuthMethod {
	case "password":
	case "key":
		if c.KeyPath == "" {
			return invalid("ssh.keyPath", "Indica la clave privada SSH")
		}
		return checkReadableFile(c.KeyPath, "ssh.keyPath", "La clave privada SSH")
	default:
		return invalid("ssh.authMethod", "Método de autenticación SSH desconocido")
	}
	return nil
}

func checkReadableFile(path, field, label string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return invalid(field, label+" no existe o no es accesible")
	}
	if info.IsDir() {
		return invalid(field, label+" debe ser un archivo, no una carpeta")
	}
	f, err := os.Open(path)
	if err != nil {
		return invalid(field, label+" no se puede leer")
	}
	return f.Close()
}
