package services

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/queryfiles"
)

// trashRetention es cuánto se conserva lo borrado en la papelera de consultas (spec 20 ⚑D11).
const trashRetention = 30 * 24 * time.Hour

// QueryFilesStore es lo que el servicio necesita de la persistencia (lo implementa *store.Store).
type QueryFilesStore interface {
	GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error)
	GetQueriesDir(ctx context.Context) (string, bool, error)
	SetQueriesDir(ctx context.Context, dir string) error
	ClearQueriesDir(ctx context.Context) error
}

type QueryFilesDeps struct {
	Store       QueryFilesStore
	DefaultRoot string // carpeta por defecto (paths.Paths.Queries)
	Home        string // directorio personal: no se admite como raíz
	// PickDir abre el diálogo nativo de carpeta; "" = cancelado.
	PickDir func(title string) (string, error)
	// Reveal muestra una ruta en el explorador del SO.
	Reveal func(path string, isDir bool) error
}

// QueryFilesService guarda y lista las consultas del usuario como archivos .sql (spec 20). Las rutas son
// relativas a la raíz; el front nunca envía rutas absolutas. Guardar o abrir no ejecuta SQL.
type QueryFilesService struct {
	d QueryFilesDeps

	mu     sync.Mutex
	dir    *queryfiles.Dir
	target string // ruta con la que se abrió dir
}

func NewQueryFilesService(d QueryFilesDeps) *QueryFilesService { return &QueryFilesService{d: d} }

// target devuelve la ruta raíz vigente y si es la de por defecto.
func (s *QueryFilesService) rootPath(ctx context.Context) (string, bool, error) {
	custom, ok, err := s.d.Store.GetQueriesDir(ctx)
	if err != nil {
		return "", false, toAppErr(err)
	}
	if ok && custom != "" {
		return custom, false, nil
	}
	if s.d.DefaultRoot == "" {
		return "", true, apperr.New(apperr.Validation, "No se pudo determinar la carpeta de consultas: elige una").WithDetail("root")
	}
	return s.d.DefaultRoot, true, nil
}

// current abre (o reutiliza) la carpeta raíz. Relee el ajuste en cada llamada: cambiarla aplica al instante.
func (s *QueryFilesService) current(ctx context.Context) (*queryfiles.Dir, bool, error) {
	path, isDefault, err := s.rootPath(ctx)
	if err != nil {
		return nil, isDefault, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir != nil && s.target == path {
		if _, statErr := os.Stat(s.dir.Root()); statErr == nil {
			return s.dir, isDefault, nil
		}
	}
	d, err := queryfiles.Open(path)
	if err != nil {
		s.dir, s.target = nil, ""
		return nil, isDefault, toAppErr(err)
	}
	s.dir, s.target = d, path
	return d, isDefault, nil
}

// Root describe la carpeta raíz sin fallar: si no se puede abrir devuelve Exists=false para que la UI lo explique.
func (s *QueryFilesService) Root(ctx context.Context) (model.QueryRoot, error) {
	path, isDefault, err := s.rootPath(ctx)
	if err != nil {
		var ae *apperr.AppError
		if errors.As(err, &ae) && ae.Detail == "root" {
			return model.QueryRoot{IsDefault: isDefault}, nil
		}
		return model.QueryRoot{}, err
	}
	r := model.QueryRoot{Path: path, IsDefault: isDefault}
	if d, _, err := s.current(ctx); err == nil {
		r.Path, r.Exists, r.Writable = d.Root(), true, d.Writable()
	}
	return r, nil
}

func (s *QueryFilesService) List(ctx context.Context) (model.QueryTree, error) {
	root, err := s.Root(ctx)
	if err != nil {
		return model.QueryTree{}, err
	}
	d, _, err := s.current(ctx)
	if err != nil {
		return model.QueryTree{Root: root, Nodes: []model.QueryNode{}}, err
	}
	nodes, trunc, err := d.List()
	if err != nil {
		return model.QueryTree{Root: root, Nodes: []model.QueryNode{}}, toAppErr(err)
	}
	return model.QueryTree{Root: root, Nodes: nodes, Truncated: trunc}, nil
}

func (s *QueryFilesService) Open(ctx context.Context, path string) (model.QueryFile, error) {
	d, _, err := s.current(ctx)
	if err != nil {
		return model.QueryFile{}, err
	}
	o, err := d.Read(path)
	if err != nil {
		return model.QueryFile{}, toAppErr(err)
	}
	f := model.QueryFile{
		Path: o.Path, Name: o.Name, SQL: o.SQL, Version: o.Version,
		ProfileID: o.Meta.ProfileID, ProfileName: o.Meta.ProfileName, Database: o.Meta.Database,
	}
	if f.ProfileID != "" {
		if _, err := s.d.Store.GetProfile(ctx, f.ProfileID); err != nil {
			var ae *apperr.AppError
			f.ProfileMissing = errors.As(err, &ae) && ae.Code == apperr.NotFound
		}
	}
	return f, nil
}

// Save crea o actualiza una consulta. Guardar está permitido también con perfiles de solo lectura: no es SQL.
func (s *QueryFilesService) Save(ctx context.Context, req model.SaveQueryRequest) (model.QueryRef, error) {
	d, _, err := s.current(ctx)
	if err != nil {
		return model.QueryRef{}, err
	}
	var profileName string
	if req.RememberConnection && req.ProfileID != "" {
		p, err := s.d.Store.GetProfile(ctx, req.ProfileID)
		if err != nil {
			var ae *apperr.AppError
			if errors.As(err, &ae) && ae.Code == apperr.NotFound {
				return model.QueryRef{}, apperr.New(apperr.Validation, "La conexión no existe").WithDetail("profileId")
			}
			return model.QueryRef{}, toAppErr(err)
		}
		profileName = p.Name
	}
	ref, err := d.Write(req, profileName)
	slog.Debug("consulta guardada", "ruta", ref.Path, "ok", err == nil) // nunca el contenido
	return ref, toAppErr(err)
}

func (s *QueryFilesService) CreateFolder(ctx context.Context, path string) error {
	d, _, err := s.current(ctx)
	if err != nil {
		return err
	}
	return toAppErr(d.Mkdir(path))
}

// Move mueve o renombra; devuelve un cambio por archivo para que las pestañas actualicen su ruta.
func (s *QueryFilesService) Move(ctx context.Context, from, to string) ([]model.PathChange, error) {
	d, _, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := d.Move(from, to)
	return ch, toAppErr(err)
}

// Delete mueve a la papelera de la carpeta de consultas; nada se borra de verdad.
func (s *QueryFilesService) Delete(ctx context.Context, path string) error {
	d, _, err := s.current(ctx)
	if err != nil {
		return err
	}
	return toAppErr(d.Trash(path))
}

func (s *QueryFilesService) Search(ctx context.Context, req model.QuerySearchRequest) ([]model.QueryHit, error) {
	d, _, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	hits, err := d.Search(ctx, req)
	return hits, toAppErr(err)
}

// PickRoot abre el diálogo nativo, valida la carpeta y la aplica. No mueve archivos. nil = cancelado.
func (s *QueryFilesService) PickRoot(ctx context.Context) (*model.QueryRoot, error) {
	if s.d.PickDir == nil {
		return nil, apperr.New(apperr.Unsupported, "Elegir carpeta no está disponible en este modo")
	}
	path, err := s.d.PickDir("Carpeta de consultas guardadas")
	if err != nil {
		return nil, apperr.Wrap(apperr.Unsupported, "No se pudo abrir el selector de carpetas", err)
	}
	if path == "" {
		return nil, nil
	}
	if err := queryfiles.ValidateRoot(path, s.d.Home); err != nil {
		return nil, toAppErr(err)
	}
	d, err := queryfiles.Open(path)
	if err != nil {
		return nil, toAppErr(err)
	}
	if !d.Writable() {
		return nil, apperr.New(apperr.Validation, "No se puede escribir en esa carpeta").WithDetail("root")
	}
	if err := s.d.Store.SetQueriesDir(ctx, path); err != nil {
		return nil, toAppErr(err)
	}
	s.mu.Lock()
	s.dir, s.target = d, path
	s.mu.Unlock()
	r, err := s.Root(ctx)
	return &r, err
}

// ResetRoot vuelve a la carpeta por defecto. No mueve ni borra nada.
func (s *QueryFilesService) ResetRoot(ctx context.Context) (model.QueryRoot, error) {
	if err := s.d.Store.ClearQueriesDir(ctx); err != nil {
		return model.QueryRoot{}, toAppErr(err)
	}
	return s.Root(ctx)
}

// Reveal muestra una carpeta o archivo ("" = la raíz) en el explorador del SO.
func (s *QueryFilesService) Reveal(ctx context.Context, path string) error {
	if s.d.Reveal == nil {
		return apperr.New(apperr.Unsupported, "Mostrar en el explorador no está disponible en este modo")
	}
	d, _, err := s.current(ctx)
	if err != nil {
		return err
	}
	abs, isDir, err := d.Abs(strings.TrimSpace(path))
	if err != nil {
		return toAppErr(err)
	}
	if err := s.d.Reveal(abs, isDir); err != nil {
		return apperr.Wrap(apperr.Unsupported, "No se pudo abrir el explorador", err)
	}
	return nil
}

// PurgeTrash elimina lo que lleva más de 30 días en la papelera. Se llama al arrancar; un fallo solo se registra.
func (s *QueryFilesService) PurgeTrash(ctx context.Context) {
	// Sin carpeta todavía no hay nada que purgar, y no se crea solo por arrancar (evita pedir permisos de Documentos).
	if path, _, err := s.rootPath(ctx); err != nil {
		return
	} else if _, err := os.Stat(path); err != nil {
		return
	}
	d, _, err := s.current(ctx)
	if err != nil {
		return
	}
	if n, err := d.PurgeTrash(trashRetention); err != nil {
		slog.Warn("no se pudo purgar la papelera de consultas", "err", err)
	} else if n > 0 {
		slog.Info("papelera de consultas purgada", "carpetas", n)
	}
}
