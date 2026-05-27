//go:build prod

package web

import "embed"

// Dist holds the built frontend bundle. `make build` copies frontend/dist into
// this directory before compiling with `-tags prod` so the embed resolves.
//
//go:embed all:dist
var Dist embed.FS
