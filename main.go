package main

import (
	"embed"

	"freebuff2api/internal/app"
)

//go:embed all:static
var assets embed.FS

func main() {
	app.Run(assets)
}
