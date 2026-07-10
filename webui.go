package webui

import (
	"embed"
	"io/fs"
)

// Files contains the production web console. Keeping the assets in the
// executable makes the deployment artifact self-contained.
//
//go:embed web-v0/* web-v0/assets/*
var embedded embed.FS

func Files() fs.FS {
	assets, err := fs.Sub(embedded, "web-v0")
	if err != nil {
		panic(err)
	}
	return assets
}
