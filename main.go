package main

import (
	"embed"
	"io/fs"
	"log"

	"tabulariswails/internal/app"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Run(dist); err != nil {
		log.Fatal(err)
	}
}
