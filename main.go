package main

import (
	"embed"
	"os"

	"github.com/kisukri/asf-ui/internal/panel"
)

//go:embed static migrations
var assets embed.FS

func main() {
	os.Exit(panel.Main(os.Args[1:], assets))
}
