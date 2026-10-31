// Package queryfiles guarda las consultas del usuario como archivos .sql bajo una carpeta raíz (spec 20).
// Todas las rutas que entran son relativas a la raíz y se validan aquí: nada sale de la carpeta.
package queryfiles

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

const (
	Ext         = ".sql"
	MaxFileSize = 2 << 20
	MaxDepth    = 8
	MaxFiles    = 10000
	maxNameLen  = 120
	maxPathLen  = 400
	trashDir    = ".papelera"
)

// Dir es una carpeta raíz de consultas. Las operaciones que modifican el disco se serializan con un mutex.
type Dir struct {
	root string

	mu     sync.Mutex
	now    func() time.Time
	rename func(oldpath, newpath string) error // sustituible en tests para inyectar fallos
}

// Open prepara la carpeta (la crea con 0700 si falta) y resuelve un enlace simbólico en la raíz, una sola vez.
func Open(root string) (*Dir, error) {
	if !filepath.IsAbs(root) {
		return nil, apperr.New(apperr.Validation, "La carpeta de consultas debe ser una ruta absoluta").WithDetail("root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, apperr.New(apperr.Validation, "No se puede crear la carpeta de consultas").WithDetail("root")
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, apperr.New(apperr.Validation, "No se puede leer la carpeta de consultas").WithDetail("root")
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return nil, apperr.New(apperr.Validation, "La ruta de consultas no es una carpeta").WithDetail("root")
	}
	return &Dir{root: real, now: time.Now, rename: os.Rename}, nil
}

// ValidateRoot rechaza raíces peligrosas: el directorio raíz del disco o el personal del usuario (se listaría todo).
func ValidateRoot(path, home string) error {
	bad := func(msg string) error { return apperr.New(apperr.Validation, msg).WithDetail("root") }
	if !filepath.IsAbs(path) {
		return bad("Elige una carpeta con ruta absoluta")
	}
	clean := filepath.Clean(path)
	if clean == filepath.Dir(clean) {
		return bad("No se puede usar la raíz del disco")
	}
	if home != "" && sameFile(clean, home) {
		return bad("No uses tu carpeta personal completa: elige o crea una subcarpeta")
	}
	return nil
}

func sameFile(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	if err1 == nil && err2 == nil {
		return os.SameFile(ai, bi)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// Writable comprueba que se pueda crear un archivo en la raíz.
func (d *Dir) Writable() bool {
	f, err := os.CreateTemp(d.root, ".dbacus-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func (d *Dir) Root() string { return d.root }

// --- Validación de rutas -------------------------------------------------------------------------

var reserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true}

func badName(msg string) error { return apperr.New(apperr.Validation, msg).WithDetail("name") }
func badPath(msg string) error { return apperr.New(apperr.Validation, msg).WithDetail("path") }

// validSegment valida un nombre de archivo o carpeta (sin separadores).
func validSegment(seg string) error {
	switch {
	case seg == "" || strings.TrimSpace(seg) == "":
		return badName("El nombre no puede estar vacío")
	case utf8.RuneCountInString(seg) > maxNameLen:
		return badName("El nombre es demasiado largo (máximo 120 caracteres)")
	case !utf8.ValidString(seg):
		return badName("El nombre no es UTF-8 válido")
	case seg == "." || seg == ".." || strings.HasPrefix(seg, "."):
		return badName("El nombre no puede empezar por un punto")
	case strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " "):
		return badName("El nombre no puede terminar en punto ni en espacio")
	case isSQLName(seg) && (strings.HasSuffix(seg[:len(seg)-len(Ext)], ".") || strings.HasSuffix(seg[:len(seg)-len(Ext)], " ")):
		// «fin..sql» o «fin .sql»: el nombre visible termina en punto o espacio
		return badName("El nombre no puede terminar en punto ni en espacio")
	case strings.ContainsAny(seg, `/\:*?"<>|`):
		return badName(`El nombre no puede contener / \ : * ? " < > |`)
	}
	for _, r := range seg {
		if r < 0x20 || r == 0x7f {
			return badName("El nombre no puede contener caracteres de control")
		}
	}
	base := seg
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if reserved[strings.ToUpper(strings.TrimRight(base, " "))] {
		return badName("Ese nombre está reservado por el sistema")
	}
	return nil
}

// splitRel valida y trocea una ruta relativa. "" es la raíz (0 segmentos).
func splitRel(rel string) ([]string, error) {
	if rel == "" {
		return nil, nil
	}
	if len(rel) > maxPathLen {
		return nil, badPath("La ruta es demasiado larga")
	}
	if strings.ContainsAny(rel, "\\\x00") || strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return nil, badPath("Ruta no válida")
	}
	segs := strings.Split(rel, "/")
	if len(segs) > MaxDepth {
		return nil, apperr.New(apperr.Validation, "Demasiados niveles de carpetas").WithDetail("depth")
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return nil, badPath("Ruta no válida")
		}
		if err := validSegment(s); err != nil {
			return nil, err
		}
	}
	return segs, nil
}

// withExt añade ".sql" al último tramo si falta (sin duplicarlo).
func withExt(rel string) string {
	if strings.HasSuffix(strings.ToLower(rel), Ext) {
		return rel
	}
	return rel + Ext
}

// abs compone la ruta absoluta de rel y comprueba que ningún tramo existente sea un enlace simbólico.
func (d *Dir) abs(rel string) (string, error) {
	segs, err := splitRel(rel)
	if err != nil {
		return "", err
	}
	cur := d.root
	for _, s := range segs {
		cur = filepath.Join(cur, s)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				continue // los tramos siguientes tampoco existen; no hay nada que seguir
			}
			return "", ioErr(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", badPath("La ruta pasa por un enlace simbólico")
		}
	}
	if r, err := filepath.Rel(d.root, cur); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", badPath("Ruta fuera de la carpeta de consultas")
	}
	return cur, nil
}

func ioErr(err error) error {
	if os.IsNotExist(err) {
		return apperr.New(apperr.NotFound, "El archivo o la carpeta ya no existe")
	}
	// El mensaje no lleva rutas absolutas; el detalle técnico sí (nunca contenido).
	return apperr.Wrap(apperr.Internal, "No se pudo acceder a las consultas guardadas", err)
}

// ensureFree falla con `exists` si en la carpeta de abs ya hay una entrada con ese nombre sin distinguir
// mayúsculas (macOS y Windows lo hacen igual a nivel de sistema de archivos). allow es la entrada que se está moviendo.
func ensureFree(abs, allow string) error {
	dir, name := filepath.Dir(abs), filepath.Base(abs)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return ioErr(err)
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), name) && filepath.Join(dir, e.Name()) != allow {
			return apperr.New(apperr.Validation, "Ya existe algo con ese nombre").WithDetail("exists")
		}
	}
	return nil
}

// findFold devuelve la entrada de la carpeta de abs cuyo nombre coincide sin distinguir mayúsculas.
func findFold(abs string) (string, bool) {
	dir, name := filepath.Dir(abs), filepath.Base(abs)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), name) {
			return filepath.Join(dir, e.Name()), true
		}
	}
	return "", false
}

// Version identifica el estado de un archivo en disco; Save la compara para no pisar ediciones externas.
func version(fi fs.FileInfo) string { return fmt.Sprintf("%d:%d", fi.ModTime().UnixNano(), fi.Size()) }

func displayName(rel string) string {
	base := rel[strings.LastIndexByte(rel, '/')+1:]
	return strings.TrimSuffix(base, Ext)
}

func isSQLName(name string) bool { return strings.EqualFold(filepath.Ext(name), Ext) }

// --- Listado -------------------------------------------------------------------------------------

// walk recorre el árbol sin seguir enlaces simbólicos ni entradas ocultas. fn recibe la ruta relativa.
// Devuelve true si se superó MaxFiles.
func (d *Dir) walk(fn func(rel string, e fs.DirEntry, abs string) error) (truncated bool, err error) {
	files := 0
	var rec func(relDir string, depth int) error
	rec = func(relDir string, depth int) error {
		abs := filepath.Join(d.root, filepath.FromSlash(relDir))
		entries, err := os.ReadDir(abs)
		if err != nil {
			if relDir == "" {
				return ioErr(err)
			}
			return nil // una subcarpeta ilegible no tumba el listado
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			rel := name
			if relDir != "" {
				rel = relDir + "/" + name
			}
			switch {
			case e.IsDir():
				if validSegment(name) != nil {
					continue
				}
				if err := fn(rel, e, filepath.Join(abs, name)); err != nil {
					return err
				}
				if depth < MaxDepth { // los hijos estarían a depth+1
					if err := rec(rel, depth+1); err != nil {
						return err
					}
				}
			case e.Type().IsRegular() && isSQLName(name):
				if validSegment(name) != nil {
					continue
				}
				if files >= MaxFiles {
					truncated = true
					return nil
				}
				files++
				if err := fn(rel, e, filepath.Join(abs, name)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err = rec("", 1)
	return truncated, err
}

// List devuelve carpetas y archivos .sql, ordenados por ruta sin distinguir mayúsculas.
func (d *Dir) List() ([]model.QueryNode, bool, error) {
	nodes := []model.QueryNode{}
	trunc, err := d.walk(func(rel string, e fs.DirEntry, abs string) error {
		fi, err := e.Info()
		if err != nil {
			return nil
		}
		n := model.QueryNode{Path: rel, Kind: model.QueryNodeFolder, Name: e.Name(), ModifiedAt: fi.ModTime()}
		if !e.IsDir() {
			n.Kind, n.Name, n.Size, n.Version = model.QueryNodeFile, displayName(rel), fi.Size(), version(fi)
			if m, ok := readMetaOnly(abs); ok {
				n.ProfileID, n.ProfileName, n.Database = m.ProfileID, m.ProfileName, m.Database
			}
		}
		nodes = append(nodes, n)
		return nil
	})
	sort.SliceStable(nodes, func(i, j int) bool { return strings.ToLower(nodes[i].Path) < strings.ToLower(nodes[j].Path) })
	return nodes, trunc, err
}

// readMetaOnly lee solo el principio del archivo: listar 10 000 archivos no puede leerlos enteros.
func readMetaOnly(abs string) (Meta, bool) {
	f, err := os.Open(abs)
	if err != nil {
		return Meta{}, false
	}
	defer f.Close()
	buf := make([]byte, maxHeaderLine+256)
	n, _ := f.Read(buf)
	s := strings.TrimPrefix(string(buf[:n]), "\ufeff")
	m, _, ok := splitHeader(s)
	return m, ok
}

// Abs devuelve la ruta absoluta de una carpeta o archivo existente ("" es la raíz), por si hay que mostrarla en el explorador.
func (d *Dir) Abs(rel string) (abs string, isDir bool, err error) {
	if rel == "" {
		return d.root, true, nil
	}
	_, abs, fi, err := d.locate(rel)
	if err != nil {
		return "", false, err
	}
	return abs, fi.IsDir(), nil
}
