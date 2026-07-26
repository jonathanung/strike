package server

import (
	"embed"
	"io/fs"
)

//go:embed static/*
var embeddedStatic embed.FS

// staticFS is the default attach-page filesystem rooted at static/.
var staticFS fs.FS

func init() {
	sub, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		panic("server: embed static: " + err.Error())
	}
	staticFS = sub
}
