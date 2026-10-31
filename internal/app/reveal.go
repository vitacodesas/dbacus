package app

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

// revealPath muestra una ruta en el explorador del SO. Los argumentos son fijos y la ruta va como un único
// argumento (sin shell). Con un archivo lo selecciona; con una carpeta la abre.
func revealPath(path string, isDir bool) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		if isDir {
			cmd = exec.Command("open", path)
		} else {
			cmd = exec.Command("open", "-R", path)
		}
	case "windows":
		if isDir {
			cmd = exec.Command("explorer", path)
		} else {
			cmd = exec.Command("explorer", "/select,"+path)
		}
	default:
		dir := path
		if !isDir {
			dir = dirOf(path)
		}
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release() // el explorador sigue su vida; no hay nada que esperar
}

func dirOf(path string) string { return filepath.Dir(path) }
