package queryfiles

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/vitacodesas/dbacus/internal/model"
)

const (
	DefaultSearchLimit = 50
	MaxSearchLimit     = 100
	maxSnippet         = 160
)

// accents pliega las vocales acentuadas y la ñ/ç del español y lenguas vecinas (sin dependencias nuevas).
var accents = strings.NewReplacer(
	"á", "a", "à", "a", "ä", "a", "â", "a", "ã", "a",
	"é", "e", "è", "e", "ë", "e", "ê", "e",
	"í", "i", "ì", "i", "ï", "i", "î", "i",
	"ó", "o", "ò", "o", "ö", "o", "ô", "o", "õ", "o",
	"ú", "u", "ù", "u", "ü", "u", "û", "u",
	"ñ", "n", "ç", "c",
)

// Fold deja un texto en minúsculas y sin acentos para comparar.
func Fold(s string) string { return accents.Replace(strings.ToLower(s)) }

// Search busca por nombre y, con content, dentro de los archivos (subcadena, sin distinguir mayúsculas ni acentos).
// Es secuencial y se detiene al cancelarse ctx. Omite archivos de más de MaxFileSize y los que no son UTF-8.
func (d *Dir) Search(ctx context.Context, q model.QuerySearchRequest) ([]model.QueryHit, error) {
	needle := Fold(strings.TrimSpace(q.Text))
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	hits := []model.QueryHit{}
	if needle == "" {
		return hits, nil
	}
	errStop := fs.SkipAll
	_, err := d.walk(func(rel string, e fs.DirEntry, abs string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		name := displayName(rel)
		hit := model.QueryHit{Path: rel, Name: name}
		matched := strings.Contains(Fold(name), needle)
		if q.Content {
			if line, snip, ok := grep(abs, needle); ok {
				hit.Line, hit.Snippet, matched = line, snip, true
			}
		}
		if matched {
			hits = append(hits, hit)
			if len(hits) >= limit {
				return errStop
			}
		}
		return nil
	})
	if err == fs.SkipAll {
		err = nil
	}
	return hits, err
}

// grep devuelve la primera línea del cuerpo (sin cabecera) que contiene needle.
func grep(abs, needle string) (int, string, bool) {
	fi, err := os.Stat(abs)
	if err != nil || fi.Size() > MaxFileSize {
		return 0, "", false
	}
	raw, err := os.ReadFile(abs)
	if err != nil || !utf8.Valid(raw) {
		return 0, "", false
	}
	s := strings.TrimPrefix(string(raw), "\ufeff")
	// La línea es relativa al cuerpo, que es lo que ve el editor (la cabecera no se muestra).
	if _, body, ok := splitHeader(s); ok {
		s = body
	}
	for i, line := range strings.Split(s, "\n") {
		if strings.Contains(Fold(line), needle) {
			line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
			if r := []rune(line); len(r) > maxSnippet {
				line = string(r[:maxSnippet]) + "…"
			}
			return i + 1, line, true
		}
	}
	return 0, "", false
}
