package services

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/model"
	"tabulariswails/internal/secrets"
	"tabulariswails/internal/session"
	"tabulariswails/internal/tunnel"
)

// ProfileStore es lo que ConnectionService necesita de la persistencia (lo implementa *store.Store).
type ProfileStore interface {
	ListProfiles(ctx context.Context) ([]model.ConnectionProfile, error)
	GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error)
	SaveProfile(ctx context.Context, p model.ConnectionProfile) (model.ConnectionProfile, error)
	DeleteProfile(ctx context.Context, id string) error
	ClearHistory(ctx context.Context, profileID string) error
}

// ConnectionSecrets viaja solo de Vue a Go. Un puntero nil significa "sin cambios"; Clear nombra los que se borran.
type ConnectionSecrets struct {
	Password         *string  `json:"password"`
	SSHPassword      *string  `json:"sshPassword"`
	SSHKeyPassphrase *string  `json:"sshKeyPassphrase"`
	Clear            []string `json:"clear"` // "password" | "sshPassword" | "sshKeyPassphrase"
}

type SessionInfo struct {
	SessionID       string `json:"sessionId"`
	ProfileID       string `json:"profileId"`
	ServerVersion   string `json:"serverVersion"`
	CurrentDatabase string `json:"currentDatabase"`
}

type SessionClosed struct {
	SessionID string `json:"sessionId"`
	Reason    string `json:"reason"`
}

type DriverInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	DefaultPort int    `json:"defaultPort"`
	SSL         bool   `json:"ssl"`
}

type ParsedDSN struct {
	Profile model.ConnectionProfile `json:"profile"`
	Secrets ConnectionSecrets       `json:"secrets"`
}

type ConnectionDeps struct {
	Store      ProfileStore
	Secrets    secrets.Store
	Sessions   *session.Manager
	KnownHosts *tunnel.KnownHosts
	// PickFile abre el diálogo nativo de archivo; "" si se cancela.
	PickFile func(title string) (string, error)
}

// ConnectionService expone la gestión de perfiles y sesiones a Vue (spec 02/04).
type ConnectionService struct{ d ConnectionDeps }

func NewConnectionService(d ConnectionDeps) *ConnectionService { return &ConnectionService{d: d} }

func init() { application.RegisterEvent[SessionClosed]("session:closed") }

func (s *ConnectionService) ListDrivers() []DriverInfo {
	var out []DriverInfo
	for _, d := range driver.List() {
		out = append(out, DriverInfo{Name: d.Name(), DisplayName: d.DisplayName(), DefaultPort: d.DefaultPort(), SSL: d.Capabilities().SSL})
	}
	return out
}

func (s *ConnectionService) List(ctx context.Context) ([]model.ConnectionProfile, error) {
	ps, err := s.d.Store.ListProfiles(ctx)
	return ps, toAppErr(err)
}

func (s *ConnectionService) Get(ctx context.Context, id string) (model.ConnectionProfile, error) {
	p, err := s.d.Store.GetProfile(ctx, id)
	return p, toAppErr(err)
}

// Save crea (ID vacío) o actualiza un perfil y aplica los cambios de secretos.
func (s *ConnectionService) Save(ctx context.Context, profile model.ConnectionProfile, sec ConnectionSecrets) (model.ConnectionProfile, error) {
	var existing *model.ConnectionProfile
	if profile.ID != "" {
		cur, err := s.d.Store.GetProfile(ctx, profile.ID)
		if err != nil {
			return model.ConnectionProfile{}, toAppErr(err)
		}
		existing = &cur
	}
	// Los flags de secretos los controla el servidor.
	profile.HasPassword, profile.HasSSHPassword, profile.HasSSHPassphrase = false, false, false
	if existing != nil {
		profile.HasPassword, profile.HasSSHPassword, profile.HasSSHPassphrase = existing.HasPassword, existing.HasSSHPassword, existing.HasSSHPassphrase
	}
	if err := s.normalizeAndValidate(&profile, sec, existing); err != nil {
		return model.ConnectionProfile{}, err
	}

	if existing != nil { // secretos primero: si el llavero falla, el perfil no cambia
		if err := s.applySecrets(profile.ID, &profile, sec); err != nil {
			return model.ConnectionProfile{}, err
		}
		saved, err := s.d.Store.SaveProfile(ctx, profile)
		return saved, toAppErr(err)
	}

	saved, err := s.d.Store.SaveProfile(ctx, profile)
	if err != nil {
		return model.ConnectionProfile{}, toAppErr(err)
	}
	if err := s.applySecrets(saved.ID, &saved, sec); err != nil {
		_ = s.d.Store.DeleteProfile(ctx, saved.ID) // un perfil nuevo no debe quedar a medias
		_ = s.d.Secrets.DeleteAll(saved.ID)
		return model.ConnectionProfile{}, err
	}
	if saved.HasPassword != profile.HasPassword || saved.HasSSHPassword != profile.HasSSHPassword || saved.HasSSHPassphrase != profile.HasSSHPassphrase {
		saved, err = s.d.Store.SaveProfile(ctx, saved)
		if err != nil {
			return model.ConnectionProfile{}, toAppErr(err)
		}
	}
	return saved, nil
}

// applySecrets aplica sec al llavero y deja en p los flags resultantes.
func (s *ConnectionService) applySecrets(id string, p *model.ConnectionProfile, sec ConnectionSecrets) error {
	kinds := []struct {
		kind  secrets.Kind
		name  string
		value *string
		flag  *bool
	}{
		{secrets.Password, "password", sec.Password, &p.HasPassword},
		{secrets.SSHPassword, "sshPassword", sec.SSHPassword, &p.HasSSHPassword},
		{secrets.SSHPassphrase, "sshKeyPassphrase", sec.SSHKeyPassphrase, &p.HasSSHPassphrase},
	}
	if !p.SavePassword { // "pedir cada vez": nada persiste
		if p.HasPassword || p.HasSSHPassword || p.HasSSHPassphrase {
			if err := s.d.Secrets.DeleteAll(id); err != nil {
				return err
			}
		}
		p.HasPassword, p.HasSSHPassword, p.HasSSHPassphrase = false, false, false
		return nil
	}
	for _, k := range kinds {
		switch {
		case contains(sec.Clear, k.name):
			if err := s.d.Secrets.Delete(id, k.kind); err != nil {
				return err
			}
			*k.flag = false
		case k.value != nil:
			if err := s.d.Secrets.Set(id, k.kind, secrets.Secret(*k.value)); err != nil {
				return err
			}
			*k.flag = true
		}
	}
	return nil
}

func (s *ConnectionService) Clone(ctx context.Context, id string) (model.ConnectionProfile, error) {
	src, err := s.d.Store.GetProfile(ctx, id)
	if err != nil {
		return model.ConnectionProfile{}, toAppErr(err)
	}
	all, err := s.d.Store.ListProfiles(ctx)
	if err != nil {
		return model.ConnectionProfile{}, toAppErr(err)
	}
	names := map[string]bool{}
	for _, p := range all {
		names[strings.ToLower(p.Name)] = true
	}
	name := src.Name + " (copia)"
	for i := 2; names[strings.ToLower(name)]; i++ {
		name = fmt.Sprintf("%s (copia %d)", src.Name, i)
	}

	clone := src
	clone.ID, clone.Name = "", name
	saved, err := s.d.Store.SaveProfile(ctx, clone)
	if err != nil {
		return model.ConnectionProfile{}, toAppErr(err)
	}
	for _, k := range []secrets.Kind{secrets.Password, secrets.SSHPassword, secrets.SSHPassphrase} {
		v, ok, err := s.d.Secrets.Get(src.ID, k)
		if err == nil && ok {
			err = s.d.Secrets.Set(saved.ID, k, v)
		}
		if err != nil {
			_ = s.d.Store.DeleteProfile(ctx, saved.ID)
			_ = s.d.Secrets.DeleteAll(saved.ID)
			return model.ConnectionProfile{}, toAppErr(err)
		}
	}
	return saved, nil
}

// Delete elimina el perfil, cierra sus sesiones y borra sus secretos; el historial solo si deleteHistory.
func (s *ConnectionService) Delete(ctx context.Context, id string, deleteHistory bool) error {
	s.d.Sessions.CloseByProfile(id)
	if err := s.d.Store.DeleteProfile(ctx, id); err != nil {
		return toAppErr(err)
	}
	if err := s.d.Secrets.DeleteAll(id); err != nil {
		// El perfil ya no existe; con el llavero caído no se puede limpiar, pero no debe bloquear el borrado.
		slog.Warn("no se pudieron borrar los secretos del perfil", "profile", id, "err", err)
	}
	if deleteHistory {
		if err := s.d.Store.ClearHistory(ctx, id); err != nil {
			return toAppErr(err)
		}
	}
	return nil
}

func (s *ConnectionService) ParseDSN(dsn string) (ParsedDSN, error) {
	var last error
	for _, d := range driver.List() {
		cfg, err := d.ParseDSN(dsn)
		if err != nil {
			last = err
			continue
		}
		out := ParsedDSN{Profile: cfg.Profile}
		if cfg.Password != "" {
			pw := cfg.Password.Reveal()
			out.Secrets.Password = &pw
		}
		return out, nil
	}
	if last == nil {
		last = apperr.New(apperr.Validation, "La cadena de conexión no es válida").WithDetail("dsn")
	}
	return ParsedDSN{}, toAppErr(last)
}

func (s *ConnectionService) PickFile(title string) (string, error) {
	if s.d.PickFile == nil {
		return "", apperr.New(apperr.Unsupported, "Este entorno no tiene diálogos nativos")
	}
	path, err := s.d.PickFile(title)
	return path, toAppErr(err)
}

// TrustHostKey confía en la huella mostrada tras SSH_HOSTKEY_UNKNOWN.
func (s *ConnectionService) TrustHostKey(host string, port int, fingerprint string) error {
	return toAppErr(s.d.KnownHosts.Trust(host, port, fingerprint))
}

// Connect abre una sesión. overrides aporta secretos no guardados (modo "pedir cada vez").
func (s *ConnectionService) Connect(ctx context.Context, id string, overrides *ConnectionSecrets) (SessionInfo, error) {
	p, err := s.d.Store.GetProfile(ctx, id)
	if err != nil {
		return SessionInfo{}, toAppErr(err)
	}
	req, err := s.resolveSecrets(p, overrides)
	if err != nil {
		return SessionInfo{}, err
	}
	sess, err := s.d.Sessions.Open(ctx, req)
	if err != nil {
		return SessionInfo{}, toAppErr(err)
	}
	return infoOf(sess), nil
}

func (s *ConnectionService) Disconnect(sessionID string) error {
	return toAppErr(s.d.Sessions.Close(sessionID))
}

func (s *ConnectionService) ListSessions() []SessionInfo {
	out := []SessionInfo{}
	for _, sess := range s.d.Sessions.List() {
		out = append(out, infoOf(sess))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

func infoOf(sess *session.Session) SessionInfo {
	return SessionInfo{SessionID: sess.ID, ProfileID: sess.ProfileID, ServerVersion: sess.ServerVersion, CurrentDatabase: sess.CurrentDatabase}
}

// resolveSecrets reúne contraseñas: override > llavero. Si el perfil no guarda contraseñas y falta una necesaria → PASSWORD_REQUIRED.
func (s *ConnectionService) resolveSecrets(p model.ConnectionProfile, o *ConnectionSecrets) (session.OpenRequest, error) {
	req := session.OpenRequest{Profile: p}
	fetch := func(kind secrets.Kind, override *string, stored bool, needed bool, field string) (secrets.Secret, error) {
		if override != nil {
			return secrets.Secret(*override), nil
		}
		if stored {
			v, ok, err := s.d.Secrets.Get(p.ID, kind)
			if err != nil {
				return "", toAppErr(err)
			}
			if ok {
				return v, nil
			}
		}
		if needed && !p.SavePassword {
			return "", apperr.New(apperr.PasswordRequired, "Escribe la contraseña para conectar").WithDetail(field)
		}
		return "", nil
	}
	if o == nil {
		o = &ConnectionSecrets{}
	}
	var err error
	if req.Password, err = fetch(secrets.Password, o.Password, p.HasPassword, true, "password"); err != nil {
		return req, err
	}
	if p.SSH.Enabled {
		if req.SSHPassword, err = fetch(secrets.SSHPassword, o.SSHPassword, p.HasSSHPassword, p.SSH.AuthMethod == "password", "sshPassword"); err != nil {
			return req, err
		}
		if req.SSHPassphrase, err = fetch(secrets.SSHPassphrase, o.SSHKeyPassphrase, p.HasSSHPassphrase, false, "sshKeyPassphrase"); err != nil {
			return req, err
		}
	}
	return req, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func toAppErr(err error) error {
	if err == nil {
		return nil
	}
	return apperr.From(err)
}
