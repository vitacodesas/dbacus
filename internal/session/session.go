// Package session gestiona sesiones activas: pool por perfil, túnel SSH opcional y una conexión dedicada por pestaña (ADR-004).
package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/model"
	"tabulariswails/internal/schemacache"
	"tabulariswails/internal/secrets"
	"tabulariswails/internal/tunnel"
)

const (
	DefaultMaxTabs = 10

	ReasonUser       = "user"
	ReasonShutdown   = "shutdown"
	ReasonTunnelDown = "ssh_tunnel_down"
)

type Options struct {
	KnownHosts *tunnel.KnownHosts
	MaxTabs    int // 0 → DefaultMaxTabs
	// OnClosed se invoca cuando una sesión se cierra por causas ajenas al usuario (p. ej. caída del túnel).
	OnClosed func(sessionID, reason string)
}

type OpenRequest struct {
	Profile       model.ConnectionProfile
	Password      secrets.Secret
	SSHPassword   secrets.Secret
	SSHPassphrase secrets.Secret
}

type Manager struct {
	opts Options

	mu       sync.Mutex
	sessions map[string]*Session
	wg       sync.WaitGroup // watchers de túneles
}

func NewManager(opts Options) *Manager {
	if opts.MaxTabs <= 0 {
		opts.MaxTabs = DefaultMaxTabs
	}
	return &Manager{opts: opts, sessions: map[string]*Session{}}
}

type Session struct {
	ID              string
	ProfileID       string
	ServerVersion   string
	CurrentDatabase string
	Pool            driver.Pool
	Driver          driver.Driver
	// Schema cachea las lecturas de esquema de esta sesión; muere con ella.
	Schema *schemacache.Cache

	m      *Manager
	ctx    context.Context // se cancela al cerrar la sesión: aborta cargas de esquema en curso
	cancel context.CancelFunc
	tunnel *tunnel.Tunnel

	mu     sync.Mutex
	closed bool
	tabs   map[string]*tabEntry
}

type tabEntry struct {
	ready chan struct{}
	conn  driver.Conn
	err   error
}

// Open abre túnel (si aplica), pool, verifica con Ping y registra la sesión. Ante cualquier fallo libera todo lo abierto.
func (m *Manager) Open(ctx context.Context, req OpenRequest) (*Session, error) {
	drv, ok := driver.Get(req.Profile.Driver)
	if !ok {
		return nil, apperr.Newf(apperr.Unsupported, "Driver no soportado: %q", req.Profile.Driver)
	}

	var (
		tn   *tunnel.Tunnel
		dial driver.DialFunc
	)
	if ssh := req.Profile.SSH; ssh.Enabled {
		if m.opts.KnownHosts == nil {
			return nil, apperr.New(apperr.Internal, "Falta el almacén de host keys SSH")
		}
		var err error
		tn, err = tunnel.Open(ctx, tunnel.Config{
			Host: ssh.Host, Port: ssh.Port, User: ssh.User, AuthMethod: ssh.AuthMethod, KeyPath: ssh.KeyPath,
			Password: req.SSHPassword, Passphrase: req.SSHPassphrase, KnownHosts: m.opts.KnownHosts,
		})
		if err != nil {
			return nil, err
		}
		dial = tn.Dial
	}
	cleanupTunnel := func() {
		if tn != nil {
			_ = tn.Close()
		}
	}

	pool, err := drv.Open(ctx, driver.ConnConfig{Profile: req.Profile, Password: req.Password}, dial)
	if err != nil {
		cleanupTunnel()
		return nil, err
	}
	fail := func(err error) (*Session, error) {
		_ = pool.Close()
		cleanupTunnel()
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return fail(err)
	}
	version, err := pool.ServerVersion(ctx)
	if err != nil {
		return fail(err)
	}

	sctx, scancel := context.WithCancel(context.Background())
	s := &Session{
		ctx: sctx, cancel: scancel, Schema: schemacache.New(schemacache.DefaultTTL),
		ID: uuid.NewString(), ProfileID: req.Profile.ID, ServerVersion: version, CurrentDatabase: req.Profile.Database,
		Pool: pool, Driver: drv, m: m, tunnel: tn, tabs: map[string]*tabEntry{},
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	if tn != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			<-tn.Done()
			// Si Close ya la retiró, no hay nada que hacer.
			if m.close(s.ID, ReasonTunnelDown) == nil {
				slog.Warn("túnel SSH caído; sesión cerrada", "session", s.ID, "err", tn.Err())
			}
		}()
	}
	return s, nil
}

// List devuelve las sesiones activas.
func (m *Manager) List() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, apperr.New(apperr.SessionClosed, "La sesión ya no está activa")
	}
	return s, nil
}

// Close cierra una sesión a petición del usuario.
func (m *Manager) Close(id string) error { return m.close(id, ReasonUser) }

// CloseByProfile cierra todas las sesiones de un perfil (p. ej. al eliminarlo).
func (m *Manager) CloseByProfile(profileID string) {
	m.mu.Lock()
	var ids []string
	for id, s := range m.sessions {
		if s.ProfileID == profileID {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.close(id, ReasonUser)
	}
}

// CloseAll cierra todo y espera a los watchers; úsalo en el shutdown de la app.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.close(id, ReasonShutdown)
	}
	m.wg.Wait()
}

func (m *Manager) close(id, reason string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return apperr.New(apperr.SessionClosed, "La sesión ya no está activa")
	}

	s.cancel()
	s.mu.Lock()
	s.closed = true
	tabs := s.tabs
	s.tabs = map[string]*tabEntry{}
	s.mu.Unlock()

	var errs []error
	for _, t := range tabs {
		<-t.ready // una adquisición en curso termina antes de cerrar
		if t.conn != nil {
			errs = append(errs, t.conn.Release())
		}
	}
	errs = append(errs, s.Pool.Close())
	if s.tunnel != nil {
		errs = append(errs, s.tunnel.Close())
	}
	if m.opts.OnClosed != nil && reason != ReasonUser && reason != ReasonShutdown {
		m.opts.OnClosed(id, reason)
	}
	return errors.Join(errs...)
}

// Context vive mientras la sesión esté abierta.
func (s *Session) Context() context.Context { return s.ctx }

// AcquireConn devuelve la conexión dedicada de la pestaña, creándola la primera vez.
// Llamadas concurrentes para la misma pestaña comparten una sola adquisición.
func (s *Session) AcquireConn(ctx context.Context, tabID string) (driver.Conn, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, apperr.New(apperr.SessionClosed, "La sesión ya no está activa")
	}
	if e, ok := s.tabs[tabID]; ok {
		s.mu.Unlock()
		return waitEntry(ctx, e)
	}
	if len(s.tabs) >= s.m.opts.MaxTabs {
		s.mu.Unlock()
		return nil, apperr.Newf(apperr.Validation, "Hay demasiadas pestañas abiertas en esta conexión (máx. %d)", s.m.opts.MaxTabs)
	}
	e := &tabEntry{ready: make(chan struct{})}
	s.tabs[tabID] = e
	s.mu.Unlock()

	e.conn, e.err = s.Pool.Acquire(ctx)
	if e.err != nil {
		s.mu.Lock()
		if s.tabs[tabID] == e {
			delete(s.tabs, tabID)
		}
		s.mu.Unlock()
	}
	close(e.ready)
	return e.conn, e.err
}

func waitEntry(ctx context.Context, e *tabEntry) (driver.Conn, error) {
	select {
	case <-e.ready:
		return e.conn, e.err
	case <-ctx.Done():
		return nil, apperr.Wrap(apperr.Timeout, "Se agotó el tiempo esperando la conexión de la pestaña", ctx.Err())
	}
}

// TabConn devuelve la conexión de la pestaña si ya existe, sin crearla.
func (s *Session) TabConn(tabID string) (driver.Conn, bool) {
	s.mu.Lock()
	e, ok := s.tabs[tabID]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	select {
	case <-e.ready:
		return e.conn, e.conn != nil
	default:
		return nil, false
	}
}

// CloseTab libera la conexión de la pestaña (idempotente).
func (s *Session) CloseTab(tabID string) error {
	s.mu.Lock()
	e, ok := s.tabs[tabID]
	delete(s.tabs, tabID)
	s.mu.Unlock()
	if !ok {
		return nil
	}
	<-e.ready
	if e.conn == nil {
		return nil
	}
	return e.conn.Release()
}

// TabCount es el número de pestañas con conexión (o en proceso de obtenerla).
func (s *Session) TabCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tabs)
}
