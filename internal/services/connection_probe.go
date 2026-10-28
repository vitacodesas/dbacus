package services

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/model"
	"tabulariswails/internal/secrets"
	"tabulariswails/internal/tunnel"
)

const (
	testTimeout = 15 * time.Second
	tcpTimeout  = 5 * time.Second
)

// Nombres de paso de TestResult (spec 02/04).
const (
	StepSSH   = "ssh"
	StepTCP   = "tcp"
	StepTLS   = "tls"
	StepAuth  = "auth"
	StepQuery = "query"
)

type TestStep struct {
	Name      string           `json:"name"`
	OK        bool             `json:"ok"`
	Skipped   bool             `json:"skipped"`
	ElapsedMs int64            `json:"elapsedMs"`
	Error     *apperr.AppError `json:"error"`
}

type TestResult struct {
	OK            bool             `json:"ok"`
	LatencyMs     int64            `json:"latencyMs"`
	ServerVersion string           `json:"serverVersion"`
	Steps         []TestStep       `json:"steps"`
	Error         *apperr.AppError `json:"error"`
}

// Test prueba una configuración sin guardar nada. Devuelve error solo si el perfil es inválido; los fallos de
// conexión viajan dentro del TestResult con el paso que falló. Cancelable y con tope de 15 s.
func (s *ConnectionService) Test(ctx context.Context, profile model.ConnectionProfile, sec ConnectionSecrets) (TestResult, error) {
	if err := s.normalizeAndValidate(&profile, sec, nil); err != nil {
		return TestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()

	password, sshPassword, passphrase, err := s.testSecrets(ctx, profile, sec)
	if err != nil {
		return TestResult{}, err
	}

	run := newProbe(profile)
	drv, _ := driver.Get(profile.Driver)

	var dial driver.DialFunc
	if profile.SSH.Enabled {
		var tn *tunnel.Tunnel
		if !run.step(StepSSH, func() error {
			var err error
			tn, err = tunnel.Open(ctx, tunnel.Config{
				Host: profile.SSH.Host, Port: profile.SSH.Port, User: profile.SSH.User, AuthMethod: profile.SSH.AuthMethod,
				KeyPath: profile.SSH.KeyPath, Password: sshPassword, Passphrase: passphrase, KnownHosts: s.d.KnownHosts,
			})
			return err
		}) {
			return run.finish(), nil
		}
		defer tn.Close()
		dial = tn.Dial
	}

	target := net.JoinHostPort(strings.Trim(profile.Host, "[]"), strconv.Itoa(profile.Port))
	if !run.step(StepTCP, func() error { return probeTCP(ctx, dial, target, profile) }) {
		return run.finish(), nil
	}

	pool, err := drv.Open(ctx, driver.ConnConfig{Profile: profile, Password: password}, dial)
	if err != nil {
		run.fail(StepAuth, apperr.From(err))
		return run.finish(), nil
	}
	defer pool.Close()

	// TLS y autenticación ocurren dentro del mismo handshake del protocolo: se atribuyen por el tipo de error.
	started := time.Now()
	if perr := pool.Ping(ctx); perr != nil {
		ae := apperr.From(perr)
		switch ae.Code {
		case apperr.TLSError:
			run.record(StepTLS, false, started, ae)
		case apperr.AuthFailed, apperr.Validation:
			run.record(StepTLS, true, started, nil)
			run.record(StepAuth, false, started, ae)
		default:
			run.record(StepAuth, false, started, ae)
		}
		return run.finish(), nil
	}
	run.record(StepTLS, true, started, nil)
	run.record(StepAuth, true, started, nil)

	if !run.step(StepQuery, func() error {
		v, err := pool.ServerVersion(ctx)
		run.result.ServerVersion = v
		return err
	}) {
		return run.finish(), nil
	}
	t0 := time.Now()
	if err := pool.Ping(ctx); err == nil {
		run.result.LatencyMs = time.Since(t0).Milliseconds()
	}
	return run.finish(), nil
}

// testSecrets: lo escrito en el formulario manda; si el campo está vacío y el perfil existe, se usa el llavero.
// Una contraseña ausente es una contraseña vacía (hay servidores sin ella); no se exige PASSWORD_REQUIRED aquí.
func (s *ConnectionService) testSecrets(ctx context.Context, p model.ConnectionProfile, sec ConnectionSecrets) (pw, sshPw, pass secrets.Secret, err error) {
	var stored model.ConnectionProfile
	if p.ID != "" {
		if cur, gerr := s.d.Store.GetProfile(ctx, p.ID); gerr == nil {
			stored = cur
		}
	}
	get := func(kind secrets.Kind, override *string, has bool) (secrets.Secret, error) {
		if override != nil {
			return secrets.Secret(*override), nil
		}
		if has {
			v, ok, err := s.d.Secrets.Get(p.ID, kind)
			if err != nil {
				return "", toAppErr(err)
			}
			if ok {
				return v, nil
			}
		}
		return "", nil
	}
	if pw, err = get(secrets.Password, sec.Password, stored.HasPassword); err != nil {
		return
	}
	if sshPw, err = get(secrets.SSHPassword, sec.SSHPassword, stored.HasSSHPassword); err != nil {
		return
	}
	pass, err = get(secrets.SSHPassphrase, sec.SSHKeyPassphrase, stored.HasSSHPassphrase)
	return
}

// probeTCP comprueba que host:puerto acepta conexiones (por el túnel si lo hay) y da un mensaje distinto por causa.
func probeTCP(ctx context.Context, dial driver.DialFunc, target string, p model.ConnectionProfile) error {
	ctx, cancel := context.WithTimeout(ctx, tcpTimeout)
	defer cancel()
	if dial == nil {
		d := net.Dialer{}
		dial = d.DialContext
	}
	c, err := dial(ctx, "tcp", target)
	if err == nil {
		return c.Close()
	}
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		if p.SSH.Enabled && ae.Code == apperr.NetworkUnreachable {
			return apperr.Wrap(apperr.NetworkUnreachable, "El servidor SSH no pudo conectar con "+target+": ¿host y puerto correctos desde allí?", err)
		}
		return ae
	}
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns):
		return apperr.Wrap(apperr.NetworkUnreachable, "No se pudo resolver el host «"+p.Host+"»", err)
	case errors.Is(err, syscall.ECONNREFUSED):
		return apperr.Wrap(apperr.NetworkUnreachable, "Conexión rechazada: el puerto "+strconv.Itoa(p.Port)+" está cerrado o el servidor no escucha ahí", err)
	case errors.Is(err, context.Canceled):
		return apperr.Wrap(apperr.QueryCanceled, "Prueba cancelada", err)
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return apperr.Wrap(apperr.Timeout, "El host no responde: revisa el firewall, la red o la dirección", err).AsRetryable()
	}
	return apperr.Wrap(apperr.NetworkUnreachable, "No se pudo establecer la conexión TCP con "+target, err)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// probe acumula los pasos de una prueba.
type probe struct {
	result TestResult
	index  map[string]int
}

func newProbe(p model.ConnectionProfile) *probe {
	names := []string{}
	if p.SSH.Enabled {
		names = append(names, StepSSH)
	}
	names = append(names, StepTCP)
	if p.TLS.Mode != "disable" {
		names = append(names, StepTLS)
	}
	names = append(names, StepAuth, StepQuery)

	pr := &probe{index: map[string]int{}}
	for i, n := range names {
		pr.result.Steps = append(pr.result.Steps, TestStep{Name: n, Skipped: true})
		pr.index[n] = i
	}
	return pr
}

// step ejecuta fn y registra el resultado; devuelve false si falló (los pasos siguientes quedan omitidos).
func (p *probe) step(name string, fn func() error) bool {
	start := time.Now()
	if err := fn(); err != nil {
		p.record(name, false, start, apperr.From(err))
		return false
	}
	p.record(name, true, start, nil)
	return true
}

func (p *probe) fail(name string, ae *apperr.AppError) { p.record(name, false, time.Now(), ae) }

func (p *probe) record(name string, ok bool, start time.Time, ae *apperr.AppError) {
	i, found := p.index[name]
	if !found { // p. ej. TLS desactivado: no hay paso que anotar
		if !ok && ae != nil && p.result.Error == nil {
			p.result.Error = ae
		}
		return
	}
	p.result.Steps[i] = TestStep{Name: name, OK: ok, ElapsedMs: time.Since(start).Milliseconds(), Error: ae}
	if !ok && p.result.Error == nil {
		p.result.Error = ae
	}
}

func (p *probe) finish() TestResult {
	p.result.OK = p.result.Error == nil
	return p.result
}
