package queryfiles

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

// Opened es un archivo leído: el cuerpo sin cabecera y con "\n" como salto de línea.
type Opened struct {
	Path    string
	Name    string
	SQL     string
	Version string
	Meta    Meta
	HasMeta bool
	CRLF    bool
}

// Read abre una consulta. Rechaza lo que no sea UTF-8 o supere el límite, para no corromperlo al guardar.
func (d *Dir) Read(rel string) (Opened, error) {
	rel = withExt(rel)
	abs, err := d.abs(rel)
	if err != nil {
		return Opened{}, err
	}
	return d.read(rel, abs)
}

func (d *Dir) read(rel, abs string) (Opened, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		return Opened{}, ioErr(err)
	}
	if !fi.Mode().IsRegular() {
		return Opened{}, apperr.New(apperr.NotFound, "El archivo ya no existe")
	}
	if fi.Size() > MaxFileSize {
		return Opened{}, apperr.New(apperr.Validation, "El archivo supera los 2 MiB: no es una consulta").WithDetail("too_large")
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return Opened{}, ioErr(err)
	}
	if !utf8.Valid(raw) {
		return Opened{}, apperr.New(apperr.Validation, "El archivo no está en UTF-8").WithDetail("not_utf8")
	}
	s := strings.TrimPrefix(string(raw), "\ufeff")
	o := Opened{Path: rel, Name: displayName(rel), Version: version(fi), CRLF: strings.Contains(s, "\r\n")}
	if m, body, ok := splitHeader(s); ok {
		o.Meta, o.HasMeta, s = m, true, body
	}
	if o.CRLF {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	o.SQL = s
	return o, nil
}

// Write crea o actualiza una consulta (ver model.SaveQueryRequest). Sin RememberConnection quita la cabecera;
// profileName lo resuelve el servicio desde el perfil (el front no lo envía).
func (d *Dir) Write(req model.SaveQueryRequest, profileName string) (model.QueryRef, error) {
	rel := withExt(req.Path)
	if _, err := splitRel(rel); err != nil {
		return model.QueryRef{}, err
	}
	rel = d.canonParents(rel)
	if len(req.SQL) > MaxFileSize {
		return model.QueryRef{}, apperr.New(apperr.Validation, "La consulta supera los 2 MiB").WithDetail("too_large")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	abs, err := d.abs(rel)
	if err != nil {
		return model.QueryRef{}, err
	}

	// Sobrescribir «X» cuando existe «x»: se escribe en el archivo que ya hay (en un sistema que distingue mayúsculas,
	// si no, el diálogo ofrecería sobrescribir y fallaría igual).
	if req.Overwrite {
		if m, ok := findFold(abs); ok && m != abs {
			abs = m
			rel = path.Join(path.Dir(rel), filepath.Base(m))
		}
	}

	var existing *Opened
	fi, statErr := os.Lstat(abs)
	switch {
	case statErr == nil && fi.IsDir():
		return model.QueryRef{}, badName("Ya hay una carpeta con ese nombre")
	case statErr == nil:
		if !req.Overwrite {
			if req.ExpectedVersion == "" {
				return model.QueryRef{}, apperr.New(apperr.Validation, "Ya existe una consulta con ese nombre").WithDetail("exists")
			}
			if req.ExpectedVersion != version(fi) {
				return model.QueryRef{}, apperr.New(apperr.Validation, "El archivo cambió en el disco desde que lo abriste").WithDetail("changed_on_disk")
			}
		}
		// Se relee para conservar el fin de línea y las claves de cabecera que no conocemos. Un archivo ilegible
		// (no UTF-8) solo se pisa con Overwrite explícito.
		if o, err := d.read(rel, abs); err == nil {
			existing = &o
		} else if !req.Overwrite {
			return model.QueryRef{}, err
		}
	case os.IsNotExist(statErr):
		if req.ExpectedVersion != "" && !req.Overwrite {
			return model.QueryRef{}, apperr.New(apperr.NotFound, "El archivo ya no existe")
		}
		if err := ensureFree(abs, ""); err != nil {
			return model.QueryRef{}, err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return model.QueryRef{}, ioErr(err)
		}
	default:
		return model.QueryRef{}, ioErr(statErr)
	}

	var meta Meta
	if req.RememberConnection {
		meta = Meta{ProfileID: req.ProfileID, Database: req.Database}
		if existing != nil {
			meta.Extra = existing.Meta.Extra
		}
	}
	if req.RememberConnection {
		meta.ProfileName = profileName
	}
	crlf := existing != nil && existing.CRLF
	if err := d.writeAtomic(abs, render(meta, req.SQL, crlf)); err != nil {
		return model.QueryRef{}, err
	}
	nfi, err := os.Stat(abs)
	if err != nil {
		return model.QueryRef{}, ioErr(err)
	}
	return model.QueryRef{Path: rel, Name: displayName(rel), Version: version(nfi)}, nil
}

// render arma el contenido del archivo: cabecera (si la hay), una línea en blanco y el cuerpo.
func render(m Meta, body string, crlf bool) []byte {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var b strings.Builder
	if h := formatHeader(m); h != "" {
		b.WriteString(h + "\n\n")
	}
	b.WriteString(body)
	out := b.String()
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return []byte(out)
}

// writeAtomic escribe en un temporal oculto de la misma carpeta y lo renombra: un fallo nunca deja el original truncado.
func (d *Dir) writeAtomic(abs string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(abs), ".dbacus-*.tmp")
	if err != nil {
		return ioErr(err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = os.Chmod(tmp, 0o600); err != nil {
		return ioErr(err)
	}
	if _, err = f.Write(data); err != nil {
		return ioErr(err)
	}
	if err = f.Sync(); err != nil {
		return ioErr(err)
	}
	if err = f.Close(); err != nil {
		return ioErr(err)
	}
	if err = d.rename(tmp, abs); err != nil {
		return ioErr(err)
	}
	return nil
}

// Mkdir crea una carpeta (y las que falten) respetando nombres, profundidad y unicidad.
func (d *Dir) Mkdir(rel string) error {
	segs, err := splitRel(rel)
	if err != nil {
		return err
	}
	if len(segs) == 0 {
		return badPath("Falta el nombre de la carpeta")
	}
	rel = d.canonParents(rel)
	d.mu.Lock()
	defer d.mu.Unlock()
	abs, err := d.abs(rel)
	if err != nil {
		return err
	}
	if err := ensureFree(abs, ""); err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return ioErr(err)
	}
	return nil
}

// locate resuelve rel como carpeta o archivo (con o sin ".sql") y devuelve la ruta que existe.
func (d *Dir) locate(rel string) (string, string, fs.FileInfo, error) {
	if _, err := splitRel(rel); err != nil {
		return "", "", nil, err
	}
	for _, cand := range []string{rel, withExt(rel)} {
		abs, err := d.abs(cand)
		if err != nil {
			return "", "", nil, err
		}
		fi, err := os.Lstat(abs)
		if err == nil {
			return cand, abs, fi, nil
		}
		if !os.IsNotExist(err) {
			return "", "", nil, ioErr(err)
		}
	}
	return "", "", nil, apperr.New(apperr.NotFound, "El archivo o la carpeta ya no existe")
}

// Move mueve o renombra un archivo o una carpeta; nunca pisa. Devuelve un cambio por archivo afectado.
func (d *Dir) Move(from, to string) ([]model.PathChange, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	from, srcAbs, fi, err := d.locate(from)
	if err != nil {
		return nil, err
	}
	isDir := fi.IsDir()
	if !isDir {
		to = withExt(to)
	}
	segs, err := splitRel(to)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return nil, badPath("Falta el destino")
	}
	to = d.canonParents(to)
	dstAbs, err := d.abs(to)
	if err != nil {
		return nil, err
	}
	if isDir && (to == from || strings.HasPrefix(to+"/", from+"/")) {
		return nil, badPath("No se puede mover una carpeta dentro de sí misma")
	}
	if to == from {
		return []model.PathChange{}, nil
	}
	if err := ensureFree(dstAbs, srcAbs); err != nil {
		return nil, err
	}

	var changes []model.PathChange
	if isDir {
		deepest := 0
		prefix := from + "/"
		sub := &Dir{root: srcAbs}
		_, werr := sub.walk(func(rel string, e fs.DirEntry, _ string) error {
			if n := strings.Count(rel, "/") + 1; n > deepest {
				deepest = n
			}
			if !e.IsDir() {
				changes = append(changes, model.PathChange{From: prefix + rel, To: to + "/" + rel})
			}
			return nil
		})
		if werr != nil {
			return nil, werr
		}
		if len(segs)+deepest > MaxDepth {
			return nil, apperr.New(apperr.Validation, "Demasiados niveles de carpetas").WithDetail("depth")
		}
	} else {
		changes = []model.PathChange{{From: from, To: to}}
	}
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0o700); err != nil {
		return nil, ioErr(err)
	}
	if err := d.rename(srcAbs, dstAbs); err != nil {
		return nil, ioErr(err)
	}
	if changes == nil {
		changes = []model.PathChange{}
	}
	return changes, nil
}

// --- Papelera --------------------------------------------------------------------------------------

const trashStamp = "20060102-150405"

var trashRe = regexp.MustCompile(`^\d{8}-\d{6}(-\d+)?$`)

// Trash mueve a <raíz>/.papelera/<fecha>/<ruta>; nada se borra de verdad (spec 20 ⚑D11).
func (d *Dir) Trash(rel string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	rel, abs, _, err := d.locate(rel)
	if err != nil {
		return err
	}
	stamp := d.now().Format(trashStamp)
	base := filepath.Join(d.root, trashDir, stamp)
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(base, filepath.FromSlash(rel))); os.IsNotExist(err) {
			break
		}
		base = filepath.Join(d.root, trashDir, stamp+"-"+strconv.Itoa(i))
	}
	dst := filepath.Join(base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return ioErr(err)
	}
	if err := d.rename(abs, dst); err != nil {
		return ioErr(err)
	}
	return nil
}

// PurgeTrash borra las carpetas de la papelera con más de maxAge. Solo toca las que creó Trash (nombre con fecha).
func (d *Dir) PurgeTrash(maxAge time.Duration) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	base := filepath.Join(d.root, trashDir)
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, ioErr(err)
	}
	cutoff := d.now().Add(-maxAge)
	n := 0
	for _, e := range entries {
		if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 || !trashRe.MatchString(e.Name()) {
			continue
		}
		stamp := e.Name()
		if len(stamp) > len(trashStamp) {
			stamp = stamp[:len(trashStamp)]
		}
		t, err := time.ParseInLocation(trashStamp, stamp, d.now().Location())
		if err != nil || !t.Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(base, e.Name())); err == nil {
			n++
		}
	}
	return n, nil
}
