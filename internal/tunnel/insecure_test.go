package tunnel

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Spec 10: ningún camino de producción puede desactivar la verificación de host key SSH. Sin golangci-lint a mano, este
// test hace de regla: falla si aparece InsecureIgnoreHostKey fuera de archivos de test.
func TestNoInsecureHostKeyCallbackInProduction(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "bindings":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "InsecureIgnoreHostKey") {
			t.Errorf("%s usa InsecureIgnoreHostKey", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
