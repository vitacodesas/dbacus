// Package tunnel abre túneles SSH sin listener local: el tráfico sale por un DialFunc (spec 04).
package tunnel

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/secrets"
)

const (
	DefaultPort      = 22
	DefaultTimeout   = 10 * time.Second
	DefaultKeepAlive = 30 * time.Second
)

type Config struct {
	Host       string
	Port       int
	User       string
	AuthMethod string // "password" | "key"
	KeyPath    string
	Password   secrets.Secret
	Passphrase secrets.Secret
	KnownHosts *KnownHosts

	DialTimeout time.Duration // 0 → DefaultTimeout
	KeepAlive   time.Duration // 0 → DefaultKeepAlive
}

type Tunnel struct {
	client *ssh.Client
	done   chan struct{}
	stop   chan struct{}
	wg     sync.WaitGroup

	mu  sync.Mutex
	err error
}

// Open conecta, verifica la host key y autentica. Respeta ctx durante todo el proceso.
func Open(ctx context.Context, cfg Config) (*Tunnel, error) {
	if cfg.Host == "" || cfg.User == "" {
		return nil, apperr.New(apperr.Validation, "El host y el usuario SSH son obligatorios").WithDetail("ssh")
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultTimeout
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = DefaultKeepAlive
	}
	if cfg.KnownHosts == nil {
		return nil, errors.New("tunnel: KnownHosts es obligatorio (nunca se ignora la verificación de host key)")
	}
	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User: cfg.User,
		Auth: auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			return cfg.KnownHosts.Check(cfg.Host, cfg.Port, key)
		},
		Timeout: cfg.DialTimeout,
	}
	addr := net.JoinHostPort(strings.Trim(cfg.Host, "[]"), strconv.Itoa(cfg.Port))

	d := net.Dialer{Timeout: cfg.DialTimeout}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, mapError(err)
	}

	// ssh.NewClientConn no acepta ctx: cerrar la conexión desbloquea el handshake si se cancela.
	handshakeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = raw.Close()
		case <-handshakeDone:
		}
	}()
	_ = raw.SetDeadline(time.Now().Add(cfg.DialTimeout))
	conn, chans, reqs, err := ssh.NewClientConn(raw, addr, sshCfg)
	close(handshakeDone)
	if err != nil {
		_ = raw.Close()
		if ctx.Err() != nil {
			return nil, mapError(ctx.Err())
		}
		return nil, mapError(err)
	}
	_ = raw.SetDeadline(time.Time{})

	t := &Tunnel{client: ssh.NewClient(conn, chans, reqs), done: make(chan struct{}), stop: make(chan struct{})}
	t.wg.Add(2)
	go t.waitClient()
	go t.keepAlive(cfg.KeepAlive)
	return t, nil
}

// Dial es el DialFunc para el driver: abre un canal direct-tcpip hacia addr desde el servidor SSH.
func (t *Tunnel) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := t.client.Dial(network, addr)
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, mapError(r.err)
		}
		return r.c, nil
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				_ = r.c.Close()
			}
		}()
		return nil, mapError(ctx.Err())
	}
}

// Done se cierra cuando la conexión SSH termina (caída, keepalive fallido o Close).
func (t *Tunnel) Done() <-chan struct{} { return t.done }

// Err devuelve la causa de la caída (nil si fue Close).
func (t *Tunnel) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *Tunnel) Close() error {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	err := t.client.Close()
	t.wg.Wait()
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (t *Tunnel) waitClient() {
	defer t.wg.Done()
	err := t.client.Wait()
	select {
	case <-t.stop: // cierre pedido: no es una caída
	default:
		t.mu.Lock()
		t.err = err
		t.mu.Unlock()
	}
	close(t.done)
}

func (t *Tunnel) keepAlive(every time.Duration) {
	defer t.wg.Done()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-t.done:
			return
		case <-tick.C:
			// SendRequest bloquea si el peer no responde; se resuelve cerrando el cliente tras el timeout.
			reply := make(chan error, 1)
			go func() { _, _, err := t.client.SendRequest("keepalive@openssh.com", true, nil); reply <- err }()
			select {
			case err := <-reply:
				if err != nil {
					_ = t.client.Close()
					return
				}
			case <-time.After(every):
				_ = t.client.Close()
				return
			case <-t.stop:
				return
			}
		}
	}
}

func authMethods(cfg Config) ([]ssh.AuthMethod, error) {
	switch cfg.AuthMethod {
	case "password", "":
		return []ssh.AuthMethod{ssh.Password(cfg.Password.Reveal())}, nil
	case "key":
		pem, err := os.ReadFile(cfg.KeyPath)
		if err != nil {
			return nil, apperr.Wrap(apperr.SSHAuthFailed, "No se pudo leer la clave privada SSH", err).WithDetail("ssh.keyPath")
		}
		var signer ssh.Signer
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(cfg.Passphrase.Reveal()))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		var missing *ssh.PassphraseMissingError
		switch {
		case errors.As(err, &missing):
			return nil, apperr.New(apperr.SSHAuthFailed, "La clave privada está protegida: indica su passphrase").WithDetail("ssh.passphrase")
		case errors.Is(err, x509.IncorrectPasswordError):
			return nil, apperr.New(apperr.SSHAuthFailed, "Passphrase incorrecta").WithDetail("ssh.passphrase")
		case err != nil:
			return nil, apperr.Wrap(apperr.SSHAuthFailed, "La clave privada SSH no es válida", err).WithDetail("ssh.keyPath")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	return nil, apperr.Newf(apperr.Validation, "Método de autenticación SSH desconocido: %q", cfg.AuthMethod).WithDetail("ssh.authMethod")
}

func mapError(err error) error {
	var ae *apperr.AppError
	var och *ssh.OpenChannelError
	var nerr net.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ae): // host key desconocida/cambiada viaja envuelta por el handshake
		return ae
	case errors.Is(err, context.Canceled):
		return apperr.Wrap(apperr.QueryCanceled, "Operación cancelada", err)
	case errors.Is(err, context.DeadlineExceeded):
		return apperr.Wrap(apperr.Timeout, "Tiempo de espera agotado en el túnel SSH", err).AsRetryable()
	case strings.Contains(err.Error(), "unable to authenticate"):
		return apperr.Wrap(apperr.SSHAuthFailed, "El servidor SSH rechazó las credenciales", err)
	case errors.As(err, &och):
		return apperr.Wrap(apperr.NetworkUnreachable, "El servidor SSH no pudo conectar con la base de datos", err).WithDetail(och.Message).AsRetryable()
	case errors.As(err, &nerr) && nerr.Timeout():
		return apperr.Wrap(apperr.Timeout, "Tiempo de espera agotado al conectar por SSH", err).AsRetryable()
	}
	return apperr.Wrap(apperr.NetworkUnreachable, "No se pudo conectar con el servidor SSH", err).AsRetryable()
}
