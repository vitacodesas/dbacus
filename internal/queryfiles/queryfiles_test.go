package queryfiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

func newDir(t *testing.T) *Dir {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "queries"))
	require.NoError(t, err)
	return d
}

func detail(t *testing.T, err error) string {
	t.Helper()
	var ae *apperr.AppError
	require.True(t, errors.As(err, &ae), "se esperaba un AppError, llegó %v", err)
	return ae.Detail
}

func save(t *testing.T, d *Dir, path, sql string) model.QueryRef {
	t.Helper()
	ref, err := d.Write(model.SaveQueryRequest{Path: path, SQL: sql}, "")
	require.NoError(t, err)
	return ref
}

func TestResolveRejectsEscapes(t *testing.T) {
	d := newDir(t)
	for _, rel := range []string{
		"../fuera", "a/../../fuera", "/etc/passwd", `a\b`, "a/./b", "a//b", "a/", "..", ".", "a\x00b",
		".oculto", "a/.oculto", ".papelera/x", "C:/x", strings.Repeat("a/", 9) + "z",
	} {
		_, err := d.Write(model.SaveQueryRequest{Path: rel, SQL: "SELECT 1"}, "")
		require.Error(t, err, rel)
		require.Contains(t, []string{"path", "name", "depth"}, detail(t, err), rel)
		_, err = d.Read(rel)
		require.Error(t, err, rel)
	}
	// Nada se escribió fuera ni dentro.
	nodes, _, err := d.List()
	require.NoError(t, err)
	require.Empty(t, nodes)
}

func TestSymlinksAreNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("los enlaces simbólicos requieren privilegios en Windows")
	}
	d := newDir(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secreto.sql"), []byte("SELECT 'x'"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(d.Root(), "enlace")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secreto.sql"), filepath.Join(d.Root(), "directo.sql")))
	save(t, d, "real", "SELECT 1")

	nodes, _, err := d.List()
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Equal(t, "real.sql", nodes[0].Path)

	_, err = d.Read("enlace/secreto")
	require.Equal(t, "path", detail(t, err))
	_, err = d.Read("directo")
	require.Error(t, err)
	_, err = d.Write(model.SaveQueryRequest{Path: "enlace/nuevo", SQL: "x"}, "")
	require.Equal(t, "path", detail(t, err))
	require.NoFileExists(t, filepath.Join(outside, "nuevo.sql"))
	require.Error(t, d.Trash("enlace"))
}

func TestReservedNames(t *testing.T) {
	d := newDir(t)
	for _, n := range []string{"CON", "con", "NUL", "com1", "LPT9", "aux.sql", "Prn.backup", "a:b", "a?b", "a*b", `a"b`, "a<b", "a|b", "fin.", "fin ", "", "  "} {
		_, err := d.Write(model.SaveQueryRequest{Path: n, SQL: "x"}, "")
		require.Error(t, err, "%q", n)
		require.Equal(t, "name", detail(t, err), "%q", n)
	}
	_, err := d.Write(model.SaveQueryRequest{Path: strings.Repeat("a", 121), SQL: "x"}, "")
	require.Equal(t, "name", detail(t, err))
	// Un nombre que solo contiene la palabra reservada se acepta.
	save(t, d, "console", "SELECT 1")
}

func TestUnicodeNamesAndCaseInsensitiveUniqueness(t *testing.T) {
	d := newDir(t)
	for _, n := range []string{"Reporte ñandú", "café con leche", "日本語", "emoji 🚀", "Informes/ventas 2026"} {
		ref := save(t, d, n, "SELECT '"+n+"'")
		got, err := d.Read(ref.Path)
		require.NoError(t, err)
		require.Equal(t, "SELECT '"+n+"'", got.SQL)
	}
	_, err := d.Write(model.SaveQueryRequest{Path: "REPORTE ÑANDÚ", SQL: "otra"}, "")
	require.Equal(t, "exists", detail(t, err))
	_, err = d.Write(model.SaveQueryRequest{Path: "informes/Ventas 2026.SQL", SQL: "otra"}, "")
	require.Equal(t, "exists", detail(t, err))
	require.Equal(t, "exists", detail(t, d.Mkdir("INFORMES")))
	// Una carpeta con otras mayúsculas no crea un duplicado: se usa la que ya existe.
	ref0 := save(t, d, "INFORMES/otro", "x")
	require.Equal(t, "Informes/otro.sql", ref0.Path)
	require.NoError(t, d.Mkdir("informes/Sub"))
	require.DirExists(t, filepath.Join(d.Root(), "Informes", "Sub"))
	entries, _ := os.ReadDir(d.Root())
	for _, e := range entries {
		require.NotEqual(t, "informes", e.Name())
		require.NotEqual(t, "INFORMES", e.Name())
	}
	require.NoError(t, os.Remove(filepath.Join(d.Root(), "Informes", "otro.sql")))
	require.NoError(t, os.Remove(filepath.Join(d.Root(), "Informes", "Sub")))
	// Sobrescribir con otras mayúsculas escribe en el archivo existente y devuelve su nombre real.
	ref, err := d.Write(model.SaveQueryRequest{Path: "REPORTE ÑANDÚ", SQL: "nuevo", Overwrite: true}, "")
	require.NoError(t, err)
	require.Equal(t, "Reporte ñandú.sql", ref.Path)
	got, err := d.Read(ref.Path)
	require.NoError(t, err)
	require.Equal(t, "nuevo", got.SQL)
	nodes, _, err := d.List()
	require.NoError(t, err)
	require.Len(t, nodes, 6, "no se creó un segundo archivo")
	// La extensión no se duplica.
	require.Equal(t, "uno.sql", save(t, d, "uno.sql", "x").Path)
}

func TestHeaderRoundTrip(t *testing.T) {
	d := newDir(t)
	req := model.SaveQueryRequest{Path: "q", SQL: "SELECT 1;\n\nSELECT 2;", ProfileID: "id-1", Database: "shop", RememberConnection: true}
	_, err := d.Write(req, `Prod "principal" \ x`)
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(d.Root(), "q.sql"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(raw), "-- @dbacus profile=id-1 name="), string(raw))

	o, err := d.Read("q")
	require.NoError(t, err)
	require.Equal(t, "SELECT 1;\n\nSELECT 2;", o.SQL, "el editor no ve la cabecera")
	require.True(t, o.HasMeta)
	require.Equal(t, Meta{ProfileID: "id-1", ProfileName: `Prod "principal" \ x`, Database: "shop"}, o.Meta)

	// Sin RememberConnection la cabecera se quita.
	req.RememberConnection = false
	req.ExpectedVersion = o.Version
	_, err = d.Write(req, "Prod")
	require.NoError(t, err)
	raw, _ = os.ReadFile(filepath.Join(d.Root(), "q.sql"))
	require.Equal(t, "SELECT 1;\n\nSELECT 2;", string(raw))
}

func TestHeaderKeepsUnknownKeysAndBlankBody(t *testing.T) {
	d := newDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(d.Root(), "x.sql"), []byte("-- @dbacus profile=p future=\"a b\" database=db\n\n\nSELECT 1"), 0o600))
	o, err := d.Read("x")
	require.NoError(t, err)
	require.Equal(t, "\nSELECT 1", o.SQL, "solo se consume una línea en blanco tras la cabecera")
	require.Equal(t, "a b", o.Meta.Extra["future"])

	_, err = d.Write(model.SaveQueryRequest{Path: "x", SQL: o.SQL, ProfileID: "p", Database: "db", RememberConnection: true, ExpectedVersion: o.Version}, "N")
	require.NoError(t, err)
	again, err := d.Read("x")
	require.NoError(t, err)
	require.Equal(t, o.SQL, again.SQL)
	require.Equal(t, "a b", again.Meta.Extra["future"], "la clave desconocida sobrevive")
}

func TestFileWithoutHeaderIsPlainSQL(t *testing.T) {
	d := newDir(t)
	ref := save(t, d, "plano", "-- comentario normal\nSELECT 1")
	raw, _ := os.ReadFile(filepath.Join(d.Root(), "plano.sql"))
	require.Equal(t, "-- comentario normal\nSELECT 1", string(raw))
	o, err := d.Read(ref.Path)
	require.NoError(t, err)
	require.False(t, o.HasMeta)
	require.Equal(t, "-- comentario normal\nSELECT 1", o.SQL)
	// "-- @dbacus" sin espacio ni claves no es cabecera.
	require.NoError(t, os.WriteFile(filepath.Join(d.Root(), "y.sql"), []byte("-- @dbacusX nada\nSELECT 1"), 0o600))
	o, err = d.Read("y")
	require.NoError(t, err)
	require.False(t, o.HasMeta)
	// Un salto de línea en el nombre del perfil no puede romper la cabecera ni inyectar SQL.
	_, err = d.Write(model.SaveQueryRequest{Path: "z", SQL: "SELECT 1", ProfileID: "p", RememberConnection: true}, "mal\nDROP TABLE x;")
	require.NoError(t, err)
	raw, _ = os.ReadFile(filepath.Join(d.Root(), "z.sql"))
	lines := strings.Split(string(raw), "\n")
	require.Len(t, lines, 3, "cabecera, línea en blanco y cuerpo: el salto del nombre no añadió líneas")
	require.Equal(t, "SELECT 1", lines[2])
}

func TestCRLFPreserved(t *testing.T) {
	d := newDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(d.Root(), "win.sql"), []byte("\xef\xbb\xbfSELECT 1;\r\nSELECT 2;\r\n"), 0o600))
	o, err := d.Read("win")
	require.NoError(t, err)
	require.True(t, o.CRLF)
	require.Equal(t, "SELECT 1;\nSELECT 2;\n", o.SQL, "el editor siempre ve \\n y sin BOM")

	_, err = d.Write(model.SaveQueryRequest{Path: "win", SQL: "SELECT 1;\nSELECT 3;\n", ExpectedVersion: o.Version}, "")
	require.NoError(t, err)
	raw, _ := os.ReadFile(filepath.Join(d.Root(), "win.sql"))
	require.Equal(t, "SELECT 1;\r\nSELECT 3;\r\n", string(raw))

	// Los archivos nuevos usan \n aunque el texto traiga \r\n.
	save(t, d, "nuevo", "a\r\nb")
	raw, _ = os.ReadFile(filepath.Join(d.Root(), "nuevo.sql"))
	require.Equal(t, "a\nb", string(raw))
}

func TestNotUTF8AndTooLarge(t *testing.T) {
	d := newDir(t)
	bin := []byte("SELECT '\xff\xfe'")
	require.NoError(t, os.WriteFile(filepath.Join(d.Root(), "bin.sql"), bin, 0o600))
	_, err := d.Read("bin")
	require.Equal(t, "not_utf8", detail(t, err))
	// Guardar encima sin Overwrite no corrompe el archivo.
	_, err = d.Write(model.SaveQueryRequest{Path: "bin", SQL: "x", ExpectedVersion: "x"}, "")
	require.Error(t, err)
	got, _ := os.ReadFile(filepath.Join(d.Root(), "bin.sql"))
	require.Equal(t, bin, got)

	require.NoError(t, os.WriteFile(filepath.Join(d.Root(), "big.sql"), []byte(strings.Repeat("x", MaxFileSize+1)), 0o600))
	_, err = d.Read("big")
	require.Equal(t, "too_large", detail(t, err))
	_, err = d.Write(model.SaveQueryRequest{Path: "grande", SQL: strings.Repeat("x", MaxFileSize+1)}, "")
	require.Equal(t, "too_large", detail(t, err))
	// El listado los muestra sin romperse.
	nodes, _, err := d.List()
	require.NoError(t, err)
	require.Len(t, nodes, 2)
}

func TestAtomicWriteLeavesOriginalOnFailure(t *testing.T) {
	d := newDir(t)
	ref := save(t, d, "q", "ORIGINAL")
	d.rename = func(string, string) error { return errors.New("disco lleno") }
	_, err := d.Write(model.SaveQueryRequest{Path: "q", SQL: "NUEVO", ExpectedVersion: ref.Version}, "")
	require.Error(t, err)
	d.rename = os.Rename
	got, _ := os.ReadFile(filepath.Join(d.Root(), "q.sql"))
	require.Equal(t, "ORIGINAL", string(got))
	entries, _ := os.ReadDir(d.Root())
	require.Len(t, entries, 1, "no quedan temporales")
}

func TestSaveDetectsChangeOnDisk(t *testing.T) {
	d := newDir(t)
	ref := save(t, d, "q", "v1")
	// Edición externa (otro tamaño ⇒ otra versión aunque el mtime coincida).
	require.NoError(t, os.WriteFile(filepath.Join(d.Root(), "q.sql"), []byte("editado fuera"), 0o600))
	_, err := d.Write(model.SaveQueryRequest{Path: "q", SQL: "v2", ExpectedVersion: ref.Version}, "")
	require.Equal(t, "changed_on_disk", detail(t, err))
	got, _ := os.ReadFile(filepath.Join(d.Root(), "q.sql"))
	require.Equal(t, "editado fuera", string(got))

	_, err = d.Write(model.SaveQueryRequest{Path: "q", SQL: "v2", ExpectedVersion: ref.Version, Overwrite: true}, "")
	require.NoError(t, err)

	// Archivo nuevo sobre uno existente → exists; borrado fuera → NOT_FOUND y se recrea sin versión.
	_, err = d.Write(model.SaveQueryRequest{Path: "q", SQL: "x"}, "")
	require.Equal(t, "exists", detail(t, err))
	require.NoError(t, os.Remove(filepath.Join(d.Root(), "q.sql")))
	_, err = d.Write(model.SaveQueryRequest{Path: "q", SQL: "x", ExpectedVersion: "1:1"}, "")
	var ae *apperr.AppError
	require.True(t, errors.As(err, &ae))
	require.Equal(t, apperr.NotFound, ae.Code)
	save(t, d, "q", "x")
}

func TestMoveFolderReturnsPathChanges(t *testing.T) {
	d := newDir(t)
	save(t, d, "a/uno", "1")
	save(t, d, "a/sub/dos", "2")
	save(t, d, "otra", "3")

	ch, err := d.Move("a", "b/c")
	require.NoError(t, err)
	require.ElementsMatch(t, []model.PathChange{{From: "a/uno.sql", To: "b/c/uno.sql"}, {From: "a/sub/dos.sql", To: "b/c/sub/dos.sql"}}, ch)
	_, err = d.Read("b/c/sub/dos")
	require.NoError(t, err)
	_, err = d.Read("a/uno")
	require.Error(t, err)

	// Renombrar un archivo (con y sin extensión) y casos que no se permiten.
	ch, err = d.Move("otra", "nueva")
	require.NoError(t, err)
	require.Equal(t, []model.PathChange{{From: "otra.sql", To: "nueva.sql"}}, ch)
	_, err = d.Move("nueva", "b/c/uno")
	require.Equal(t, "exists", detail(t, err))
	_, err = d.Move("b", "b/c/dentro")
	require.Equal(t, "path", detail(t, err))
	_, err = d.Move("nueva", "../fuera")
	require.Error(t, err)
	_, err = d.Move("no-existe", "x")
	require.Error(t, err)
	// Solo cambia las mayúsculas del mismo archivo: se permite.
	_, err = d.Move("nueva", "NUEVA")
	require.NoError(t, err)
	// Una carpeta profunda no puede pasar de 8 niveles al moverla.
	save(t, d, "p/q/r/s/t/u/v", "x") // 7 segmentos
	_, err = d.Move("p", "1/2/3")
	require.Equal(t, "depth", detail(t, err))
}

func TestDeleteMovesToTrash(t *testing.T) {
	d := newDir(t)
	d.now = func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }
	save(t, d, "carp/q", "IMPORTANTE")
	require.NoError(t, d.Trash("carp/q"))
	require.NoError(t, d.Trash("carp"))

	nodes, _, err := d.List()
	require.NoError(t, err)
	require.Empty(t, nodes, "la papelera no aparece en el listado")
	b, err := os.ReadFile(filepath.Join(d.Root(), ".papelera", "20261031-120000", "carp", "q.sql"))
	require.NoError(t, err)
	require.Equal(t, "IMPORTANTE", string(b))

	// Borrar de nuevo algo con la misma ruta en el mismo segundo no pisa lo anterior.
	save(t, d, "carp/q", "OTRA")
	require.NoError(t, d.Trash("carp/q"))
	b, err = os.ReadFile(filepath.Join(d.Root(), ".papelera", "20261031-120000-2", "carp", "q.sql"))
	require.NoError(t, err)
	require.Equal(t, "OTRA", string(b))

	// La papelera no se puede tocar desde la API.
	require.Error(t, d.Trash(".papelera"))
	_, err = d.Read(".papelera/20261031-120000/carp/q")
	require.Error(t, err)
}

func TestTrashPurge(t *testing.T) {
	d := newDir(t)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return clock }
	save(t, d, "vieja", "1")
	require.NoError(t, d.Trash("vieja"))
	clock = clock.Add(20 * 24 * time.Hour)
	save(t, d, "reciente", "2")
	require.NoError(t, d.Trash("reciente"))
	// Algo que no creó Trash no se toca.
	require.NoError(t, os.MkdirAll(filepath.Join(d.Root(), ".papelera", "mio"), 0o700))

	clock = clock.Add(15 * 24 * time.Hour) // vieja: 35 días; reciente: 15
	n, err := d.PurgeTrash(30 * 24 * time.Hour)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoDirExists(t, filepath.Join(d.Root(), ".papelera", "20260101-000000"))
	require.DirExists(t, filepath.Join(d.Root(), ".papelera", "20260121-000000"))
	require.DirExists(t, filepath.Join(d.Root(), ".papelera", "mio"))
}

func TestSearchNameAndContent(t *testing.T) {
	d := newDir(t)
	save(t, d, "ventas/Reporte ÚNICO", "SELECT 1")
	ref := save(t, d, "otra", "-- nota\nSELECT total FROM Pedidos\nWHERE x = 1")
	_, err := d.Write(model.SaveQueryRequest{Path: "otra", SQL: "-- nota\nSELECT total FROM Pedidos\nWHERE x = 1", ProfileID: "p", RememberConnection: true, ExpectedVersion: ref.Version}, "Prod")
	require.NoError(t, err)
	save(t, d, "vacia", "")

	hits, err := d.Search(context.Background(), model.QuerySearchRequest{Text: "unico"})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "ventas/Reporte ÚNICO.sql", hits[0].Path)
	require.Zero(t, hits[0].Line)

	hits, err = d.Search(context.Background(), model.QuerySearchRequest{Text: "pedidos"})
	require.NoError(t, err)
	require.Empty(t, hits, "sin Content solo se busca por nombre")

	hits, err = d.Search(context.Background(), model.QuerySearchRequest{Text: "PEDIDOS", Content: true})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, 2, hits[0].Line, "relativa al cuerpo, sin la cabecera")
	require.Equal(t, "SELECT total FROM Pedidos", hits[0].Snippet)

	// La cabecera no es contenido buscable.
	hits, err = d.Search(context.Background(), model.QuerySearchRequest{Text: "dbacus", Content: true})
	require.NoError(t, err)
	require.Empty(t, hits)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = d.Search(ctx, model.QuerySearchRequest{Text: "select", Content: true})
	require.ErrorIs(t, err, context.Canceled)

	hits, err = d.Search(context.Background(), model.QuerySearchRequest{Text: "select", Content: true, Limit: 1})
	require.NoError(t, err)
	require.Len(t, hits, 1)
}

func TestListIgnoresHiddenAndOtherExtensions(t *testing.T) {
	d := newDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(d.Root(), ".oculto.sql"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(d.Root(), "notas.txt"), []byte("x"), 0o600))
	save(t, d, "ok", "1")
	require.NoError(t, d.Mkdir("vacia"))
	nodes, trunc, err := d.List()
	require.NoError(t, err)
	require.False(t, trunc)
	var paths []string
	for _, n := range nodes {
		paths = append(paths, n.Path)
	}
	require.Equal(t, []string{"ok.sql", "vacia"}, paths)
}

func TestValidateRoot(t *testing.T) {
	home := t.TempDir()
	require.Error(t, ValidateRoot("relativa/x", home))
	require.Error(t, ValidateRoot(string(filepath.Separator), home))
	require.Error(t, ValidateRoot(home, home))
	require.NoError(t, ValidateRoot(filepath.Join(home, "Consultas"), home))
}
